package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func (s *cardRecoveryStore) SaveSourceHandoff(ctx context.Context, root, child, pending Run, receipt *Receipt) error {
	if s.fail {
		return fmt.Errorf("fixture handoff transaction failed")
	}
	s.runs[child.ID], s.runs[pending.ID] = Clone(child), Clone(pending)
	return s.Save(ctx, root, nil, receipt)
}

func sourceHandoffFixture(t *testing.T) (*Core, *cardRecoveryStore, AdjudicateSourceHandoffRequest) {
	t.Helper()
	c, store, oldReq := serialCardRecoveryFixture(t)
	a, err := c.CheckUnpublishedCardRecovery(oldReq)
	if err != nil {
		t.Fatal(err)
	}
	oldReq.BindingsSHA256 = a.BindingsSHA256
	if _, err = c.RecoverUnpublishedCard(oldReq); err != nil {
		t.Fatal(err)
	}
	root := Clone(c.runs[oldReq.RunID])
	group := root.TaskGroups["cards"]
	add := func(owner, id, value string) Value {
		if err := c.disk.Write("artifacts/"+id, []byte(value), c.disk.Policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
		store.artifacts[id] = Artifact{ID: id, RunID: owner, ProjectID: root.ProjectID, Bytes: int64(len(value))}
		return Value{ArtifactID: id}
	}
	var template Attempt
	for i := range 4 {
		id := group.Children[i].RunID
		child := Clone(c.runs[id])
		if i == 0 {
			template = Clone(child.Attempts[1])
			for j := range 2 {
				child.Attempts = append(child.Attempts, Attempt{ID: fmt.Sprintf("first-finish-%d", j), Step: j + 1, State: Finished, Released: true})
			}
		} else {
			for j := range 3 {
				child.Attempts = append(child.Attempts, Attempt{ID: fmt.Sprintf("peer-%d-%d", i, j), Step: j, State: Finished, Released: true})
			}
		}
		child.Status, child.Reason, child.Step, child.Visit = "Completed", "", 3, 3
		child.ResourcesReleased, child.Revision = true, int64(80+i)
		patch := add(id, fmt.Sprintf("accepted-%d/changes.patch", i), fmt.Sprintf("accepted source%d", i))
		pin := TaskCandidate{Identity: child.Task.Identity, RunID: id, Repository: root.Repository, Base: group.Base, Commit: fmt.Sprintf("%040x", i+1), Tree: fmt.Sprintf("%040x", i+11), PatchID: patch.ArtifactID}
		raw, _ := json.Marshal(pin)
		child.Outputs = map[string]Value{"result": {Inline: raw}}
		group.Children[i].Candidate = &pin
		c.runs[id] = child
	}
	id := group.Children[4].RunID
	child := childForItem(root, group, group.Items[4], id)
	child.Status, child.Reason = "Failed", "later caller review unavailable"
	child.Revision, child.Step, child.Visit, child.ResourcesReleased = 61, 1, 1, true
	c.runs[root.ID] = root
	authority, err := c.taskAssignment(child, child.Process.Steps[1])
	if err != nil {
		t.Fatal(err)
	}
	deps, _ := json.Marshal(authority.Dependencies)
	child.Inputs["_task_dependencies"] = Value{Inline: deps}
	seed := Clone(c.runs[oldReq.ChildRunID].Attempts[0])
	seed.ID = "handoff-seed"
	input := add(id, seed.ID+"/changes.patch", "original handoff dependencies")
	seed.End = &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{"patch": input, "manifest": add(id, seed.ID+"/manifest", "manifest")}}}
	failed := template
	failed.ID = "handoff-blocked"
	failed.TimedOut = false
	failed.StopRequested = false
	patch := add(id, failed.ID+"/changes.patch", "complete captured source")
	failed.Source = &SourceResult{AttemptID: failed.ID, PatchID: patch.ArtifactID, InputPatchID: input.ArtifactID, Base: group.Base, Tree: strings.Repeat("b", 40), Repository: root.Repository, WorkDir: "owned/absent"}
	failed.End = &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "blocked", Reason: child.Reason, Outputs: map[string]Value{"patch": patch, "summary": Text("source complete; ordinary checks and caller review remain pending")}}}
	child.Attempts = []Attempt{seed, failed}
	child.StepInputs, err = bindings(child, child.Process.Steps[1].Inputs)
	if err != nil {
		t.Fatal(err)
	}
	c.runs[id] = child
	pending := childForItem(root, group, group.Items[5], group.Children[5].RunID)
	pending.Status, pending.Reason = "Cancelled", "parent task group is stopping"
	pending.CancelRequested, pending.ResourcesReleased, pending.Revision = true, true, 3
	c.runs[pending.ID] = pending
	root.Status, root.Reason, root.ResourcesReleased, root.Revision = "Failed", "task "+group.Items[4].ID+": "+child.Reason, true, 126
	group.Done, group.Failure = true, root.Reason
	c.runs[root.ID] = root
	req := AdjudicateSourceHandoffRequest{RecoverUnpublishedCardRequest: RecoverUnpublishedCardRequest{RequestID: "handoff-once", RunID: root.ID, Revision: c.outputRun(root).Revision, GroupAttemptID: group.AttemptID, ChildRunID: id, ChildRevision: child.Revision, AttemptID: failed.ID, PatchSHA256: recoveryDigestRawText("complete captured source")}, ReasonSHA256: recoveryDigestRawText(child.Reason)}
	return c, store, req
}

