package control

import (
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestResultWaitReturnsSavedChildDecisionBesideIndependentExecution(t *testing.T) {
	now := time.Now().UTC()
	reader := &taskResultReader{run: runner.Run{
		ID: "root", Status: "Running", Created: now,
		TaskStatuses: []runner.TaskStatus{
			{RunID: "active", Status: "Running", Attempts: 1, Active: true, Unsettled: true, Progress: &runner.Progress{
				Active: 1, Execution: "active", LastProgressAt: now,
				Attempts: []runner.AttemptTiming{{RunID: "active", AttemptID: "work", State: runner.Running, Started: now, Deadline: now.Add(20 * time.Minute), DeadlineKnown: true}},
			}},
			{RunID: "decision", Status: "Waiting", Attempts: 1, Progress: &runner.Progress{
				Attention: []runner.Attention{{RunID: "decision", Kind: "human_decision", DecisionID: "saved-approval"}},
			}},
		},
	}}
	out, err := result(t.Context(), reader, ResultArgs{RunID: "root", Wait: true})
	if err != nil || reader.waits != 0 || out.ExecutionSettled || out.Status != "Running" || out.Attempts != 2 || out.Progress.Active != 1 || out.Progress.NextAction != "decide_saved_approval" {
		t.Fatal("waiting presentation hid decision or changed independent execution", out, err)
	}
	if len(out.Progress.Attempts) != 1 || !out.Progress.Attempts[0].Deadline.Equal(now.Add(20*time.Minute)) {
		t.Fatal("active child's authoritative deadline was omitted")
	}
}
