package config

import (
	"errors"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestFrontendEvidenceAlwaysExistsBeforeOwnerAndBlockedCannotRepair(t *testing.T) {
	for _, name := range []string{"frontend.json", "retained-frontend.json"} {
		t.Run(name, func(t *testing.T) {
			p, project := productDeliveryFixture(t, name)
			validate := func(p runner.Process) error {
				return errors.Join(validateProcess(p, project), validateReleaseProcess(p, project))
			}
			if err := validate(p); err != nil {
				t.Fatal(err)
			}
			steps := map[string]runner.Step{}
			for _, s := range p.Steps {
				steps[s.ID] = s
				if s.Role == "frontend-check" || s.Role == "frontend-source-check" {
					if s.Routes["blocked"] != (runner.Route{Next: "$fail"}) {
						t.Fatal("unestablished execution must stop", s.ID)
					}
				}
			}
			checks := steps["owner"].Inputs["checks"]
			initial := "baseline"
			if name == "retained-frontend.json" {
				initial = "retained-check"
				if p.Steps[0].ID != "seed" || steps["seed"].Inputs["patch"] != (runner.Binding{FromInput: "patch"}) || steps["retained-check"].Inputs["patch"] != (runner.Binding{FromInput: "patch"}) {
					t.Fatal("retained patch must seed before readiness")
				}
			} else if p.Steps[0].ID != "admit" || len(project.Roles[steps[initial].Role].Inputs) != 1 {
				t.Fatal("fresh baseline must check admitted source without a fake patch")
			}
			if p.MaxAttempts != 35 || p.MaxAttemptsPerStep != 1 || p.AttemptTimeout != "20m" || p.InfrastructureRetries != 1 ||
				checks.FromStep != "check" || checks.Output != "artifact" || !checks.Previous || checks.Default != nil || checks.Fallback == nil ||
				*checks.Fallback != (runner.Binding{FromStep: initial, Output: "artifact"}) || project.Roles["frontend-owner"].Inputs["checks"] != "artifact" {
				t.Fatal("full evidence handoff or existing ceilings changed")
			}
			bad := runner.Clone(p)
			for i := range bad.Steps {
				if bad.Steps[i].ID == initial {
					bad.Steps[i].Routes["blocked"] = runner.Route{Next: "owner"}
				}
			}
			if validate(bad) == nil {
				t.Fatal("inconclusive prefix execution was routed to model repair")
			}
		})
	}
}

func TestResearchIsEvidenceOnlyAndKeepsRealBaselineSeparate(t *testing.T) {
	p, project := productDeliveryFixture(t, "research-nonogram.json")
	if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
		t.Fatal(err)
	}
	if p.MaxAttempts != 35 || p.MaxAttemptsPerStep != 1 || p.AttemptTimeout != "20m" || p.InfrastructureRetries != 1 || len(p.Steps) != 6 ||
		p.Steps[0].Inputs["benchmark"] != (runner.Binding{FromInput: "baseline_benchmark"}) || p.Steps[4].Inputs["benchmark"] != (runner.Binding{FromInput: "benchmark"}) ||
		p.Steps[5].Routes["ready"] != (runner.Route{Next: "$complete"}) || p.Outputs["conclusion"] != (runner.Binding{FromStep: "review", Output: "conclusion"}) {
		t.Fatal("research lost its distinct baseline, bounded evidence endpoint or conclusion")
	}
	if p.Outputs["checks"] != (runner.Binding{FromStep: "check", Output: "artifact"}) {
		t.Fatal("research omitted its scoped proof artifact")
	}
	for _, step := range p.Steps {
		profile := project.Roles[step.Role].Execution
		if profile.Harness == "release" || profile.Harness == "git-candidate" || step.ID == "publish" || step.ID == "integrate" {
			t.Fatal("research acquired product delivery authority")
		}
	}
	retained, roles := productDeliveryFixture(t, "retained-research-nonogram.json")
	if err := validateProcess(retained, roles); err != nil {
		t.Fatal(err)
	}
	owner := retained.Steps[2]
	if len(roles.Roles[owner.Role].Inputs) != 8 || owner.Inputs["prior_patch"] != (runner.Binding{FromInput: "prior_patch"}) || retained.Steps[0].Inputs["patch"].FromInput != "" {
		t.Fatal("retained reference was made a late workspace seed")
	}
}
