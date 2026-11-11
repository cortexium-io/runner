package config

import (
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestExperienceDeliveryRejectsSkippedReviewEvidenceAndGates(t *testing.T) {
	base, project := plannedFixture(t, "jacks-break", "planned-frontend.json")
	for name, change := range map[string]func(*runner.Process, *runner.Project){
		"skip review": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "experience-evidence").Routes["ready"] = runner.Route{Next: "prepare"}
		},
		"skip native gate": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "experience-review").Routes["ready"] = runner.Route{Next: "prepare"}
		},
		"foreign receipt": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "experience-review").Inputs["receipt"] = runner.Binding{Literal: &runner.Value{Inline: []byte(`{}`)}}
		},
		"extra corrections": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "experience-review").Routes["changes_requested"] = runner.Route{Next: "experience-feedback", MaxTimes: 4}
		},
		"writer review": func(_ *runner.Process, p *runner.Project) {
			p.Roles["experience-review"].Execution.Access = "workspace-write"
		},
		"reused review": func(p *runner.Process, _ *runner.Project) { plannedStep(t, p, "experience-review").Reuse = true },
		"unreviewed verdict": func(p *runner.Process, _ *runner.Project) {
			value := runner.Text("APPROVE")
			plannedStep(t, p, "experience-gate").Inputs["verdict"] = runner.Binding{Literal: &value}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, roles := runner.Clone(base), runner.Clone(project)
			change(&p, &roles)
			if validateReleaseProcess(p, roles) == nil {
				t.Fatal("experience proof or protected decision bypass accepted")
			}
		})
	}
}
