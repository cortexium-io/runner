package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestWorkflowValidateAndExplainUseTypedRules(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(configPath, completeCLITestConfig(t.TempDir())); err != nil {
		t.Fatal(err)
	}

	var validated bytes.Buffer
	if err := runWorkflow([]string{"validate", "--config", configPath}, &validated); err != nil {
		t.Fatalf("validate workflow: %v", err)
	}
	for _, expected := range []string{"Workflow valid: 9 lanes, 8 typed rules, 3 active role profiles", configPath} {
		if !strings.Contains(validated.String(), expected) {
			t.Fatalf("validation output omitted %q:\n%s", expected, validated.String())
		}
	}

	var explained bytes.Buffer
	if err := runWorkflow([]string{"explain", "--config", configPath}, &explained); err != nil {
		t.Fatalf("explain workflow: %v", err)
	}
	for _, expected := range []string{
		"Default Plan: Plan (plan)",
		"implement: card enters Ready (ready) -> run role implementer (implementer contract)",
		"review: card enters Agent QA (agent_qa) -> run role reviewer (reviewer contract); block on rejection 3",
		"pull_request.merged -> move card to Done (done)",
		"Mandatory Runner safety",
		"integration is serialized per repository and base branch",
	} {
		if !strings.Contains(explained.String(), expected) {
			t.Fatalf("explanation omitted %q:\n%s", expected, explained.String())
		}
	}
}

func TestReadOnlyConfigViewsEscapeTerminalControls(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.json")
	cfg := completeCLITestConfig(t.TempDir())
	plan := cfg.Workflow.Lanes["plan"]
	plan.Name = "Plan\x1b]8;;https://attacker.invalid\alink\x1b]8;;\a\r\u202e"
	cfg.Workflow.Lanes["plan"] = plan
	reviewer := cfg.Roles[config.WorkRoleReviewer]
	model := "review-model\x1b[2J\r\u202e"
	reviewer.Model = &model
	cfg.Roles[config.WorkRoleReviewer] = reviewer
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runWorkflow([]string{"explain", "--config", configPath}, &output); err != nil {
		t.Fatal(err)
	}
	if err := runRole([]string{"show", config.WorkRoleReviewer, "--config", configPath}, &output); err != nil {
		t.Fatal(err)
	}
	writeMetrics(&output, metricsOutput{
		RunnerID: "runner\x1b[2J", Project: &config.GitHubProjectConfig{Owner: "owner\r\u202e", Number: 7}, HistoryPath: "history\a",
	})
	writeWorkSection(&output, "Blocked", nil, true, "config\x1b]8;;https://attacker.invalid\a", "Ready")

	rendered := output.String()
	for _, control := range []string{"\x1b", "\r", "\a", "\u202e"} {
		if strings.Contains(rendered, control) {
			t.Fatalf("read-only config output retained %q in %q", control, rendered)
		}
	}
	for _, escaped := range []string{`\x1b`, `\r`, `\u202e`} {
		if !strings.Contains(rendered, escaped) {
			t.Fatalf("read-only config output omitted %q in %q", escaped, rendered)
		}
	}
}
