package agentic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var DefinedTools = []Tool{
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "shell",
			Description: "Execute a shell command on the local machine and return its stdout/stderr.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string", "description": "The shell command to execute."},
					"timeout": map[string]any{"type": "integer", "description": "Maximum seconds to wait.", "default": 30},
				},
				"required": []string{"command"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "web",
			Description: "Make an HTTP request (GET or POST) and return the response body.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url":     map[string]any{"type": "string", "description": "The URL to request."},
					"method":  map[string]any{"type": "string", "description": "HTTP method (default GET).", "default": "GET"},
					"headers": map[string]any{"type": "object", "description": "Optional HTTP headers mapping."},
					"body":    map[string]any{"type": "object", "description": "Optional JSON body for POST."},
					"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds.", "default": 15},
				},
				"required": []string{"url"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "read_file",
			Description: "Read the contents of a file on the local filesystem.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filepath": map[string]any{"type": "string", "description": "The absolute or relative path to the file."},
				},
				"required": []string{"filepath"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "write_file",
			Description: "Write text content to a local file. Overwrites if it exists.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filepath": map[string]any{"type": "string", "description": "The absolute or relative path to the file."},
					"content":  map[string]any{"type": "string", "description": "The content to write."},
				},
				"required": []string{"filepath", "content"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "list_dir",
			Description: "List contents of a directory. Explore the codebase using this.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"dirpath": map[string]any{"type": "string", "description": "The directory path to list."},
				},
				"required": []string{"dirpath"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "grep_search",
			Description: "Search for a regex pattern in files within a directory.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"dirpath": map[string]any{"type": "string", "description": "Directory to search in."},
					"pattern": map[string]any{"type": "string", "description": "Regex pattern to search for."},
				},
				"required": []string{"dirpath", "pattern"},
			},
		},
	},
}

func ExecuteTool(name, argsRaw string) string {
	// If the tool belongs to an MCP server, route it there
	if strings.HasPrefix(name, "mcp_") {
		return ExecuteMCPTool(name, argsRaw)
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsRaw), &args); err != nil {
		return fmt.Sprintf("[error] failed to parse arguments: %v", err)
	}

	switch name {
	case "shell":
		return toolShell(args)
	case "web":
		return toolWeb(args)
	case "read_file":
		return toolReadFile(args)
	case "write_file":
		return toolWriteFile(args)
	case "list_dir":
		return toolListDir(args)
	case "grep_search":
		return toolGrepSearch(args)
	default:
		return fmt.Sprintf("[error] Unknown tool: %s", name)
	}
}

func toolShell(args map[string]any) string {
	cmdStr, _ := args["command"].(string)
	if cmdStr == "" {
		return "[error] command is required"
	}
	timeoutF, _ := args["timeout"].(float64)
	timeout := time.Duration(30) * time.Second
	if timeoutF > 0 {
		timeout = time.Duration(timeoutF) * time.Second
	}

	fmt.Printf("\n%s[tool:shell] $ %s%s\n", ColorTool, cmdStr, ColorReset)

	// Since we execute in sh/bash, we wrap the command
	cmd := exec.Command("bash", "-c", cmdStr)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Start()
	if err != nil {
		return fmt.Sprintf("[error] failed to start command: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-time.After(timeout):
		if err := cmd.Process.Kill(); err != nil {
			return fmt.Sprintf("[error] command timed out and failed to kill: %v\nOutput so far:\n%s", err, out.String())
		}
		return fmt.Sprintf("[error] Command timed out after %v\nOutput so far:\n%s", timeout, out.String())
	case err := <-done:
		exitCode := 0
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else if err != nil {
			return fmt.Sprintf("[error] command error: %v\nOutput:\n%s", err, out.String())
		}
		fmt.Printf("%s[tool:shell] exit=%d%s\n", ColorTool, exitCode, ColorReset)
		res := strings.TrimSpace(out.String())
		if res == "" {
			return fmt.Sprintf("(exit code %d, no output)", exitCode)
		}
		return res
	}
}

func toolWeb(args map[string]any) string {
	url, _ := args["url"].(string)
	if url == "" {
		return "[error] url is required"
	}
	method, ok := args["method"].(string)
	if !ok || method == "" {
		method = "GET"
	}
	timeoutF, _ := args["timeout"].(float64)
	timeout := time.Duration(15) * time.Second
	if timeoutF > 0 {
		timeout = time.Duration(timeoutF) * time.Second
	}

	headers, _ := args["headers"].(map[string]any)
	bodyMap, _ := args["body"].(map[string]any)

	fmt.Printf("\n%s[tool:web] %s %s%s\n", ColorTool, method, url, ColorReset)

	var reqBody io.Reader
	if len(bodyMap) > 0 {
		b, err := json.Marshal(bodyMap)
		if err == nil {
			reqBody = bytes.NewBuffer(b)
		}
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return fmt.Sprintf("[error] %v", err)
	}

	for k, v := range headers {
		if vs, ok := v.(string); ok {
			req.Header.Set(k, vs)
		}
	}
	if reqBody != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("[error] %v", err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return fmt.Sprintf("[error] reading body: %v", err)
	}

	fmt.Printf("%s[tool:web] status=%d, len=%d%s\n", ColorTool, resp.StatusCode, len(b), ColorReset)
	return fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, string(b))
}

