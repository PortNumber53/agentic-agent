package agentic

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

type MCPConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}

type MCPServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Enabled *bool             `json:"enabled,omitempty"`
}

// Global state to hold MCP clients
var mcpClients = make(map[string]*MCPClientWrapper)
var mcpTools = make(map[string]string) // map tool name to server name

type MCPClientWrapper struct {
	client.MCPClient
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

func (w *MCPClientWrapper) Close() error {
	w.cancel()
	w.MCPClient.Close()
	if w.cmd != nil && w.cmd.Process != nil {
		return w.cmd.Process.Kill()
	}
	return nil
}

func LoadMCPConfig() error {
	usr, err := user.Current()
	if err != nil {
		return err
	}
	configPath := filepath.Join(usr.HomeDir, ".agentic", "mcp.json")

	b, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // OK if it doesn't exist
		}
		return err
	}

	var config MCPConfig
	if err := json.Unmarshal(b, &config); err != nil {
		return fmt.Errorf("failed to parse mcp.json: %v", err)
	}

	for serverName, serverConfig := range config.MCPServers {
		if serverConfig.Enabled != nil && !*serverConfig.Enabled {
			fmt.Printf("%s[info] Skipping disabled MCP server: %s%s\n", ColorSystem, serverName, ColorReset)
			continue
		}

		fmt.Printf("%s[info] Initializing MCP server: %s%s\n", ColorSystem, serverName, ColorReset)

		// Start with the current process environment variables so basic things like PATH and HOME exist
		envList := os.Environ()

		for k, v := range serverConfig.Env {
			envList = append(envList, fmt.Sprintf("%s=%s", k, v))
		}

		// Add DEBUG and CI logging to force non-interactive mode
		envList = append(envList, "DEBUG=mcp*")
		envList = append(envList, "CI=1")
		envList = append(envList, "FORCE_COLOR=0")
		envList = append(envList, "NPM_CONFIG_UPDATE_NOTIFIER=false")
		envList = append(envList, "NO_UPDATE_NOTIFIER=true")

		fmt.Printf("%s[debug] Using env variables for MCP client %s, length: %d%s\n", ColorSystem, serverName, len(envList), ColorReset)

		// Explicitly lookup the executable in PATH just in case Go exec struggles
		// with bare commands depending on the environment context.
		resolvedCmd, err := exec.LookPath(serverConfig.Command)
		if err != nil {
			fmt.Printf("%s[warning] Command %s not found in PATH: %v%s\n", ColorError, serverConfig.Command, err, ColorReset)
			continue
		}

		// The mark3labs SDK Stdio client does not pipe stderr. Stderr output from npx (like download bars)
		// might hang or interfere. We will manually construct the client.

		// Because the standard client completely swallows Stderr or fails if we pipe it,
		// we must use StdioMCPClient and hope the command doesn't freeze.
		c, err := client.NewStdioMCPClient(resolvedCmd, envList, serverConfig.Args...)
		if err != nil {
			fmt.Printf("%s[warning] Failed to start MCP client %s: %v%s\n", ColorError, serverName, err, ColorReset)
			continue
		}

		// Add a timeout so we don't hang forever
		initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer initCancel()

		initRequest := mcp.InitializeRequest{}
		initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		initRequest.Params.ClientInfo = mcp.Implementation{
			Name:    "agentic-go",
			Version: "1.0.0",
		}

		_, err = c.Initialize(initCtx, initRequest)
		if err != nil {
			fmt.Printf("%s[warning] Failed to initialize MCP client %s: %v%s\n", ColorError, serverName, err, ColorReset)
			c.Close()
			continue
		}

		mcpClients[serverName] = &MCPClientWrapper{
			MCPClient: c,
			cmd:       nil, // StdioMCPClient handles its own cmd internally but doesn't expose it
			cancel:    func() {},
		}

		// Fetch tools
		toolsReqCtx, toolsCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer toolsCancel()
		toolsRes, err := c.ListTools(toolsReqCtx, mcp.ListToolsRequest{})
		if err != nil {
			fmt.Printf("%s[warning] Failed to list tools for MCP client %s: %v%s\n", ColorError, serverName, err, ColorReset)
			continue
		}

		for _, t := range toolsRes.Tools {
			toolName := fmt.Sprintf("mcp_%s_%s", serverName, t.Name)
			mcpTools[toolName] = serverName

			// Map schema properly
			paramsMap := make(map[string]any)
			b, _ := json.Marshal(t.InputSchema)
			_ = json.Unmarshal(b, &paramsMap)

			definedTool := Tool{
				Type: "function",
				Function: ToolFunction{
					Name:        toolName,
					Description: fmt.Sprintf("This is a tool from the %s MCP server.\n%s", serverName, t.Description),
					Parameters:  paramsMap,
				},
			}
			DefinedTools = append(DefinedTools, definedTool)
		}
	}

	return nil
}

func ExecuteMCPTool(toolName, argsRaw string) string {
	serverName, ok := mcpTools[toolName]
	if !ok {
		return fmt.Sprintf("[error] No MCP server found for tool %s", toolName)
	}

	c, ok := mcpClients[serverName]
	if !ok {
		return fmt.Sprintf("[error] MCP client %s is not connected", serverName)
	}

	// Remove prefix to get the original tool name
	// the prefix is "mcp_" + serverName + "_"
	prefix := fmt.Sprintf("mcp_%s_", serverName)
	originalToolName := strings.TrimPrefix(toolName, prefix)

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsRaw), &args); err != nil {
		return fmt.Sprintf("[error] failed to parse arguments: %v", err)
	}

	fmt.Printf("\n%s[tool:mcp] Executing %s on server %s%s\n", ColorTool, originalToolName, serverName, ColorReset)

	ctx := context.Background()
	req := mcp.CallToolRequest{}
	req.Params.Name = originalToolName
	req.Params.Arguments = args

	res, err := c.CallTool(ctx, req)
	if err != nil {
		return fmt.Sprintf("[error] MCP CallTool error: %v", err)
	}

	if res.IsError {
		// Attempt to grab error text from content
		var errText strings.Builder
		for _, content := range res.Content {
			if textContent, ok := content.(mcp.TextContent); ok {
				errText.WriteString(textContent.Text)
			}
		}
		return fmt.Sprintf("[error] Server returned error: %s", errText.String())
	}

	var output strings.Builder
	for _, content := range res.Content {
		switch c := content.(type) {
		case mcp.TextContent:
			output.WriteString(c.Text)
		case mcp.ImageContent:
			output.WriteString(fmt.Sprintf("[ImageContent: %s (base64 omitted)]", c.MIMEType))
		case mcp.EmbeddedResource:
			resUri := "(unknown uri)"
			if textRes, ok := mcp.AsTextResourceContents(c.Resource); ok {
				resUri = textRes.URI
			} else if blobRes, ok := mcp.AsBlobResourceContents(c.Resource); ok {
				resUri = blobRes.URI
			}
			output.WriteString(fmt.Sprintf("[EmbeddedResource: %s]", resUri))
		default:
			output.WriteString(fmt.Sprintf("[%T content]", c))
		}
	}

	return output.String()
}

func CloseMCPClients() {
	for name, client := range mcpClients {
		if err := client.Close(); err != nil {
			fmt.Printf("%s[warning] Error closing MCP client %s: %v%s\n", ColorError, name, err, ColorReset)
		}
	}
}
