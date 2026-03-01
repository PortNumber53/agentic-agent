package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentic "agentic-go/internal/agentic"
)

// JiraWebhookPayload represents the relevant fields from a Jira webhook event.
// It supports both standard Jira webhooks and 'Automation format' webhooks.
type JiraWebhookPayload struct {
	WebhookEvent string `json:"webhookEvent"`
	Timestamp    int64  `json:"timestamp"`
	// Standard format: issue is nested
	Issue struct {
		Key    string `json:"key"`
		Fields struct {
			Summary     string `json:"summary"`
			Description string `json:"description"`
			IssueType   struct {
				Name string `json:"name"`
			} `json:"issuetype"`
			Status struct {
				Name string `json:"name"`
			} `json:"status"`
			Labels  []string `json:"labels"`
			Project struct {
				Key string `json:"key"`
			} `json:"project"`
		} `json:"fields"`
	} `json:"issue"`
	// Automation format (sometimes flat or slightly different)
	// We'll use these as fallbacks in processJiraWebhook
	Key    string `json:"key"`
	Fields struct {
		Summary     string `json:"summary"`
		Description string `json:"description"`
		IssueType   struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Status struct {
			Name string `json:"name"`
		} `json:"status"`
		Labels  []string `json:"labels"`
		Project struct {
			Key string `json:"key"`
		} `json:"project"`
	} `json:"fields"`
}

// selectPersona determines which agent persona to use based on issue type and labels.
// Priority: labels override issue type.
func selectPersona(issueType string, labels []string) string {
	// Check labels first (highest priority)
	for _, label := range labels {
		l := strings.ToLower(label)
		switch {
		case l == "qa" || l == "testing" || l == "test":
			return "qa-engineer"
		case l == "docs" || l == "documentation" || l == "product":
			return "product-manager"
		case l == "dev" || l == "engineering" || l == "code":
			return "software-engineer"
		}
	}

	// Fall back to issue type mapping
	switch strings.ToLower(issueType) {
	case "bug", "test", "qa":
		return "qa-engineer"
	case "documentation", "epic":
		return "product-manager"
	case "story", "task", "sub-task", "subtask", "improvement":
		return "software-engineer"
	default:
		return "software-engineer" // default persona
	}
}

