package config

import (
	"errors"
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestRetainedFrontendRepairKeepsStrictChecksAndProtectedDelivery(t *testing.T) {
	p, project := productDeliveryFixture(t, "retained-frontend-repair.json")
	if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
		t.Fatal(err)
	}
	if p.MaxAttempts != 35 || p.MaxAttemptsPerStep != 1 || p.InfrastructureRetries != 1 || p.AttemptTimeout != "20m" {
		t.Fatal("repair changed its approved limits")
	}
	steps := map[string]runner.Step{}
	for _, s := range p.Steps {
		steps[s.ID] = s
		if s.Role == "delivery-measure" {
			t.Fatal("frontend-only repair acquired an unrelated Go benchmark")
		}
	}
	if p.Steps[0].ID != "seed" || steps["seed"].Inputs["patch"] != (runner.Binding{FromInput: "patch"}) || steps["admit"].Routes["ready"].Next != "baseline-report" || steps["baseline-report"].Inputs["evidence"] != (runner.Binding{FromInput: "baseline_checks"}) {
		t.Fatal("source restoration or failed diagnostic provenance lost")
	}
	if !reflect.DeepEqual(steps["owner"].Inputs["checks"], runner.Binding{FromStep: "check", Output: "artifact", Previous: true, Fallback: &runner.Binding{FromStep: "baseline-report", Output: "artifact"}}) || steps["check"].Role != "frontend-check" || steps["check"].Routes["blocked"].Next != "$fail" || steps["check"].Routes["ready"].Next != "review" || steps["review"].Inputs["checks"] != (runner.Binding{FromStep: "check", Output: "artifact"}) {
		t.Fatal("failed baseline became candidate acceptance or full report handoff changed")
	}
	// The credential-bearing suffix is byte-for-byte the qualified retained workflow.
	original, _ := productDeliveryFixture(t, "retained-frontend.json")
	suffix := func(p runner.Process) []runner.Step {
		for i, s := range p.Steps {
			if s.ID == "integrate" {
				return p.Steps[i:]
			}
		}
		return nil
	}
	if !reflect.DeepEqual(suffix(p), suffix(original)) {
		t.Fatal("repair changed independent integration, qualification or owner approval gates")
	}
	for _, id := range []string{"frontend-repair-owner", "frontend-repair-review"} {
		role := project.Roles[id]
		if role.Execution.Model != "gpt-6.1-sol" || role.Execution.Reasoning != "xhigh" || role.Execution.MaxInput != 8<<10 || role.Execution.MaxOutput != 512 {
			t.Fatal("model bounds changed", id)
		}
	}
}
