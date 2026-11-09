//go:build darwin || linux

package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

// Only native dispatch and stop acknowledgement are simulated. Core, task
// seeding, stopped-source retention, SQLite and release use the real adapters.
type stoppedCardExecutor struct {
	*Service
	started      chan runner.Assignment
	fail         <-chan struct{}
	nativeStarts atomic.Int32
	stopped      atomic.Int32
}

func (s *stoppedCardExecutor) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	if a.Setup.Profile.Harness != "codex" {
		return s.Service.Start(ctx, a)
	}
	s.nativeStarts.Add(1)
	if s.started == nil {
		return runner.Observation{}, errors.New("recovery unexpectedly launched another native attempt")
	}
	if a.TaskSource.Identity.ItemID == "reject" {
		s.started <- a
		select {
		case <-s.fail:
			return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "failed", Reason: "fixture readiness rejection"}}, nil
		case <-ctx.Done():
			return runner.Observation{}, ctx.Err()
		}
	}
	w, err := s.workspaces.Prepare(ctx, a)
	if err != nil {
		return runner.Observation{}, err
	}
	if err = os.WriteFile(filepath.Join(w.WorkDir, "partial.txt"), []byte("stopped card edits\n"), 0600); err != nil {
		return runner.Observation{}, err
	}
	s.Codex.mu.Lock()
	err = s.Codex.saveLocked(nativeJob{Assignment: a, Workspace: &w, Stage: "active"})
	if err == nil {
		// The test owns this simulated turn, so no native peer should reconcile it.
		s.Codex.preparing[a.AttemptID] = true
	}
	s.Codex.mu.Unlock()
	if err != nil {
		return runner.Observation{}, err
	}
	s.started <- a
	return runner.Observation{State: runner.Running, NativeHandle: a.AttemptID}, nil
}

func (s *stoppedCardExecutor) Stop(ctx context.Context, id string) error {
	s.Codex.mu.Lock()
	if s.Codex.preparing[id] {
		j, err := s.Codex.loadLocked(id)
		if err == nil {
			j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "fixture native interruption confirmed"}
			j.StopRequested = true
			err = s.Codex.saveLocked(j)
		}
		if err != nil {
			s.Codex.mu.Unlock()
			return err
		}
		delete(s.Codex.preparing, id)
		s.stopped.Add(1)
	}
	s.Codex.mu.Unlock()
	return s.Service.Stop(ctx, id)
}

