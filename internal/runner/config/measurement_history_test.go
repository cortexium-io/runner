package config

import (
	"errors"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestInvestigationHistoryCannotDropRoundsOrReplaceCurrentDiagnostics(t *testing.T) {
	for _, file := range []string{"planned-investigation.json", "planned-retained-investigation.json", "retained-investigation.json"} {
		t.Run(file, func(t *testing.T) {
			base, original := plannedFixture(t, "jacks-break", file)
			for name, change := range map[string]func(*runner.Process, *runner.Project){
				"skip append": func(p *runner.Process, _ *runner.Project) {
					plannedStep(t, p, "measure").Routes["ready"] = runner.Route{Next: "review"}
				},
				"reset history": func(p *runner.Process, _ *runner.Project) {
					plannedStep(t, p, "measurement-history").Inputs["previous"] = runner.Binding{FromStep: "retain-measurement", Output: "artifact"}
				},
				"current report only": func(p *runner.Process, _ *runner.Project) {
					plannedStep(t, p, "review").Inputs["measurements"] = runner.Binding{FromStep: "measure", Output: "artifact"}
				},
				"lost owner history": func(p *runner.Process, _ *runner.Project) {
					plannedStep(t, p, "owner").Inputs["measurement_history"] = runner.Binding{FromStep: "baseline", Output: "artifact"}
				},
				"source mismatch hidden": func(p *runner.Process, project *runner.Project) {
					s := plannedStep(t, p, "measurement-history")
					role := project.Roles[s.Role]
					role.Execution.Command.Args[2] = "{scratch}"
					project.Roles[s.Role] = role
				},
				"helper shell": func(p *runner.Process, project *runner.Project) {
					s := plannedStep(t, p, "measurement-history")
					role := project.Roles[s.Role]
					role.Execution.Command.Args[0] = "/bin/sh"
					project.Roles[s.Role] = role
				},
			} {
				t.Run(name, func(t *testing.T) {
					p, project := runner.Clone(base), runner.Clone(original)
					change(&p, &project)
					if errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)) == nil {
						t.Fatal("incomplete or unbound history reached delivery")
					}
				})
			}
		})
	}
}

func TestHistoricalRetainedInvestigationWithoutHistoryRemainsValid(t *testing.T) {
	p, project := plannedFixture(t, "jacks-break", "retained-investigation.json")
	for i, s := range p.Steps {
		if s.ID == "measurement-history" {
			p.Steps = append(p.Steps[:i], p.Steps[i+1:]...)
			break
		}
	}
	owner := plannedStep(t, &p, "owner")
	delete(owner.Inputs, "measurement_history")
	role := project.Roles[owner.Role]
	delete(role.Inputs, "measurement_history")
	project.Roles[owner.Role] = role
	plannedStep(t, &p, "measure").Routes["ready"] = runner.Route{Next: "review"}
	plannedStep(t, &p, "review").Inputs["measurements"] = runner.Binding{FromStep: "measure", Output: "artifact"}
	if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
		t.Fatal("historical frozen contract was broadened or invalidated", err)
	}
}

func TestInvestigationReviewerSourceIdentityComesFromFixedWorkspaceHistory(t *testing.T) {
	for _, file := range []string{"planned-investigation.json", "planned-retained-investigation.json", "retained-investigation.json", "measured-investigation.json"} {
		t.Run(file, func(t *testing.T) {
			p, project := plannedFixture(t, "jacks-break", file)
			if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"review", "initial-review"} {
				for _, step := range p.Steps {
					if step.ID != id {
						continue
					}
					historyID := "measurement-history"
					if id == "initial-review" {
						historyID = "initial-history"
					}
					if step.Inputs["measurements"] != (runner.Binding{FromStep: historyID, Output: "artifact"}) || !measurementHistoryProfile(project, *plannedStep(t, &p, historyID), id == "initial-review") {
						t.Fatal("reviewer source identity did not come from the fixed --source workspace helper")
					}
				}
			}
		})
	}
}
