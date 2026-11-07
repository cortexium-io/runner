package control

import (
	"context"
	"errors"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

type taskResultReader struct {
	run   runner.Run
	waits int
}

func (s *taskResultReader) Get(string) (runner.Run, error) {
	r := runner.Clone(s.run)
	r.TaskStatuses = append([]runner.TaskStatus(nil), s.run.TaskStatuses...)
	return r, nil
}
func (s *taskResultReader) Wait(ctx context.Context, _ string, _ int64) (runner.Summary, error) {
	s.waits++
	return runner.Summary{}, ctx.Err()
}

func staleTaskWait() runner.Run {
	return runner.Run{
		ID: "parent", Status: "Waiting", Reason: "task harness: reconciliation required: same owned source capture remains active",
		Process: runner.Process{Steps: []runner.Step{{ID: "cards", ForEach: &runner.TaskGroup{Mode: "isolated"}}}},
		TaskGroups: map[string]*runner.TaskGroupState{"cards": {
			StepID: "cards", Children: []runner.TaskChild{{ItemID: "harness", RunID: "child-harness"}, {ItemID: "report", RunID: "child-report"}},
		}},
		TaskStatuses: []runner.TaskStatus{
			{ID: "harness", RunID: "child-harness", Status: "Queued", Attempts: 2},
			{ID: "report", RunID: "child-report", Status: "Waiting", Reason: "waiting for declared task prerequisites"},
		},
	}
}

func TestResultWaitJoinsResumedTaskWithoutChangingSavedFacts(t *testing.T) {
	for _, status := range []string{"Queued", "Running", "Completed"} {
		t.Run(status, func(t *testing.T) {
			saved := staleTaskWait()
			saved.TaskStatuses[0].Status = status
			reader := &taskResultReader{run: saved}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := result(ctx, reader, ResultArgs{RunID: saved.ID, Wait: true}); !errors.Is(err, context.Canceled) || reader.waits != 1 {
				t.Fatal("resumed child was reported as a workflow blocker", err, reader.waits)
			}
			out, err := result(t.Context(), reader, ResultArgs{RunID: saved.ID})
			if err != nil || out.Status != "Waiting" || out.Reason != saved.Reason || out.Reconciling || !out.ExecutionSettled || reader.waits != 1 {
				t.Fatal("nonwaiting read changed ownership/settlement facts or waited", out, err)
			}
		})
	}
	reader := &taskResultReader{run: staleTaskWait()}
	reader.run.TaskStatuses[0].Status, reader.run.TaskStatuses[0].Reconciling = "Waiting", true
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := result(ctx, reader, ResultArgs{RunID: reader.run.ID, Wait: true}); !errors.Is(err, context.Canceled) || reader.waits != 1 {
		t.Fatal("bounded task reconciliation stopped waiting", err, reader.waits)
	}
}

func TestResultWaitStillReturnsActualTaskAndOwnerBlockers(t *testing.T) {
	for name, change := range map[string]func(*runner.Run){
		"owner decision during child reconciliation": func(r *runner.Run) {
			r.Decision = &runner.Decision{ID: "merge"}
			r.TaskStatuses[0].Status, r.TaskStatuses[0].Reconciling = "Waiting", true
		},
		"reconciliation beside real blocker": func(r *runner.Run) {
			r.TaskStatuses[0].Status, r.TaskStatuses[0].Reconciling = "Waiting", true
			r.TaskStatuses[1].Reason = "unknown source ownership"
		},
		"cancellation during child reconciliation": func(r *runner.Run) {
			r.CancelRequested = true
			r.TaskStatuses[0].Status, r.TaskStatuses[0].Reconciling = "Waiting", true
		},
		"owner decision":          func(r *runner.Run) { r.Decision = &runner.Decision{ID: "merge"} },
		"cancellation":            func(r *runner.Run) { r.CancelRequested = true },
		"parent storage blocker":  func(r *runner.Run) { r.Reason = "task manifest persistence: managed storage limit" },
		"parent history blocker":  func(r *runner.Run) { r.Reason = "history capacity" },
		"child storage blocker":   func(r *runner.Run) { r.TaskStatuses[1].Reason = "managed storage limit" },
		"child budget blocker":    func(r *runner.Run) { r.TaskStatuses[1].Reason = "attempt allowance exhausted" },
		"child integrity blocker": func(r *runner.Run) { r.TaskStatuses[1].Reason = "unknown source ownership" },
		"unresolved capture": func(r *runner.Run) {
			r.TaskStatuses[0].Status, r.TaskStatuses[0].Reason = "Waiting", "capture ownership unknown"
		},
		"expired reconciliation": func(r *runner.Run) {
			r.TaskStatuses[0].Status, r.TaskStatuses[0].Reconciling = "Waiting", false
		},
		"missing child":               func(r *runner.Run) { r.TaskStatuses = r.TaskStatuses[:1] },
		"retained creation wait":      func(r *runner.Run) { r.TaskStatuses[1].Reason = "waiting for retained child creation" },
		"attempted prerequisite wait": func(r *runner.Run) { r.TaskStatuses[1].Attempts = 1 },
		"failed child":                func(r *runner.Run) { r.TaskStatuses[1].Status = "Failed" },
		"unrecognized status":         func(r *runner.Run) { r.TaskStatuses[1].Status = "Unrecognized" },
		"foreign item":                func(r *runner.Run) { r.TaskStatuses[0].ID = "other" },
		"duplicate status":            func(r *runner.Run) { r.TaskStatuses = append(r.TaskStatuses, r.TaskStatuses[0]) },
		"completed group":             func(r *runner.Run) { r.TaskGroups["cards"].Done = true },
		"failed group":                func(r *runner.Run) { r.TaskGroups["cards"].Failure = "preserve uncertain source" },
		"missing group":               func(r *runner.Run) { delete(r.TaskGroups, "cards") },
		"different group identity":    func(r *runner.Run) { r.TaskGroups["cards"].StepID = "old" },
		"old queued group only": func(r *runner.Run) {
			r.TaskStatuses[0].RunID = "old-child"
			r.TaskGroups["old"] = &runner.TaskGroupState{StepID: "old", Children: []runner.TaskChild{{ItemID: "harness", RunID: "old-child"}}}
		},
		"no current group": func(r *runner.Run) { r.Step = 1 },
		"serial group":     func(r *runner.Run) { r.Process.Steps[0].ForEach.Mode = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := staleTaskWait()
			change(&r)
			reader := &taskResultReader{run: r}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			out, err := result(ctx, reader, ResultArgs{RunID: r.ID, Wait: true})
			if err != nil || out.Status != "Waiting" || reader.waits != 0 {
				t.Fatal("real blocker hidden by queued work", out, err, reader.waits)
			}
		})
	}
}
