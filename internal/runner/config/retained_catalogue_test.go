package config

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestRetainedCatalogueKeepsAdoptionBothChecksAndProtectedRelease(t *testing.T) {
	p, project := productDeliveryFixture(t, "retained-catalogue.json")
	if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
		t.Fatal(err)
	}
	if p.ID != "deliver-retained-catalogue" || p.MaxAttempts != 28 || p.MaxAttemptsPerStep != 1 || p.InfrastructureRetries != 1 || p.AttemptTimeout != "20m" {
		t.Fatal("catalogue continuation changed its approved execution bounds")
	}
	if !reflect.DeepEqual(p.Inputs, map[string]string{
		"request": "text", "package": "text", "tests": "text", "frontend_tests": "text", "publication": "json", "patch": "artifact",
	}) {
		t.Fatal("catalogue continuation lost its retained source or separate check selections")
	}
	steps := map[string]runner.Step{}
	for i, id := range []string{"source-adopt", "admit", "owner", "frontend-check", "check", "review", "integrate", "publish", "qualify", "prepare"} {
		step := p.Steps[i]
		if step.ID != id {
			t.Fatalf("step %d = %s, want %s", i, step.ID, id)
		}
		steps[id] = step
	}
	frontend := project.Roles[steps["frontend-check"].Role].Execution
	if frontend == nil || frontend.Command == nil || !slices.Contains(frontend.Command.Args, "--serial") ||
		steps["owner"].Role != "catalogue-owner" || steps["review"].Role != "catalogue-review" || steps["check"].Role != "planned-go-check" || steps["frontend-check"].Role != "catalogue-frontend-check" ||
		steps["frontend-check"].Inputs["tests"] != (runner.Binding{FromInput: "frontend_tests"}) ||
		steps["check"].Inputs["tests"] != (runner.Binding{FromInput: "tests"}) ||
		steps["review"].Inputs["go_checks"] != (runner.Binding{FromStep: "check", Output: "artifact"}) ||
		steps["review"].Inputs["frontend_checks"] != (runner.Binding{FromStep: "frontend-check", Output: "artifact"}) {
		t.Fatal("review must receive full current proof from both checks")
	}
	for _, id := range []string{"frontend-check", "check", "review", "integrate"} {
		if steps[id].Inputs["patch"] != (runner.Binding{FromCandidate: true}) {
			t.Fatalf("%s does not consume the current cumulative candidate", id)
		}
	}
	for _, id := range []string{"frontend-check", "check", "review", "qualify"} {
		if steps[id].Routes["changes_requested"] != (runner.Route{Next: "owner", MaxTimes: 1}) || steps[id].Routes["blocked"] != (runner.Route{Next: "$fail"}) {
			t.Fatalf("%s lost the bounded correction or blocked route", id)
		}
	}
	if steps["integrate"].Inputs["verdict"] != (runner.Binding{FromStep: "review", Output: "verdict"}) ||
		!reflect.DeepEqual(steps["publish"].Inputs["previous"], runner.Binding{
			FromStep: "publish", Output: "receipt", Previous: true, Fallback: &runner.Binding{FromStep: "source-adopt", Output: "receipt"},
		}) {
		t.Fatal("integrate or publish lost its review or authenticated predecessor")
	}
	product, _ := productDeliveryFixture(t, "product.json")
	for i, step := range product.Steps {
		if step.ID == "qualify" {
			if !reflect.DeepEqual(p.Steps[8:], product.Steps[i:]) {
				t.Fatal("catalogue continuation changed qualification or the protected release suffix")
			}
			return
		}
	}
	t.Fatal("product workflow lost its qualification step")
}

func TestRetainedCatalogueRejectsAdoptionAndApprovalBypass(t *testing.T) {
	p, project := productDeliveryFixture(t, "retained-catalogue.json")
	for name, change := range map[string]func(*runner.Process){
		"model before adoption": func(p *runner.Process) { p.Steps[0], p.Steps[2] = p.Steps[2], p.Steps[0] },
		"publish from unverified input": func(p *runner.Process) {
			p.Steps[7].Inputs["previous"] = runner.Binding{FromInput: "publication"}
		},
		"merge without approval": func(p *runner.Process) {
			p.Steps[9].Routes["ready"] = runner.Route{Next: "merge"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			mutated := runner.Clone(p)
			change(&mutated)
			if err := validateReleaseProcess(mutated, project); err == nil {
				t.Fatal("unsafe release graph accepted")
			}
		})
	}
}