// buildJiraAgentPrompt constructs a detailed prompt for the Jira webhook agent
// based on the parsed payload and selected persona.
func buildJiraAgentPrompt(payload JiraWebhookPayload, persona string) string {
	issue := payload.Issue
	fields := issue.Fields

	var sb strings.Builder

	sb.WriteString("## Jira Issue Assigned to You\n\n")
	sb.WriteString(fmt.Sprintf("- **Issue Key**: %s\n", issue.Key))
	sb.WriteString(fmt.Sprintf("- **Summary**: %s\n", fields.Summary))
	sb.WriteString(fmt.Sprintf("- **Issue Type**: %s\n", fields.IssueType.Name))
	sb.WriteString(fmt.Sprintf("- **Current Status**: %s\n", fields.Status.Name))
	sb.WriteString(fmt.Sprintf("- **Project**: %s\n", fields.Project.Key))
	if len(fields.Labels) > 0 {
		sb.WriteString(fmt.Sprintf("- **Labels**: %s\n", strings.Join(fields.Labels, ", ")))
	}
	sb.WriteString(fmt.Sprintf("\n### Description\n%s\n\n", fields.Description))

	sb.WriteString("## Your Active Persona: " + persona + "\n\n")

	// Common instructions for all personas
	sb.WriteString("## Important Instructions\n\n")
	sb.WriteString("### Jira Status Updates\n")
	sb.WriteString("- Use `mcp_PROD-jira-thing_jiraIssueToolkit` with action `getTransitions` for issue `" + issue.Key + "` to discover available transitions.\n")
	sb.WriteString("- Then use action `transitionIssue` to move the issue through its workflow as you make progress.\n\n")

	sb.WriteString("### Clarification\n")
	sb.WriteString("- If the story description is vague, incomplete, or missing acceptance criteria, **post a Jira comment** using `mcp_PROD-jira-thing_jiraIssueToolkit` with action `addComment` on issue `" + issue.Key + "` explaining what needs clarification.\n")
	sb.WriteString("- Then output TASK_COMPLETED — do not guess at requirements.\n\n")

	// GitHub Integration
	cfg, err := agentic.LoadConfig()
	repoURL := ""
	if err == nil && cfg.JiraProjectRepos != nil {
		repoURL = cfg.JiraProjectRepos[fields.Project.Key]
	}

	if repoURL != "" {
		cloneURL := repoURL
		if strings.HasPrefix(cloneURL, "https://github.com/") {
			cloneURL = strings.Replace(cloneURL, "https://github.com/", "https://$GITHUB_TOKEN@github.com/", 1)
		}

		sb.WriteString("### GitHub Repository\n")
		sb.WriteString(fmt.Sprintf("- The repository for this project is: `%s`\n", repoURL))
		sb.WriteString("- Before making changes, check if it's already cloned in your workspace.\n")
		sb.WriteString(fmt.Sprintf("- If not, clone it: `git clone \"%s\" /workspace`\n\n", cloneURL))
	}

	// Persona-specific instructions
	if persona == "software-engineer" || persona == "qa-engineer" {
		sb.WriteString("### Branch Management\n")
		branchPrefix := "feature"
		if persona == "qa-engineer" {
			branchPrefix = "test"
		}
		sb.WriteString(fmt.Sprintf("- Check if a branch already exists: `git branch -a | grep -i %s`\n", issue.Key))
		sb.WriteString(fmt.Sprintf("- If no branch exists, create one: `git checkout main && git pull && git checkout -b %s/%s`\n", branchPrefix, issue.Key))
		sb.WriteString(fmt.Sprintf("- If the branch already exists, check it out: `git checkout %s/%s && git pull`\n\n", branchPrefix, issue.Key))

		sb.WriteString("### Syntax Validation (MANDATORY before any commit)\n")
		sb.WriteString("- Detect the project language from file extensions and build files.\n")
		sb.WriteString("- Run the appropriate syntax checker:\n")
		sb.WriteString("  - Go: `go vet ./...` and `go build ./...`\n")
		sb.WriteString("  - Python: `python -m py_compile <file>`\n")
		sb.WriteString("  - JavaScript/TypeScript: `node --check <file>` or `npx tsc --noEmit`\n")
		sb.WriteString("- **DO NOT commit if any syntax errors are found.** Fix them first.\n\n")

		sb.WriteString("### Commit & Push\n")
		sb.WriteString(fmt.Sprintf("- Stage and commit: `git add -A && git commit -m \"%s: <concise description>\"`\n", issue.Key))
		sb.WriteString(fmt.Sprintf("- Push: `git push origin %s/%s`\n\n", branchPrefix, issue.Key))
	}

	sb.WriteString("Process this task completely. Only output 'TASK_COMPLETED' and nothing else when you are unequivocally done.\n")
	sb.WriteString("If you are completely stuck and cannot proceed (e.g. missing permissions, missing information, reproducible failures you cannot fix), output 'TASK_BLOCKED: <explain reason>' and stop.\n")

	return sb.String()
}

