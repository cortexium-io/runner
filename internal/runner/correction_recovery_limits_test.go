package runner

import "testing"

func TestCorrectionRecoveryMeansThreeTotalUsesOnlyForSourceBackedges(t *testing.T) {
	r := Run{
		Process: Process{Steps: []Step{
			{ID: "owner", Role: "owner"},
			{ID: "check", Role: "check", Inputs: map[string]Binding{"patch": {FromCandidate: true}}, Routes: map[string]Route{"changes_requested": {Next: "owner", MaxTimes: 1}}},
		}},
		Roles: map[string]Role{
			"owner": {Execution: &Profile{Harness: "codex", Access: "workspace-write"}},
			"check": {Execution: &Profile{Harness: "command", Access: "workspace-read", Command: &Command{Args: []string{"/fixture/check"}}}},
		},
		Step: 1, Status: "Failed", RouteUses: map[string]int{"check/changes_requested": 3},
		CorrectionRecovery: &CorrectionRecovery{MaxUses: 3, RouteLimits: map[string]int{"check/changes_requested": 3}},
	}
	a := &Attempt{End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "changes_requested"}}}
	c := &Core{}
	c.route(&r, a)
	if r.Status != "Failed" || r.Reason != "repetition allowance exhausted: check/changes_requested" || r.RouteUses["check/changes_requested"] != 3 || r.Process.Steps[1].Routes["changes_requested"].MaxTimes != 1 {
		t.Fatal("grant reset counts, changed definition or meant three additional uses", r)
	}
	route := r.Process.Steps[1].Routes["changes_requested"]
	route.Approval = true
	if routeLimit(r, 1, "ready", route) != 1 {
		t.Fatal("unexpected route lookup")
	}
	r.Process.Steps[1].Routes["changes_requested"] = route
	if routeLimit(r, 1, "changes_requested", route) != 1 {
		t.Fatal("recovery granted an approval edge")
	}
}
