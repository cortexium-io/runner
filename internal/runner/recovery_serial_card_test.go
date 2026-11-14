package runner

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func serialCardRecoveryFixture(t *testing.T) (*Core, *cardRecoveryStore, RecoverUnpublishedCardRequest) {
	t.Helper()
	c, store, _, req := cardRecoveryFixture(t)
	root := Clone(c.runs[req.RunID])
	group := root.TaskGroups["cards"]
	items, reservations := group.Items, group.Children
	group.Items = append([]TaskItem{items[7]}, items[:5]...)
	group.Children = append([]TaskChild{reservations[7]}, reservations[:5]...)
	group.Items[0].DependsOn = nil
	for i := 1; i < len(group.Items); i++ {
		group.Items[i].DependsOn = []string{group.Items[i-1].ID}
	}
	root.Process.MaxAttempts = 65
	root.Attempts = root.Attempts[:4]
	root.Revision = 108
	for _, reservation := range reservations[:7] {
		delete(c.runs, reservation.RunID)
	}
	for i := 1; i < len(group.Children); i++ {
		group.Children[i].Candidate = nil
		pending := childForItem(root, group, group.Items[i], group.Children[i].RunID)
		pending.Revision = 3
		pending.Status, pending.Reason = "Cancelled", "parent task group is stopping"
		pending.CancelRequested, pending.ResourcesReleased = true, true
		c.runs[pending.ID] = pending
	}
	c.runs[root.ID] = root
	failed := c.runs[req.ChildRunID]
	failed.Revision, failed.Process.MaxAttempts = 57, 65
	failed.Inputs["_task_dependencies"] = Value{Inline: json.RawMessage(`[]`)}
	var err error
	failed.StepInputs, err = bindings(failed, failed.Process.Steps[1].Inputs)
	if err != nil {
		t.Fatal(err)
	}
	c.runs[failed.ID] = failed
	req.Revision, req.ChildRevision = c.outputRun(root).Revision, failed.Revision
	return c, store, req
}

func TestSerialCardRecoveryBindsAndRestoresOnlyUnstartedReservations(t *testing.T) {
	c, store, req := serialCardRecoveryFixture(t)
	before := Clone(c.runs)
	assessment, err := c.CheckUnpublishedCardRecovery(req)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Revision != 180 || assessment.Spent != 6 || assessment.Ceiling != 65 || assessment.ReservedCompletion != 38 || len(assessment.Unstarted) != 5 || len(assessment.Pins) != 0 || !reflect.DeepEqual(before, c.runs) || store.saves != 0 {
		t.Fatal("serial preflight changed work or omitted full completion reserve", assessment)
	}
	req.BindingsSHA256 = assessment.BindingsSHA256
	root, err := c.RecoverUnpublishedCard(req)
	if err != nil {
		t.Fatal(err)
	}
	if root.RecordFormat != 6 || !reflect.DeepEqual(root.UnpublishedCardRecovery.Unstarted, assessment.Unstarted) || c.familyAttempts(root) != 6 || !reflect.DeepEqual(root.Process, before[root.ID].Process) {
		t.Fatal("serial recovery lost cancellation audit, history or frozen ceiling")
	}
	for _, original := range assessment.Unstarted {
		pending, old := c.runs[original.RunID], before[original.RunID]
		if original.Revision != old.Revision || original.Updated != old.Updated || pending.Status != "Waiting" || pending.Reason != "waiting for declared task prerequisites" || pending.CancelRequested || pending.ResourcesReleased || pending.Revision != old.Revision+1 || len(pending.Attempts) != 0 || !reflect.DeepEqual(pending.Process, old.Process) || !reflect.DeepEqual(pending.Inputs, old.Inputs) || !reflect.DeepEqual(pending.Task, old.Task) {
			t.Fatal("unstarted reservation lost its original scope or gained execution", pending.ID)
		}
	}
	failed := c.runs[req.ChildRunID]
	if failed.Status != "Queued" || !reflect.DeepEqual(failed.Attempts, before[failed.ID].Attempts) {
		t.Fatal("failed owner history changed")
	}
	assignment, err := c.assignment(failed, Attempt{
		ID:       "recovered-owner",
		Step:     1,
		Visit:    failed.Visit,
		Deadline: time.Now().Add(20 * time.Minute),
	})
	if err != nil || assignment.UnpublishedCardRecovery == nil || assignment.UnpublishedCardRecovery.Source.PatchID != req.AttemptID+"/changes.patch" {
		t.Fatal("captured source did not reach ordinary recovery preparation", err)
	}
	if !c.driveTaskGroup(root) {
		t.Fatal("recovered group was not observed")
	}
	for _, original := range assessment.Unstarted {
		if c.runs[original.RunID].Status != "Waiting" {
			t.Fatal("dependent ran before the failed card's normal accepted pin")
		}
	}
	saves := store.saves
	if _, err = c.RecoverUnpublishedCard(req); err != nil || store.saves != saves {
		t.Fatal("lost acknowledgement replay repeated the atomic grant", err)
	}
}

