package runner_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

type cleanupMaintenanceExecutor struct {
	runner.Executor
	entered chan context.Context
	finish  chan struct{}
}

func (e *cleanupMaintenanceExecutor) ReleaseRun(ctx context.Context, _ runner.Run) error {
	e.entered <- ctx
	select {
	case <-e.finish:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*cleanupMaintenanceExecutor) ForgetRun(context.Context, string) error { return nil }

func TestCoreCleanupRetainsOneOperationAndCancelsOnClose(t *testing.T) {
	for _, closeOwner := range []bool{false, true} {
		t.Run(map[bool]string{false: "settle", true: "close"}[closeOwner], func(t *testing.T) {
			cfg := snapshot(policy())
			project := cfg.Projects["demo"]
			process := project.Processes["demo"]
			process.AttemptTimeout = "20m"
			project.Processes["demo"] = process
			cfg.Projects["demo"] = project
			before := runner.Run{
				ID: "run_cleanup", ProjectID: "demo", Process: process, Roles: project.Roles,
				Profiles: project.Profiles, Status: "Failed", Reason: "retained source failure",
				Created: time.Now().UTC(), Inputs: map[string]runner.Value{"text": runner.Text("Hello")},
				Attempts: []runner.Attempt{{
					ID: "attempt_cleanup", State: runner.Finished, Released: true,
					End: &runner.ExecutionEnd{Kind: "failed", Reason: "retained source failure"},
				}},
			}
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			store, err := sqlite.Open(dir, cfg.Instance.Storage)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Save(t.Context(), before, nil, nil); err != nil {
				t.Fatal(err)
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			executor := &cleanupMaintenanceExecutor{entered: make(chan context.Context, 1), finish: make(chan struct{})}
			rig := openRig(t, dir, cfg, func(base runner.Executor) runner.Executor {
				executor.Executor = base
				return executor
			})
			defer rig.close()
			var cleanup context.Context
			select {
			case cleanup = <-executor.entered:
			case <-time.After(4 * time.Second):
				t.Fatal("cleanup did not start")
			}
			deadline, ok := cleanup.Deadline()
			if !ok || time.Until(deadline) <= 5*time.Second || time.Until(deadline) > 5*time.Minute {
				t.Fatal("cleanup uses the short observer or an unbounded/extended task clock", deadline)
			}
			status := make(chan runner.Run, 1)
			go func() { r, _ := rig.core.Get(before.ID); status <- r }()
			select {
			case got := <-status:
				if got.Status != before.Status || got.ResourcesReleased || len(got.Attempts) != 1 {
					t.Fatal("unfinished maintenance changed task evidence", got)
				}
			case <-time.After(time.Second):
				t.Fatal("cleanup held the coordinator lock")
			}
			if _, err = rig.core.Retry(runner.RetryRequest{RequestID: "unsafe-during-cleanup", RunID: before.ID}); err == nil || !strings.Contains(err.Error(), "run resource cleanup is active") {
				t.Fatal("cleanup lost its existing inFlight recovery barrier", err)
			}
			if closeOwner {
				closed := make(chan struct{})
				go func() { rig.core.Close(); close(closed) }()
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("Core.Close did not cancel its cleanup operation")
				}
				if cleanup.Err() != context.Canceled {
					t.Fatal("cleanup outlived its Core owner", cleanup.Err())
				}
			} else {
				close(executor.finish)
				await(t, rig.core, before.ID, func(r runner.Run) bool { return r.ResourcesReleased })
			}
			got, err := rig.core.Get(before.ID)
			_, originalDefinition, _ := runner.MarshalRunRecord(before)
			_, definition, _ := runner.MarshalRunRecord(got)
			if err != nil || got.Status != before.Status || len(got.Attempts) != len(before.Attempts) || got.Process.MaxAttempts != before.Process.MaxAttempts || !bytes.Equal(definition, originalDefinition) || got.Attempts[0].End.Reason != before.Attempts[0].End.Reason {
				t.Fatal("cleanup changed task history, limits or frozen definition", got, err)
			}
			if closeOwner && got.ResourcesReleased {
				t.Fatal("owner cancellation invented successful cleanup")
			}
		})
	}
}
