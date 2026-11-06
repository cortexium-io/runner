//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func startBlockedTaskSeed(t *testing.T, f *taskSourceFixture, gate *captureGitFixture) runner.Assignment {
	t.Helper()
	a := f.assignment("a", "run_a", "attempt_a_seed", "seed", []string{"a.txt"})
	a.Inputs["dependencies"] = runner.Value{Inline: json.RawMessage("[]")}
	call, cancel := context.WithCancel(t.Context())
	done := make(chan runner.Observation, 1)
	go func() {
		o, err := f.service.Start(call, a)
		if err != nil {
			t.Error(err)
		}
		done <- o
	}()
	select {
	case <-gate.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("source phase did not reach deterministic gate")
	}
	cancel()
	select {
	case o := <-done:
		if o.State != runner.Running || !o.Reconciling {
			t.Fatal("observer cancellation ended operation", o)
		}
	case <-time.After(time.Second):
		t.Fatal("observer did not detach")
	}
	return a
}

func TestTaskSourcesWorkerJoinsObserverCancellationWithoutReplayingSource(t *testing.T) {
	f := newTaskSourceFixture(t, false)
	gate := captureGitGate(t, "tree", false)
	wake := make(chan string, 1)
	f.service.SetWake(func(id string) { wake <- id })
	a := startBlockedTaskSeed(t, f, gate)
	f.service.tasks.mu.Lock()
	original := f.service.tasks.workers[a.AttemptID]
	f.service.tasks.mu.Unlock()
	call, cancel := context.WithCancel(t.Context())
	cancel()
	if o, err := f.service.Observe(call, a.AttemptID); err != nil || o.State != runner.Running {
		t.Fatal(o, err)
	}
	f.service.tasks.mu.Lock()
	same := f.service.tasks.workers[a.AttemptID] == original
	f.service.tasks.mu.Unlock()
	if !same {
		t.Fatal("observer replaced active attempt worker")
	}
	if err := f.service.Release(t.Context(), a.AttemptID); err == nil {
		t.Fatal("active worker was released")
	}
	if _, err := gate.hold.Write([]byte("continue\n")); err != nil {
		t.Fatal(err)
	}
	o, err := f.service.Observe(t.Context(), a.AttemptID)
	taskReturned(t, o, err, "ready")
	select {
	case id := <-wake:
		if id != a.AttemptID {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("missing Core wake")
	}
	data, err := os.ReadFile(filepath.Join(gate.dir, "calls"))
	if err != nil || strings.Count(string(data), "tree\n") != 1 {
		t.Fatalf("source replayed: %q %v", data, err)
	}
}

func TestTaskSourcesRestartRejoinsLiteralSourceAndStopRetainsMutation(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "restart", true: "stop"}[stop], func(t *testing.T) {
			f := newTaskSourceFixture(t, false)
			gate := captureGitGate(t, "tree", false)
			a := startBlockedTaskSeed(t, f, gate)
			closeCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if stop {
				if err := f.service.Stop(closeCtx, a.AttemptID); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.service.Close(closeCtx); err != nil {
				t.Fatal(err)
			}
			reader := func(_ context.Context, id string) ([]byte, error) {
				return f.disk.Read("artifacts/"+id, f.disk.Policy.MaxArtifact)
			}
			resumed := NewService(f.disk, reader, nil)
			t.Cleanup(func() { _ = resumed.Close(context.Background()) })
			o, err := resumed.Observe(t.Context(), a.AttemptID)
			if stop {
				if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "stopped" || o.Source == nil {
					t.Fatal("stopped source not retained", o, err)
				}
			} else {
				taskReturned(t, o, err, "ready")
			}
			if o.Source == nil || o.Source.PatchID != a.AttemptID+"/changes.patch" {
				t.Fatal("restart replaced literal source", o)
			}
			if err = resumed.Release(t.Context(), a.AttemptID); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Lstat(filepath.Join(f.disk.Root, "work", a.AttemptID)); !os.IsNotExist(err) {
				t.Fatal("owned helper scratch retained", err)
			}
		})
	}
}

func TestTaskSourcesStopBeforeSeedLeavesNoWorkspace(t *testing.T) {
	f := newTaskSourceFixture(t, false)
	a := f.assignment("a", "run_a", "attempt_a_seed", "seed", []string{"a.txt"})
	a.Inputs["dependencies"] = runner.Value{Inline: json.RawMessage("[]")}
	_, lease, err := f.service.workspaces.taskLease(a)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	call, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = f.service.Start(call, a); err != nil {
		t.Fatal(err)
	}
	if err = f.service.Stop(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	o, err := f.service.Observe(t.Context(), a.AttemptID)
	if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "stopped" || o.Source != nil {
		t.Fatal(o, err)
	}
	if _, err = f.service.workspaces.load(a.RunID); !os.IsNotExist(err) {
		t.Fatal("cancellation installed source", err)
	}
}