func TestSourceHandoffPreservesFailureAndQueuesOnlyNormalPin(t *testing.T) {
	c, s, q := sourceHandoffFixture(t)
	before := Clone(c.runs)
	a, err := c.CheckSourceHandoff(q)
	if err != nil {
		t.Fatal(err)
	}
	if a.Spent != 19 || a.Ceiling != 65 || a.ReservedCompletion != 26 || len(a.Pins) != 4 || len(a.Unstarted) != 1 || !reflect.DeepEqual(before, c.runs) {
		t.Fatal("wrong read-only source handoff reserve", a)
	}
	q.BindingsSHA256 = a.BindingsSHA256
	r, err := c.AdjudicateSourceHandoff(q)
	if err != nil {
		t.Fatal(err)
	}
	child := c.runs[q.ChildRunID]
	if r.RecordFormat != 7 || r.SourceHandoff == nil || !reflect.DeepEqual(r.UnpublishedCardRecovery, before[r.ID].UnpublishedCardRecovery) || !reflect.DeepEqual(child.Attempts, before[child.ID].Attempts) || child.Step != 2 || child.Visit != 2 || child.Status != "Queued" || c.familyAttempts(r) != 19 {
		t.Fatal("handoff rewrote history or granted an owner")
	}
	for _, pin := range a.Pins {
		if !reflect.DeepEqual(c.runs[pin.RunID], before[pin.RunID]) {
			t.Fatal("accepted peer changed")
		}
	}
	assignment, err := c.assignment(child, Attempt{ID: "ordinary-pin", Step: 2, Visit: 2, Deadline: time.Now().Add(20 * time.Minute)})
	if err != nil || assignment.SourceHandoff == nil || assignment.Setup.Profile.Harness != "task-source" || assignment.Setup.Profile.Mode != "pin" || assignment.Inputs["patch"].ArtifactID != a.Source.PatchID || assignment.CallerReviewScope != SourceHandoffScope || !reflect.DeepEqual(assignment.SourceHandoff.Authority, *assignment.TaskSource) {
		t.Fatal("handoff skipped ordinary pin authority", err)
	}
	if _, err = c.Retry(RetryRequest{RequestID: "no-owner", RunID: child.ID, ExtraAttempts: 1}); err == nil {
		t.Fatal("handoff renewed an editing visit")
	}
	saves := s.saves
	if _, err = c.AdjudicateSourceHandoff(q); err != nil || s.saves != saves {
		t.Fatal("lost acknowledgement repeated the grant", err)
	}
	if _, err = c.AdjudicateSourceHandoff(AdjudicateSourceHandoffRequest{RecoverUnpublishedCardRequest: RecoverUnpublishedCardRequest{RequestID: "second", RunID: r.ID}}); err == nil {
		t.Fatal("family received a second handoff")
	}
}

