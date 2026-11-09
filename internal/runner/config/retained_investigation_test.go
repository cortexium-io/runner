package config

import (
	"errors"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestRetainedInvestigationPreservesEvidenceAndPublicationWithoutRepeatingCards(t *testing.T) {
	p, project := plannedFixture(t, "jacks-break", "retained-investigation.json")
	if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
		t.Fatal(err)
	}
	if p.MaxAttempts != 35 || len(p.Inputs) != 8 {
		t.Fatal("continuation changed allowance/input bounds")
	}
	for _, s := range p.Steps {
		if s.ForEach != nil || s.Role == "planned-plan" {
			t.Fatal("retained continuation repeats planning/cards")
		}
	}
	if plannedStep(t, &p, "owner").Inputs["manifest"].FromInput != "manifest" || plannedStep(t, &p, "review").Inputs["baseline_measurements"].FromInput != "original_baseline" {
		t.Fatal("original accepted evidence was regenerated")
	}
}

func TestRetainedInvestigationRejectsAuthorityAndBeforeEvidenceBypasses(t *testing.T) {
	base, original := plannedFixture(t, "jacks-break", "retained-investigation.json")
	command := func(p *runner.Process, project *runner.Project, step string, change func(*runner.Command)) {
		s := plannedStep(t, p, step)
		role := runner.Clone(project.Roles[s.Role])
		change(role.Execution.Command)
		project.Roles[s.Role] = role
	}
	for name, change := range map[string]func(*runner.Process, *runner.Project){
		"budget":                  func(p *runner.Process, _ *runner.Project) { p.MaxAttempts = 36 },
		"source before authority": func(p *runner.Process, _ *runner.Project) { p.Steps[0], p.Steps[1] = p.Steps[1], p.Steps[0] },
		"source-only adoption": func(p *runner.Process, project *runner.Project) {
			s := plannedStep(t, p, "publication-reference")
			r := project.Roles[s.Role]
			r.Execution.Mode = "source-adopt"
			project.Roles[s.Role] = r
		},
		"unverified first report copy": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "retained-measure").Routes["changes_requested"] = runner.Route{Next: "retain-measurement"}
		},
		"model before admission": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "retained-measure").Routes["changes_requested"] = runner.Route{Next: "owner"}
		},
		"missing original baseline": func(p *runner.Process, _ *runner.Project) { delete(p.Inputs, "original_baseline") },
		"replacement manifest": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "owner").Inputs["manifest"] = runner.Binding{FromInput: "patch"}
		},
		"before copy after owner": func(p *runner.Process, _ *runner.Project) { p.Steps[3], p.Steps[5] = p.Steps[5], p.Steps[3] },
		"unbounded collection repair": func(p *runner.Process, _ *runner.Project) {
			r := plannedStep(t, p, "measure").Routes["changes_requested"]
			r.MaxTimes = 4
			plannedStep(t, p, "measure").Routes["changes_requested"] = r
		},
		"task publication authority": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "publish").Inputs["previous"] = runner.Binding{FromInput: "publication"}
		},
		"latest receipt omitted": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "publish").Inputs["previous"] = runner.Binding{FromStep: "publication-reference", Output: "receipt"}
		},
		"weakened evidence": func(p *runner.Process, project *runner.Project) {
			s := plannedStep(t, p, "measure")
			r := runner.Clone(project.Roles[s.Role])
			r.Execution.Command.Args = r.Execution.Command.Args[:len(r.Execution.Command.Args)-1]
			project.Roles[s.Role] = r
		},
		"duplicate false strict flag": func(p *runner.Process, project *runner.Project) {
			command(p, project, "measure", func(c *runner.Command) { c.Args = append(c.Args, "--require-evidence=false") })
		},
		"duplicate false source flag": func(p *runner.Process, project *runner.Project) {
			command(p, project, "retained-measure", func(c *runner.Command) { c.Args = append(c.Args, "--bind-source=false") })
		},
		"source substitution": func(p *runner.Process, project *runner.Project) {
			command(p, project, "measure", func(c *runner.Command) { c.Args[4] = "{scratch}" })
		},
		"previous round substitution": func(p *runner.Process, project *runner.Project) {
			command(p, project, "measure", func(c *runner.Command) { c.Args[12] = "runner-measure round=2 " })
		},
		"measurement environment override": func(p *runner.Process, project *runner.Project) {
			command(p, project, "measure", func(c *runner.Command) { c.Env["GOFLAGS"] = "-overlay=/other/source" })
		},
		"copy maps failure to success": func(p *runner.Process, project *runner.Project) {
			command(p, project, "retain-measurement", func(c *runner.Command) { c.ExitOutcomes = map[int]string{1: "ready"} })
		},
		"check maps failure to success": func(p *runner.Process, project *runner.Project) {
			command(p, project, "check", func(c *runner.Command) { c.ExitOutcomes[1] = "ready" })
		},
		"check selector substitution": func(p *runner.Process, project *runner.Project) {
			command(p, project, "retained-check", func(c *runner.Command) { c.Args[6] = "^$" })
		},
		"owner deadline increase": func(p *runner.Process, _ *runner.Project) {
			plannedStep(t, p, "owner").AttemptTimeout = "1h"
		},
		"merge approval": func(p *runner.Process, _ *runner.Project) {
			r := plannedStep(t, p, "prepare").Routes["ready"]
			r.Approval = false
			plannedStep(t, p, "prepare").Routes["ready"] = r
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, project := runner.Clone(base), runner.Clone(original)
			change(&p, &project)
			if errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)) == nil {
				t.Fatal("unsafe retained workflow accepted")
			}
		})
	}
}
