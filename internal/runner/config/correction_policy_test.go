package config

import (
	"errors"
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestFrontendAndResearchSourceCorrectionPolicyIsThreeTotal(t *testing.T) {
	for _, file := range []string{"frontend.json", "retained-frontend.json", "research-nonogram.json", "retained-research-nonogram.json"} {
		t.Run(file, func(t *testing.T) {
			p, project := productDeliveryFixture(t, file)
			if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
				t.Fatal(err)
			}
			if p.MaxAttempts != 35 || p.MaxAttemptsPerStep != 1 || p.AttemptTimeout != "20m" || p.InfrastructureRetries != 1 {
				t.Fatal("correction policy changed execution limits")
			}
			corrections := 0
			for _, s := range p.Steps {
				if s.ID == "check" || s.ID == "review" || s.ID == "qualify" {
					if s.Routes["changes_requested"] != (runner.Route{Next: "owner", MaxTimes: 3}) {
						t.Fatal("source correction must return to actual owner, at most three times total", s.ID)
					}
					corrections++
				}
				if s.ID == "measure" && s.Routes["continue"] != (runner.Route{Next: "owner", MaxTimes: 1}) {
					t.Fatal("measurement is a separate one-use bound")
				}
			}
			if corrections < 2 {
				t.Fatal("fixture omitted configured check/review edges")
			}
		})
	}
	for _, file := range []string{"process.json", "product.json", "retained-product.json"} {
		p, _ := productDeliveryFixture(t, file)
		for _, s := range p.Steps {
			if r := s.Routes["changes_requested"]; r.Next == "owner" && r.MaxTimes > 0 && r.MaxTimes != 1 {
				t.Fatal("unrelated delivery policy changed", file, s.ID)
			}
		}
	}
}

func TestQualificationThreeUseBoundKeepsExactOwnerAndProtectedGates(t *testing.T) {
	p, project := productDeliveryFixture(t, "frontend.json")
	for _, tc := range []struct {
		name  string
		route runner.Route
		valid bool
	}{
		{"legacy one", runner.Route{Next: "owner", MaxTimes: 1}, true},
		{"three total", runner.Route{Next: "owner", MaxTimes: 3}, true},
		{"unbounded", runner.Route{Next: "owner"}, false},
		{"four", runner.Route{Next: "owner", MaxTimes: 4}, false},
		{"approval bypass", runner.Route{Next: "owner", MaxTimes: 3, Approval: true}, false},
		{"skip checks", runner.Route{Next: "publish", MaxTimes: 3}, false},
		{"self retry", runner.Route{Next: "qualify", MaxTimes: 3}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := runner.Clone(p)
			for i := range changed.Steps {
				if changed.Steps[i].ID == "qualify" {
					changed.Steps[i].Routes["changes_requested"] = tc.route
				}
			}
			if err := validateReleaseProcess(changed, project); (err == nil) != tc.valid {
				t.Fatal("qualification bound changed closed suffix", err)
			}
		})
	}
	for _, id := range []string{"prepare", "backup", "inspect-migration", "inspect"} {
		changed := runner.Clone(p)
		for i := range changed.Steps {
			if changed.Steps[i].ID == id {
				for outcome, route := range changed.Steps[i].Routes {
					if route.Approval {
						route.Approval = false
						changed.Steps[i].Routes[outcome] = route
					}
				}
			}
		}
		if reflect.DeepEqual(changed, p) || validateReleaseProcess(changed, project) == nil {
			t.Fatal("three corrections waived a protected approval", id)
		}
	}
}
