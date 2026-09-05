package config

import (
	"strings"
	"testing"
)

func TestMaxReasoningIsCodexOnlyAndUnknownEffortsStillFailClosed(t *testing.T) {
	for _, harness := range []string{HarnessCodexCLI, HarnessClaudeCLI, HarnessPiCLI} {
		for _, effort := range []string{"max", "invented"} {
			t.Run(harness+"/"+effort, func(t *testing.T) {
				cfg := explicitTestConfig()
				enabled := true
				cfg.Harnesses = []HarnessConfig{{Kind: harness, Command: harness, Enabled: &enabled, WorkspaceWriteRoot: "/worktrees"}}
				cfg.Roles = RoleTemplate(harness)
				role := cfg.Roles[WorkRoleImplementer]
				role.Reasoning = effort
				cfg.Roles[WorkRoleImplementer] = role
				err := cfg.Validate()
				if harness == HarnessCodexCLI && effort == "max" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "reasoning") {
					t.Fatalf("expected reasoning validation failure, got %v", err)
				}
			})
		}
	}
}
