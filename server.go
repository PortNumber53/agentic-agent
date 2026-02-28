package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	agentic "agentic-go/internal/agentic"
)

func startServer(systemContent string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/github", makeWebhookHandler("GitHub", systemContent))
	mux.HandleFunc("/webhook/gitlab", makeWebhookHandler("GitLab", systemContent))
	mux.HandleFunc("/webhook/slack", makeWebhookHandler("Slack", systemContent))
	mux.HandleFunc("/webhook/jira", makeWebhookHandler("Jira", systemContent))

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK\n")
	})

	fmt.Printf("%s[info] Starting agentic server on :20511...%s\n", agentic.ColorSystem, agentic.ColorReset)
	if err := http.ListenAndServe(":20511", mux); err != nil {
		fmt.Printf("%s[error] Server failed: %v%s\n", agentic.ColorError, err, agentic.ColorReset)
	}
}

func makeWebhookHandler(source string, systemContent string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) == 0 {
			http.Error(w, "Failed to read body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "Webhook received and processing started\n")

		go processWebhook(source, string(body), systemContent)
	}
}

func processWebhook(source string, payload string, baseSystemContent string) {
	fmt.Printf("\n%s[info] Processing new %s webhook%s\n", agentic.ColorSystem, source, agentic.ColorReset)

	agent := createNewAgent()

	// Start Docker session if needed for this agent run
	if agent.DockerEnabled && agentic.ActiveDockerSession == nil {
		_, err := agentic.StartDockerSession(agent.DockerImage)
		if err != nil {
			fmt.Printf("%s[error] Failed to start Docker session: %v%s\n", agentic.ColorError, err, agentic.ColorReset)
			agent.DockerEnabled = false
		}
	}

	systemContent := baseSystemContent
	if agent.DockerEnabled {
		systemContent += "\n\nIMPORTANT: Your shell commands are executed inside a Docker container (image: " + agent.DockerImage + "). You are NOT running on the host machine. All shell tool invocations run inside this container. The container persists for the duration of the chat session, so installed packages and file changes are preserved between commands."
	}

	agent.History = []agentic.Message{{Role: "system", Content: systemContent}}

	timestamp := time.Now().Format("20060102_150405")
	agent.HistoryFile = fmt.Sprintf(".agentic_webhook_%s_%s.json", strings.ToLower(source), timestamp)

	prompt := fmt.Sprintf("You received a %s webhook with the following payload:\n%s\n\nProcess this webhook completely and perform any requested actions. Only output 'TASK_COMPLETED' and nothing else when you are unequivocally done with processing.", source, payload)

	agent.AppendMessage(agentic.Message{Role: "user", Content: prompt})

	runAutonomousAgent(agent)
}

func runAutonomousAgent(agent *agentic.Agent) {
	maxIterations := 50
	for i := 1; i <= maxIterations; i++ {
		fmt.Printf("\n%s%s============================================================%s\n", agentic.ColorSystem, agentic.ColorBold, agentic.ColorReset)
		fmt.Printf("%s%s  Webhook Agent iteration %d/%d%s\n", agentic.ColorSystem, agentic.ColorBold, i, maxIterations, agentic.ColorReset)
		fmt.Printf("%s%s============================================================%s\n", agentic.ColorSystem, agentic.ColorBold, agentic.ColorReset)

		msg, err := agent.CallLLM()
		if err != nil {
			fmt.Printf("%s[error] LLM call failed: %v%s\n", agentic.ColorError, err, agentic.ColorReset)
			return
		}

		agent.AppendMessage(*msg)

		if msg.Content != "" {
			fmt.Printf("\n%s[webhook agent] %s%s\n", agentic.ColorAgent, msg.Content, agentic.ColorReset)
		}

		if len(msg.ToolCalls) == 0 {
			if strings.Contains(msg.Content, "TASK_COMPLETED") {
				fmt.Printf("\n%s%sWebhook processing finished successfully.%s\n", agentic.ColorAgent, agentic.ColorBold, agentic.ColorReset)
				return
			}
			fmt.Printf("\n%s[system] Injecting loop continuation prompt.%s\n", agentic.ColorSystem, agentic.ColorReset)
			agent.AppendMessage(agentic.Message{
				Role:    "user",
				Content: "Are you unequivocally done with the task requested? If not, continue working and outputting tool calls. If you are entirely finished and your goals are met, simply state 'TASK_COMPLETED' and nothing else.",
			})
			continue
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

	fmt.Printf("\n%s[warning] Webhook agent reached max iterations without finishing.%s\n", agentic.ColorError, agentic.ColorReset)
}
