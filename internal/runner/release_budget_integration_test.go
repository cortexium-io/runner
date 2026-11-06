package runner_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

type releaseBudgetExecutor struct {
	runner.Executor
	started    chan string
	observed   chan string
	notStarted bool
}

func (e *releaseBudgetExecutor) Start(_ context.Context, a runner.Assignment) (runner.Observation, error) {
	e.started <- a.Setup.Profile.Mode
	return releaseBudgetReturned(), nil
}
func (e *releaseBudgetExecutor) Observe(_ context.Context, id string) (runner.Observation, error) {
	e.observed <- id
	if e.notStarted {
		return runner.Observation{State: runner.NotStarted}, nil
	}
	return releaseBudgetReturned(), nil
}
func releaseBudgetReturned() runner.Observation {
	return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: "ready", Outputs: map[string]runner.Value{"receipt": {Inline: json.RawMessage(`{"qualified":"preserved"}`)}}}}}
}

func releaseBudgetRig(t *testing.T, run runner.Run, notStarted ...bool) (*rig, *releaseBudgetExecutor) {
	t.Helper()
	s := snapshot(policy())
	s.Instance.Storage.Database.MaxRecord = 64 << 10
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(dir, s.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), run, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	executor := &releaseBudgetExecutor{started: make(chan string, 8), observed: make(chan string, 8)}
	if len(notStarted) > 0 {
		executor.notStarted = notStarted[0]
	}
	rig := openRig(t, dir, s, func(e runner.Executor) runner.Executor { executor.Executor = e; return executor })
	return rig, executor
}

func releaseBudgetHistory(r *runner.Run, count int) {
	r.ID, r.ProjectID = "budget_fixture", "demo"
	r.Attempts = make([]runner.Attempt, count)
	for i := range r.Attempts {
		r.Attempts[i] = runner.Attempt{ID: "previous", Step: 0, State: runner.Finished, Released: true}
	}
}

func TestReleaseBudgetBlocksNewStopButReconcilesStartedStop(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-stop", true: "existing-stop"}[active], func(t *testing.T) {
			r := runner.ReleaseBudgetTestRun(t, true)
			r.Step = runner.ReleaseBudgetTestStep(r, "stop")
			r.Status = "Queued"
			releaseBudgetHistory(&r, 28) // Seven slots cannot cover eight release stages.
			if active {
				releaseBudgetHistory(&r, 35)
				r.Status = "Running"
				r.Attempts[34] = runner.Attempt{ID: "already-dispatched", Step: r.Step, State: runner.Unknown, Deadline: time.Now().Add(time.Minute)}
			}
			rig, executor := releaseBudgetRig(t, r)
			defer rig.close()
			done := await(t, rig.core, r.ID, func(r runner.Run) bool {
				if active {
					return r.Status == "Waiting" && r.Step == runner.ReleaseBudgetTestStep(r, "migrate")
				}
				return r.Status == "Waiting" && strings.HasPrefix(r.Reason, "release attempt reserve:")
			})
			if len(done.Attempts) != len(r.Attempts) || len(executor.started) != 0 {
				t.Fatal("reserve guard dispatched a new release effect", done.Status, done.Reason)
			}
			if active && (len(executor.observed) == 0 || done.Attempts[34].End == nil || done.Attempts[34].End.Result.Outcome != "ready") {
				t.Fatal("budget guard abandoned already-started stop reconciliation")
			}
		})
	}
}

