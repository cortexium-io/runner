package github

import (
	"reflect"
	"testing"
)

func TestAmendmentClosureUsesOldAndNewDependencyGraphs(t *testing.T) {
	old := []PlanMember{{ID: "a"}, {ID: "b", Dependencies: []string{"a"}}, {ID: "c", Dependencies: []string{"b"}}, {ID: "d"}}
	updated := []PlanMember{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d", Dependencies: []string{"b"}}}
	got := amendmentDependencyClosure(map[string]bool{"a": true}, old, updated)
	if !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("lost removed/transitive/new edge: %v", got)
	}
}

func TestPlanAmendmentExactMembershipAndConservativeSharedScope(t *testing.T) {
	for _, kind := range []string{"shared criteria", "last member only", "changed ordinal", "add member", "remove member", "retarget", "unexpected body"} {
		t.Run(kind, func(t *testing.T) {
			p, parent, children := deliveryFixture(t)
			d, err := p.ValidatePlanDelivery(parent, append([]WorkItem{parent}, children...))
			if err != nil {
				t.Fatal(err)
			}
			req := PlanAmendmentRequest{ExpectedRevision: d.Revision, Reason: "Explicit revised acceptance", Manifest: d.Manifest}
			req.Manifest.Amendment++
			switch kind {
			case "shared criteria":
				req.Manifest.SuccessCriteria = []string{"Both behaviors work, including revised behavior"}
			case "last member only":
				req.Manifest.Members = append([]PlanMember(nil), req.Manifest.Members...)
				req.Manifest.Members[1].ProfileReason = "Revised bounded task rationale"
			case "changed ordinal":
				req.Manifest.Amendment++
			case "add member":
				req.Manifest.Members = append(req.Manifest.Members, PlanMember{ID: "extra"})
			case "remove member":
				req.Manifest.Members = req.Manifest.Members[:1]
			case "retarget":
				req.Manifest.DestinationBranch = "other"
			case "unexpected body":
				req.MemberBodies = map[string]string{"other": "new"}
			}
			state, err := p.buildPlanAmendment(d, req)
			if kind != "shared criteria" && kind != "last member only" {
				if err == nil {
					t.Fatal("unsafe amendment accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := p.ValidatePlanAmendmentState(state); err != nil {
				t.Fatal(err)
			}
			want := []string{children[1].ID}
			if kind == "shared criteria" {
				want = []string{children[0].ID, children[1].ID}
			}
			if !reflect.DeepEqual(state.Affected, want) {
				t.Fatalf("affected=%v want%v", state.Affected, want)
			}
			if kind == "last member only" && !reflect.DeepEqual(state.Before[1], state.After[1]) {
				t.Fatal("unaffected integrated child changed")
			}
			if _, err := p.ValidatePlanDelivery(state.After[0], state.After); err != nil {
				t.Fatalf("new release invalid: %v", err)
			}
			for i, before := range state.Before {
				if before.QAFailures != state.After[i].QAFailures || before.Result != state.After[i].Result {
					t.Fatal("history/counts reset")
				}
			}
		})
	}
}

func TestAmendmentIntermediateRefusesUnownedChanges(t *testing.T) {
	before := WorkItem{ID: "card", Body: "old", Status: "Backlog", Phase: PlanDeliveryPhase, Result: "history", QAFailures: 2}
	after := before
	after.Body = "new"
	after.Approval = "newauthority"
	after.Phase = PlanDeliveryPhase
	partial := before
	partial.Body = after.Body
	partial.Phase = PlanAmendingPhase
	partial.Transition = transitionLockValue
	if !amendmentIntermediate(partial, before, after, true) {
		t.Fatal("recorded intermediate state refused")
	}
	partial.Result = "operator feedback"
	if amendmentIntermediate(partial, before, after, true) {
		t.Fatal("unowned operator change would be overwritten")
	}
}
