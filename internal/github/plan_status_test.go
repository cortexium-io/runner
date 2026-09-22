package github

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlanningProgressDistinguishesIntegratedFromDeliveredWithoutMutation(t *testing.T) {
	p, parent, children := deliveryFixture(t)
	all := append([]WorkItem{parent}, children...)
	before := append([]WorkItem(nil), all...)
	progress := p.PlanningProgress(all)
	if len(progress.IntegratedUndelivered) != 1 || progress.IntegratedUndelivered[0].ID != children[0].ID || len(progress.PlanningCompleted) != 0 {
		t.Fatalf("integrated work was not clearly undelivered: %#v", progress)
	}
	if !reflect.DeepEqual(all, before) {
		t.Fatal("status changed stored lifecycle")
	}
	parent.Status, parent.Phase, parent.PullRequest, parent.QACommit = "Done", "", "https://github.com/owner/repo/pull/10", strings.Repeat("c", 40)
	parent = signDeliveryFixture(t, p, parent, "reviewer", "done")
	if got := p.PlanningProgress(append([]WorkItem{parent}, children...)); len(got.IntegratedUndelivered) != 0 || len(got.PlanningCompleted) != 0 {
		t.Fatal("delivered plan or its members relabeled as unfinished planning")
	}
	// A raw phase or forged lifecycle cannot create an accepted-work claim.
	all[1].Approval = "invalid"
	if got := p.PlanningProgress(all); len(got.IntegratedUndelivered) != 0 {
		t.Fatal("unverified integration displayed as accepted work")
	}
}

func TestPlanningProgressPreservesHistoricalPlanningCompletion(t *testing.T) {
	p, _, _ := deliveryFixture(t)
	parent := WorkItem{ID: "PVTI_old", Title: "Historical planning", Body: "Original request", Repository: "owner/repo", Status: "Done"}
	children := []WorkItem{{ID: "PVTI_old_child", Title: "Pending work", Body: "Pending work", Repository: "owner/repo", Status: "Ready",
		PlanningSourceID: parent.ID, PlanningSourceLane: "plan", PlanningSourceFingerprint: PlanningSourceFingerprint(parent),
		PlanningDestination: "Ready", PlanningBatchFingerprint: "v1:old", PlanningBatchSize: 1, PlanningItemIndex: 1}}
	var err error
	parent.Approval, err = p.signPlanningBatch(parent, children, batchReleasedState, "generation")
	if err != nil {
		t.Fatal(err)
	}
	all := append([]WorkItem{parent}, children...)
	progress := p.PlanningProgress(all)
	if len(progress.PlanningCompleted) != 1 || progress.PlanningCompleted[0].ID != parent.ID || len(progress.IntegratedUndelivered) != 0 {
		t.Fatalf("historical completion hidden or relabeled: %#v", progress)
	}
	if p.planningSourceWorkCompleted(parent, all) {
		t.Fatal("completed planning became successful delivery")
	}
	all[0].Body = "Altered request"
	if got := p.PlanningProgress(all); len(got.PlanningCompleted) != 0 {
		t.Fatal("altered historical proposal retained authenticated display")
	}
}

func TestPlanningProgressCancelledPlanRemainsInspectableButNotExecutable(t *testing.T) {
	p, parent, children := deliveryFixture(t)
	parent.Phase = PlanCancelledPhase
	parent = signDeliveryFixture(t, p, parent, "planner", "backlog")
	all := append([]WorkItem{parent}, children...)
	progress := p.PlanningProgress(all)
	if len(progress.CancelledPlans) != 1 || len(progress.IntegratedUndelivered) != 1 {
		t.Fatal("cancellation hid retained work")
	}
	if _, err := p.ValidatePlanDelivery(parent, all); err == nil {
		t.Fatal("inspection reopened cancelled authority")
	}
	if p.dependenciesSatisfied(children[1], all) {
		t.Fatal("cancelled internal dependency admitted work")
	}
	all[0].Approval = "altered"
	if got := p.PlanningProgress(all); len(got.CancelledPlans) != 0 || len(got.IntegratedUndelivered) != 0 {
		t.Fatal("forged cancellation displayed as authoritative")
	}
}
