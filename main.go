package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	agentic "agentic-go/internal/agentic"

	"github.com/chzyer/readline"
	"github.com/joho/godotenv"
)

// getConfigVal returns the first non-empty value: env var override, then config file value.
func getConfigVal(envKey, cfgVal string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return cfgVal
}

type cliFlags struct {
	showHelp    bool
	showVersion bool
	showConfig  bool
	serve       bool
	promptArgs  []string
}

func parseCLIFlags(args []string) (cliFlags, error) {
	var out cliFlags

	fs := flag.NewFlagSet("agentic-go", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&out.showHelp, "help", false, "Show help")
	fs.BoolVar(&out.showVersion, "version", false, "Show version")
	fs.BoolVar(&out.showConfig, "config", false, "Show config path")
	fs.BoolVar(&out.serve, "serve", false, "Run in server mode")

	if err := fs.Parse(args); err != nil {
		return out, err
	}

	out.promptArgs = fs.Args()
	return out, nil
}

func appVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Version == "" || bi.Main.Version == "(devel)" {
		return "dev"
	}
	return bi.Main.Version
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  agentic-go [prompt]")
	fmt.Println("  agentic-go --help")
	fmt.Println("  agentic-go --version")
	fmt.Println("  agentic-go --config")
	fmt.Println("  agentic-go --serve")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --help      Show this help message")
	fmt.Println("  --version   Print application version")
	fmt.Println("  --config    Print path to ~/.agentic/config.json")
	fmt.Println("  --serve     Run in server mode listening on port 20511")
}

