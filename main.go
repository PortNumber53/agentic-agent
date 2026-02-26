package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	agentic "agentic-go/internal/agentic"

	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		fmt.Printf("Warning: error loading .env file: %v\n", err)
	}

	// Initialize MCP servers
	if err := agentic.LoadMCPConfig(); err != nil {
		fmt.Printf("%s[warning] Failed to load MCP config: %v%s\n", agentic.ColorError, err, agentic.ColorReset)
	}
	defer agentic.CloseMCPClients()

	// Handle graceful shutdown for Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Printf("\n%s[info] Shutting down...%s\n", agentic.ColorSystem, agentic.ColorReset)

		// Run CloseMCPClients in a goroutine so we can timeout if it hangs
		done := make(chan struct{})
		go func() {
			agentic.CloseMCPClients()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(3 * time.Second):
			fmt.Printf("%s[warning] Shutdown timed out, forcing exit...%s\n", agentic.ColorError, agentic.ColorReset)
		}
		os.Exit(0)
	}()

	// Determine LLM provider
	provider := strings.ToLower(os.Getenv("LLM_PROVIDER"))
	var apiKey, model, invokeURL string
	var extraHeaders map[string]string

	switch provider {
	case "openrouter":
		apiKey = os.Getenv("OPENROUTER_API_KEY")
		if apiKey == "" {
			fmt.Printf("%s[error] OPENROUTER_API_KEY is not set. Add it to your .env file.%s\n", agentic.ColorError, agentic.ColorReset)
			os.Exit(1)
		}
		model = os.Getenv("MODEL")
		if model == "" {
			model = "nvidia/nemotron-3-nano-30b-a3b:free"
		}
		invokeURL = "https://openrouter.ai/api/v1/chat/completions"
		extraHeaders = map[string]string{
			"HTTP-Referer": "https://github.com/agentic-go",
			"X-Title":      "agentic-go",
		}
		fmt.Printf("%s[info] Using OpenRouter provider (model: %s)%s\n", agentic.ColorSystem, model, agentic.ColorReset)
	default: // "nvidia" or unset
		apiKey = os.Getenv("NVIDIA_API_KEY")
		if apiKey == "" {
			fmt.Printf("%s[error] NVIDIA_API_KEY is not set. Add it to your .env file or set LLM_PROVIDER=openrouter.%s\n", agentic.ColorError, agentic.ColorReset)
			os.Exit(1)
		}
		model = os.Getenv("MODEL")
		if model == "" {
			model = "moonshotai/kimi-k2.5"
		}
		invokeURL = "https://integrate.api.nvidia.com/v1/chat/completions"
	}

	maxTokens, err := strconv.Atoi(os.Getenv("MAX_TOKENS"))
	if err != nil || maxTokens <= 0 {
		maxTokens = 16384
	}

	temp, err := strconv.ParseFloat(os.Getenv("TEMPERATURE"), 64)
	if err != nil || temp < 0 {
		temp = 1.0
	}

	agent := agentic.NewAgent(apiKey, model, invokeURL, maxTokens, temp, extraHeaders)
	agent.LoadHistory()

	systemContent := "You are a highly capable AI programming assistant. You have access to a variety of tools (shell, web, read_file, write_file, list_dir, grep_search). Use them proactively to solve the user's request. Always examine files before modifying them, and explain your reasoning clearly."

	// Load AGENTIC.md context if exists
	agenticMD, err := os.ReadFile("AGENTIC.md")
	if err == nil {
		systemContent += "\n\n# Project Context (from AGENTIC.md)\n" + string(agenticMD)
		fmt.Printf("%s[info] Loaded context from AGENTIC.md%s\n", agentic.ColorSystem, agentic.ColorReset)
	}

	// Ensure system prompt is the first message and properly up-to-date
	if len(agent.History) == 0 || agent.History[0].Role != "system" {
		sysMsg := agentic.Message{Role: "system", Content: systemContent}
		if len(agent.History) == 0 {
			agent.History = []agentic.Message{sysMsg}
		} else {
			agent.History[0] = sysMsg
		}
	} else {
		// Update system prompt in case AGENTIC.md changed
		agent.History[0].Content = systemContent
	}

	// If arguments passed, run once
	if len(os.Args) > 1 {
		prompt := strings.Join(os.Args[1:], " ")
		fmt.Printf("%sUser:%s %s\n", agentic.ColorUser, agentic.ColorReset, prompt)
		runAgentStep(agent, prompt)
		return
	}

	// REPL
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("\n%s%sEnter your prompt (Ctrl-C or 'exit' to quit, '/clear' to wipe history):%s\n> ", agentic.ColorUser, agentic.ColorBold, agentic.ColorReset)

		prompt, err := reader.ReadString('\n')
		if err != nil { // EOF
			break
		}

		prompt = strings.TrimSpace(prompt)
		if prompt == "exit" || prompt == "/exit" {
			break
		}
		if prompt == "/clear" {
			agent.ClearHistory()
			agent.History = []agentic.Message{{Role: "system", Content: systemContent}}
			agent.SaveHistory()
			fmt.Printf("%s[info] History cleared.%s\n", agentic.ColorSystem, agentic.ColorReset)
			continue
		}
		if prompt == "" {
			continue
		}

		runAgentStep(agent, prompt)
	}
}

func runAgentStep(agent *agentic.Agent, prompt string) {
	agent.AppendMessage(agentic.Message{Role: "user", Content: prompt})

	maxIterations := 15
	for i := 1; i <= maxIterations; i++ {
		fmt.Printf("\n%s%s============================================================%s\n", agentic.ColorSystem, agentic.ColorBold, agentic.ColorReset)
		fmt.Printf("%s%s  Agent iteration %d/%d%s\n", agentic.ColorSystem, agentic.ColorBold, i, maxIterations, agentic.ColorReset)
		fmt.Printf("%s%s============================================================%s\n", agentic.ColorSystem, agentic.ColorBold, agentic.ColorReset)

		msg, err := agent.CallLLM()
		if err != nil {
			fmt.Printf("%s[error] LLM call failed: %v%s\n", agentic.ColorError, err, agentic.ColorReset)
			return
		}

		agent.AppendMessage(*msg)

		if msg.Content != "" {
			fmt.Printf("\n%s[agent] %s%s\n", agentic.ColorAgent, msg.Content, agentic.ColorReset)
		}

		if len(msg.ToolCalls) == 0 {
			fmt.Printf("\n%s%sTask complete.%s\n", agentic.ColorAgent, agentic.ColorBold, agentic.ColorReset)
			return
		}

		for _, tc := range msg.ToolCalls {
			fnName := tc.Function.Name
			fnArgs := tc.Function.Arguments

			result := agentic.ExecuteTool(fnName, fnArgs)

			agent.AppendMessage(agentic.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       fnName,
				Content:    result,
			})
		}
	}

	fmt.Printf("\n%s[warning] Reached max iterations without a final answer.%s\n", agentic.ColorError, agentic.ColorReset)
}
