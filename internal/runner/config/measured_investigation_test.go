package config

import (
	"errors"
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestMeasuredInvestigationClosedIntakePreservesReviewAndReleaseBoundaries(t *testing.T) {
	p, project := plannedFixture(t, "jacks-break", "measured-investigation.json")
	if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
		t.Fatal(err)
	}
	if len(p.Inputs) != 13 || p.MaxAttempts != 18 || p.MaxAttemptsPerStep != 1 {
		t.Fatal("measured continuation changed its closed intake or attempt ceiling")
	}
	for _, role := range project.Roles {
		if len(role.Inputs) > 8 {
			t.Fatal("root intake changed a role's input bound", role.ID)
		}
		if role.Execution.Harness == "codex" && (role.Execution.MaxInput != 8192 || role.Execution.MaxOutput != 512) {
			t.Fatal("measurement handoff increased model allowance")
		}
	}
	initial := plannedStep(t, &p, "initial-review")
	if initial.Inputs["patch"] != (runner.Binding{FromInput: "patch"}) || initial.Inputs["checks"] != (runner.Binding{FromInput: "checks"}) || initial.Inputs["before_measurements"] != (runner.Binding{FromInput: "measurement1"}) || initial.Routes["ready"].Next != "initial-integrate" || plannedStep(t, &p, "admit").Routes["ready"].Next != initial.ID {
		t.Fatal("initial independent review was replaced by coding or mutable evidence")
	}
	publication := plannedStep(t, &p, "publish")
	if !reflect.DeepEqual(publication.Inputs["candidate"], runner.Binding{FromStep: "integrate", Output: "candidate", Fallback: &runner.Binding{FromStep: "initial-integrate", Output: "candidate"}}) {
		t.Fatal("initial and corrected approval paths do not publish their own candidates")
	}
	// The new structural exception must not widen the ordinary process bound.
	p.ID = "ordinary-intake"
	if validateProcess(p, project) == nil {
		t.Fatal("thirteen-input exception leaked into generic processes")
	}
}

func TestMeasuredInvestigationRejectsEvidenceAuthorityAndBudgetBypasses(t *testing.T) {
	original, roles := plannedFixture(t, "jacks-break", "measured-investigation.json")
	for name, change := range map[string]func(*runner.Process, *runner.Project){
		"larger intake":           func(p *runner.Process, _ *runner.Project) { p.Inputs["extra"] = "artifact" },
		"larger envelope":         func(p *runner.Process, _ *runner.Project) { p.MaxAttempts = 19 },
		"source before authority": func(p *runner.Process, _ *runner.Project) { p.Steps[0], p.Steps[1] = p.Steps[1], p.Steps[0] },
		"skip initial review": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "admit").Routes["ready"] = runner.Route{Next: "owner"}
		},
		"reject becomes approval": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "initial-review").Routes["changes_requested"] = runner.Route{Next: "initial-integrate"}
		},
		"replace pristine baseline": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "initial-review").Inputs["baseline_measurements"] = runner.Binding{FromInput: "measurement1"}
		},
		"replace first before": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "review").Inputs["before_measurements"] = runner.Binding{FromInput: "measurement3"}
		},
		"omit history": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "review").Inputs["measurements"] = runner.Binding{FromStep: "measure", Output: "artifact"}
		},
		"reset history": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "measurement-history").Inputs["previous"] = runner.Binding{FromStep: "initial-history", Output: "artifact"}
		},
		"stale corrected checks": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "review").Inputs["checks"] = runner.Binding{FromInput: "checks"}
		},
		"stale corrected patch": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "integrate").Inputs["patch"] = runner.Binding{FromInput: "patch"}
		},
		"unreviewed publication": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "publish").Inputs["candidate"] = runner.Binding{FromStep: "owner", Output: "summary"}
		},
		"merge gate": func(p *runner.Process, _ *runner.Project) {
			r := plannedStep(t, p, "prepare").Routes["ready"]
			r.Approval = false
			plannedStep(t, p, "prepare").Routes["ready"] = r
		},
		"production gate": func(p *runner.Process, _ *runner.Project) {
			r := plannedStep(t, p, "backup").Routes["ready"]
			r.Approval = false
			plannedStep(t, p, "backup").Routes["ready"] = r
		},
		"source hash not checked": func(p *runner.Process, project *runner.Project) {
			s := plannedStep(t, p, "initial-history")
			role := project.Roles[s.Role]
			role.Execution.Command.Args[2] = "{scratch}"
			project.Roles[s.Role] = role
		},
		"reordered reports": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "initial-history").Inputs["measurement3"] = runner.Binding{FromInput: "measurement2"}
		},
		"history network": func(p *runner.Process, project *runner.Project) {
			s := plannedStep(t, p, "measurement-history")
			role := project.Roles[s.Role]
			role.Execution.Command.Network = true
			project.Roles[s.Role] = role
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, project := runner.Clone(original), runner.Clone(roles)
			change(&p, &project)
			if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err == nil {
				t.Fatal("unsafe measured continuation admitted")
			}
		})
	}
}