func main() {
	flags, err := parseCLIFlags(os.Args[1:])
	if err != nil {
		fmt.Printf("%s[error] %v%s\n", agentic.ColorError, err, agentic.ColorReset)
		printUsage()
		return
	}

	if flags.showHelp {
		printUsage()
		return
	}

	if flags.showVersion {
		fmt.Println(appVersion())
		return
	}

	if flags.showConfig {
		path := agentic.ConfigPath()
		if path == "" {
			fmt.Printf("%s[error] could not determine config path%s\n", agentic.ColorError, agentic.ColorReset)
			return
		}
		fmt.Println(path)
		return
	}

	// .env provides local overrides (optional)
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		fmt.Printf("Warning: error loading .env file: %v\n", err)
	}

	agent := createNewAgent()

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

		// Run cleanup in a goroutine so we can timeout if it hangs
		done := make(chan struct{})
		go func() {
			agentic.StopDockerSession()
			agentic.CloseMCPClients()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			fmt.Printf("%s[warning] Shutdown timed out, forcing exit...%s\n", agentic.ColorError, agentic.ColorReset)
		}
		os.Exit(0)
	}()

	// Load shell command allowlist
	agentic.Allowlist.Load()

	agent.LoadHistory()

	systemContent := "You are a highly capable AI programming assistant. You have access to a variety of tools (shell, web, read_file, write_file, list_dir, grep_search). Use them proactively to solve the user's request. Always examine files before modifying them, and explain your reasoning clearly."

	var mcpNames []string
	for _, t := range agentic.DefinedTools {
		if strings.HasPrefix(t.Function.Name, "mcp_") {
			mcpNames = append(mcpNames, t.Function.Name)
		}
	}
	if len(mcpNames) > 0 {
		systemContent += fmt.Sprintf("\n\nAvailable MCP Tools: %s\nYou MUST use these specialized MCP tools when interacting with their respective services instead of using the generic 'web' or 'shell' tools.", strings.Join(mcpNames, ", "))
	}

	// Inform the LLM about Docker execution context
	if agent.DockerEnabled {
		systemContent += "\n\nIMPORTANT: Your shell commands are executed inside a Docker container (image: " + agent.DockerImage + "). You are NOT running on the host machine. All shell tool invocations run inside this container. The container persists for the duration of the chat session, so installed packages and file changes are preserved between commands."
	}

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

	if flags.serve {
		startServer(systemContent)
		return
	}

	if len(flags.promptArgs) > 0 {
		prompt := strings.Join(flags.promptArgs, " ")
		fmt.Printf("%sUser:%s %s\n", agentic.ColorUser, agentic.ColorReset, prompt)
		runAgentStep(agent, prompt, systemContent)
		return
	}

	// Start Docker session if enabled
	if agent.DockerEnabled {
		_, err := agentic.StartDockerSession(agent.DockerImage)
		if err != nil {
			fmt.Printf("%s[error] Failed to start Docker session: %v%s\n", agentic.ColorError, err, agentic.ColorReset)
			fmt.Printf("%s[info] Continuing without Docker...%s\n", agentic.ColorSystem, agentic.ColorReset)
			agent.DockerEnabled = false
		}
	}

	// Prompt history for autocomplete
	var promptHistory []string

	// Readline-based REPL
	completer := readline.NewPrefixCompleter(
		readline.PcItem("/clear"),
		readline.PcItem("/exit"),
		readline.PcItem("/config",
			readline.PcItem("MODEL"),
			readline.PcItem("MAX_TOKENS"),
			readline.PcItem("TEMPERATURE"),
			readline.PcItem("LLM_PROVIDER"),
			readline.PcItem("INVOKE_URL"),
			readline.PcItem("API_KEY"),
			readline.PcItem("DOCKER_ENABLED"),
			readline.PcItem("DOCKER_IMAGE"),
		),
		readline.PcItem("/status"),
		readline.PcItem("/agent"),
		readline.PcItem("/loop"),
	)

	rl, err := readline.NewEx(&readline.Config{
		Prompt:            "> ",
		HistoryFile:       ".agentic_prompt_history",
		AutoComplete:      completer,
		InterruptPrompt:   "^C",
		EOFPrompt:         "exit",
		HistorySearchFold: true,
		FuncFilterInputRune: func(r rune) (rune, bool) {
			if r == readline.CharCtrlZ {
				return r, false
			}
			return r, true
		},
	})
	if err != nil {
		fmt.Printf("%s[error] Failed to initialize readline: %v%s\n", agentic.ColorError, err, agentic.ColorReset)
		return
	}
	defer rl.Close()

	// Wire allowlist: full line input for regex pattern via readline
	agentic.Allowlist.ReadLineFunc = func(prompt string) (string, error) {
		fmt.Print(prompt)
		rl.SetPrompt("")
		rl.Config.DisableAutoSaveHistory = true
		defer func() {
			rl.SetPrompt("> ")
			rl.Config.DisableAutoSaveHistory = false
		}()
		return rl.Readline()
	}

	// Dynamic completer that also suggests from prompt history
	rl.Config.AutoComplete = readline.NewPrefixCompleter(
		readline.PcItem("/clear"),
		readline.PcItem("/exit"),
		readline.PcItem("/config",
			readline.PcItem("MODEL"),
			readline.PcItem("MAX_TOKENS"),
			readline.PcItem("TEMPERATURE"),
			readline.PcItem("LLM_PROVIDER"),
			readline.PcItem("INVOKE_URL"),
			readline.PcItem("API_KEY"),
			readline.PcItem("DOCKER_ENABLED"),
			readline.PcItem("DOCKER_IMAGE"),
		),
		readline.PcItem("/status"),
		readline.PcItem("/allow",
			readline.PcItem("list"),
			readline.PcItem("add"),
			readline.PcItem("remove"),
		),
		readline.PcItem("/agent"),
		readline.PcItem("/loop"),
		readline.PcItemDynamic(func(line string) []string {
			// Suggest from prompt history for non-slash inputs
			if strings.HasPrefix(line, "/") {
				return nil
			}
			var matches []string
			for _, h := range promptHistory {
				if strings.HasPrefix(strings.ToLower(h), strings.ToLower(line)) && h != line {
					matches = append(matches, h)
				}
			}
			return matches
		}),
	)

	fmt.Printf("%s%sReady. Type your prompt (Ctrl-C or '/exit' to quit, Tab for autocomplete, Shift+Enter or \\ for multi-line)%s\n", agentic.ColorUser, agentic.ColorBold, agentic.ColorReset)

	for {
		prompt, err := readMultiLine(rl)
		if err != nil {
			if err == readline.ErrInterrupt {
				continue
			}
			break
		}
		if prompt == "" {
			continue
		}

		switch {
		case prompt == "exit" || prompt == "/exit":
			return

		case prompt == "/clear":
			agent.ClearHistory()
			agent.History = []agentic.Message{{Role: "system", Content: systemContent}}
			agent.SaveHistory()
			// Stop Docker session on clear
			if agentic.ActiveDockerSession != nil {
				agentic.StopDockerSession()
				if agent.DockerEnabled {
					_, err := agentic.StartDockerSession(agent.DockerImage)
					if err != nil {
						fmt.Printf("%s[error] Failed to restart Docker session: %v%s\n", agentic.ColorError, err, agentic.ColorReset)
					}
				}
			}
			fmt.Printf("%s[info] History cleared.%s\n", agentic.ColorSystem, agentic.ColorReset)

		case prompt == "/status":
			fmt.Printf("\n%s%s── Agent Status ──%s\n", agentic.ColorSystem, agentic.ColorBold, agentic.ColorReset)
			fmt.Printf("%s%s%s", agentic.ColorSystem, agent.Status(), agentic.ColorReset)

		case prompt == "/config":
			fmt.Printf("\n%s%s── Configuration ──%s\n", agentic.ColorSystem, agentic.ColorBold, agentic.ColorReset)
			cfg := agent.GetConfig()
			// Sort keys for stable output
			keys := make([]string, 0, len(cfg))
			for k := range cfg {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("%s  %-16s = %s%s\n", agentic.ColorSystem, k, cfg[k], agentic.ColorReset)
			}
			fmt.Printf("%s  Usage: /config <KEY> <VALUE>%s\n", agentic.ColorSystem, agentic.ColorReset)

		case prompt == "/allow" || prompt == "/allow list":
			fmt.Printf("\n%s%s── Shell Allowlist ──%s\n", agentic.ColorSystem, agentic.ColorBold, agentic.ColorReset)
			fmt.Printf("%s%s%s", agentic.ColorSystem, agentic.Allowlist.List(), agentic.ColorReset)
			fmt.Printf("%s  Usage: /allow add <regex> [description]%s\n", agentic.ColorSystem, agentic.ColorReset)
			fmt.Printf("%s         /allow remove <index>%s\n", agentic.ColorSystem, agentic.ColorReset)

		case strings.HasPrefix(prompt, "/allow add "):
			rest := strings.TrimPrefix(prompt, "/allow add ")
			parts := strings.SplitN(rest, " ", 2)
			pattern := parts[0]
			desc := ""
			if len(parts) == 2 {
				desc = parts[1]
			}
			if err := agentic.Allowlist.Add(pattern, desc); err != nil {
				fmt.Printf("%s[error] %v%s\n", agentic.ColorError, err, agentic.ColorReset)
			} else {
				fmt.Printf("%s[allowlist] Added pattern: %s%s\n", agentic.ColorSystem, pattern, agentic.ColorReset)
			}

		case strings.HasPrefix(prompt, "/allow remove "):
			idxStr := strings.TrimPrefix(prompt, "/allow remove ")
			idx, err := strconv.Atoi(strings.TrimSpace(idxStr))
			if err != nil {
				fmt.Printf("%s[error] Invalid index: %s%s\n", agentic.ColorError, idxStr, agentic.ColorReset)
			} else if err := agentic.Allowlist.Remove(idx); err != nil {
				fmt.Printf("%s[error] %v%s\n", agentic.ColorError, err, agentic.ColorReset)
			} else {
				fmt.Printf("%s[allowlist] Rule %d removed.%s\n", agentic.ColorSystem, idx, agentic.ColorReset)
			}

		case strings.HasPrefix(prompt, "/config "):
			parts := strings.SplitN(prompt, " ", 3)
			if len(parts) < 3 {
				fmt.Printf("%s[error] Usage: /config <KEY> <VALUE>%s\n", agentic.ColorError, agentic.ColorReset)
			} else {
				result, err := agent.SetConfig(parts[1], parts[2])
				if err != nil {
					fmt.Printf("%s[error] %v%s\n", agentic.ColorError, err, agentic.ColorReset)
				} else {
					fmt.Printf("%s[config] %s%s\n", agentic.ColorSystem, result, agentic.ColorReset)
					// Handle Docker state changes
					handleDockerConfigChange(agent, parts[1])
				}
			}

		default:
			// Track prompt in history for autocomplete
			if !strings.HasPrefix(prompt, "/") {
				promptHistory = append(promptHistory, prompt)
			}
			runAgentStep(agent, prompt, systemContent)
		}
	}
}

