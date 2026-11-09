package runner_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Only domain judgments are scripted. SQLite, routing, binding, admission,
// deterministic execution, restart, decisions and cleanup are real.
type workflowExecutor struct {
	runner.Executor
	mu            sync.Mutex
	counts        map[string]int
	outcomes      map[string]string
	alwaysRepeat  bool
	ownerFeedback []map[string]runner.Value
}

func (e *workflowExecutor) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	e.mu.Lock()
	stage := a.Task
	e.counts[stage]++
	n := e.counts[stage]
	outcome := "ready"
	if stage == "measure" && (n == 1 || e.alwaysRepeat) {
		outcome = "continue"
	}
	if stage == "verify" && n == 1 {
		outcome = "changes_requested"
	}
	if stage == "review" && n == 1 {
		outcome = "changes_requested"
	}
	e.outcomes[a.AttemptID] = outcome
	if stage == "owner" {
		e.ownerFeedback = append(e.ownerFeedback, runner.Clone(a.Inputs))
	}
	a.Setup.Profile.Text = fmt.Sprintf("%s-%d", stage, n)
	e.mu.Unlock()
	return e.Executor.Start(ctx, a)
}
func (e *workflowExecutor) Observe(ctx context.Context, id string) (runner.Observation, error) {
	o, err := e.Executor.Observe(ctx, id)
	if err == nil && o.End != nil && o.End.Result != nil {
		e.mu.Lock()
		outcome := e.outcomes[id]
		e.mu.Unlock()
		o.End = runner.Clone(o.End)
		o.End.Result.Outcome = outcome
	}
	return o, err
}
func workflowSnapshot() runner.Snapshot {
	p := policy()
	p.Database.MaxRecord = 64 << 10
	s := snapshot(p)
	s.Instance.Concurrency = 1
	project := s.Projects["demo"]
	project.Roles = map[string]runner.Role{}
	steps := []runner.Step{}
	empty := runner.Text("")
	for _, name := range []string{"prepare", "owner", "measure", "verify", "review", "integrate", "deliver", "smoke"} {
		role := runner.Role{ID: name, Purpose: name, Inputs: map[string]string{}, Outputs: map[string]string{"result": "text"}, Execution: &runner.Profile{Harness: "deterministic", Mode: "echo", Output: "result"}}
		step := runner.Step{ID: name, Role: name, Inputs: map[string]runner.Binding{}}
		if name == "owner" {
			for _, feedback := range []string{"measure", "verify", "review"} {
				role.Inputs[feedback] = "text"
				step.Inputs[feedback] = runner.Binding{FromStep: feedback, Output: "result", Default: &empty}
			}
		} else if len(steps) > 0 {
			role.Inputs["previous"] = "text"
			step.Inputs["previous"] = runner.Binding{FromStep: steps[len(steps)-1].ID, Output: "result"}
		}
		if name == "measure" || name == "verify" || name == "review" {
			outcome := "changes_requested"
			if name == "measure" {
				outcome = "continue"
			}
			step.Routes = map[string]runner.Route{outcome: {Next: "owner", MaxTimes: 2}, "ready": {Next: map[string]string{"measure": "verify", "verify": "review", "review": "integrate"}[name]}}
		}
		if name == "integrate" {
			step.Routes = map[string]runner.Route{"ready": {Next: "deliver", Approval: true}}
		}
		project.Roles[name] = role
		steps = append(steps, step)
	}
	proc := runner.Process{ID: "demo", Inputs: map[string]string{"text": "text"}, Steps: steps, Outputs: map[string]runner.Binding{"result": {FromStep: "smoke", Output: "result"}}, MaxAttempts: 32, MaxAttemptsPerStep: 1, AttemptTimeout: "3s"}
	project.Processes["demo"] = proc
	s.Projects["demo"] = project
	return s
}
func TestWorkflowCorrectionsMeasurementsAndApprovalResume(t *testing.T) {
	s := workflowSnapshot()
	dir := t.TempDir()
	e := &workflowExecutor{counts: map[string]int{}, outcomes: map[string]string{}}
	r := openRig(t, dir, s, func(backend runner.Executor) runner.Executor { e.Executor = backend; return e })
	started := begin(t, r.core, "full-workflow", "demo")
	// The complete correction journey commits many real SQLite transitions
	// before the approval gate. Give its test observer time under race CI load.
	waiting := awaitWithin(t, r.core, started.ID, 12*time.Second, func(r runner.Run) bool { return r.Decision != nil })
	if waiting.Status != "Waiting" || waiting.Active() || waiting.RouteUses["measure/continue"] != 1 || waiting.RouteUses["verify/changes_requested"] != 1 || waiting.RouteUses["review/changes_requested"] != 1 {
		t.Fatalf("lost routing/allowances: %+v", waiting)
	}
	e.mu.Lock()
	if e.counts["prepare"] != 1 || e.counts["owner"] != 4 || e.counts["measure"] != 4 || e.counts["verify"] != 3 || e.counts["review"] != 2 || e.counts["deliver"] != 0 {
		t.Fatal(e.counts)
	}
	if string(e.ownerFeedback[1]["measure"].Inline) != `"measure-1"` || string(e.ownerFeedback[2]["verify"].Inline) != `"verify-1"` || string(e.ownerFeedback[3]["review"].Inline) != `"review-1"` {
		t.Fatal("correction lost feedback", e.ownerFeedback)
	}
	e.mu.Unlock()
	if _, err := r.core.Retry(runner.RetryRequest{RequestID: "bypass", RunID: waiting.ID, ExtraAttempts: 1}); err == nil {
		t.Fatal("retry bypassed approval")
	}
	r.close()
	r = openRig(t, dir, s, func(backend runner.Executor) runner.Executor { e.Executor = backend; return e })
	defer r.close()
	if _, err := r.core.Decide(runner.DecisionRequest{RequestID: "stale", RunID: waiting.ID, DecisionID: "wrong", Choice: "approve"}); err == nil {
		t.Fatal("stale approval accepted")
	}
	req := runner.DecisionRequest{RequestID: "approve", RunID: waiting.ID, DecisionID: waiting.Decision.ID, Choice: "approve"}
	if _, err := r.core.Decide(req); err != nil {
		t.Fatal(err)
	}
	done := await(t, r.core, waiting.ID, released)
	if done.Status != "Completed" || string(done.Outputs["result"].Inline) != `"smoke-1"` {
		t.Fatal(done.Status, done.Reason, done.Outputs)
	}
	if _, err := r.core.Decide(req); err != nil {
		t.Fatal("decision replay failed", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.counts["deliver"] != 1 || e.counts["smoke"] != 1 {
		t.Fatal("delivery repeated", e.counts)
	}
	// Only current candidate evidence remains eligible for bindings.
	for _, a := range done.Attempts {
		if a.Step >= 1 && a.Step <= 4 && a.End.Result.Outputs["result"].Inline != nil {
			want := map[int]string{1: "owner-4", 2: "measure-4", 3: "verify-3", 4: "review-2"}[a.Step]
			if !a.Superseded && string(a.End.Result.Outputs["result"].Inline) != fmt.Sprintf("%q", want) {
				t.Fatal("stale evidence remains current", a)
			}
		}
	}
}
func TestWorkflowRepetitionExhaustion(t *testing.T) {
	s := workflowSnapshot()
	e := &workflowExecutor{counts: map[string]int{}, outcomes: map[string]string{}, alwaysRepeat: true}
	r := openRig(t, t.TempDir(), s, func(backend runner.Executor) runner.Executor { e.Executor = backend; return e })
	defer r.close()
	done := await(t, r.core, begin(t, r.core, "exhaustion", "demo").ID, released)
	if done.Status != "Failed" || !strings.Contains(done.Reason, "repetition allowance exhausted") || done.RouteUses["measure/continue"] != 2 {
		t.Fatal(done.Status, done.Reason, done.RouteUses)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.counts["verify"] != 0 || e.counts["deliver"] != 0 {
		t.Fatal("exhaustion escaped", e.counts)
	}
}

func TestWorkflowRejectedDecisionStopsDelivery(t *testing.T) {
	s := workflowSnapshot()
	e := &workflowExecutor{counts: map[string]int{}, outcomes: map[string]string{}}
	r := openRig(t, t.TempDir(), s, func(backend runner.Executor) runner.Executor { e.Executor = backend; return e })
	defer r.close()
	waiting := await(t, r.core, begin(t, r.core, "reject-decision", "demo").ID, func(r runner.Run) bool { return r.Decision != nil })
	req := runner.DecisionRequest{RequestID: "reject", RunID: waiting.ID, DecisionID: waiting.Decision.ID, Choice: "reject"}
	if _, err := r.core.Decide(req); err != nil {
		t.Fatal(err)
	}
	done := await(t, r.core, waiting.ID, released)
	if done.Status != "Failed" || !strings.Contains(done.Reason, "decision rejected") {
		t.Fatal(done.Status, done.Reason)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.counts["deliver"] != 0 || e.counts["smoke"] != 0 {
		t.Fatal("rejected delivery executed", e.counts)
	}
}
