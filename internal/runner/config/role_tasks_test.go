package config

import (
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestNativeRoleConfiguration(t *testing.T) {
	base := runner.Profile{
		Harness: "native", Access: "workspace-write", Model: "gpt-6.1-sol",
		Reasoning: "high", MaxInput: 32 << 10, MaxOutput: 8 << 10,
		Native: &runner.NativeProfile{
			Harness: "codex", Tools: runner.CapabilitySelection{Mode: "native"},
			Skills: runner.CapabilitySelection{Mode: "native"},
		},
	}
	if err := ready(base); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*runner.Profile)
	}{
		{"missing policy", func(p *runner.Profile) { p.Native = nil }},
		{"missing model", func(p *runner.Profile) { p.Model = "" }},
		{"competing command", func(p *runner.Profile) { p.Command = &runner.Command{} }},
		{"invented access", func(p *runner.Profile) { p.Access = "all" }},
		{"oversized output", func(p *runner.Profile) { p.MaxOutput = 256 << 10 }},
		{"unenforced skill filter", func(p *runner.Profile) {
			p.Native.Skills = runner.CapabilitySelection{Mode: "selected", Names: []string{"genny-cli"}}
		}},
		{"claude writer", func(p *runner.Profile) {
			p.Native.Harness = "claude"
			p.Native.Tools = runner.CapabilitySelection{Mode: "selected", Names: []string{"Read"}}
		}},
		{"claude input-only with file tools", func(p *runner.Profile) {
			p.Access = "input-only"
			p.Native.Harness = "claude"
			p.Native.Tools = runner.CapabilitySelection{Mode: "selected", Names: []string{"Read"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := runner.Clone(base)
			tc.edit(&p)
			if err := ready(p); err == nil {
				t.Fatal("accepted an unsupported capability contract")
			}
		})
	}
	p := runner.Clone(base)
	p.Harness = "codex"
	if err := ready(p); err == nil {
		t.Fatal("restricted adapter accepted native settings")
	}
}

func TestRoleTaskPolicyConfiguration(t *testing.T) {
	project := runner.Project{
		Roles: map[string]runner.Role{
			"planner": {ID: "planner", Purpose: "Plan work", Execution: &runner.Profile{Harness: "deterministic"}},
			"writer":  {ID: "writer", Purpose: "Write work", Execution: &runner.Profile{Harness: "deterministic"}},
		},
	}
	process := runner.Process{Steps: []runner.Step{{ID: "lead", Role: "planner"}}, Tasks: &runner.TaskPolicy{
		Roles: []string{"planner", "writer"}, MaxTasks: 40, MaxDepth: 5, MaxRevisions: 4, MaxConcurrent: 3,
	}}
	if err := validateRoleTasks(process, project); err != nil {
		t.Fatal("general work should not inherit the legacy eight-card/source restrictions", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*runner.Process)
	}{
		{"missing role", func(p *runner.Process) { p.Tasks.Roles = []string{"missing"} }},
		{"duplicate role", func(p *runner.Process) { p.Tasks.Roles = []string{"writer", "writer"} }},
		{"unbounded depth", func(p *runner.Process) { p.Tasks.MaxDepth = 0 }},
		{"unbounded tasks", func(p *runner.Process) { p.Tasks.MaxTasks = 0 }},
		{"excess concurrency", func(p *runner.Process) { p.Tasks.MaxConcurrent = 33 }},
		{"competing root roles", func(p *runner.Process) { p.Steps = append(p.Steps, runner.Step{ID: "write", Role: "writer"}) }},
		{"mixed expansion", func(p *runner.Process) { p.Steps = []runner.Step{{ForEach: &runner.TaskGroup{}}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := runner.Clone(process)
			tc.edit(&p)
			if err := validateRoleTasks(p, project); err == nil {
				t.Fatal("accepted invalid task authority")
			}
		})
	}
	legacy := runner.Clone(project)
	legacy.Roles["writer"] = runner.Role{ID: "writer", Purpose: "Wrong workspace checker", Execution: &runner.Profile{Harness: "command"}}
	if err := validateRoleTasks(process, legacy); err == nil {
		t.Fatal("general task silently selected an adapter that materializes legacy source")
	}
}