func TestSerialCardRecoveryRejectsAmbiguousSiblingsAndPlanDrift(t *testing.T) {
	for name, mutate := range map[string]func(*Run){
		"started":                     func(r *Run) { r.Attempts = []Attempt{{ID: "old-start", State: Finished, Released: true}} },
		"active":                      func(r *Run) { r.Attempts = []Attempt{{ID: "writer", State: Unknown}} },
		"external cancellation":       func(r *Run) { r.Reason = "cancellation requested" },
		"missing parent cancellation": func(r *Run) { r.CancelRequested = false },
		"cleanup":                     func(r *Run) { r.ResourcesReleased = false },
		"scope":                       func(r *Run) { r.Inputs["paths"] = Text(`["outside.txt"]`) },
		"profile":                     func(r *Run) { r.NextProfile = "other" },
		"visit":                       func(r *Run) { r.Visit = 1 },
		"outputs":                     func(r *Run) { r.Outputs = map[string]Value{"result": Text("unowned")} },
	} {
		t.Run(name, func(t *testing.T) {
			c, store, req := serialCardRecoveryFixture(t)
			assessment, err := c.CheckUnpublishedCardRecovery(req)
			if err != nil {
				t.Fatal(err)
			}
			req.BindingsSHA256 = assessment.BindingsSHA256
			id := assessment.Unstarted[0].RunID
			pending := Clone(c.runs[id])
			mutate(&pending)
			c.runs[id] = pending
			before := Clone(c.runs)
			if _, err = c.RecoverUnpublishedCard(req); err == nil || store.saves != 0 || !reflect.DeepEqual(before, c.runs) {
				t.Fatal("ambiguous sibling changed the family", err)
			}
		})
	}
	for _, kind := range []string{"parallel plan", "later failed card", "stale sibling", "revision sum collision", "thin reserve", "atomic failure"} {
		t.Run(kind, func(t *testing.T) {
			c, store, req := serialCardRecoveryFixture(t)
			a, err := c.CheckUnpublishedCardRecovery(req)
			if err != nil {
				t.Fatal(err)
			}
			req.BindingsSHA256 = a.BindingsSHA256
			r := Clone(c.runs[req.RunID])
			switch kind {
			case "parallel plan":
				r.TaskGroups["cards"].Items[1].DependsOn = nil
			case "later failed card":
				r.TaskGroups["cards"].Items[0], r.TaskGroups["cards"].Items[1] = r.TaskGroups["cards"].Items[1], r.TaskGroups["cards"].Items[0]
			case "stale sibling":
				pending := c.runs[a.Unstarted[0].RunID]
				pending.Updated = pending.Updated.Add(time.Second)
				c.runs[pending.ID] = pending
			case "revision sum collision":
				for i, delta := range []int64{1, -1} {
					pending := c.runs[a.Unstarted[i].RunID]
					pending.Revision += delta
					c.runs[pending.ID] = pending
				}
			case "thin reserve":
				for len(r.Attempts)+2+1+a.ReservedCompletion <= r.Process.MaxAttempts {
					r.Attempts = append(r.Attempts, Attempt{ID: fmt.Sprintf("spent_%d", len(r.Attempts)), State: Finished, Released: true})
				}
			case "atomic failure":
				store.fail = true
			}
			c.runs[r.ID] = r
			if kind == "thin reserve" {
				req.Revision = c.outputRun(r).Revision
			}
			before := Clone(c.runs)
			if _, err = c.RecoverUnpublishedCard(req); err == nil || !reflect.DeepEqual(before, c.runs) || len(c.wake) != 0 {
				t.Fatal("ineligible or uncommitted recovery changed work", err)
			}
			if kind == "thin reserve" && !strings.Contains(err.Error(), "reserved completion") {
				t.Fatal("admission did not enforce the complete family reserve", err)
			}
			if kind == "atomic failure" {
				store.fail = false
				if _, err = c.RecoverUnpublishedCard(req); err != nil {
					t.Fatal("failed commit consumed the grant", err)
				}
			}
		})
	}
}

func TestSerialCardRecoveryReserveIncludesEveryUnstartedCard(t *testing.T) {
	c, _, req := serialCardRecoveryFixture(t)
	a, err := c.CheckUnpublishedCardRecovery(req)
	if err != nil {
		t.Fatal(err)
	}
	req.BindingsSHA256 = a.BindingsSHA256
	if _, err = c.RecoverUnpublishedCard(req); err != nil {
		t.Fatal(err)
	}
	root := c.runs[req.RunID]
	for len(root.Attempts)+2+1+a.ReservedCompletion <= root.Process.MaxAttempts {
		root.Attempts = append(root.Attempts, Attempt{ID: fmt.Sprintf("spent_%d", len(root.Attempts)), State: Finished, Released: true})
	}
	c.runs[root.ID] = root
	for _, id := range append([]string{req.ChildRunID}, a.Unstarted[0].RunID) {
		child := c.runs[id]
		reserve, err := c.taskCompletionReserve(child)
		if err != nil {
			t.Fatal(err)
		}
		if err = c.familyReserve(child, 1, reserve); err == nil || !strings.Contains(err.Error(), "reserved completion") {
			t.Fatal("queued work could consume the unfinished family reserve", id, err)
		}
	}
}

