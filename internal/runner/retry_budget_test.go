package runner_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestWithinBudgetRetryPreservesCapAndCompletionReserve(t *testing.T) {
	for _, total := range []int{2, 3} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			s := snapshot(policy())
			p := s.Projects["demo"]
			proc := p.Processes["demo"]
			proc.MaxAttempts, proc.MaxAttemptsPerStep = total, 1
			proc.Steps = append(proc.Steps, runner.Step{
				ID: "finish", Role: "finisher",
				Inputs: map[string]runner.Binding{"text": {FromStep: "work", Output: "result"}},
			})
			p.Processes["demo"] = proc
			role := p.Roles["worker"]
			good, bad := *role.Execution, *role.Execution
			bad.Mode = "fail"
			role.Execution, role.DefaultProfile = nil, "bad"
			role.Profiles = map[string]runner.Profile{"bad": bad, "good": good}
			p.Roles["worker"] = role
			p.Roles["finisher"] = runner.Role{
				ID:        "finisher",
				Inputs:    role.Inputs,
				Outputs:   role.Outputs,
				Execution: &good,
			}
			s.Projects["demo"] = p
			dir := t.TempDir()
			r := openRig(t, dir, s, nil)
			defer func() { r.close() }()
			run := begin(t, r.core, "initial-within-budget", "demo")
			before := await(t, r.core, run.ID, released)
			for _, invalid := range []runner.RetryRequest{
				{RequestID: "empty-within-budget", RunID: run.ID, WithinBudget: true},
				{RequestID: "cleanup-within-budget", RunID: run.ID, WithinBudget: true, ExtraAttempts: 1, CleanupOnly: true},
			} {
				if _, err := r.core.Retry(invalid); err == nil {
					t.Fatal("within-budget mode accepted an empty or cleanup grant")
				}
			}
			req := runner.RetryRequest{
				RequestID:     "one-current-visit",
				RunID:         run.ID,
				Profile:       "good",
				ExtraAttempts: 1,
				WithinBudget:  true,
			}
			_, err := r.core.Retry(req)
			if total == 2 {
				if err == nil {
					t.Fatal("retry spent the attempt reserved for downstream completion")
				}
				after, _ := r.core.Get(run.ID)
				if !reflect.DeepEqual(after, before) {
					t.Fatal("rejected grant changed saved work")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			done := await(t, r.core, run.ID, released)
			if done.Status != "Completed" || len(done.Attempts) != 3 || !reflect.DeepEqual(done.Process, before.Process) || done.VisitExtraAttempts != 0 {
				t.Fatal("within-budget retry changed the frozen cap, skipped work or leaked the visit grant", done.Status)
			}
			r.close()
			r = openRig(t, dir, s, nil)
			replay, err := r.core.Retry(req)
			if err != nil || len(replay.Attempts) != 3 || replay.Process.MaxAttempts != total {
				t.Fatal("audited retry did not replay after restart", err)
			}
			req.WithinBudget = false
			if _, err := r.core.Retry(req); err == nil {
				t.Fatal("same request ID accepted changed total-budget authority")
			}
		})
	}
}
