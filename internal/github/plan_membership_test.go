package github

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlanMembershipAdoptsOnlyExactAssessmentSnapshot(t *testing.T) {
	for _, change := range []string{"valid", "approved", "foreign source", "closed", "draft", "dependency", "profile", "workspace", "missing snapshot"} {
		t.Run(change, func(t *testing.T) {
			p, parent, children := deliveryFixture(t)
			d, err := p.ValidatePlanDelivery(parent, append([]WorkItem{parent}, children...))
			if err != nil {
				t.Fatal(err)
			}
			request := PlanAmendmentRequest{ExpectedRevision: d.Revision, Reason: "Adopt this exact additional requirement", Manifest: d.Manifest}
			request.Manifest.Amendment++
			request.Manifest.Members = append(append([]PlanMember(nil), d.Manifest.Members...), PlanMember{ID: "new", Dependencies: []string{children[0].ID}, ImplementationProfile: "implementer", ProfileDigest: p.cfg.PlanProfileDigests["implementer"], ProfileReason: "Approved bounded follow-up"})
			before := WorkItem{ID: "new", Title: "Additional requirement", Body: "Implement only the approved additional requirement.", Repository: "owner/repo", URL: "https://github.com/owner/repo/issues/999", IssueState: "OPEN", Status: p.assessmentStatus(), Dependencies: []string{children[0].ID}}
			switch change {
			case "approved":
				before.Approval = "existing authority"
			case "foreign source":
				before.PlanningSourceID = "another"
			case "closed":
				before.IssueState = "CLOSED"
			case "draft":
				before.DraftContentID = "draft"
			case "dependency":
				before.Dependencies = nil
			case "profile":
				before.ImplementationProfile = "other"
			case "workspace":
				before.Branch = "runner/previous"
			}
			additions := []WorkItem{before}
			if change == "missing snapshot" {
				additions = nil
			}
			state, err := p.buildPlanAmendment(d, request, additions...)
			if change != "valid" {
				if err == nil {
					t.Fatal("non-adoptable snapshot acquired plan authority")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := p.ValidatePlanAmendmentState(state); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(state.Before[3], before) || !reflect.DeepEqual(state.Before[1:3], state.After[1:3]) {
				t.Fatal("adoption changed old contracts or lost the original intake snapshot")
			}
			adopted := state.After[3]
			if !strings.HasPrefix(adopted.Body, before.Body) || adopted.PlanningSourceID != parent.ID || adopted.PlanningBatchSize != 3 || adopted.PlanningItemIndex != 3 || adopted.Approval == "" || adopted.Status != p.readyStatus() {
				t.Fatalf("adoption did not establish exact authenticated provenance: %#v", adopted)
			}
			for _, old := range state.After[1:3] {
				if old.PlanningBatchSize != 2 {
					t.Fatal("original staging provenance rewritten")
				}
			}
			newDelivery, err := p.ValidatePlanDelivery(state.After[0], state.After)
			if err != nil || len(newDelivery.Children) != 3 {
				t.Fatalf("amended exact union invalid: %v", err)
			}
			if !amendmentIntermediate(adopted, before, adopted, false) {
				t.Fatal("recorded partial adoption not recoverable")
			}
			unknown := adopted
			unknown.ID = "unapproved-extra"
			if _, err := p.ValidatePlanDelivery(state.After[0], append(append([]WorkItem(nil), state.After...), unknown)); err == nil {
				t.Fatal("unknown extra member was ignored")
			}
		})
	}
}

func TestPlanMembershipRetirementPreservesHistoryButNeverCountsAsSuccess(t *testing.T) {
	p, parent, children := deliveryFixture(t)
	children[1].Status, children[1].Phase = p.backlogStatus(), PlanIntegratedPhase
	children[1].Branch, children[1].QACommit = "runner/second", strings.Repeat("b", 40)
	children[1].Result, children[1].QAFailures = "Historical finding", 2
	children[1] = signDeliveryFixture(t, p, children[1], "reviewer", "backlog")
	d, err := p.ValidatePlanDelivery(parent, append([]WorkItem{parent}, children...))
	if err != nil {
		t.Fatal(err)
	}
	request := PlanAmendmentRequest{ExpectedRevision: d.Revision, Reason: "Remove second outcome from delivery, retain implemented code", Manifest: d.Manifest}
	request.Manifest.Amendment++
	request.Manifest.Members = append([]PlanMember(nil), d.Manifest.Members...)
	request.Manifest.Members[1].Retired = true
	request.Manifest.Members[1].RetirementReason = "Explicitly no longer part of delivery; existing implementation remains"
	state, err := p.buildPlanAmendment(d, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ValidatePlanAmendmentState(state); err != nil {
		t.Fatal(err)
	}
	retired := state.After[2]
	if retired.Phase != PlanRetiredPhase || retired.Status == p.doneStatus() || retired.Body != children[1].Body || retired.QACommit != children[1].QACommit || retired.Branch != children[1].Branch || retired.Result != children[1].Result || retired.QAFailures != 2 {
		t.Fatal("retirement lost history or pretended delivery")
	}
	newDelivery, err := p.ValidatePlanDelivery(state.After[0], state.After)
	if err != nil || len(newDelivery.Children) != 1 || len(newDelivery.RetiredChildren) != 1 || len(newDelivery.AllChildren()) != 2 {
		t.Fatalf("active/retired union: %v", err)
	}
	if !p.PlanMembersIntegrated(newDelivery) {
		t.Fatal("retirement prevented remaining accepted work from delivery")
	}
	request.Manifest.Members[1].Retired = false
	request.Manifest.Members[1].RetirementReason = ""
	request.ExpectedRevision, request.Manifest.Amendment = newDelivery.Revision, newDelivery.Manifest.Amendment+1
	if _, err := p.buildPlanAmendment(newDelivery, request); err == nil {
		t.Fatal("retired row reactivated")
	}
	completed := state.After[0]
	completed.Status, completed.Phase, completed.PullRequest, completed.QACommit = p.doneStatus(), "", "https://github.com/owner/repo/pull/12", strings.Repeat("c", 40)
	completed = signDeliveryFixture(t, p, completed, "reviewer", "done")
	all := append([]WorkItem{completed}, state.After[1:]...)
	external := WorkItem{ID: "external", Dependencies: []string{retired.ID}}
	if p.hasSuccessfulOutcome(retired, all) || p.dependenciesSatisfied(external, all) {
		t.Fatal("retired scope satisfied external dependency after plan delivery")
	}
	progress := p.PlanningProgress(all)
	if len(progress.RetiredMembers) != 1 || len(progress.IntegratedUndelivered) != 0 {
		t.Fatal("retired history disappeared or became undelivered active work after delivery")
	}
	p.cfg.PlanProfileDigests = nil
	if _, err := p.validateCompletedPlanDelivery(completed, all); err != nil {
		t.Fatalf("profile retirement undid completed delivery: %v", err)
	}
	if _, err := p.requireCompletedPreRolloutWork(all); err != nil {
		t.Fatalf("intact retirement history prevented rollout: %v", err)
	}
	if p.hasSuccessfulOutcome(retired, all) {
		t.Fatal("retired scope gained dependency authority after policy change")
	}
	all[2].Status = p.doneStatus()
	if _, err := p.validateCompletedPlanDelivery(completed, all); err == nil {
		t.Fatal("retired member moved to Done retained completion authority")
	}
	// Removing a required dependency needs an explicit dependent-contract
	// amendment. Merely retiring its owner must fail before any state changes.
	bad := d.Manifest
	bad.Members = append([]PlanMember(nil), d.Manifest.Members...)
	bad.Members[0].Retired, bad.Members[0].RetirementReason = true, "Remove prerequisite"
	if _, err := FormatPlanManifest(bad); err == nil {
		t.Fatal("active dependent silently lost its prerequisite")
	}
}
