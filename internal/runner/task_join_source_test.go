package runner

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func symbolicJoinFixture(t *testing.T) (*Core, *cardRecoveryStore, Run) {
	t.Helper()
	c, store, q := completedCardRecoveryFixture(t)
	r := Clone(c.runs[q.RunID])
	base := r.Source.Ref
	r.Source.Ref = "refs/remotes/origin/main"
	// The original admission recorded the resolved base, independently of the
	// requested ref. No repository lookup is available to this Core fixture.
	r.Attempts[0].Source = &SourceResult{Repository: r.Repository, Base: base}
	r.Attempts[0].End = &ExecutionEnd{Kind: "returned"}
	c.runs[r.ID] = r
	return c, store, r
}

func TestTaskJoinSourceUsesAdmittedBaseWithoutChangingFrozenRef(t *testing.T) {
	c, _, r := symbolicJoinFixture(t)
	before := Clone(r)
	deadline := time.Now().UTC().Add(20 * time.Minute)
	a, err := c.assignment(r, Attempt{ID: "join", Step: r.Step, Visit: r.Visit, Deadline: deadline})
	if err != nil || a.Source == nil || a.Source.Ref != r.TaskGroups["cards"].Base || !a.Deadline.Equal(deadline) || len(a.TaskSource.Dependencies) != 8 {
		t.Fatal("join did not bind the original group base", a.Source, err)
	}
	if !reflect.DeepEqual(r, before) || !reflect.DeepEqual(c.runs[r.ID], before) {
		t.Fatal("assignment rewrote frozen source/group/history")
	}
	a.Source.Ref = "observer mutation"
	if r.Source.Ref != before.Source.Ref {
		t.Fatal("assignment aliases frozen source")
	}
	// Literal immutable assignments remain compatible with the unchanged
	// execution contract. A symbolic request needs admitted immutable history.
	r.Source.Ref = r.TaskGroups["cards"].Base
	if _, err := c.assignment(r, Attempt{Step: r.Step}); err != nil {
		t.Fatal("legacy immutable source refused", err)
	}
}

func TestTaskJoinSourceRefusesMissingOrDriftedAuthority(t *testing.T) {
	c, _, original := symbolicJoinFixture(t)
	cases := map[string]func(*Run){
		"missing source":      func(r *Run) { r.Source = nil },
		"empty ref":           func(r *Run) { r.Source.Ref = "" },
		"wrong immutable ref": func(r *Run) { r.Source.Ref = strings.Repeat("b", 40) },
		"unadmitted symbolic": func(r *Run) { r.Attempts[0].Source = nil },
		"admission base":      func(r *Run) { r.Attempts[0].Source.Base = strings.Repeat("b", 40) },
		"admission repo":      func(r *Run) { r.Attempts[0].Source.Repository = "/foreign" },
		"group base":          func(r *Run) { r.TaskGroups["cards"].Base = strings.Repeat("b", 40) },
		"group repo":          func(r *Run) { r.TaskGroups["cards"].Repository = "/foreign" },
		"group identity":      func(r *Run) { r.TaskGroups["cards"].StepID = "foreign" },
		"missing group":       func(r *Run) { delete(r.TaskGroups, "cards") },
		"unfinished group":    func(r *Run) { r.TaskGroups["cards"].Done = false },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := Clone(original)
			mutate(&r)
			before, _ := json.Marshal(r)
			if _, err := c.assignment(r, Attempt{Step: r.Step}); err == nil {
				t.Fatal("join accepted altered authority")
			}
			after, _ := json.Marshal(r)
			if string(after) != string(before) {
				t.Fatal("refusal changed source/group/history")
			}
		})
	}
}