func TestSourceHandoffRejectsDriftAndFailedCommit(t *testing.T) {
	for _, kind := range []string{"reason", "timeout", "source-output", "started-final", "pin", "prior-audit", "thin-reserve", "stale", "atomic"} {
		t.Run(kind, func(t *testing.T) {
			c, s, q := sourceHandoffFixture(t)
			a, err := c.CheckSourceHandoff(q)
			if err != nil {
				t.Fatal(err)
			}
			q.BindingsSHA256 = a.BindingsSHA256
			r, child := Clone(c.runs[q.RunID]), Clone(c.runs[q.ChildRunID])
			switch kind {
			case "reason":
				q.ReasonSHA256 = strings.Repeat("0", 64)
			case "timeout":
				child.Attempts[1].TimedOut = true
			case "source-output":
				child.Attempts[1].End.Result.Outputs["patch"] = Value{ArtifactID: "other"}
			case "started-final":
				p := c.runs[a.Unstarted[0].RunID]
				p.Attempts = []Attempt{{ID: "old", State: Finished, Released: true}}
				c.runs[p.ID] = p
			case "pin":
				r.TaskGroups["cards"].Children[0].Candidate.Tree = strings.Repeat("f", 40)
			case "prior-audit":
				r.UnpublishedCardRecovery.Unstarted[0].RunID = "another"
			case "thin-reserve":
				for c.familyAttempts(r)+26 <= 65 {
					r.Attempts = append(r.Attempts, Attempt{ID: fmt.Sprint("spent", len(r.Attempts)), State: Finished, Released: true})
					c.runs[r.ID] = r
				}
			case "stale":
				r.Updated = r.Updated.Add(time.Second)
			case "atomic":
				s.fail = true
			}
			c.runs[r.ID], c.runs[child.ID] = r, child
			if kind == "thin-reserve" {
				q.Revision = c.outputRun(r).Revision
			}
			before := Clone(c.runs)
			if _, err = c.AdjudicateSourceHandoff(q); err == nil || !reflect.DeepEqual(before, c.runs) {
				t.Fatal("unsafe handoff changed family", err)
			}
			if kind == "atomic" {
				s.fail = false
				if _, err = c.AdjudicateSourceHandoff(q); err != nil {
					t.Fatal("failed transaction consumed grant", err)
				}
			}
		})
	}
}

func TestSourceHandoffNormalPinRestoresLastCardAndJoin(t *testing.T) {
	c, s, q := sourceHandoffFixture(t)
	a, err := c.CheckSourceHandoff(q)
	if err != nil {
		t.Fatal(err)
	}
	q.BindingsSHA256 = a.BindingsSHA256
	if _, err = c.AdjudicateSourceHandoff(q); err != nil {
		t.Fatal(err)
	}
	finish := func(id string, seed bool) TaskCandidate {
		child := c.runs[id]
		appendResult := func(step int, result *StepResult) {
			child.Attempts = append(child.Attempts, Attempt{ID: fmt.Sprintf("finish-%s-%d", id, step), Step: step, Visit: child.Visit, State: Finished, Released: true, End: &ExecutionEnd{Kind: "returned", Result: result}})
			c.route(&child, child.Current())
		}
		patch := Value{ArtifactID: "finished-" + id + "/changes.patch"}
		if seed {
			assignment, e := c.taskAssignment(child, child.Process.Steps[0])
			if e != nil || len(assignment.Dependencies) != 1 || assignment.Dependencies[0].RunID != q.ChildRunID {
				t.Fatal("last original seed lost its predecessor", e)
			}
			appendResult(0, &StepResult{Outcome: "ready", Outputs: map[string]Value{"patch": patch, "manifest": {ArtifactID: "seed-manifest"}}})
			appendResult(1, &StepResult{Outcome: "ready", Outputs: map[string]Value{"patch": patch, "summary": Text("final card source")}})
		} else {
			patch = child.StepInputs["patch"]
		}
		candidate := TaskCandidate{Identity: child.Task.Identity, RunID: id, Repository: child.Repository, Base: child.Source.Ref, Commit: strings.Repeat("c", 40), Tree: strings.Repeat("d", 40), PatchID: patch.ArtifactID}
		s.artifacts[patch.ArtifactID] = Artifact{ID: patch.ArtifactID, RunID: id, ProjectID: child.ProjectID}
		raw, _ := json.Marshal(candidate)
		appendResult(2, &StepResult{Outcome: "ready", Outputs: map[string]Value{"candidate": {Inline: raw}}})
		child.ResourcesReleased = true
		c.runs[id] = child
		c.driveTaskGroup(c.runs[q.RunID])
		return candidate
	}
	finish(q.ChildRunID, false)
	pending := c.runs[a.Unstarted[0].RunID]
	assignment, err := c.assignment(pending, Attempt{ID: "final-seed", Step: 0, Visit: pending.Visit})
	if pending.Status != "Queued" || err != nil || assignment.CallerReviewScope != SourceHandoffScope {
		t.Fatal("final card lost authoritative caller scope", err)
	}
	finish(pending.ID, true)
	root := c.runs[q.RunID]
	join, err := c.assignment(root, Attempt{ID: "ordinary-join", Step: root.Step, Visit: root.Visit})
	if root.Status != "Queued" || root.Process.Steps[root.Step].ID != "join" || c.familyAttempts(root) != 24 || err != nil || join.TaskSource == nil || len(join.TaskSource.Dependencies) != 6 {
		t.Fatal("handoff bypassed normal six-pin join", root.Status, c.familyAttempts(root), err)
	}
}

