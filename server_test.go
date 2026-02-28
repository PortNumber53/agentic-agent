package main

import (
	"encoding/json"
	"testing"
)

func TestSelectPersona(t *testing.T) {
	tests := []struct {
		name      string
		issueType string
		labels    []string
		want      string
	}{
		{"Story maps to software-engineer", "Story", nil, "software-engineer"},
		{"Task maps to software-engineer", "Task", nil, "software-engineer"},
		{"Bug maps to qa-engineer", "Bug", nil, "qa-engineer"},
		{"Documentation maps to product-manager", "Documentation", nil, "product-manager"},
		{"Epic maps to product-manager", "Epic", nil, "product-manager"},
		{"Unknown defaults to software-engineer", "CustomType", nil, "software-engineer"},
		{"Label qa overrides Story", "Story", []string{"qa"}, "qa-engineer"},
		{"Label docs overrides Task", "Task", []string{"docs"}, "product-manager"},
		{"Label dev overrides Bug", "Bug", []string{"dev"}, "software-engineer"},
		{"Case insensitive issue type", "story", nil, "software-engineer"},
		{"Case insensitive label", "Story", []string{"QA"}, "qa-engineer"},
		{"First matching label wins", "Story", []string{"testing", "docs"}, "qa-engineer"},
		{"Sub-task maps to software-engineer", "Sub-task", nil, "software-engineer"},
		{"Improvement maps to software-engineer", "Improvement", nil, "software-engineer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectPersona(tt.issueType, tt.labels)
			if got != tt.want {
				t.Errorf("selectPersona(%q, %v) = %q, want %q", tt.issueType, tt.labels, got, tt.want)
			}
		})
	}
}

func TestBuildJiraAgentPrompt(t *testing.T) {
	var payload JiraWebhookPayload
	payload.WebhookEvent = "jira:issue_updated"
	payload.Issue.Key = "TEST-42"
	payload.Issue.Fields.Summary = "Add login feature"
	payload.Issue.Fields.Description = "Implement OAuth login"
	payload.Issue.Fields.IssueType.Name = "Story"
	payload.Issue.Fields.Status.Name = "To Do"
	payload.Issue.Fields.Project.Key = "TEST"

	prompt := buildJiraAgentPrompt(payload, "software-engineer")

	// Check that key elements are present
	checks := []string{
		"TEST-42",
		"Add login feature",
		"Implement OAuth login",
		"Branch Management",
		"Syntax Validation",
		"MANDATORY before any commit",
		"Jira Status Updates",
		"Clarification",
		"feature/TEST-42",
		"TASK_COMPLETED",
	}

	for _, check := range checks {
		if !contains(prompt, check) {
			t.Errorf("prompt missing expected content: %q", check)
		}
	}

	// QA persona should use test/ prefix
	qaPrompt := buildJiraAgentPrompt(payload, "qa-engineer")
	if !contains(qaPrompt, "test/TEST-42") {
		t.Errorf("QA prompt should use test/ branch prefix")
	}

	// Product manager should NOT have branch management
	pmPrompt := buildJiraAgentPrompt(payload, "product-manager")
	if contains(pmPrompt, "Branch Management") {
		t.Errorf("Product manager prompt should NOT include branch management")
	}
}

func TestJiraPayloadNormalization(t *testing.T) {
	// Standard format
	standardJSON := `{"webhookEvent":"jira:issue_updated","issue":{"key":"PROJ-1","fields":{"summary":"Test summary"}}}`
	var p1 JiraWebhookPayload
	json.Unmarshal([]byte(standardJSON), &p1)
	if p1.Issue.Key != "PROJ-1" {
		t.Errorf("Expected standard key PROJ-1, got %s", p1.Issue.Key)
	}

	// Flat (Automation) format
	flatJSON := `{"key":"PROJ-2","fields":{"summary":"Flat summary"}}`
	var p2 JiraWebhookPayload
	json.Unmarshal([]byte(flatJSON), &p2)

	// Simulation of normalization logic from server.go
	if p2.Issue.Key == "" && p2.Key != "" {
		p2.Issue.Key = p2.Key
		p2.Issue.Fields.Summary = p2.Fields.Summary
	}

	if p2.Issue.Key != "PROJ-2" {
		t.Errorf("Expected normalized key PROJ-2, got %s", p2.Issue.Key)
	}
	if p2.Issue.Fields.Summary != "Flat summary" {
		t.Errorf("Expected normalized summary 'Flat summary', got %s", p2.Issue.Fields.Summary)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsSubstr(s, substr)
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
