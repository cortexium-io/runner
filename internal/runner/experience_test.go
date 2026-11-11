package runner

import "testing"

func TestExperienceReviewSharesQualificationCorrectionUses(t *testing.T) {
	r := Run{
		Process: Process{Steps: []Step{
			{ID: "qualify", Role: "qualify", Routes: map[string]Route{"changes_requested": {Next: "owner", MaxTimes: 3}}},
			{
				ID: "experience", Role: "image",
				Inputs: map[string]Binding{"receipt": {FromStep: "qualify", Output: "receipt"}},
				Routes: map[string]Route{"changes_requested": {Next: "feedback", MaxTimes: 3}},
			},
			{ID: "feedback", Role: "feedback"},
		}},
		Roles:     map[string]Role{"image": {Execution: &Profile{Harness: "codex", Mode: "experience-review", Access: "workspace-read"}}},
		Step:      1,
		RouteUses: map[string]int{"qualify/changes_requested": 2},
	}
	c := &Core{}
	a := &Attempt{End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "changes_requested"}}}
	c.route(&r, a)
	if r.Step != 2 || r.RouteUses["qualify/changes_requested"] != 3 || r.RouteUses["experience/changes_requested"] != 0 {
		t.Fatal("visual review created an additional correction allowance", r.Status, r.Reason, r.RouteUses)
	}
	r.Step = 1
	c.route(&r, a)
	if r.Status != "Failed" || r.Reason != "repetition allowance exhausted: qualify/changes_requested" {
		t.Fatal("spent qualification allowance was replenished", r.Status, r.Reason)
	}
}
