package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// A source worker may wait behind another operation's workspace lock. Closing
// the service must cancel that wait without requiring the other holder to exit
// first, and must retain the original incomplete attempt for reconciliation.
func TestTaskSourceServiceCloseCancelsWorkspaceLockWait(t *testing.T) {
	f := newTaskSourceFixture(t, false)
	a := f.assignment("a", "run_lock_wait", "attempt_lock_wait", "seed", []string{"a.txt"})
	a.Inputs["dependencies"] = runner.Value{Inline: json.RawMessage(`[]`)}
	f.service.workspaces.mu.Lock()
	defer func() {
		f.service.workspaces.mu.Unlock()
		_ = f.service.Close(context.Background())
	}()
	observer, detach := context.WithCancel(t.Context())
	detach()
	o, err := f.service.Start(observer, a)
	if err != nil || o.State != runner.Running || !o.Reconciling {
		t.Fatal("observer did not leave its admitted source worker active", o, err)
	}
	shutdown, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err = f.service.Close(shutdown); err != nil {
		t.Fatal("source Close required the unrelated workspace holder to exit", err)
	}
	saved, err := f.service.tasks.load(a.AttemptID)
	if err != nil || saved.End != nil || saved.Stopped || saved.PatchID != "" || !saved.Assignment.Deadline.Equal(a.Deadline) {
		t.Fatal("shutdown changed or discarded the original incomplete attempt", saved, err)
	}
	if _, err = f.service.workspaces.load(a.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled workspace wait mutated source", err)
	}
}

func TestWorkspaceCleanupAndRecoveryWaitsRespectCancellation(t *testing.T) {
	w := &Workspaces{disk: workspaceDisk(t)}
	a := runner.Assignment{RunID: "run_missing", AttemptID: "attempt_missing"}
	run := runner.Run{ID: a.RunID, Status: "Failed"}
	for _, tc := range []struct {
		name string
		call func(context.Context) error
	}{
		{"run cleanup", func(ctx context.Context) error { return w.ReleaseRun(ctx, a.RunID) }},
		{"forget", func(ctx context.Context) error { return w.ForgetRun(ctx, a.RunID) }},
		{"attempt cleanup", func(ctx context.Context) error { return w.ReleaseAttempt(ctx, a) }},
		{"task scratch", func(ctx context.Context) error { return w.releaseTaskScratch(ctx, taskSourceJob{Assignment: a}) }},
		{"adoption pins", func(ctx context.Context) error { return w.forgetAdoptedSource(ctx, a.RunID) }},
		{"task pins", func(ctx context.Context) error { return w.forgetTaskPins(ctx, a.RunID) }},
		{"source validation", func(ctx context.Context) error { return w.validateSourceRecovery(ctx, run, runner.SourceResult{}) }},
		{"source preservation", func(ctx context.Context) error {
			_, err := w.preserveRecoverySource(ctx, run, runner.SourceResult{})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w.mu.Lock()
			defer w.mu.Unlock()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered, done := make(chan struct{}), make(chan error, 1)
			go func() { close(entered); done <- tc.call(ctx) }()
			<-entered
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cleanup ignored its cancelled lock wait", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cleanup waited for an unrelated workspace owner")
			}
		})
	}
}
