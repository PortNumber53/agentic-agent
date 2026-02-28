package agentic

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// ActiveDockerSession is the current Docker session for tool execution.
var ActiveDockerSession *DockerSession

type DockerSession struct {
	ContainerID string
	Image       string
	cli         *client.Client
}

func ensureDockerImage(ctx context.Context, cli *client.Client, imageName string) error {
	if _, _, err := cli.ImageInspectWithRaw(ctx, imageName); err == nil {
		fmt.Printf("%s[docker] Using local image %s.%s\n", ColorSystem, imageName, ColorReset)
		return nil
	}

	fmt.Printf("%s[docker] Pulling image %s...%s\n", ColorSystem, imageName, ColorReset)
	pullResp, err := cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("image %s is not available locally and pull failed: %w", imageName, err)
	}
	defer pullResp.Close()

	// Drain the pull response to completion.
	if _, err := io.Copy(io.Discard, pullResp); err != nil {
		return fmt.Errorf("failed reading pull output for %s: %w", imageName, err)
	}

	fmt.Printf("%s[docker] Image ready.%s\n", ColorSystem, ColorReset)
	return nil
}

func StartDockerSession(imageName string) (*DockerSession, error) {
	imageName = strings.TrimSpace(imageName)
	if imageName == "" {
		imageName = "ubuntu:22.04"
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	ctx := context.Background()

	if err := ensureDockerImage(ctx, cli, imageName); err != nil {
		cli.Close()
		return nil, err
	}

	// Create container with long-running entrypoint
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: imageName,
		Cmd:   []string{"tail", "-f", "/dev/null"},
		Tty:   false,
	}, nil, nil, nil, "")
	if err != nil {
		cli.Close()
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	// Start container
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		cli.Close()
		return nil, fmt.Errorf("failed to start container: %w", err)
	}

	session := &DockerSession{
		ContainerID: resp.ID,
		Image:       imageName,
		cli:         cli,
	}

	ActiveDockerSession = session
	fmt.Printf("%s[docker] Container %s started.%s\n", ColorSystem, resp.ID[:12], ColorReset)
	return session, nil
}

func (s *DockerSession) Exec(command string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	execConfig := container.ExecOptions{
		Cmd:          []string{"bash", "-c", command},
		AttachStdout: true,
		AttachStderr: true,
	}

	execResp, err := s.cli.ContainerExecCreate(ctx, s.ContainerID, execConfig)
	if err != nil {
		return "", fmt.Errorf("exec create failed: %w", err)
	}

	attachResp, err := s.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach failed: %w", err)
	}
	defer attachResp.Close()

	var stdoutBuf, stderrBuf bytes.Buffer
	_, err = stdcopy.StdCopy(&stdoutBuf, &stderrBuf, attachResp.Reader)
	if err != nil {
		return "", fmt.Errorf("reading exec output failed: %w", err)
	}

	// Get exit code
	inspectResp, err := s.cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return "", fmt.Errorf("exec inspect failed: %w", err)
	}

	output := strings.TrimSpace(stdoutBuf.String() + stderrBuf.String())
	if output == "" {
		return fmt.Sprintf("(exit code %d, no output)", inspectResp.ExitCode), nil
	}
	if inspectResp.ExitCode != 0 {
		return fmt.Sprintf("(exit code %d)\n%s", inspectResp.ExitCode, output), nil
	}
	return output, nil
}

func (s *DockerSession) Stop() error {
	if s == nil || s.cli == nil {
		return nil
	}

	ctx := context.Background()
	timeout := 5
	stopOpts := container.StopOptions{Timeout: &timeout}
	s.cli.ContainerStop(ctx, s.ContainerID, stopOpts)
	s.cli.ContainerRemove(ctx, s.ContainerID, container.RemoveOptions{Force: true})
	s.cli.Close()

	if ActiveDockerSession == s {
		ActiveDockerSession = nil
	}
	fmt.Printf("%s[docker] Container %s stopped and removed.%s\n", ColorSystem, s.ContainerID[:12], ColorReset)
	return nil
}

func StopDockerSession() {
	if ActiveDockerSession != nil {
		ActiveDockerSession.Stop()
	}
}