func TestTaskJoinWithinBudgetRetryPreservesRecoveredFamilyAndTail(t *testing.T) {
	c, store, r := symbolicJoinFixture(t)
	r.Status, r.Reason, r.ResourcesReleased = "Failed", "saved join refusal", true
	r.Attempts = append(r.Attempts, Attempt{
		ID: "failed-join", Step: r.Step, Visit: r.Visit, State: Finished, Released: true,
		End: &ExecutionEnd{Kind: "failed", Reason: r.Reason},
	})
	c.runs[r.ID] = r
	if err := c.persist(r, nil, nil); err != nil {
		t.Fatal(err)
	}
	before := Clone(c.runs[r.ID])
	if c.familyAttempts(before) != 32 || before.Process.MaxAttempts != 64 || before.UnpublishedCardRecovery == nil {
		t.Fatal("fixture lost the consumed grant/original usage")
	}
	children := map[string]Run{}
	for _, child := range r.TaskGroups["cards"].Children {
		children[child.RunID] = Clone(c.runs[child.RunID])
	}
	need, err := readyTail(r, r.Step)
	if err != nil {
		t.Fatal(err)
	}
	thin := Clone(r)
	thin.Process.MaxAttempts = 32 + need - 1
	c.runs[r.ID] = thin
	req := RetryRequest{RequestID: "same-root-join", RunID: r.ID, ExtraAttempts: 1, WithinBudget: true}
	if _, err = c.Retry(req); err == nil {
		t.Fatal("retry consumed the full completion reserve")
	}
	c.runs[r.ID] = before
	queued, err := c.Retry(req)
	if err != nil || queued.Status != "Queued" || c.familyAttempts(queued) != 32 || queued.Process.MaxAttempts != 64 || queued.VisitExtraAttempts != 1 {
		t.Fatal("ordinary root-visit retry failed or expanded its ceiling", err)
	}
	if !reflect.DeepEqual(queued.Process, before.Process) || !reflect.DeepEqual(queued.Attempts, before.Attempts) || !reflect.DeepEqual(queued.TaskGroups, before.TaskGroups) || !reflect.DeepEqual(queued.UnpublishedCardRecovery, before.UnpublishedCardRecovery) {
		t.Fatal("retry rewrote the consumed recovery or pinned history")
	}
	// Reopen only the saved model-free Core facts, then replay the existing
	// receipt. This creates neither another retry grant nor a card dispatch.
	c.runs = map[string]Run{}
	loaded, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range loaded {
		c.runs[saved.ID] = saved
	}
	for id, child := range children {
		c.runs[id] = child
	}
	again, err := c.Retry(req)
	if err != nil || again.VisitExtraAttempts != 1 || c.familyAttempts(again) != 32 {
		t.Fatal("restart replay renewed join allowance", err)
	}
	a, err := c.assignment(again, Attempt{ID: "new-join", Step: again.Step, Visit: again.Visit, Deadline: time.Now().UTC().Add(20 * time.Minute)})
	if err != nil || a.Source.Ref != again.TaskGroups["cards"].Base || a.UnpublishedCardRecovery != nil {
		t.Fatal("join retry became another source owner", err)
	}
	for id, child := range children {
		if !reflect.DeepEqual(c.runs[id], child) {
			t.Fatal("retry replayed or changed a completed card")
		}
	}
	passed := Clone(again)
	passed.Attempts = append(passed.Attempts, Attempt{
		ID: "new-join", Step: passed.Step, Visit: passed.Visit, State: Finished, Released: true,
		Source: &SourceResult{AttemptID: "new-join", Repository: passed.Repository, Base: a.Source.Ref, Tree: strings.Repeat("d", 40), PatchID: "new-join/changes.patch"},
		End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{
			"patch": {ArtifactID: "new-join/changes.patch"}, "manifest": {ArtifactID: "new-join/task-manifest.json"}, "feedback": Text(""),
		}}},
	})
	c.route(&passed, passed.Current())
	next := taskStepIndex(again.Process, again.Process.Steps[again.Step].Routes["ready"].Next)
	if passed.Step != next || passed.Process.Steps[passed.Step].ID != "check" || passed.Status != "Queued" || !reflect.DeepEqual(passed.Process, before.Process) || c.familyAttempts(passed) != 33 {
		t.Fatal("successful join bypassed the normal downstream gates", passed.Step, passed.Reason)
	}
	if got, err := c.Cancel(r.ID); err != nil || got.Status != "Cancelled" || c.familyAttempts(got) != 32 {
		t.Fatal("queued retry did not cancel without spending another attempt", err)
	}
	t.Logf("spent=32 ceiling=64; join retry=1, full downstream reserve=%d, spare=%d", need-1, 64-32-need)
}
