package runner_test

import (
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestGeneralTasksUnlimitedWorkRetainsLocalRetryLimit(t *testing.T) {
	s := graphSnapshot(t)
	s.Instance.EnforceWorkLimits = false
	project := s.Projects["demo"]
	process := project.Processes["demo"]
	process.MaxAttempts, process.Tasks.MaxTasks = 1, 1
	process.MaxAttemptsPerStep = 2
	project.Processes["demo"] = process
	s.Projects["demo"] = project
	var mu sync.Mutex
	starts := map[string]int{}
	var contextFailure bool
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			mu.Lock()
			defer mu.Unlock()
			ctx := a.TaskContext
			starts[ctx.TaskID]++
			if ctx.AttemptBudget == nil || !ctx.AttemptBudget.Unlimited || ctx.AttemptBudget.Remaining != -1 || ctx.RemainingAttempts != -1 || !ctx.UnlimitedTasks || ctx.RemainingTasks != -1 {
				contextFailure = true
			}
			if ctx.TaskID == "root" {
				if starts["root"] == 1 {
					return graphAction("delegate", graphTask("first", "writer"), graphTask("second", "writer"), graphTask("failing", "writer"))
				}
				return graphFinal("Both successful tasks retained; failing task exhausted only its local retries")
			}
			if ctx.TaskID == "failing" {
				return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "failed", Reason: "reproducible local failure"}}
			}
			return graphFinal(ctx.TaskID)
		}}
	})
	defer r.close()
	root := begin(t, r.core, "unlimited-work", "demo")
	done := await(t, r.core, root.ID, released)
	mu.Lock()
	defer mu.Unlock()
	budget := runner.Summarize(done, time.Now()).AttemptBudget
	if contextFailure || done.Status != "Completed" || len(done.TaskStatuses) != 3 || starts["root"] != 2 || starts["first"] != 1 || starts["second"] != 1 || starts["failing"] != 2 || budget == nil || !budget.Unlimited || budget.Spent != 6 || budget.Remaining != -1 {
		t.Fatalf("unlimited work or finite local retries differ: context=%v status=%s reason=%s starts=%v budget=%+v", contextFailure, done.Status, done.Reason, starts, budget)
	}
	if done.Process.MaxAttempts != 1 || done.Process.Tasks.MaxTasks != 1 || done.Process.MaxAttemptsPerStep != 2 {
		t.Fatal("instance policy rewrote the frozen workflow", done.Process)
	}
}