// processJiraWebhook handles Jira webhooks with intelligent persona selection and
// specialized prompting for branch management, syntax validation, and status updates.
func processJiraWebhook(payload string, baseSystemContent string) {
	// Parse the Jira payload
	var jiraPayload JiraWebhookPayload
	if err := json.Unmarshal([]byte(payload), &jiraPayload); err != nil {
		fmt.Printf("%s[warning] Failed to parse Jira JSON: %v. Using generic handler.%s\n", agentic.ColorError, err, agentic.ColorReset)
		processWebhook("Jira", payload, baseSystemContent)
		return
	}

	// Normalize payload: if it was a flat automation payload, move fields into the Issue struct
	if jiraPayload.Issue.Key == "" && jiraPayload.Key != "" {
		fmt.Printf("%s[info] Detected flat (automation format) Jira payload for %s%s\n", agentic.ColorSystem, jiraPayload.Key, agentic.ColorReset)
		jiraPayload.Issue.Key = jiraPayload.Key
		jiraPayload.Issue.Fields.Summary = jiraPayload.Fields.Summary
		jiraPayload.Issue.Fields.Description = jiraPayload.Fields.Description
		jiraPayload.Issue.Fields.IssueType = jiraPayload.Fields.IssueType
		jiraPayload.Issue.Fields.Status = jiraPayload.Fields.Status
		jiraPayload.Issue.Fields.Labels = jiraPayload.Fields.Labels
		jiraPayload.Issue.Fields.Project = jiraPayload.Fields.Project
	}

	issueKey := jiraPayload.Issue.Key
	if issueKey == "" {
		truncated := payload
		if len(truncated) > 500 {
			truncated = truncated[:500] + "..."
		}
		fmt.Printf("%s[info] Jira webhook has no issue key. Event type: '%s'. Payload snippet: %s%s\n", agentic.ColorSystem, jiraPayload.WebhookEvent, truncated, agentic.ColorReset)
		processWebhook("Jira", payload, baseSystemContent)
		return
	}

	// Select persona based on issue type and labels
	persona := selectPersona(
		jiraPayload.Issue.Fields.IssueType.Name,
		jiraPayload.Issue.Fields.Labels,
	)

	fmt.Printf("%s[info] Jira issue %s (%s) → persona: %s%s\n",
		agentic.ColorSystem, issueKey, jiraPayload.Issue.Fields.IssueType.Name, persona, agentic.ColorReset)

	// Load persona template
	wd, _ := os.Getwd()
	personaPath := filepath.Join(wd, "agents", persona+".md")
	agentMD, err := os.ReadFile(personaPath)
	if err != nil {
		fmt.Printf("%s[warning] Persona template not found at %s, using base system content: %v%s\n", agentic.ColorError, personaPath, err, agentic.ColorReset)
		agentMD = []byte(baseSystemContent)
	}

	agent := createNewAgent()

	// Start Docker session if needed
	if agent.DockerEnabled && agentic.ActiveDockerSession == nil {
		_, err := agentic.StartDockerSession(agent.DockerImage, agent.GitHubToken)
		if err != nil {
			fmt.Printf("%s[error] Failed to start Docker session: %v%s\n", agentic.ColorError, err, agentic.ColorReset)
			agent.DockerEnabled = false
		}
	}

	// Build system content from persona template
	systemContent := string(agentMD)

	// Add MCP tool awareness
	var mcpNames []string
	for _, t := range agentic.DefinedTools {
		if strings.HasPrefix(t.Function.Name, "mcp_") {
			mcpNames = append(mcpNames, t.Function.Name)
		}
	}
	if len(mcpNames) > 0 {
		systemContent += fmt.Sprintf("\n\nAvailable MCP Tools: %s\nYou MUST use these specialized MCP tools when interacting with Jira and GitHub instead of using the generic 'web' or 'shell' tools for API calls.", strings.Join(mcpNames, ", "))
	}

	if agent.DockerEnabled {
		systemContent += "\n\nIMPORTANT: Your shell commands are executed inside a Docker container (image: " + agent.DockerImage + "). You are NOT running on the host machine."
	}

	agent.History = []agentic.Message{{Role: "system", Content: systemContent}}

	timestamp := time.Now().Format("20060102_150405")
	agent.HistoryFile = fmt.Sprintf(".agentic_webhook_jira_%s_%s.json", strings.ToLower(issueKey), timestamp)

	// Build the detailed prompt
	prompt := buildJiraAgentPrompt(jiraPayload, persona)

	agent.AppendMessage(agentic.Message{Role: "user", Content: prompt})

	fmt.Printf("%s[info] Starting Jira agent for %s with persona %s%s\n", agentic.ColorSystem, issueKey, persona, agentic.ColorReset)

	runAutonomousAgent(agent, issueKey)
}

