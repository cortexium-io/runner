package config

import (
	"errors"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestRetainedProductChecksRestoreBeforeAdmissionAndKeepOriginalBaseline(t *testing.T) {
	p, project := productDeliveryFixture(t, "retained-product.json")
	validate := func(p runner.Process, project runner.Project) error {
		return errors.Join(validateProcess(p, project), validateReleaseProcess(p, project))
	}
	if err := validate(p, project); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"retained-measure", "admit", "retained-frontend-check", "retained-check", "owner", "frontend-check", "check", "measure", "review", "integrate", "publish"} {
		if p.Steps[i].ID != id {
			t.Fatal("retained preparation lost its source/evidence order", id)
		}
	}
	first, owner, measure, review := p.Steps[0], p.Steps[4], p.Steps[7], p.Steps[8]
	for _, arg := range project.Roles[first.Role].Execution.Command.Args {
		if arg == "--baseline" || arg == "--baseline=true" {
			t.Fatal("retained candidate was relabelled as the original baseline")
		}
	}
	if p.MaxAttempts != 35 || p.MaxAttemptsPerStep != 1 || p.AttemptTimeout != "20m" || len(p.Inputs) != 7 || len(project.Roles[owner.Role].Inputs) != 8 ||
		first.Role != "delivery-measure" || first.Inputs["patch"] != (runner.Binding{FromInput: "patch"}) ||
		first.Inputs["previous"].Previous || first.Inputs["previous"].Default == nil || string(first.Inputs["previous"].Default.Inline) != `""` ||
		owner.Inputs["baseline"] != (runner.Binding{FromInput: "original_baseline"}) ||
		review.Inputs["baseline_measurements"] != (runner.Binding{FromInput: "original_baseline"}) {
		t.Fatal("candidate measurement was confused with original evidence or changed the ceilings")
	}
	for _, binding := range []runner.Binding{owner.Inputs["measurement"], measure.Inputs["previous"]} {
		if binding.Fallback == nil || binding.Fallback.FromStep != "retained-measure" || !binding.Previous {
			t.Fatal("current candidate rounds lost their initial measured handoff", binding)
		}
	}
	for name, mutate := range map[string]func(*runner.Process, *runner.Project){
		"missing seed":        func(p *runner.Process, _ *runner.Project) { p.Steps = p.Steps[1:] },
		"model before checks": func(p *runner.Process, _ *runner.Project) { p.Steps[1].Routes["ready"] = runner.Route{Next: "owner"} },
		"prefix skips source failure": func(p *runner.Process, _ *runner.Project) {
			p.Steps[2].Routes["changes_requested"] = runner.Route{Next: "review"}
		},
		"prefix uses unavailable candidate": func(p *runner.Process, _ *runner.Project) {
			p.Steps[2].Inputs["patch"] = runner.Binding{FromCandidate: true}
		},
		"prefix reseeds source": func(p *runner.Process, _ *runner.Project) {
			p.Steps[2].Inputs["patch"] = runner.Binding{FromInput: "original_baseline"}
		},
		"prefix network": func(_ *runner.Process, project *runner.Project) {
			project.Roles["frontend-check"].Execution.Command.Network = true
		},
		"prefix source writes": func(_ *runner.Process, project *runner.Project) {
			project.Roles["frontend-check"].Execution.Access = "workspace-write"
		},
		"prefix replay": func(p *runner.Process, _ *runner.Project) { p.Steps[3].Reuse = true },
		"qualification repairs wrong phase": func(p *runner.Process, _ *runner.Project) {
			p.Steps[11].Routes["changes_requested"] = runner.Route{Next: "retained-check", MaxTimes: 1}
		},
		"merge approval": func(p *runner.Process, _ *runner.Project) { p.Steps[12].Routes["ready"] = runner.Route{Next: "merge"} },
	} {
		t.Run(name, func(t *testing.T) {
			bad, roles := runner.Clone(p), runner.Clone(project)
			mutate(&bad, &roles)
			if validate(bad, roles) == nil {
				t.Fatal("unsafe retained continuation accepted")
			}
		})
	}
}