func TestOldUnsafeReleaseDecisionBecomesAuditedBudgetWaitAndUsesExistingRetry(t *testing.T) {
	for _, used := range []int{28, 35} {
		r := runner.ReleaseBudgetTestRun(t, true)
		r.Status = "Waiting"
		releaseBudgetHistory(&r, used)
		r.Attempts[used-1] = runner.Attempt{ID: "saved-prepare", Step: r.Step, State: runner.Finished, Released: true, End: releaseBudgetReturned().End}
		r.Attempts[used-2] = runner.Attempt{ID: "saved-qualification", Step: runner.ReleaseBudgetTestStep(r, "qualify"), State: runner.Finished, Released: true, End: releaseBudgetReturned().End}
		r.Decision = &runner.Decision{ID: "older-owner-prompt", AttemptID: "saved-prepare", Step: r.Process.Steps[r.Step].ID, Outcome: "ready", Next: "merge", Reason: "qualified candidate"}
		originalEnd, _ := json.Marshal(r.Attempts[used-1].End)
		rig, executor := releaseBudgetRig(t, r)
		wait := await(t, rig.core, r.ID, func(r runner.Run) bool {
			return r.Status == "Waiting" && r.Decision == nil && strings.HasPrefix(r.Reason, "release attempt reserve:")
		})
		if wait.CancelRequested || len(wait.Attempts) != used {
			t.Fatal("unsafe approval retirement cancelled or repeated work")
		}
		audit, err := rig.store.FindReceipt(t.Context(), "release-budget-"+r.Decision.ID)
		if err != nil || audit.Kind != "release_budget_wait" || !strings.Contains(string(audit.Args), r.Decision.ID) {
			t.Fatal("retirement lost the unapproved prompt audit", err)
		}
		got, _ := json.Marshal(wait.Attempts[used-1].End)
		if string(got) != string(originalEnd) {
			t.Fatal("retirement rewrote the qualified result/receipt")
		}
		req := runner.RetryRequest{RequestID: "separately-approved-extra4", RunID: r.ID, ExtraAttempts: 4}
		if _, err := rig.core.Retry(req); err != nil {
			t.Fatal("existing explicit retry cannot rejoin budget wait", err)
		}
		after := await(t, rig.core, r.ID, func(r runner.Run) bool { return r.Status == "Waiting" && len(r.Attempts) == used+1 })
		if after.Process.MaxAttempts != 39 || after.Process.MaxAttemptsPerStep != 1 || len(executor.started) != 1 || <-executor.started != "prepare" {
			t.Fatal("retry changed setup or started merge rather than repeating read-only prepare")
		}
		if used == 28 && (after.Decision == nil || after.Decision.ID == r.Decision.ID) {
			t.Fatal("sufficient explicit extra allowance did not require a fresh approval")
		}
		if used == 35 && (after.Decision != nil || !strings.HasPrefix(after.Reason, "release attempt reserve:")) {
			t.Fatal("extra4 was treated as sufficient regardless of actual remaining budget")
		}
		rig.close()
	}
}

func TestLegacyAllocatedStopChecksReserveWithItsExistingRowCredit(t *testing.T) {
	for _, used := range []int{28, 29} {
		r := runner.ReleaseBudgetTestRun(t, true)
		r.Step = runner.ReleaseBudgetTestStep(r, "stop")
		r.Status = "Running"
		releaseBudgetHistory(&r, used)
		r.Attempts[used-1] = runner.Attempt{ID: "allocated-before-old-owner-exit", Step: r.Step, State: runner.Unknown, Deadline: time.Now().Add(time.Minute)}
		r.Attempts[used-2] = runner.Attempt{ID: "saved-backup", Step: runner.ReleaseBudgetTestStep(r, "backup"), State: runner.Finished, Released: true, End: releaseBudgetReturned().End}
		rig, executor := releaseBudgetRig(t, r, true)
		if used == 28 {
			done := await(t, rig.core, r.ID, terminal)
			if done.Status != "Completed" || len(done.Attempts) != used+4 || len(executor.started) == 0 || <-executor.started != "stop" {
				t.Fatal("already-counted exact Stop reserve was charged twice", done.Status, done.Reason)
			}
		} else {
			wait := await(t, rig.core, r.ID, func(r runner.Run) bool {
				return r.Status == "Waiting" && strings.HasPrefix(r.Reason, "release attempt reserve:")
			})
			if wait.Current().State != runner.NotStarted || wait.Current().StopRequested || wait.CancelRequested || len(executor.started) != 0 || len(wait.Attempts) != used {
				t.Fatal("unsafe undispatched Stop was started, cancelled or replayed")
			}
		}
		rig.close()
	}
}