// readMultiLine reads a potentially multi-line prompt from readline.
// Shift+Enter is handled at the readline layer (patched third_party/readline).
// A trailing backslash (\) at the end of a line remains a portable fallback.
func readMultiLine(rl *readline.Instance) (string, error) {
	var lines []string

	for {
		line, err := rl.Readline()
		if err != nil {
			// If we already have partial input, return it on interrupt
			if err == readline.ErrInterrupt && len(lines) > 0 {
				return "", err
			}
			return "", err
		}

		// Check for trailing backslash continuation
		if strings.HasSuffix(line, "\\") {
			lines = append(lines, strings.TrimSuffix(line, "\\"))
			rl.SetPrompt("... ")
			continue
		}

		lines = append(lines, line)
		rl.SetPrompt("> ")

		result := strings.TrimSpace(strings.Join(lines, "\n"))
		return result, nil
	}
}

func handleDockerConfigChange(agent *agentic.Agent, key string) {
	key = strings.ToUpper(strings.TrimSpace(key))
	switch key {
	case "DOCKER_ENABLED":
		if agent.DockerEnabled {
			if agentic.ActiveDockerSession == nil {
				_, err := agentic.StartDockerSession(agent.DockerImage)
				if err != nil {
					fmt.Printf("%s[error] Failed to start Docker session: %v%s\n", agentic.ColorError, err, agentic.ColorReset)
					agent.DockerEnabled = false
				}
			}
		} else {
			agentic.StopDockerSession()
		}
	case "DOCKER_IMAGE":
		if agent.DockerEnabled && agentic.ActiveDockerSession != nil {
			fmt.Printf("%s[info] Restarting Docker session with new image...%s\n", agentic.ColorSystem, agentic.ColorReset)
			agentic.StopDockerSession()
			_, err := agentic.StartDockerSession(agent.DockerImage)
			if err != nil {
				fmt.Printf("%s[error] Failed to start Docker session: %v%s\n", agentic.ColorError, err, agentic.ColorReset)
			}
		}
	}
}

