package agentic

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

// AgenticConfig represents ~/.agentic/config.json
type AgenticConfig struct {
	LLMProvider string  `json:"llm_provider"`
	Model       string  `json:"model"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
	InvokeURL   string  `json:"invoke_url,omitempty"`

	// API keys per provider
	NvidiaAPIKey     string `json:"nvidia_api_key,omitempty"`
	OpenRouterAPIKey string `json:"openrouter_api_key,omitempty"`

	// Docker
	DockerEnabled bool   `json:"docker_enabled"`
	DockerImage   string `json:"docker_image,omitempty"`
}

// ConfigDir returns the path to ~/.agentic/
func ConfigDir() string {
	usr, err := user.Current()
	if err != nil {
		return ""
	}
	return filepath.Join(usr.HomeDir, ".agentic")
}

// ConfigPath returns the path to ~/.agentic/config.json
func ConfigPath() string {
	dir := ConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.json")
}

// LoadConfig reads ~/.agentic/config.json and returns the parsed config.
// Returns a zero-value config (not an error) if the file doesn't exist.
func LoadConfig() (AgenticConfig, error) {
	var cfg AgenticConfig
	// Set defaults
	cfg.Temperature = -1 // sentinel: unset
	cfg.MaxTokens = -1   // sentinel: unset

	path := ConfigPath()
	var b []byte
	var err error

	if path != "" {
		b, err = os.ReadFile(path)
	}

	// Fallback to system-wide config if local config isn't found
	if path == "" || (err != nil && os.IsNotExist(err)) {
		globalPath := "/etc/agentic/config.json"
		if b2, err2 := os.ReadFile(globalPath); err2 == nil {
			b = b2
			err = nil
			path = globalPath
		}
	}

	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("failed to read %s: %w", path, err)
	}

	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("failed to parse %s: %w", path, err)
	}

	return cfg, nil
}

// WriteSampleConfig creates a sample ~/.agentic/config.json if it doesn't exist.
func WriteSampleConfig() error {
	dir := ConfigDir()
	if dir == "" {
		return fmt.Errorf("could not determine home directory")
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	path := filepath.Join(dir, "config.json")
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}

	sample := AgenticConfig{
		LLMProvider:      "openrouter",
		Model:            "nvidia/nemotron-3-nano-30b-a3b:free",
		MaxTokens:        16384,
		Temperature:      1.0,
		OpenRouterAPIKey: "sk-or-v1-YOUR-KEY-HERE",
		DockerEnabled:    false,
		DockerImage:      "ubuntu:22.04",
	}

	b, err := json.MarshalIndent(sample, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, b, 0600)
}
