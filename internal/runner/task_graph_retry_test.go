package runner_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestGeneralTasksRootRetryAbove128AndProfileGuards(t *testing.T) {
	s := graphSnapshot(t)
	p := s.Projects["demo"]
	proc := p.Processes["demo"]
	proc.MaxAttempts = 256
	proc.MaxAttemptsPerStep = 2
	proc.Steps[0].Routes = map[string]runner.Route{"blocked": {Next: "$fail"}}
	p.Processes["demo"] = proc
	role := p.Roles["planner"]
	good := *role.Execution
	writer := good
	writer.Access = "workspace-write"
	role.Profiles = map[string]runner.Profile{
		"good":   good,
		"writer": writer,
		"legacy": {Harness: "command", Access: "workspace-read"},
	}
	p.Roles["planner"] = role
	s.Projects["demo"] = p
	var mu sync.Mutex
	calls := 0
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return graphReturned(runner.StepResult{Outcome: "blocked", Reason: "first approach failed", Outputs: map[string]runner.Value{"result": runner.Text("blocked")}})
			}
			return graphFinal("accepted retry")
		}}
	})
	defer r.close()
	root := begin(t, r.core, "large-goal", "demo")
	before := await(t, r.core, root.ID, released)
	for _, profile := range []string{"writer", "legacy"} {
		if _, err := r.core.Retry(runner.RetryRequest{RequestID: profile, RunID: root.ID, Profile: profile, ExtraAttempts: 1}); err == nil {
			t.Fatalf("retry bypassed general task profile guard for %s", profile)
		}
	}
	req := runner.RetryRequest{RequestID: "root-grant", RunID: root.ID, Profile: "good", ExtraAttempts: 1}
	if _, err := r.core.Retry(req); err != nil {
		t.Fatal("configured general allowance above 128 rejected explicit retry", err)
	}
	done := await(t, r.core, root.ID, released)
	if done.Status != "Completed" || done.Process.MaxAttempts != 257 || len(done.Attempts) != 2 || !reflect.DeepEqual(done.Attempts[0], before.Attempts[0]) {
		t.Fatalf("root retry lost history or changed the wrong allowance: %+v", done)
	}
	replay, err := r.core.Retry(req)
	if err != nil || replay.Process.MaxAttempts != 257 || len(replay.Attempts) != 2 {
		t.Fatal("root retry grant replay changed allowance or execution count", err)
	}
}

