package agentic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Name       string     `json:"name,omitempty"`         // For tool role
	ToolCallID string     `json:"tool_call_id,omitempty"` // For tool role
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
	TopP        float64   `json:"top_p"`
	Tools       []Tool    `json:"tools,omitempty"`
	ToolChoice  string    `json:"tool_choice,omitempty"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type Agent struct {
	APIKey        string
	Model         string
	MaxTokens     int
	Temperature   float64
	InvokeURL     string
	ExtraHeaders  map[string]string
	History       []Message
	HistoryFile   string
	Provider      string
	StartTime     time.Time
	DockerEnabled bool
	DockerImage   string
	GitHubToken   string
	MaxIterations int
	Autonomous    bool // If true, skip manual approvals (e.g. shell allowlist)
}

func NewAgent(apiKey, model, invokeURL string, maxTokens int, temperature float64, extraHeaders map[string]string) *Agent {
	return &Agent{
		APIKey:       apiKey,
		Model:        model,
		MaxTokens:    maxTokens,
		Temperature:  temperature,
		InvokeURL:    invokeURL,
		ExtraHeaders: extraHeaders,
		HistoryFile:  ".agentic_history.json",
		StartTime:    time.Now(),
		DockerImage:  "ubuntu:22.04",
	}
}

func (a *Agent) LoadHistory() {
	b, err := os.ReadFile(a.HistoryFile)
	if err == nil {
		var msgs []Message
		if err := json.Unmarshal(b, &msgs); err == nil {
			a.History = msgs
		}
	}
}

func (a *Agent) SaveHistory() {
	b, err := json.MarshalIndent(a.History, "", "  ")
	if err == nil {
		os.WriteFile(a.HistoryFile, b, 0644)
	}
}

func (a *Agent) ClearHistory() {
	a.History = []Message{}
	os.Remove(a.HistoryFile)
}

func (a *Agent) HandleToolCalls(toolCalls []ToolCall) {
	for _, call := range toolCalls {
		fmt.Printf("%s[tool:%s] calling with args: %s%s\n", ColorTool, call.Function.Name, call.Function.Arguments, ColorReset)

		res := ExecuteTool(call.Function.Name, call.Function.Arguments, a.Autonomous)

		a.History = append(a.History, Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Name:       call.Function.Name,
			Content:    res,
		})
	}
	a.SaveHistory()
}

func (a *Agent) AppendMessage(msg Message) {
	a.History = append(a.History, msg)
	a.SaveHistory()
}

func (a *Agent) CallLLM() (*Message, error) {
	// Trim history
	sendHistory := make([]Message, 0, len(a.History))
	if len(a.History) > 31 {
		sendHistory = append(sendHistory, a.History[0]) // Keep system prompt
		sendHistory = append(sendHistory, a.History[len(a.History)-30:]...)
	} else {
		sendHistory = a.History
	}

	reqBody := ChatRequest{
		Model:       a.Model,
		Messages:    sendHistory,
		MaxTokens:   a.MaxTokens,
		Temperature: a.Temperature,
		TopP:        1.0,
		Tools:       DefinedTools,
		ToolChoice:  "auto",
	}

	b, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", a.InvokeURL, bytes.NewBuffer(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range a.ExtraHeaders {
		req.Header.Set(k, v)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		out, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(out))
	}

	var res struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	if len(res.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned")
	}

	return &res.Choices[0].Message, nil
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func (a *Agent) GetConfig() map[string]string {
	return map[string]string{
		"LLM_PROVIDER":   a.Provider,
		"MODEL":          a.Model,
		"MAX_TOKENS":     strconv.Itoa(a.MaxTokens),
		"TEMPERATURE":    strconv.FormatFloat(a.Temperature, 'f', 2, 64),
		"INVOKE_URL":     a.InvokeURL,
		"API_KEY":        maskKey(a.APIKey),
		"DOCKER_ENABLED": strconv.FormatBool(a.DockerEnabled),
		"DOCKER_IMAGE":   a.DockerImage,
	}
}

func (a *Agent) SetConfig(key, value string) (string, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	value = strings.TrimSpace(value)

	switch key {
	case "MODEL":
		old := a.Model
		a.Model = value
		return fmt.Sprintf("MODEL: %s → %s", old, value), nil
	case "MAX_TOKENS":
		v, err := strconv.Atoi(value)
		if err != nil || v <= 0 {
			return "", fmt.Errorf("MAX_TOKENS must be a positive integer")
		}
		old := a.MaxTokens
		a.MaxTokens = v
		return fmt.Sprintf("MAX_TOKENS: %d → %d", old, v), nil
	case "TEMPERATURE":
		v, err := strconv.ParseFloat(value, 64)
		if err != nil || v < 0 {
			return "", fmt.Errorf("TEMPERATURE must be a non-negative number")
		}
		old := a.Temperature
		a.Temperature = v
		return fmt.Sprintf("TEMPERATURE: %.2f → %.2f", old, v), nil
	case "INVOKE_URL":
		old := a.InvokeURL
		a.InvokeURL = value
		return fmt.Sprintf("INVOKE_URL: %s → %s", old, value), nil
	case "DOCKER_ENABLED":
		v := strings.ToLower(value)
		if v != "true" && v != "false" {
			return "", fmt.Errorf("DOCKER_ENABLED must be 'true' or 'false'")
		}
		old := a.DockerEnabled
		a.DockerEnabled = v == "true"
		return fmt.Sprintf("DOCKER_ENABLED: %v → %v", old, a.DockerEnabled), nil
	case "DOCKER_IMAGE":
		old := a.DockerImage
		a.DockerImage = value
		return fmt.Sprintf("DOCKER_IMAGE: %s → %s", old, value), nil
	case "LLM_PROVIDER":
		old := a.Provider
		a.Provider = value
		return fmt.Sprintf("LLM_PROVIDER: %s → %s", old, value), nil
	case "API_KEY":
		a.APIKey = value
		return fmt.Sprintf("API_KEY: updated (masked: %s)", maskKey(value)), nil
	default:
		return "", fmt.Errorf("unknown config key: %s", key)
	}
}

func (a *Agent) Status() string {
	uptime := time.Since(a.StartTime).Round(time.Second)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("  Provider:       %s\n", a.Provider))
	sb.WriteString(fmt.Sprintf("  Model:          %s\n", a.Model))
	sb.WriteString(fmt.Sprintf("  Max Tokens:     %d\n", a.MaxTokens))
	sb.WriteString(fmt.Sprintf("  Temperature:    %.2f\n", a.Temperature))
	sb.WriteString(fmt.Sprintf("  History:        %d messages\n", len(a.History)))
	sb.WriteString(fmt.Sprintf("  MCP Servers:    %d connected\n", len(mcpClients)))
	sb.WriteString(fmt.Sprintf("  Docker:         enabled=%v image=%s\n", a.DockerEnabled, a.DockerImage))

	if ActiveDockerSession != nil {
		sb.WriteString(fmt.Sprintf("  Container:      %s (running)\n", ActiveDockerSession.ContainerID[:12]))
	} else if a.DockerEnabled {
		sb.WriteString("  Container:      not started\n")
	}

	sb.WriteString(fmt.Sprintf("  Uptime:         %s\n", uptime))
	return sb.String()
}
