package runner

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type taskCancellationStore struct {
	Store
	saves              int
	rejectTerminalOnce bool
}

func (s *taskCancellationStore) Save(_ context.Context, r Run, _ []Artifact, _ *Receipt) error {
	s.saves++
	if s.rejectTerminalOnce && r.Task == nil && r.Terminal() {
		s.rejectTerminalOnce = false
		return errors.New("terminal persistence interrupted")
	}
	return nil
}

func failedTaskGroupFixture(t *testing.T) (*Core, Run, Run, *taskCancellationStore) {
	t.Helper()
	c, root := familyBudgetFixture()
	store := &taskCancellationStore{}
	c.ctx, c.store = t.Context(), store
	c.changed, c.wake = make(chan struct{}), make(chan struct{}, 1)
	root.Status, root.Attempts = "Running", nil
	failed := c.runs["child-one"]
	failed.Status, failed.Reason = "Failed", "card check failed"
	failed.Attempts = []Attempt{{
		ID: "failed-check", State: Finished, Released: true,
		End: &ExecutionEnd{Kind: "failed", Reason: failed.Reason},
	}}
	c.runs[root.ID], c.runs[failed.ID] = root, failed
	return c, root, c.runs["child-two"], store
}

func TestFailedTaskGroupCancelsSiblingWithoutRepeatingWrites(t *testing.T) {
	c, root, sibling, store := failedTaskGroupFixture(t)
	sibling.Status = "Running"
	sibling.Attempts = []Attempt{{ID: "active-owner", State: Running}}
	c.runs[sibling.ID] = sibling
	drive := func() { c.driveTaskGroup(Clone(c.runs[root.ID])) }
	drive()
	select {
	case <-c.wake:
	default:
		t.Fatal("new sibling cancellation did not wake Core")
	}
	const failure = "task one: card check failed"
	sibling = Clone(c.runs[sibling.ID])
	if !sibling.CancelRequested || sibling.Reason != "parent task group is stopping" || c.runs[root.ID].TaskGroups["cards"].Failure != failure || store.saves != 2 {
		t.Fatal("initial failure and cancellation were not persisted once", sibling, store.saves)
	}

	// An executor may still be collecting source after acknowledging Stop.
	// Model that saved observation and drive the parent without timers or workers.
	sibling.Status, sibling.Reason = "Waiting", "reconciliation required: owned source capture remains active"
	a := sibling.Current()
	a.State, a.Reconciling, a.StopRequested = Unknown, true, true
	a.StopAttempts, a.StopAllowance = 1, 3
	a.NextCheck = time.Unix(10, 0).UTC()
	if err := c.persist(sibling, nil, nil); err != nil {
		t.Fatal(err)
	}
	saved := Clone(c.runs[sibling.ID])
	changed, saves := c.changed, store.saves
	for range 3 {
		drive()
	}
	if store.saves != saves || c.changed != changed || len(c.wake) != 0 || !reflect.DeepEqual(c.runs[sibling.ID], saved) || c.runs[root.ID].Terminal() {
		t.Fatalf("waiting for sibling capture rewrote or re-woke the group (saves %d→%d, wake %d, sibling reason %q)", saves, store.saves, len(c.wake), c.runs[sibling.ID].Reason)
	}

	sibling = Clone(saved)
	sibling.Status, sibling.Reason = "Cancelled", "cancellation settled"
	a = sibling.Current()
	a.State, a.Reconciling, a.End = Finished, false, &ExecutionEnd{Kind: "stopped"}
	c.runs[sibling.ID] = sibling
	drive()
	if c.runs[root.ID].Terminal() || store.saves != saves || len(c.wake) != 0 {
		t.Fatal("group settled before the cancelled sibling attempt was released")
	}
	a.Released = true
	drive()
	done := c.runs[root.ID]
	group := done.TaskGroups["cards"]
	if done.Status != "Failed" || done.Reason != failure || !group.Done || group.AttemptID != root.TaskGroups["cards"].AttemptID || store.saves != saves+1 || !reflect.DeepEqual(c.runs[sibling.ID], sibling) {
		t.Fatal("group failed to settle with its original failure and attempts", done.Status, done.Reason, group, store.saves)
	}
}

func TestFailedTaskGroupSettlesIdleSiblingWithCancellationRequested(t *testing.T) {
	c, root, sibling, _ := failedTaskGroupFixture(t)
	sibling.Status, sibling.Reason = "Waiting", "waiting for declared task prerequisites"
	sibling.CancelRequested, sibling.Attempts = true, nil
	c.runs[sibling.ID] = sibling
	c.driveTaskGroup(Clone(root))
	if c.runs[sibling.ID].Status != "Cancelled" || c.runs[root.ID].Status != "Failed" {
		t.Fatal("group must finish cancelling the idle sibling before settling", c.runs[sibling.ID].Status, c.runs[root.ID].Status)
	}
}

func TestFailedTaskGroupRetriesTerminalSaveWithoutLosingCause(t *testing.T) {
	c, root, sibling, store := failedTaskGroupFixture(t)
	sibling.Status, sibling.Reason, sibling.CancelRequested = "Cancelled", "cancellation settled", true
	sibling.Attempts = []Attempt{{
		ID: "stopped-owner", State: Finished, Released: true,
		End: &ExecutionEnd{Kind: "stopped"},
	}}
	c.runs[sibling.ID] = sibling
	store.rejectTerminalOnce = true

	const failure = "task one: card check failed"
	c.driveTaskGroup(Clone(root))
	saved := c.runs[root.ID]
	group := saved.TaskGroups["cards"]
	if saved.Terminal() || group.Done || group.Failure != failure || len(c.wake) != 0 || store.saves != 2 || c.maintenanceError != "terminal persistence interrupted" {
		t.Fatal("failed terminal save changed the retained group or woke Core", saved.Status, group, store.saves, len(c.wake), c.maintenanceError)
	}

	c.driveTaskGroup(Clone(saved))
	done := c.runs[root.ID]
	group = done.TaskGroups["cards"]
	if done.Status != "Failed" || done.Reason != failure || !group.Done || group.Failure != failure || store.saves != 3 || len(c.wake) != 1 || c.maintenanceError != "" || !reflect.DeepEqual(c.runs[sibling.ID], sibling) {
		t.Fatal("next drive did not settle the original failure", done.Status, done.Reason, group, store.saves, len(c.wake), c.maintenanceError)
	}
}