func TestGeneralTasksChildRetryUsesExtendedRootBudget(t *testing.T) {
	s := graphSnapshot(t)
	p := s.Projects["demo"]
	proc := p.Processes["demo"]
	proc.MaxAttempts = 4
	p.Processes["demo"] = proc
	s.Projects["demo"] = p
	started, settle := make(chan runner.Assignment, 1), make(chan struct{})
	var mu sync.Mutex
	childCalls := 0
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return &retryGraphExecutor{
			Executor: e,
			start: func(a runner.Assignment) runner.Observation {
				if a.TaskContext.TaskID == "root" {
					if len(a.TaskContext.Results) == 0 {
						return graphAction("delegate", graphTask("child", "writer"))
					}
					return graphFinal("accepted result")
				}
				mu.Lock()
				childCalls++
				call := childCalls
				mu.Unlock()
				if call == 1 {
					started <- a
					return runner.Observation{State: runner.Running, NextCheck: a.Deadline}
				}
				return graphFinal("recovered child")
			},
			observe: func(string) runner.Observation {
				select {
				case <-settle:
					return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "failed", Reason: "first child approach failed"}}
				default:
					return runner.Observation{State: runner.Running}
				}
			},
		}
	})
	defer r.close()
	root := begin(t, r.core, "child-root-budget", "demo")
	var assignment runner.Assignment
	select {
	case assignment = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("child did not start")
	}
	await(t, r.core, assignment.RunID, func(r runner.Run) bool { return r.Current() != nil && r.Current().State == runner.Running })
	root, _ = r.core.Get(root.ID)
	if _, err := r.core.InterveneTask(runner.TaskInterventionRequest{RequestID: "pause-root", RunID: root.ID, Revision: root.Revision, Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	close(settle)
	r.core.ExecutionChanged(assignment.AttemptID)
	child := await(t, r.core, assignment.RunID, released)
	if _, err := r.core.Retry(runner.RetryRequest{RequestID: "private-grant", RunID: child.ID, ExtraAttempts: 1}); err == nil || !strings.Contains(err.Error(), root.ID) {
		t.Fatalf("child private allowance extension did not identify root ownership: %v", err)
	}
	grant := runner.RetryRequest{RequestID: "goal-grant", RunID: root.ID, ExtraAttempts: 2}
	if _, err := r.core.Retry(grant); err != nil {
		t.Fatal(err)
	}
	root, _ = r.core.Get(root.ID)
	if root.Process.MaxAttempts != 6 || runner.Summarize(root, time.Now()).Attempts != 2 {
		t.Fatal("root budget grant consumed execution or changed the wrong allowance")
	}
	if _, err := r.core.Retry(runner.RetryRequest{RequestID: "child-within", RunID: child.ID, ExtraAttempts: 2, WithinBudget: true}); err != nil {
		t.Fatal("child retry used its stale private cap instead of the extended root allowance", err)
	}
	root, _ = r.core.Get(root.ID)
	if _, err := r.core.InterveneTask(runner.TaskInterventionRequest{RequestID: "resume-root", RunID: root.ID, Revision: root.Revision, Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	done := await(t, r.core, root.ID, released)
	child, _ = r.core.Get(child.ID)
	if done.Status != "Completed" || done.Process.MaxAttempts != 6 || child.Process.MaxAttempts != 4 || len(child.Attempts) != 2 || runner.Summarize(done, time.Now()).Attempts != 4 {
		t.Fatalf("family retry changed history or charged control operations: root=%+v child=%+v", done, child)
	}
}

type retryGraphExecutor struct {
	runner.Executor
	start   func(runner.Assignment) runner.Observation
	observe func(string) runner.Observation
}

func (e *retryGraphExecutor) Start(_ context.Context, a runner.Assignment) (runner.Observation, error) {
	return e.start(a), nil
}

func (e *retryGraphExecutor) Observe(_ context.Context, id string) (runner.Observation, error) {
	return e.observe(id), nil
}

func TestGeneralTasksRetryRetainsCompletionReserve(t *testing.T) {
	for _, total := range []int{3, 4} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			s := graphSnapshot(t)
			p := s.Projects["demo"]
			proc := p.Processes["demo"]
			proc.MaxAttempts = total
			p.Processes["demo"] = proc
			s.Projects["demo"] = p
			var calls int
			r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
				return graphExecutor{e, func(a runner.Assignment) runner.Observation {
					calls++
					if calls == 1 {
						return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "failed", Reason: "retry reserve"}}
					}
					return graphFinal("complete")
				}}
			})
			defer r.close()
			root := begin(t, r.core, "reserve", "demo")
			await(t, r.core, root.ID, released)
			_, err := r.core.Retry(runner.RetryRequest{RequestID: "within-reserve", RunID: root.ID, WithinBudget: true, ExtraAttempts: 3})
			if total == 3 {
				if err == nil {
					t.Fatal("within-budget grant exceeded root remaining allowance")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if done := await(t, r.core, root.ID, released); done.Status != "Completed" || done.Process.MaxAttempts != total {
					t.Fatal("within-budget retry changed the frozen total allowance")
				}
			}
		})
	}
}

func TestGeneralTasksAutomaticRetryPreservesParentEvaluation(t *testing.T) {
	for _, retryable := range []bool{false, true} {
		for _, total := range []int{3, 4} {
			t.Run(fmt.Sprintf("infrastructure=%t/total=%d", retryable, total), func(t *testing.T) {
				s := graphSnapshot(t)
				project := s.Projects["demo"]
				proc := project.Processes["demo"]
				proc.MaxAttempts = total
				proc.MaxAttemptsPerStep = 2
				proc.InfrastructureRetries = 1
				project.Processes["demo"] = proc
				s.Projects["demo"] = project
				var mu sync.Mutex
				childCalls := 0
				var evaluated runner.RoleTaskResult
				r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
					return graphExecutor{e, func(a runner.Assignment) runner.Observation {
						mu.Lock()
						defer mu.Unlock()
						if a.TaskContext.TaskID == "root" {
							if len(a.TaskContext.Results) == 0 {
								return graphAction("delegate", graphTask("child", "writer"))
							}
							evaluated = a.TaskContext.Results[0]
							return graphFinal("evaluated actual child outcome")
						}
						childCalls++
						if childCalls == 1 {
							return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{
								Kind:      "failed",
								Reason:    "child execution failed",
								Retryable: retryable,
							}}
						}
						return graphFinal("recovered within available allowance")
					}}
				})
				defer r.close()
				root := begin(t, r.core, "automatic-reserve", "demo")
				done := await(t, r.core, root.ID, released)
				mu.Lock()
				defer mu.Unlock()
				wantChildStatus, wantChildCalls := "Failed", 1
				if total == 4 {
					wantChildStatus, wantChildCalls = "Completed", 2
				}
				if done.Status != "Completed" || evaluated.Status != wantChildStatus || childCalls != wantChildCalls || runner.Summarize(done, time.Now()).Attempts != total {
					t.Fatalf("automatic retry stranded parent or consumed its reserve: status=%s child=%s calls=%d total=%d", done.Status, evaluated.Status, childCalls, runner.Summarize(done, time.Now()).Attempts)
				}
				if total == 3 && !strings.Contains(evaluated.Reason, "completion attempts") {
					t.Fatalf("child failure lost the completion-reserve reason: %s", evaluated.Reason)
				}
			})
		}
	}
}
