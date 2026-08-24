package setup

import "testing"

func TestPrerequisiteInstallerReturnsOnlyManualInstructionsForMissingTools(t *testing.T) {
	installer := NewPrerequisiteInstaller()
	installer.lookPath = func(command string) (string, error) {
		switch command {
		case "git":
			return "/usr/bin/git", nil
		default:
			return "", errNotFoundForTest
		}
	}
	steps, err := installer.Plan([]string{"git", "gh", "codex"})
	if err != nil {
		t.Fatalf("plan prerequisites: %v", err)
	}
	if len(steps) != 2 || steps[0].Tool != "gh" || steps[1].Tool != "codex" {
		t.Fatalf("unexpected install plan %#v", steps)
	}
	for _, step := range steps {
		if step.Instruction == "" {
			t.Fatalf("expected manual-only guidance, got %#v", step)
		}
	}
	if _, err := installer.Plan([]string{"curl"}); err == nil {
		t.Fatal("expected arbitrary tool installation to be rejected")
	}
}

func TestPrerequisiteInstallerNeverBootstrapsNodeForCodex(t *testing.T) {
	installer := NewPrerequisiteInstaller()
	installer.lookPath = func(command string) (string, error) { return "", errNotFoundForTest }
	steps, err := installer.Plan([]string{"codex"})
	if err != nil {
		t.Fatalf("plan Codex prerequisites: %v", err)
	}
	if len(steps) != 1 || steps[0].Tool != "codex" || steps[0].Instruction == "" {
		t.Fatalf("expected manual Codex installation without Node/npm, got %#v", steps)
	}
}

var errNotFoundForTest = &notFoundError{}

type notFoundError struct{}

func (*notFoundError) Error() string { return "not found" }
