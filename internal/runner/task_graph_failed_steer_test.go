package runner_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type taskResourcesExecutor struct{ runner.Executor }

func (taskResourcesExecutor) ReleaseRun(context.Context, runner.Run) error { return nil }
func (taskResourcesExecutor) ForgetRun(context.Context, string) error      { return nil }

func TestGeneralTasksFailedSteerCompletesSameGoalWithinExactRemainingBudget(t *testing.T) {
	s := graphSnapshot(t)
	p := s.Projects["demo"]
	proc := p.Processes["demo"]
	proc.MaxAttempts, proc.Tasks.MaxTasks = 6, 2
	proc.Steps[0].Routes = map[string]runner.Route{"rejected": {Next: "$fail"}}
	p.Processes["demo"] = proc
	s.Projects["demo"] = p
	var mu sync.Mutex
	calls := map[string]int{}
	var failure string
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return taskResourcesExecutor{graphExecutor{e, func(a runner.Assignment) runner.Observation {
			mu.Lock()
			defer mu.Unlock()
			id := a.TaskContext.TaskID
			calls[id]++
			if id == "original-check" {
				return graphFinal("original report: runtime blocked the check")
			}
			if id == "recheck" {
				return graphFinal("current report: checks pass with the corrected runtime")
			}
			switch calls[id] {
			case 1:
				return graphAction("delegate", graphTask("original-check", "writer"))
			case 2:
				return graphReturned(runner.StepResult{Outcome: "rejected", Outputs: map[string]runner.Value{"result": runner.Text("retained failed assessment")}, Reason: "verification could not complete"})
			case 3:
				if a.TaskContext.Directive != "The verifier runtime is corrected; recheck and assess this same goal" || len(a.TaskContext.Results) != 1 || a.TaskContext.RemainingAttempts != 2 {
					failure = "failed goal did not resume with the directive, retained report and original remaining allowance"
				}
				return graphAction("delegate", graphTask("recheck", "writer"))
			default:
				if len(a.TaskContext.Results) != 2 {
					failure = "successful prior work or the new verification report was lost"
				}
				return graphFinal("accepted current verification")
			}
		}}}
	})
	defer r.close()
	root := begin(t, r.core, "failed-steer-goal", "demo")
	before := await(t, r.core, root.ID, func(r runner.Run) bool { return r.Status == "Failed" && r.ResourcesReleased })
	oldChild, err := r.core.Get(before.TaskStatuses[0].RunID)
	if err != nil || runner.Summarize(before, time.Now()).Attempts != 3 || before.Process.MaxAttempts != 6 {
		t.Fatal("fixture did not retain three consumed attempts", err)
	}
	req := runner.TaskInterventionRequest{RequestID: "owner-correction", RunID: root.ID, Revision: before.Revision, Action: "steer", Text: "The verifier runtime is corrected; recheck and assess this same goal"}
	queued, err := r.core.InterveneTask(req)
	if err != nil {
		t.Fatal(err)
	}
	if queued.ID != before.ID || queued.ResourcesReleased || queued.Process.MaxAttempts != 6 || queued.NextProfile != before.NextProfile || runner.Summarize(queued, time.Now()).Attempts != 3 {
		t.Fatal("atomic steering extended allowance, changed profile or consumed execution")
	}
	done := await(t, r.core, root.ID, func(r runner.Run) bool { return r.Status == "Completed" && r.ResourcesReleased })
	afterChild, _ := r.core.Get(oldChild.ID)
	mu.Lock()
	defer mu.Unlock()
	if failure != "" || runner.Summarize(done, time.Now()).Attempts != 6 || done.Process.MaxAttempts != 6 || len(done.TaskGraph.Children) != 2 || calls["original-check"] != 1 || calls["recheck"] != 1 || !reflect.DeepEqual(oldChild, afterChild) {
		t.Fatalf("steering replayed completed work or escaped the frozen goal: %s calls=%+v status=%s", failure, calls, done.Status)
	}
	if !reflect.DeepEqual(done.Attempts[:len(before.Attempts)], before.Attempts) {
		t.Fatal("steering rewrote the previous failed assessment or its attempts")
	}
	replay, err := r.core.InterveneTask(req)
	if err != nil || replay.ID != root.ID || runner.Summarize(replay, time.Now()).Attempts != 6 {
		t.Fatal("intervention receipt replay created another visit or changed completed work", err)
	}
}
