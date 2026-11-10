package config

import (
	"slices"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestRetainedFrontendDiagnosticUsesTwoReadOnlyChecks(t *testing.T) {
	p, project := productDeliveryFixture(t, "frontend-retained-diagnostic.json")
	if err := validateProcess(p, project); err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 2 || p.MaxAttempts != 2 || p.MaxAttemptsPerStep != 1 {
		t.Fatal("expected one attempt for the suite and one for the isolated case")
	}
	for _, step := range p.Steps {
		role := project.Roles[step.Role].Execution
		if role.Harness != "command" || role.Access != "workspace-read" || role.Command == nil || role.Command.Network ||
			!slices.Contains(role.Command.Args, "--serial") || step.Inputs["patch"] != (runner.Binding{FromInput: "patch"}) {
			t.Fatalf("%s must read the same retained patch offline with one worker", step.ID)
		}
	}
	if slices.Contains(project.Roles[p.Steps[0].Role].Execution.Command.Args, "--test-name") ||
		p.Steps[1].Inputs["test_name"] != (runner.Binding{FromInput: "case_name"}) ||
		p.Steps[0].Routes["changes_requested"].Next != p.Steps[1].ID || p.Steps[1].Routes["changes_requested"].Next != "$complete" {
		t.Fatal("a test failure must retain diagnostic evidence without filtering the full suite")
	}
}
