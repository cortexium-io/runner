package config

import (
	"strings"
	"testing"
)

func TestSpecialistPolicyPathsAndDisabledDefault(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []string
		valid bool
	}{
		{"focused", []string{"tests/unit.test.ts", "src/widget.test.ts", "fixtures/widgets.json"}, true},
		{"directory grant", []string{"tests"}, false},
		{"configuration", []string{"tests/vite.config.ts"}, false},
		{"missing", nil, false},
		{"whole repository", []string{"."}, false},
		{"escape", []string{"../tests"}, false},
		{"absolute", []string{"/tests"}, false},
		{"overlap", []string{"tests", "tests/a"}, false},
		{"git", []string{"tests/.git/index"}, false},
		{"controls", []string{"tests/AGENTS.md"}, false},
		{"dependency", []string{"package.json"}, false},
		{"pattern", []string{"tests/*.ts"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := explicitTestConfig()
			before := cfg.planImplementationProfileDigests()[WorkRoleImplementer]
			cfg.TestSpecialist = &TestSpecialistConfig{Enabled: true, AllowedPaths: tc.paths}
			resolved, err := cfg.Resolve()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
			if !tc.valid {
				return
			}
			if cfg.planImplementationProfileDigests()[WorkRoleImplementer] == before {
				t.Fatal("enabled specialist not bound to approval")
			}
			execution := resolved.Execution(WorkRoleImplementer, cfg.Roles[WorkRoleImplementer].Harness, "/work")
			if execution.TestSpecialist == nil {
				t.Fatal("missing implementer policy")
			}
			execution.TestSpecialist.AllowedPaths[0] = "changed"
			if resolved.TestSpecialist.AllowedPaths[0] == "changed" || cfg.TestSpecialist.AllowedPaths[0] == "changed" {
				t.Fatal("policy aliases mutable caller data")
			}
			cfg.TestSpecialist.Enabled = false
			if cfg.planImplementationProfileDigests()[WorkRoleImplementer] != before {
				t.Fatal("disabled policy broadened authority")
			}
		})
	}
}

func TestSpecialistRefusesOnlyEnabledReachableUnsupportedImplementers(t *testing.T) {
	for _, tc := range []struct {
		name, harness, reach string
		enabled, valid       bool
	}{
		{"native Codex", HarnessCodexCLI, "default", true, true},
		{"native Claude", HarnessClaudeCLI, "default", true, true},
		{"Pi default", HarnessPiCLI, "default", true, false},
		{"Pi planner selection", HarnessPiCLI, "planner", true, false},
		{"Pi repair ladder", HarnessPiCLI, "ladder", true, false},
		{"Pi stored but unreachable", HarnessPiCLI, "stored", true, true},
		{"Pi disabled specialist", HarnessPiCLI, "default", false, true},
		{"Pi reviewer only", HarnessPiCLI, "reviewer", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := explicitTestConfig()
			cfg.TestSpecialist = &TestSpecialistConfig{Enabled: tc.enabled, AllowedPaths: []string{"tests/focused.test.ts"}}
			if tc.harness != HarnessCodexCLI {
				enabled := true
				cfg.Harnesses = append(cfg.Harnesses, HarnessConfig{Kind: tc.harness, Command: tc.harness, Enabled: &enabled, WorkspaceWriteRoot: "/worktrees"})
			}
			profile := cfg.Roles[WorkRoleImplementer]
			profile.Harness = tc.harness
			profile.Description = "Explicit existing implementation profile"
			switch tc.reach {
			case "default":
				cfg.Roles[WorkRoleImplementer] = profile
			case "planner", "ladder", "stored":
				profile.Extends = WorkRoleImplementer
				cfg.Roles["selected"] = profile
				if tc.reach == "planner" {
					cfg.PlannerImplementers = []string{"selected"}
				} else if tc.reach == "ladder" {
					cfg.ImplementerLadder = []string{WorkRoleImplementer, "selected"}
				}
			case "reviewer":
				profile = cfg.Roles[WorkRoleReviewer]
				profile.Harness = tc.harness
				cfg.Roles[WorkRoleReviewer] = profile
			}
			_, err := cfg.Resolve()
			if tc.valid && err != nil {
				t.Fatalf("supported or unenabled policy refused: %v", err)
			}
			if !tc.valid && (err == nil || !strings.Contains(err.Error(), "test_specialist") || !strings.Contains(err.Error(), "pi")) {
				t.Fatalf("unsupported reachable adapter did not fail config resolution: %v", err)
			}
		})
	}
}
