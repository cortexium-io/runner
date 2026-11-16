package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestRoleTaskResultShowsHumanWaitWithoutCopyingContext(t *testing.T) {
	r := runner.Run{Status: "Waiting", TaskGraph: &runner.TaskGraphState{
		RootRunID: "root", ParentRunID: "parent", TaskID: "choose", Role: "planner",
		Phase: runner.TaskWaitingInput, Question: "Which audience?", Revisions: 2,
		Brief: "private original brief", Directive: "private steering context",
		Children: []runner.RoleTaskLink{{RunID: "retained", Retired: true}, {RunID: "current"}},
		Results:  []runner.TaskResultRef{{ID: "private-child", RunID: "private-child-run"}},
	}}
	out := compactResult(r, time.Now())
	if out.TaskGraph == nil || out.TaskGraph.Question != "Which audience?" || out.TaskGraph.Children != 1 || out.TaskGraph.Retired != 1 || waitingForProgress(r, out) {
		t.Fatal("a human question must be actionable without waiting for automatic progress", out)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private") {
		t.Fatal("compact result copied task context or child reports")
	}
	r.TaskGraph.Phase = runner.TaskWaitingChildren
	if !waitingForProgress(r, out) {
		t.Fatal("automatic child progress returned as an owner blocker")
	}
	r.TaskGraph.Paused = true
	if waitingForProgress(r, out) {
		t.Fatal("explicit pause hidden by automatic waiting")
	}
}

func TestNativeResultDoesNotTreatUnreportedUsageAsZero(t *testing.T) {
	r := runner.Run{
		Status: "Completed", Process: runner.Process{Steps: []runner.Step{{Role: "writer"}}},
		Roles:    map[string]runner.Role{"writer": {Execution: &runner.Profile{Harness: "native"}}},
		Attempts: []runner.Attempt{{State: runner.Finished, Released: true}},
	}
	if got := compactResult(r, time.Now()); got.Usage.IncompleteAttempts != 1 {
		t.Fatal("native unknown usage was presented as a known zero", got.Usage)
	}
}

func TestCompactResultRetainsBoundedFailureDiagnostic(t *testing.T) {
	diagnostic := runner.DiagnosticTail(strings.Repeat("old ", 200) + "stage=browser")
	r := runner.Run{
		Status: "Failed", Process: runner.Process{Steps: []runner.Step{{Role: "check"}}},
		Roles:        map[string]runner.Role{"check": {Execution: &runner.Profile{Harness: "command"}}},
		Attempts:     []runner.Attempt{{State: runner.Finished, Released: true, End: &runner.ExecutionEnd{Kind: "stopped", Diagnostic: diagnostic}}},
		TaskStatuses: []runner.TaskStatus{{ID: "check", RunID: "child", Status: "Failed", Diagnostic: diagnostic}},
	}
	out := compactResult(r, time.Now())
	if out.Diagnostic != diagnostic || len(out.Tasks) != 1 || out.Tasks[0].Diagnostic != diagnostic || !out.ExecutionSettled || out.CleanupPending || out.Status != "Failed" {
		t.Fatal("compact result lost failure facts or changed settlement/status", out)
	}
	r.Attempts = append(r.Attempts, runner.Attempt{State: runner.Finished, Released: true, End: &runner.ExecutionEnd{Kind: "returned"}})
	if compactResult(r, time.Now()).Diagnostic != "" {
		t.Fatal("later success revived the previous diagnostic")
	}
}

