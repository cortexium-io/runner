package config

import (
	"errors"
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestPlannedInvestigationRetainsFirstMeasurementWithoutChangingAuthority(t *testing.T) {
	for _, file := range []string{"planned-investigation.json", "planned-retained-investigation.json"} {
		t.Run(file, func(t *testing.T) {
			p, project := plannedFixture(t, "jacks-break", file)
			if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
				t.Fatal(err)
			}
			copy := plannedStep(t, &p, "retain-measurement")
			owner := plannedStep(t, &p, "owner")
			measure := plannedStep(t, &p, "measure")
			review := plannedStep(t, &p, "review")
			indices := map[string]int{}
			for i, step := range p.Steps {
				indices[step.ID] = i
			}
			if indices["join"] >= indices[copy.ID] || indices[copy.ID] >= indices[owner.ID] || indices[owner.ID] >= indices[measure.ID] || indices[measure.ID] >= indices[review.ID] || plannedStep(t, &p, "join").Routes["ready"].Next != "check" {
				t.Fatal("copy must precede correction owner so later backedges preserve it")
			}
			if measure.Routes["continue"] != (runner.Route{Next: copy.ID, MaxTimes: 1}) || copy.Routes["ready"] != (runner.Route{Next: owner.ID}) || !reflect.DeepEqual(copy.Inputs["measurement"], runner.Binding{FromStep: measure.ID, Output: "artifact", Previous: true, Fallback: &runner.Binding{FromStep: "baseline", Output: "artifact"}}) {
				t.Fatal("first measurement lost its bounded, previous-result copy handoff")
			}
			role := project.Roles[copy.Role]
			if !reflect.DeepEqual(role.Inputs, map[string]string{"measurement": "artifact"}) || !reflect.DeepEqual(role.Outputs, map[string]string{"check": "text", "artifact": "artifact"}) || role.Execution.Harness != "command" || role.Execution.Access != "workspace-read" || role.Execution.Command.Network ||
				!reflect.DeepEqual(role.Execution.Command.Args, []string{"/bin/cp", "{input.measurement}", "{scratch}/before-measurements.json"}) || role.Execution.Command.Artifact != "before-measurements.json" {
				t.Fatal("measurement retention acquired source writes, model work or arbitrary execution")
			}
			reviewer := project.Roles[review.Role]
			if len(reviewer.Inputs) != 8 || reviewer.Inputs["measurement"] != "" || reviewer.Inputs["before_measurements"] != "artifact" || review.Inputs["before_measurements"] != (runner.Binding{FromStep: copy.ID, Output: "artifact"}) || review.Inputs["measurements"] != (runner.Binding{FromStep: measure.ID, Output: "artifact"}) || review.Inputs["baseline_measurements"] != (runner.Binding{FromStep: "baseline", Output: "artifact"}) {
				t.Fatal("review conflated original, first candidate and current measurements")
			}
			for _, step := range []runner.Step{*owner, *review} {
				profile := project.Roles[step.Role].Execution
				if profile.MaxInput != 8<<10 || profile.MaxOutput != 512 || profile.Model != "gpt-6.1-sol" || profile.Reasoning != "xhigh" {
					t.Fatal("measurement retention increased model budgets")
				}
			}
			if reviewer.Execution.Access != "workspace-read" || project.Roles[owner.Role].Execution.Access != "workspace-write" || p.MaxAttempts != 64 || p.MaxAttemptsPerStep != 1 || p.InfrastructureRetries != 1 || p.AttemptTimeout != "20m" ||
				!plannedStep(t, &p, "prepare").Routes["ready"].Approval || !plannedStep(t, &p, "backup").Routes["ready"].Approval {
				t.Fatal("measurement retention changed frozen ceilings or protected approvals")
			}
		})
	}
}

func TestPlannedInvestigationRejectsUnfrozenMeasurementHandoffs(t *testing.T) {
	base, original := plannedFixture(t, "jacks-break", "planned-investigation.json")
	for name, change := range map[string]func(*runner.Process, *runner.Project){
		"copy after owner": func(p *runner.Process, _ *runner.Project) {
			copyIndex, ownerIndex := -1, -1
			for i, step := range p.Steps {
				if step.ID == "retain-measurement" {
					copyIndex = i
				}
				if step.ID == "owner" {
					ownerIndex = i
				}
			}
			p.Steps[copyIndex], p.Steps[ownerIndex] = p.Steps[ownerIndex], p.Steps[copyIndex]
		},
		"original instead of candidate": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "retain-measurement").Inputs["measurement"] = runner.Binding{FromStep: "baseline", Output: "artifact"}
		},
		"copy before actual measurement": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "join").Routes["ready"] = runner.Route{Next: "retain-measurement"}
		},
		"not previous": func(p *runner.Process, _ *runner.Project) {
			b := plannedStep(t, p, "retain-measurement").Inputs["measurement"]
			b.Previous = false
			plannedStep(t, p, "retain-measurement").Inputs["measurement"] = b
		},
		"source write": func(_ *runner.Process, project *runner.Project) {
			role := project.Roles["retain-measurement"]
			role.Execution.Access = "workspace-write"
			project.Roles[role.ID] = role
		},
		"arbitrary copy command": func(_ *runner.Process, project *runner.Project) {
			role := project.Roles["retain-measurement"]
			role.Execution.Command.Args[0] = "/bin/sh"
			project.Roles[role.ID] = role
		},
		"unbounded copy visits": func(p *runner.Process, _ *runner.Project) {
			route := plannedStep(t, p, "measure").Routes["continue"]
			route.MaxTimes = 2
			plannedStep(t, p, "measure").Routes["continue"] = route
		},
		"review current as before": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "review").Inputs["before_measurements"] = runner.Binding{FromStep: "measure", Output: "artifact"}
		},
		"review before fallback": func(p *runner.Process, _ *runner.Project) {
			b := plannedStep(t, p, "review").Inputs["before_measurements"]
			b.Fallback = &runner.Binding{FromStep: "baseline", Output: "artifact"}
			plannedStep(t, p, "review").Inputs["before_measurements"] = b
		},
		"unprotected merge": func(p *runner.Process, _ *runner.Project) {
			route := plannedStep(t, p, "prepare").Routes["ready"]
			route.Approval = false
			plannedStep(t, p, "prepare").Routes["ready"] = route
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, project := runner.Clone(base), runner.Clone(original)
			change(&p, &project)
			if errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)) == nil {
				t.Fatal("unfrozen before/after handoff was accepted")
			}
		})
	}
}
