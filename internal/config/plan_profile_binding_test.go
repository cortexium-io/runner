package config

import "testing"

func TestPlanProfileBindingIncludesEffectiveExecutionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"model", func(c *Config) {
			p := c.Roles[WorkRoleImplementer]
			m := "different-model"
			p.Model = &m
			c.Roles[WorkRoleImplementer] = p
		}},
		{"reasoning", func(c *Config) {
			p := c.Roles[WorkRoleImplementer]
			p.Reasoning = "medium"
			c.Roles[WorkRoleImplementer] = p
		}},
		{"timeout", func(c *Config) {
			p := c.Roles[WorkRoleImplementer]
			p.TimeoutSeconds++
			c.Roles[WorkRoleImplementer] = p
		}},
		{"access", func(c *Config) {
			p := c.Roles[WorkRoleImplementer]
			p.Access = RoleAccessHost
			c.Roles[WorkRoleImplementer] = p
		}},
		{"harness config", func(c *Config) {
			p := c.Roles[WorkRoleImplementer]
			p.HarnessConfig = HarnessConfigModeInherit
			c.Roles[WorkRoleImplementer] = p
		}},
		{"skills", func(c *Config) {
			p := c.Roles[WorkRoleImplementer]
			p.Skills = append(p.Skills, "another-skill")
			c.Roles[WorkRoleImplementer] = p
		}},
		{"MCP grants", func(c *Config) {
			p := c.Roles[WorkRoleImplementer]
			p.MCPServers = []string{"browser"}
			c.Roles[WorkRoleImplementer] = p
		}},
		{"safe tools", func(c *Config) {
			p := c.Roles[WorkRoleImplementer]
			off := false
			p.SafeTools = &off
			c.Roles[WorkRoleImplementer] = p
		}},
		{"harness command", func(c *Config) { c.Harnesses[0].Command = "/opt/other-codex" }},
		{"write root", func(c *Config) { c.Harnesses[0].WorkspaceWriteRoot = "/work/other-root" }},
		{"selected allowlist", func(c *Config) { c.PlannerImplementers = []string{"unavailable"} }},
		{"disabled harness", func(c *Config) { off := false; c.Harnesses[0].Enabled = &off }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := explicitTestConfig()
			before := cfg.planImplementationProfileDigests()[WorkRoleImplementer]
			if before == "" {
				t.Fatal("fixture has no resolved implementation profile")
			}
			tc.change(&cfg)
			if after := cfg.planImplementationProfileDigests()[WorkRoleImplementer]; after == before {
				t.Fatal("changed execution settings kept approved profile identity")
			}
		})
	}
}

func TestPlanProfileBindingResolvesInheritanceAndReachableLadder(t *testing.T) {
	cfg := explicitTestConfig()
	cfg.Roles["small"] = RoleConfig{Extends: WorkRoleImplementer, Description: "Small bounded work"}
	cfg.PlannerImplementers = []string{"small"}
	cfg.ImplementerLadder = []string{"small", WorkRoleImplementer}
	before := cfg.planImplementationProfileDigests()["small"]
	p := cfg.Roles[WorkRoleImplementer]
	model := "inherited-or-escalated-model"
	p.Model = &model
	cfg.Roles[WorkRoleImplementer] = p
	if before == "" || cfg.planImplementationProfileDigests()["small"] == before {
		t.Fatal("inherited and reachable execution settings were not bound")
	}
	// Override the first step: a later configured escalation still belongs to
	// the approved execution policy even when current work has no rejections.
	p = cfg.Roles["small"]
	firstModel := "first-step"
	p.Model = &firstModel
	cfg.Roles["small"] = p
	before = cfg.planImplementationProfileDigests()["small"]
	p = cfg.Roles[WorkRoleImplementer]
	otherModel := "different-escalation"
	p.Model = &otherModel
	cfg.Roles[WorkRoleImplementer] = p
	if cfg.planImplementationProfileDigests()["small"] == before {
		t.Fatal("changing only a reachable escalation did not invalidate approval")
	}
}

func TestPlanProfileBindingIgnoresDescriptionAndEquivalentDefaults(t *testing.T) {
	cfg := explicitTestConfig()
	before := cfg.planImplementationProfileDigests()[WorkRoleImplementer]
	p := cfg.Roles[WorkRoleImplementer]
	p.Description = "A more helpful operator description"
	on := true
	p.SafeTools = &on
	cfg.Roles[WorkRoleImplementer] = p
	if before == "" || cfg.planImplementationProfileDigests()[WorkRoleImplementer] != before {
		t.Fatal("non-execution prose or equivalent effective default changed binding")
	}
}
