package config

import (
	"strings"
	"testing"
)

func TestPiCodemodeDefaultsInheritanceAndHarnessBoundary(t *testing.T) {
	cfg := explicitTestConfig()
	enabled, disabled := true, false
	cfg.Harnesses = []HarnessConfig{{Kind: HarnessPiCLI, Command: "pi", Enabled: &enabled, WorkspaceWriteRoot: "/worktrees"}}
	cfg.Roles = RoleTemplate(HarnessPiCLI)
	role := cfg.Roles[WorkRoleReviewer]
	role.Codemode = &enabled
	cfg.Roles[WorkRoleReviewer] = role
	cfg.Roles["batched_reviewer"] = RoleConfig{Extends: WorkRoleReviewer}
	cfg.Roles["direct_reviewer"] = RoleConfig{Extends: WorkRoleReviewer, Codemode: &disabled}
	runtime, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		role string
		want bool
	}{{WorkRoleImplementer, false}, {WorkRoleReviewer, true}, {"batched_reviewer", true}, {"direct_reviewer", false}} {
		if got := runtime.Execution(tc.role, HarnessPiCLI, cfg.ProjectDir).Codemode; got != tc.want {
			t.Fatalf("%s Codemode = %t, want %t", tc.role, got, tc.want)
		}
	}
	for _, value := range []bool{true, false} {
		cfg = explicitTestConfig()
		role = cfg.Roles[WorkRoleReviewer]
		role.Codemode = &value
		cfg.Roles[WorkRoleReviewer] = role
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "requires the pi harness") {
			t.Fatalf("Codex Codemode override %t accepted: %v", value, err)
		}
	}
}