func TestSourceHandoffRejectedPinRestartCannotRenewOwner(t *testing.T) {
	c, s, q := sourceHandoffFixture(t)
	a, err := c.CheckSourceHandoff(q)
	if err != nil {
		t.Fatal(err)
	}
	q.BindingsSHA256 = a.BindingsSHA256
	if _, err = c.AdjudicateSourceHandoff(q); err != nil {
		t.Fatal(err)
	}
	child := c.runs[q.ChildRunID]
	old := Clone(child.Attempts)
	child.Attempts = append(child.Attempts, Attempt{ID: "rejected-handoff-pin", Step: 2, Visit: child.Visit, State: Finished, Released: true, End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "changes_requested", Reason: "scope changed"}}})
	c.route(&child, child.Current())
	child.ResourcesReleased = true
	c.runs[child.ID] = child
	if child.Status != "Failed" || !reflect.DeepEqual(child.Attempts[:2], old) {
		t.Fatal("pin rejection renewed owner or rewrote history")
	}
	c.driveTaskGroup(c.runs[q.RunID])
	pending := c.runs[a.Unstarted[0].RunID]
	pending.ResourcesReleased = true
	c.runs[pending.ID] = pending
	c.driveTaskGroup(c.runs[q.RunID])
	root := c.runs[q.RunID]
	root.ResourcesReleased = true
	c.runs[root.ID] = root
	s.runs = Clone(c.runs)
	restarted, err := New(t.Context(), s, c.executor, c.disk, c.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, err = restarted.AdjudicateSourceHandoff(q); err != nil {
		t.Fatal("replay lost immutable receipt", err)
	}
	for _, id := range []string{root.ID, child.ID} {
		if _, err = restarted.Retry(RetryRequest{RequestID: "retry-" + id, RunID: id, ExtraAttempts: 1}); err == nil {
			t.Fatal("failed handoff granted another visit")
		}
	}
	if c.executor.(*cardRecoveryExecutor).starts.Load() != 0 {
		t.Fatal("restart dispatched a model owner")
	}
}

func TestSourceHandoffDispatchPreservesFullCompletionReserve(t *testing.T) {
	c, _, q := sourceHandoffFixture(t)
	a, err := c.CheckSourceHandoff(q)
	if err != nil {
		t.Fatal(err)
	}
	q.BindingsSHA256 = a.BindingsSHA256
	if _, err = c.AdjudicateSourceHandoff(q); err != nil {
		t.Fatal(err)
	}
	r := c.runs[q.RunID]
	for c.familyAttempts(r)+26 <= 65 {
		r.Attempts = append(r.Attempts, Attempt{ID: fmt.Sprint("used-", len(r.Attempts)), State: Finished, Released: true})
		c.runs[r.ID] = r
	}
	for _, id := range []string{q.ChildRunID, a.Unstarted[0].RunID} {
		child := c.runs[id]
		reserve, err := c.taskCompletionReserve(child)
		if err != nil {
			t.Fatal(err)
		}
		if err = c.familyReserve(child, 1, reserve); err == nil {
			t.Fatal("dispatch consumed protected completion reserve", id)
		}
	}
}