func TestTaskGroupRetainsStoppedCardAcrossObservationAndRestart(t *testing.T) {
	cfg, _, _ := taskSourceJourneySnapshot(t, sourceRepository(t))
	dir := filepath.Join(t.TempDir(), "state")
	open := func(exec *stoppedCardExecutor) *nativeRig {
		t.Helper()
		store, err := sqlite.Open(dir, cfg.Instance.Storage)
		if err != nil {
			t.Fatal(err)
		}
		disk, err := runner.OpenDisk(dir, cfg.Instance.Storage)
		if err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		exec.Service = NewService(disk, func(_ context.Context, id string) ([]byte, error) {
			return disk.Read("artifacts/"+id, disk.Policy.MaxArtifact)
		}, cfg.Instance.Codex)
		core, err := runner.New(t.Context(), store, exec, disk, cfg)
		if err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		exec.SetWake(core.ExecutionChanged)
		return &nativeRig{core: core, service: exec.Service, store: store, disk: disk}
	}
	fail := make(chan struct{})
	initial := &stoppedCardExecutor{started: make(chan runner.Assignment, 2), fail: fail}
	rig := open(initial)
	closed := false
	defer func() {
		if !closed {
			rig.close()
		}
	}()
	root, err := rig.core.Start(runner.StartRequest{RequestID: "stopped-parallel-card", Project: "demo", Inputs: map[string]runner.Value{
		"request": runner.Text("Exercise stopped source retention"),
		"tasks":   {Inline: []byte(`[{"id":"retain","inputs":{"task":"edit","paths":["partial.txt"]}},{"id":"reject","inputs":{"task":"reject","paths":["other.txt"]}}]`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var held runner.Assignment
	for range 2 {
		select {
		case a := <-initial.started:
			if a.TaskSource.Identity.ItemID == "retain" {
				held = a
			}
		case <-time.After(10 * time.Second):
			t.Fatal("parallel fixture did not start both owners")
		}
	}
	if held.AttemptID == "" {
		t.Fatal("missing stopped card assignment")
	}
	gate := captureGitGate(t, "tree", false)
	close(fail)
	select {
	case <-gate.ready:
	case <-time.After(10 * time.Second):
		t.Fatal("stopped card never reached the capture checkpoint")
	}
	rig.service.captureMu.Lock()
	worker := rig.service.captures[held.AttemptID]
	rig.service.captureMu.Unlock()
	if worker == nil {
		t.Fatal("stopped capture has no owned worker")
	}
	child := waitSourceRun(t, rig.core, held.RunID, func(r runner.Run) bool {
		a := r.Current()
		return a != nil && a.State == runner.Unknown && a.Reconciling
	}, 15*time.Second)
	if !child.CancelRequested || !captureLeaseIsHeld(t, rig.service, held) {
		t.Fatal("short Core observation interrupted the stopped capture", child.Status, child.Reason)
	}
	stopCtx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	if err = rig.service.Stop(stopCtx, held.AttemptID); err != nil {
		stop()
		t.Fatal(err)
	}
	stop()
	select {
	case <-worker.done:
		t.Fatal("repeated stop cancelled stopped-source retention")
	default:
	}
	// Stop the service with an owned, uncommitted capture. Its durable native
	// stop and manifest must let the next service settle that same attempt.
	rig.core.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err = rig.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = rig.store.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	if captureLeaseIsHeld(t, rig.service, held) {
		t.Fatal("old service left an owned Git writer active")
	}
	resumed := &stoppedCardExecutor{}
	newRig := open(resumed)
	defer newRig.close()
	done := waitSourceRun(t, newRig.core, root.ID, func(r runner.Run) bool {
		return r.Terminal() && r.ResourcesReleased
	}, 30*time.Second)
	if done.Status != "Failed" || !strings.Contains(done.Reason, "fixture readiness rejection") || done.Decision != nil || len(done.TaskStatuses) != 2 {
		t.Fatal("recovery changed the group failure or crossed a gate", done.Status, done.Reason)
	}
	for _, card := range done.TaskStatuses {
		if card.Attempts != 2 || card.Active || card.CleanupPending {
			t.Fatal("recovery changed card attempts or left resources active", card)
		}
	}
	child, err = newRig.core.Get(held.RunID)
	if err != nil || len(child.Attempts) != 2 {
		t.Fatal("stopped child history changed", err)
	}
	a := child.Attempts[1]
	if a.ID != held.AttemptID || a.End == nil || a.End.Kind != "stopped" || !a.Released || a.Source == nil || a.Source.PatchID == "" {
		t.Fatal("same stopped attempt did not retain source and release", a)
	}
	patch, err := newRig.disk.Read("artifacts/"+a.Source.PatchID, newRig.disk.Policy.MaxArtifact)
	if err != nil || !strings.Contains(string(patch), "+stopped card edits") {
		t.Fatal("partial source was not retained", err)
	}
	if artifact, err := newRig.store.Artifact(t.Context(), a.Source.PatchID); err != nil || artifact.RunID != child.ID {
		t.Fatal("retained source was not registered to the stopped child", artifact, err)
	}
	if initial.nativeStarts.Load() != 2 || initial.stopped.Load() != 1 || resumed.nativeStarts.Load() != 0 {
		t.Fatal("recovery replayed native work or changed stop ownership")
	}
}
