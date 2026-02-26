package agentic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
	APIKey       string
	Model        string
	MaxTokens    int
	Temperature  float64
	InvokeURL    string
	ExtraHeaders map[string]string
	History      []Message
	HistoryFile  string
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