func runAgentStep(agent *agentic.Agent, prompt string, systemContent string) {
	// Parse commands
	isLoop := false
	agentName := ""
	cleanPrompt := prompt

	if strings.HasPrefix(prompt, "/agent ") {
		parts := strings.SplitN(prompt, " ", 3)
		if len(parts) >= 2 {
			agentName = parts[1]
			if len(parts) == 3 {
				cleanPrompt = parts[2]
			} else {
				cleanPrompt = ""
			}
		}
	} else if strings.HasPrefix(prompt, "/loop ") {
		isLoop = true
		cleanPrompt = strings.TrimPrefix(prompt, "/loop ")
	}

	// Apply agent template if found
	var localSystemContent = systemContent
	if agentName != "" {
		agentMD, err := os.ReadFile("agents/" + agentName + ".md")
		if err == nil {
			localSystemContent = string(agentMD)
			fmt.Printf("%s[info] Loaded agent template: %s%s\n", agentic.ColorSystem, agentName, agentic.ColorReset)
		} else {
			fmt.Printf("%s[warning] Agent template not found: %s. Using default.%s\n", agentic.ColorError, agentName, agentic.ColorReset)
		}
	}

	// Update system prompt for this step
	if len(agent.History) == 0 {
		sysMsg := agentic.Message{Role: "system", Content: localSystemContent}
		agent.History = []agentic.Message{sysMsg}
	} else if agent.History[0].Role == "system" {
		// Save original context to restore later
		defer func(orig string) { agent.History[0].Content = orig }(agent.History[0].Content)
		agent.History[0].Content = localSystemContent
	}

	if cleanPrompt != "" {
		agent.AppendMessage(agentic.Message{Role: "user", Content: cleanPrompt})
	}

	maxIterations := 15
	if isLoop {
		maxIterations = 50 // Extended loop
		fmt.Printf("%s[info] Starting Ralph Wiggum Autonomous Loop (Max: %d)%s\n", agentic.ColorSystem, maxIterations, agentic.ColorReset)
	}
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
			if isLoop {
				// Autonomous Iteration Loop logic: force continuation
				if strings.Contains(msg.Content, "TASK_COMPLETED") {
					fmt.Printf("\n%s%sRalph loop finished successfully.%s\n", agentic.ColorAgent, agentic.ColorBold, agentic.ColorReset)
					return
				}
				fmt.Printf("\n%s[system] Injecting loop continuation prompt.%s\n", agentic.ColorSystem, agentic.ColorReset)
				agent.AppendMessage(agentic.Message{
					Role:    "user",
					Content: "Are you unequivocally done with the task requested? If not, continue working and outputting tool calls. If you are entirely finished and your goals are met, simply state 'TASK_COMPLETED' and nothing else.",
				})
				continue
			}

			fmt.Printf("\n%s%sTask complete.%s\n", agentic.ColorAgent, agentic.ColorBold, agentic.ColorReset)
			return
		}

		for _, tc := range msg.ToolCalls {
			fnName := tc.Function.Name
			fnArgs := tc.Function.Arguments

			result := agentic.ExecuteTool(fnName, fnArgs, isLoop)

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

func createNewAgent() *agentic.Agent {
	// Load ~/.agentic/config.json as primary config
	cfg, err := agentic.LoadConfig()
	if err != nil {
		fmt.Printf("%s[warning] %v%s\n", agentic.ColorError, err, agentic.ColorReset)
	}

	// Resolve config: config.json → .env → env var → default
	// Priority: env vars > .env > config.json > defaults
	provider := strings.ToLower(getConfigVal("LLM_PROVIDER", cfg.LLMProvider))
	var apiKey, model, invokeURL string
	var extraHeaders map[string]string

	switch provider {
	case "openrouter":
		apiKey = getConfigVal("OPENROUTER_API_KEY", cfg.OpenRouterAPIKey)
		if apiKey == "" {
			fmt.Printf("%s[error] OPENROUTER_API_KEY is not set. Add it to ~/.agentic/config.json or .env%s\n", agentic.ColorError, agentic.ColorReset)
			os.Exit(1)
		}
		model = getConfigVal("MODEL", cfg.Model)
		if model == "" {
			model = "nvidia/nemotron-3-nano-30b-a3b:free"
		}
		invokeURL = getConfigVal("INVOKE_URL", cfg.InvokeURL)
		if invokeURL == "" {
			invokeURL = "https://openrouter.ai/api/v1/chat/completions"
		}
		extraHeaders = map[string]string{
			"HTTP-Referer": "https://github.com/agentic-go",
			"X-Title":      "agentic-go",
		}
		fmt.Printf("%s[info] Using OpenRouter provider (model: %s)%s\n", agentic.ColorSystem, model, agentic.ColorReset)
	default: // "nvidia" or unset
		if provider == "" {
			provider = "nvidia"
		}
		apiKey = getConfigVal("NVIDIA_API_KEY", cfg.NvidiaAPIKey)
		if apiKey == "" {
			fmt.Printf("%s[error] NVIDIA_API_KEY is not set. Add it to ~/.agentic/config.json or .env, or set llm_provider to openrouter.%s\n", agentic.ColorError, agentic.ColorReset)
			os.Exit(1)
		}
		model = getConfigVal("MODEL", cfg.Model)
		if model == "" {
			model = "moonshotai/kimi-k2.5"
		}
		invokeURL = getConfigVal("INVOKE_URL", cfg.InvokeURL)
		if invokeURL == "" {
			invokeURL = "https://integrate.api.nvidia.com/v1/chat/completions"
		}
	}

	// Max tokens: env > config.json > default
	maxTokens := cfg.MaxTokens
	if v, err := strconv.Atoi(os.Getenv("MAX_TOKENS")); err == nil && v > 0 {
		maxTokens = v
	}
	if maxTokens <= 0 {
		maxTokens = 16384
	}

	// Temperature: env > config.json > default
	temp := cfg.Temperature
	if v, err := strconv.ParseFloat(os.Getenv("TEMPERATURE"), 64); err == nil && v >= 0 {
		temp = v
	}
	if temp < 0 {
		temp = 1.0
	}

	agent := agentic.NewAgent(apiKey, model, invokeURL, maxTokens, temp, extraHeaders)
	agent.Provider = provider

	// Docker config: config.json → env override
	agent.DockerEnabled = cfg.DockerEnabled
	if strings.ToLower(os.Getenv("DOCKER_ENABLED")) == "true" {
		agent.DockerEnabled = true
	} else if strings.ToLower(os.Getenv("DOCKER_ENABLED")) == "false" {
		agent.DockerEnabled = false
	}
	if cfg.DockerImage != "" {
		agent.DockerImage = cfg.DockerImage
	}
	if img := os.Getenv("DOCKER_IMAGE"); img != "" {
		agent.DockerImage = img
	}

	return agent
}