func TestSerialCardRecoveryNormalPinsFinishOriginalChainAndBindJoin(t *testing.T) {
	c, store, req := serialCardRecoveryFixture(t)
	a, err := c.CheckUnpublishedCardRecovery(req)
	if err != nil {
		t.Fatal(err)
	}
	req.BindingsSHA256 = a.BindingsSHA256
	if _, err = c.RecoverUnpublishedCard(req); err != nil {
		t.Fatal(err)
	}
	ids := []string{req.ChildRunID}
	for _, original := range a.Unstarted {
		ids = append(ids, original.RunID)
	}
	var previous TaskCandidate
	for i, id := range ids {
		child := c.runs[id]
		if i > 0 {
			assignment, err := c.taskAssignment(child, child.Process.Steps[0])
			if child.Status != "Queued" || child.Step != 0 || err != nil || len(assignment.Dependencies) != 1 || assignment.Dependencies[0] != previous {
				t.Fatal("next original seed did not authenticate its predecessor", id, err)
			}
			child.Attempts = append(child.Attempts, Attempt{
				ID:       "seed-" + id,
				Step:     0,
				Visit:    child.Visit,
				State:    Finished,
				Released: true,
				End: &ExecutionEnd{Kind: "returned", Result: &StepResult{
					Outcome: "ready",
					Outputs: map[string]Value{
						"patch":    {ArtifactID: "seed/" + id},
						"manifest": {ArtifactID: "manifest/" + id},
					},
				}},
			})
			c.route(&child, child.Current())
		}
		patch := Value{ArtifactID: "owner-" + id + "/changes.patch"}
		child.Attempts = append(child.Attempts, Attempt{
			ID:       "owner-" + id,
			Step:     1,
			Visit:    child.Visit,
			State:    Finished,
			Released: true,
			End: &ExecutionEnd{Kind: "returned", Result: &StepResult{
				Outcome: "ready",
				Outputs: map[string]Value{
					"patch":   patch,
					"summary": Text("fixture source; checks remain downstream"),
				},
			}},
		})
		c.route(&child, child.Current())
		if child.Step != 2 || child.Status != "Queued" {
			t.Fatal("ordinary owner skipped its normal pin", id)
		}
		candidate := TaskCandidate{
			Identity:   child.Task.Identity,
			RunID:      child.ID,
			Repository: child.Repository,
			Base:       child.Source.Ref,
			Commit:     strings.Repeat("c", 40),
			Tree:       strings.Repeat("d", 40),
			PatchID:    patch.ArtifactID,
		}
		store.artifacts[candidate.PatchID] = Artifact{ID: candidate.PatchID, RunID: child.ID, ProjectID: child.ProjectID}
		raw, _ := json.Marshal(candidate)
		child.Attempts = append(child.Attempts, Attempt{
			ID:       "pin-" + id,
			Step:     2,
			Visit:    child.Visit,
			State:    Finished,
			Released: true,
			End: &ExecutionEnd{Kind: "returned", Result: &StepResult{
				Outcome: "ready",
				Outputs: map[string]Value{"candidate": {Inline: raw}},
			}},
		})
		c.route(&child, child.Current())
		child.ResourcesReleased = true
		c.runs[id] = child
		c.driveTaskGroup(c.runs[req.RunID])
		root := c.runs[req.RunID]
		if child.Status != "Completed" || root.TaskGroups["cards"].Children[i].Candidate == nil {
			t.Fatal("normal pin did not finish its original card", id)
		}
		for j := i + 1; j < len(ids); j++ {
			pending := c.runs[ids[j]]
			want := "Waiting"
			if j == i+1 {
				want = "Queued"
			}
			if pending.Status != want || pending.Step != 0 || len(pending.Attempts) != 0 {
				t.Fatal("serial prerequisite order changed", pending.ID, pending.Status)
			}
		}
		previous = candidate
	}
	root := c.runs[req.RunID]
	assignment, err := c.assignment(root, Attempt{ID: "join-next", Step: root.Step, Visit: root.Visit})
	// Six prior attempts, two recovered steps, fifteen ordinary child steps and
	// the structural group completion are retained in the same family.
	if root.Status != "Queued" || root.Process.Steps[root.Step].ID != "join" || !root.TaskGroups["cards"].Done || c.familyAttempts(root) != 24 || err != nil || assignment.TaskSource == nil || len(assignment.TaskSource.Dependencies) != 6 || assignment.Inputs["results"].ArtifactID != req.GroupAttemptID+"/results" {
		t.Fatal("finished chain skipped or changed the ordinary manifest/join", root.Status, c.familyAttempts(root), err)
	}
}
