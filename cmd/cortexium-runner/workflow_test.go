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