func TestRoleTaskWaitReturnsDescendantAttention(t *testing.T) {
	for _, kind := range []string{"task_question", "task_paused", "blocked", "cleanup_blocked"} {
		t.Run(kind, func(t *testing.T) {
			reader := &taskResultReader{run: runner.Run{
				ID: "root", Status: "Waiting", TaskGraph: &runner.TaskGraphState{Phase: runner.TaskWaitingChildren},
				TaskStatuses: []runner.TaskStatus{{RunID: "nested", Progress: &runner.Progress{
					Attention: []runner.Attention{{RunID: "nested", Kind: kind}},
				}}},
			}}
			if _, err := result(t.Context(), reader, ResultArgs{RunID: "root", Wait: true}); err != nil || reader.waits != 0 {
				t.Fatal("root wait hid actionable descendant attention", err, reader.waits)
			}
		})
	}
	reader := &taskResultReader{run: runner.Run{ID: "root", Status: "Waiting", TaskGraph: &runner.TaskGraphState{Phase: runner.TaskWaitingChildren}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := result(ctx, reader, ResultArgs{RunID: "root", Wait: true}); !errors.Is(err, context.Canceled) || reader.waits != 1 {
		t.Fatal("ordinary child work failed to join automatically", err, reader.waits)
	}
}

func TestRoleTaskResultBoundsPresentationWithoutLosingAccounting(t *testing.T) {
	r := runner.Run{Status: "Waiting", TaskGraph: &runner.TaskGraphState{Phase: runner.TaskWaitingChildren}}
	for range 200 {
		r.TaskStatuses = append(r.TaskStatuses, runner.TaskStatus{Status: "Completed", Attempts: 1})
	}
	r.TaskStatuses[199].Active = true
	r.TaskStatuses[199].RunID = "important"
	out := compactResult(r, time.Now())
	if out.TaskCount != 200 || !out.TasksTruncated || len(out.Tasks) != 64 || out.Attempts != 200 || out.Tasks[0].RunID != "important" || out.ExecutionSettled {
		t.Fatal("presentation bound hid active work or changed whole-family accounting", out)
	}
}

func TestCancelledRoleTaskDoesNotAskForSavedInput(t *testing.T) {
	r := runner.Run{Status: "Cancelled", TaskGraph: &runner.TaskGraphState{
		Phase: runner.TaskWaitingInput, Question: "Earlier missing runtime permission", Paused: true,
	}}
	out := compactResult(r, time.Now())
	if len(out.Progress.Attention) != 0 || out.Progress.NextAction != "none" {
		t.Fatal("cancelled work still asks the owner to answer or resume", out.Progress)
	}
}

func TestRoleTaskFailureRemainsWithParentForAutomaticRepair(t *testing.T) {
	r := runner.Run{ID: "root", Status: "Waiting", TaskGraph: &runner.TaskGraphState{Phase: runner.TaskWaitingChildren},
		TaskStatuses: []runner.TaskStatus{{ID: "test", RunID: "child", Status: "Failed", Reason: "A concrete failed check",
			Progress: &runner.Progress{Attention: []runner.Attention{{RunID: "child", Kind: "blocked"}}},
		}},
	}
	out := compactResult(r, time.Now())
	if out.Progress.NextAction != "wait_for_tasks" || out.Progress.Pending != 0 || len(out.Progress.Attention) != 0 || out.Tasks[0].Status != "Failed" {
		t.Fatal("ordinary repair became an owner blocker or hid the failed task", out)
	}
	reader := &taskResultReader{run: r}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := result(ctx, reader, ResultArgs{RunID: "root", Wait: true}); !errors.Is(err, context.Canceled) || reader.waits != 1 {
		t.Fatal("result stopped waiting while parent owns the failure", err, reader.waits)
	}
	r.Status = "Failed"
	if got := compactResult(r, time.Now()); got.Progress.NextAction != "inspect_blocker" {
		t.Fatal("root failure stopped being actionable", got.Progress)
	}
	for _, status := range []string{"Completed", "Cancelled"} {
		r.Status = status
		if got := compactResult(r, time.Now()); got.Progress.NextAction != "none" || len(got.Progress.Attention) != 0 || got.Tasks[0].Status != "Failed" {
			t.Fatal("resolved child failure overruled the terminal root or disappeared", status, got)
		}
	}
}