func startServer(systemContent string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/github", makeWebhookHandler("GitHub", systemContent))
	mux.HandleFunc("/webhook/gitlab", makeWebhookHandler("GitLab", systemContent))
	mux.HandleFunc("/webhook/slack", makeWebhookHandler("Slack", systemContent))
	mux.HandleFunc("/webhook/jira", makeJiraWebhookHandler(systemContent))

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK\n")
	})

	fmt.Printf("%s[info] Starting agentic server on :20511...%s\n", agentic.ColorSystem, agentic.ColorReset)
	if err := http.ListenAndServe(":20511", mux); err != nil {
		fmt.Printf("%s[error] Server failed: %v%s\n", agentic.ColorError, err, agentic.ColorReset)
	}
}

// makeJiraWebhookHandler returns a handler specifically for Jira webhooks that routes
// to the specialized Jira processing pipeline.
func makeJiraWebhookHandler(systemContent string) http.HandlerFunc {
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
		fmt.Fprintf(w, "Jira webhook received and processing started\n")

		go processJiraWebhook(string(body), systemContent)
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
		_, err := agentic.StartDockerSession(agent.DockerImage, agent.GitHubToken)
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

	runAutonomousAgent(agent, "")
}

func runAutonomousAgent(agent *agentic.Agent, issueKey string) {
	maxIterations := agent.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 50
	}
	for i := 1; i <= maxIterations; i++ {
		fmt.Printf("\n%s[info] Webhook Agent iteration %d/%d%s\n", agentic.ColorSystem, i, maxIterations, agentic.ColorReset)

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
			if strings.Contains(msg.Content, "TASK_BLOCKED:") {
				idx := strings.Index(msg.Content, "TASK_BLOCKED:")
				reason := strings.TrimSpace(msg.Content[idx+len("TASK_BLOCKED:"):])
				fmt.Printf("\n%s%sWebhook agent is stuck. Reason: %s%s\n", agentic.ColorError, agentic.ColorBold, reason, agentic.ColorReset)

				if issueKey != "" {
					commentArgs := map[string]any{
						"action":       "/comment",
						"issueIdOrKey": issueKey,
						"commentBody":  fmt.Sprintf("Agent stopped tracking this issue.\nStatus: BLOCKED\nReason: %s", reason),
					}
					commentArgsBytes, _ := json.Marshal(commentArgs)
					agentic.ExecuteTool("mcp_PROD-jira-thing_jiraIssueToolkit", string(commentArgsBytes), true)
				}
				return
			}
			fmt.Printf("\n%s[system] Injecting loop continuation prompt.%s\n", agentic.ColorSystem, agentic.ColorReset)
			agent.AppendMessage(agentic.Message{
				Role:    "user",
				Content: "Are you unequivocally done with the task requested? If not, continue working and outputting tool calls. If you are entirely stuck, output 'TASK_BLOCKED: <reason>'. If you are entirely finished, state 'TASK_COMPLETED'.",
			})
			continue
		}

		for _, tc := range msg.ToolCalls {
			fnName := tc.Function.Name
			fnArgs := tc.Function.Arguments
			// If autonomous (webhook/server), skip manual approvals
			result := agentic.ExecuteTool(fnName, fnArgs, true)

			agent.AppendMessage(agentic.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       fnName,
				Content:    result,
			})
		}
	}

	fmt.Printf("\n%s[warning] Webhook agent reached max iterations without finishing.%s\n", agentic.ColorError, agentic.ColorReset)

	if issueKey != "" {
		commentArgs := map[string]any{
			"action":       "/comment",
			"issueIdOrKey": issueKey,
			"commentBody":  fmt.Sprintf("Agent stopped tracking this issue.\nStatus: MAX_ITERATIONS_REACHED\nReason: Reached maximum allocated iterations (%d) without completing the task.", maxIterations),
		}
		commentArgsBytes, _ := json.Marshal(commentArgs)
		agentic.ExecuteTool("mcp_PROD-jira-thing_jiraIssueToolkit", string(commentArgsBytes), true)
	}
}
