package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// Captured with the v0.6.1 configuration implementation. The v0.7.0 payload
// differs only by the added Codemode:false field. These are release fixtures,
// deliberately independent of the binding projection under test.
const reviewerSettingsV061 = `{"TestSpecialist":null,"WorkspaceBaseRef":"origin/main","RoleAccess":"sandboxed","HarnessConfigMode":"isolated","Harness":{"kind":"codex","command":"codex","enabled":true,"workspace_write_root":"/worktrees"},"Skills":["runner-reviewer"],"MCPServers":null,"SafeTools":true,"PreserveReasoning":false,"ResourceLimits":{"SnapshotMaxEntries":100000,"SnapshotMaxFileBytes":67108864,"SnapshotMaxTotalBytes":1073741824},"RepositoryReferences":null,"ReferenceProtectedRoots":null,"ReviewEvidenceRoot":"","ReviewEvidencePaths":null}`

func TestEvidenceSettingsSurviveKnownReleaseSchemas(t *testing.T) {
	var cfg ExecutionConfig
	if err := json.Unmarshal([]byte(reviewerSettingsV061), &cfg); err != nil {
		t.Fatal(err)
	}
	previous := "8d09faf620b762833e361330e5659f9e5d677b03dc9b63ec7803aa8b72f92dee"
	current := "87cce7e3e3f87941374b136fec23d0690aca00693713511432eea891ef5c7eb0"
	if cfg.EvidenceSettingsDigest() != current {
		t.Fatal("unchanged contract no longer matches the v0.7.0 persisted binding")
	}
	for _, digest := range []string{previous, current} {
		if !cfg.MatchesEvidenceSettings(digest) {
			t.Fatal("unchanged policy invalidated release evidence")
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*ExecutionConfig)
	}{
		{"codemode enabled", func(c *ExecutionConfig) { c.Codemode = true }},
		{"access", func(c *ExecutionConfig) { c.RoleAccess = RoleAccessHost }},
		{"harness configuration", func(c *ExecutionConfig) { c.HarnessConfigMode = HarnessConfigModeInherit }},
		{"safe tools", func(c *ExecutionConfig) { c.SafeTools = false }},
		{"MCP grants", func(c *ExecutionConfig) { c.MCPServers = []string{"operator"} }},
		{"skills", func(c *ExecutionConfig) { c.Skills = []string{"replacement"} }},
		{"command", func(c *ExecutionConfig) { c.Harness.Command = "different-codex" }},
		{"base", func(c *ExecutionConfig) { c.WorkspaceBaseRef = "origin/other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := cfg
			tc.change(&changed)
			if changed.MatchesEvidenceSettings(previous) || changed.MatchesEvidenceSettings(current) {
				t.Fatal("changed contract inherited old evidence")
			}
		})
	}
}

func TestPlanProfileBindingsSurviveKnownReleaseSchemas(t *testing.T) {
	const previous = "v1:bb5544be79adce5da935dfd5a06bdd6282d6a9423fa0d055a25bace47faefc21"
	const current = "v1:e01860000f7f9c789f52f9fa654bec5633cd8b0ce75e511d99fac1d42590113f"
	cfg := explicitTestConfig()
	bindings, older := cfg.planImplementationProfileBindings()
	if bindings[WorkRoleImplementer] != current || older[WorkRoleImplementer] != previous {
		t.Fatal("unchanged profile lost a captured release binding")
	}
	for _, tc := range []struct {
		name   string
		change func(*RoleConfig)
	}{
		{"model", func(p *RoleConfig) { model := "other-model"; p.Model = &model }},
		{"reasoning", func(p *RoleConfig) { p.Reasoning = "low" }},
		{"deadline", func(p *RoleConfig) { p.TimeoutSeconds++ }},
		{"codemode enabled", func(p *RoleConfig) { enabled := true; p.Codemode = &enabled }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := explicitTestConfig()
			p := changed.Roles[WorkRoleImplementer]
			tc.change(&p)
			changed.Roles[WorkRoleImplementer] = p
			bindings, older := changed.planImplementationProfileBindings()
			if bindings[WorkRoleImplementer] == current || older[WorkRoleImplementer] == previous {
				t.Fatal("changed policy kept an old approved binding")
			}
		})
	}
	// A reachable enabled escalation cannot inherit the pre-codemode contract.
	cfg.Roles["small"] = RoleConfig{Extends: WorkRoleImplementer, Description: "Bounded work"}
	cfg.PlannerImplementers = []string{"small"}
	cfg.ImplementerLadder = []string{"small", WorkRoleImplementer}
	p := cfg.Roles[WorkRoleImplementer]
	enabled := true
	p.Codemode = &enabled
	cfg.Roles[WorkRoleImplementer] = p
	_, older = cfg.planImplementationProfileBindings()
	if strings.TrimSpace(older["small"]) != "" {
		t.Fatal("pre-codemode binding admitted a reachable enabled escalation")
	}
}
