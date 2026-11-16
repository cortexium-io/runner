package runner

import (
	"fmt"
	"reflect"
	"testing"
)

func TestTaskAllowanceGrantPreservesContinuationDebitAndVisitLimit(t *testing.T) {
	c, store, _, request := taskContinuationFixture(t)
	root, err := c.ContinueTask(request)
	if err != nil {
		t.Fatal(err)
	}
	root = c.runs[root.ID]
	root.Status, root.Reason, root.ResourcesReleased = "Failed", "repair and review need more capacity", true
	root.TaskGraph.Directive = "Previous owner guidance remains retained in its original receipt"
	for i := range 27 {
		root.Attempts = append(root.Attempts, Attempt{
			ID:         fmt.Sprintf("attempt_retained_%d", i),
			Visit:      i + 1,
			State:      Finished,
			Released:   true,
			Superseded: i < 26,
		})
	}
	root.Visit = 27
	c.runs[root.ID] = root
	before := Clone(root)
	original := Clone(c.runs[request.RunID])
	req := RetryRequest{
		RequestID:     "owner-eight-more",
		RunID:         root.ID,
		ExtraAttempts: 8,
		Reason:        "Owner approved completing the repair, verification and review cycle",
		Directive:     "Use the corrected verifier contract to complete this same retained goal",
	}
	granted, err := c.Retry(req)
	if err != nil {
		t.Fatal(err)
	}
	wantBudget := TaskAttemptBudget{Limit: 72, PriorAttempts: 36, Spent: 63, Remaining: 9}
	if granted.TaskBudget == nil || *granted.TaskBudget != wantBudget || granted.Status != "Queued" || granted.TaskGraph.Directive != req.Directive || granted.VisitExtraAttempts != before.VisitExtraAttempts || granted.Process.MaxAttemptsPerStep != before.Process.MaxAttemptsPerStep || !reflect.DeepEqual(granted.Attempts, before.Attempts) || !reflect.DeepEqual(granted.TaskGraph.Lineage, before.TaskGraph.Lineage) || !reflect.DeepEqual(c.runs[request.RunID], original) {
		t.Fatal("owner grant reset history, debit, lineage or individual visit policy", granted.TaskBudget)
	}
	grant := granted.TaskGraph.AllowanceGrants[0]
	if grant.RequestID != req.RequestID || grant.PreviousLimit != 64 || grant.Limit != 72 || grant.Spent != 63 || grant.Reason != req.Reason || grant.GrantedAt.IsZero() {
		t.Fatal("owner grant audit differs from its explicit request", grant)
	}
	ctx, err := c.graphContext(c.runs[root.ID])
	if err != nil || ctx.AttemptBudget == nil || *ctx.AttemptBudget != wantBudget || ctx.AllowanceGrant == nil || !reflect.DeepEqual(*ctx.AllowanceGrant, grant) || ctx.Lineage.MaxAttempts != 64 || ctx.Directive != req.Directive {
		t.Fatal("native task context obscured amended authority", ctx, err)
	}
	saves := store.saves
	replayed, err := c.Retry(req)
	if err != nil || replayed.Process.MaxAttempts != 72 || len(replayed.TaskGraph.AllowanceGrants) != 1 || len(replayed.Attempts) != 27 || store.saves != saves {
		t.Fatal("idempotent retry spent capacity or repeated its grant", err)
	}
	if err = c.validateTaskContinuationLinks(); err != nil {
		t.Fatal("new effective limit invalidated immutable original link", err)
	}
	state, definition, err := MarshalRunRecord(c.runs[root.ID])
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := UnmarshalRunRecord(state, definition)
	if err != nil || reloaded.Process.MaxAttempts != 72 || !reflect.DeepEqual(reloaded.TaskGraph.AllowanceGrants, granted.TaskGraph.AllowanceGrants) {
		t.Fatal("record reload lost effective owner authority", err)
	}
	if _, err = c.Retry(RetryRequest{RequestID: "old-root-still-frozen", RunID: original.ID, ExtraAttempts: 8}); err == nil {
		t.Fatal("new root grant unfroze historical source spending")
	}
}

func TestTaskAllowanceGrantDoesNotIncreaseAnExhaustedVisit(t *testing.T) {
	c, _, _, request := taskContinuationFixture(t)
	root, err := c.ContinueTask(request)
	if err != nil {
		t.Fatal(err)
	}
	root = c.runs[root.ID]
	root.Status, root.ResourcesReleased = "Failed", true
	for i := range root.Process.MaxAttemptsPerStep {
		root.Attempts = append(root.Attempts, Attempt{ID: fmt.Sprint(i), State: Finished, Released: true})
	}
	c.runs[root.ID] = root
	if _, err = c.Retry(RetryRequest{RequestID: "goal-only", RunID: root.ID, ExtraAttempts: 8}); err == nil {
		t.Fatal("aggregate grant silently extended the exhausted role visit")
	}
	if !reflect.DeepEqual(c.runs[root.ID], root) {
		t.Fatal("rejected grant changed retained state")
	}
	if _, err = c.Retry(RetryRequest{RequestID: "per-visit-too-large", RunID: root.ID, ExtraAttempts: 8, WithinBudget: true}); err == nil {
		t.Fatal("aggregate grant widened the bounded per-visit retry operation")
	}
}

func TestTaskAllowanceGrantRetainsHistoryBeyondDormantCeiling(t *testing.T) {
	c, _, _, request := taskContinuationFixture(t)
	root, err := c.ContinueTask(request)
	if err != nil {
		t.Fatal(err)
	}
	c.snapshot.Instance.EnforceWorkLimits = false
	root = c.runs[root.ID]
	root.Status, root.ResourcesReleased = "Failed", true
	for i := range 30 {
		root.Attempts = append(root.Attempts, Attempt{ID: fmt.Sprintf("spent_unlimited_%d", i), Visit: i + 1, State: Finished, Released: true})
	}
	root.Visit = 30
	c.runs[root.ID] = root
	got, err := c.Retry(RetryRequest{RequestID: "retain-finite-definition-amendment", RunID: root.ID, ExtraAttempts: 8})
	if err != nil || got.TaskBudget == nil || !got.TaskBudget.Unlimited || got.TaskBudget.Spent != 66 || got.Process.MaxAttempts != 72 || got.TaskGraph.AllowanceGrants[0].Spent != 66 {
		t.Fatal("owner amendment rejected or reset legitimately unbounded history", got.TaskBudget, err)
	}
	if _, _, err := MarshalRunRecord(c.runs[root.ID]); err != nil {
		t.Fatal("authenticated grant could not retain spending beyond its dormant prior limit", err)
	}
}