func toolReadFile(args map[string]any) string {
	filepath, _ := args["filepath"].(string)
	if filepath == "" {
		return "[error] filepath is required"
	}

	fmt.Printf("\n%s[tool:read_file] %s%s\n", ColorTool, filepath, ColorReset)
	b, err := os.ReadFile(filepath)
	if err != nil {
		return fmt.Sprintf("[error] %v", err)
	}
	return string(b)
}

func toolWriteFile(args map[string]any) string {
	fp, _ := args["filepath"].(string)
	content, _ := args["content"].(string)
	if fp == "" {
		return "[error] filepath is required"
	}

	fmt.Printf("\n%s[tool:write_file] %s%s\n", ColorTool, fp, ColorReset)

	dir := filepath.Dir(fp)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Sprintf("[error] failed to create directories: %v", err)
	}

	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		return fmt.Sprintf("[error] %v", err)
	}
	return "File written successfully."
}

func toolListDir(args map[string]any) string {
	dirpath, _ := args["dirpath"].(string)
	if dirpath == "" {
		return "[error] dirpath is required"
	}

	fmt.Printf("\n%s[tool:list_dir] %s%s\n", ColorTool, dirpath, ColorReset)
	entries, err := os.ReadDir(dirpath)
	if err != nil {
		return fmt.Sprintf("[error] %v", err)
	}

	var out []string
	for _, e := range entries {
		t := "file"
		if e.IsDir() {
			t = "dir "
		}
		out = append(out, fmt.Sprintf("[%s] %s", t, e.Name()))
	}
	if len(out) == 0 {
		return "(empty directory)"
	}
	return strings.Join(out, "\n")
}

func toolGrepSearch(args map[string]any) string {
	dirpath, _ := args["dirpath"].(string)
	pattern, _ := args["pattern"].(string)
	if dirpath == "" || pattern == "" {
		return "[error] dirpath and pattern are required"
	}

	fmt.Printf("\n%s[tool:grep_search] %s in %s%s\n", ColorTool, pattern, dirpath, ColorReset)

	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Sprintf("[error] invalid regex pattern: %v", err)
	}

	var results []string
	err = filepath.WalkDir(dirpath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			// Read file, check lines
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			lines := strings.Split(string(b), "\n")
			for i, line := range lines {
				if re.MatchString(line) {
					results = append(results, fmt.Sprintf("%s:%d: %s", path, i+1, strings.TrimSpace(line)))
					// limit to 50 results to avoid massive output
					if len(results) >= 50 {
						results = append(results, "...(truncated)")
						return fmt.Errorf("truncated")
					}
				}
			}
		}
		return nil
	})

	if len(results) == 0 {
		return "No matches found."
	}
	return strings.Join(results, "\n")
}
