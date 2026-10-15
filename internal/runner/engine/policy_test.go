package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
)

func applyPolicy(t *testing.T, e *Engine, change func(*config.Config)) {
	t.Helper()
	old, _, err := e.Store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(old)
	var next config.Settings
	if err = json.Unmarshal(b, &next); err != nil {
		t.Fatal(err)
	}
	change(&next.Config)
	if err = e.Store.ApplySettings(t.Context(), old.Config.Revision(), next); err != nil {
		t.Fatal(err)
	}
}
func waiting(t *testing.T, e *Engine, id, contains string) {
	t.Helper()
	v, err := e.Show(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Eligibility.WaitingFor, contains) {
		t.Fatalf("want wait %q, got %+v", contains, v.Eligibility)
	}
	if _, ok, err := e.claim(t.Context()); err != nil || ok {
		t.Fatal("ineligible action admitted", err)
	}
}
func waitTask(t *testing.T, ctx context.Context, e *Engine, id string, predicate func(core.Task) bool) core.Task {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		task := current(t, e, id)
		if predicate(task) {
			return task
		}
		select {
		case <-ctx.Done():
			t.Fatal("task did not reach expected state", task)
		case <-ticker.C:
		}
	}
}

func TestCapacityEditsPinActiveProfilesAndKeepReconciliationResponsive(t *testing.T) {
	e := fixture(t)
	one, two := approved(t, e), approved(t, e)
	type call struct {
		request execution.Request
		ctx     context.Context
		release chan struct{}
	}
	started := make(chan call, 4)
	e.Executor = adapter(func(ctx context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
		release := make(chan struct{})
		started <- call{r, ctx, release}
		select {
		case <-ctx.Done():
			return execution.Completion{Stopped: true, Error: ctx.Err().Error()}
		case <-release:
		}
		if r.Attempt.Role == "implementer" {
			if err := os.WriteFile(filepath.Join(r.Dir, "greeting.txt"), []byte("hello\n"), 0600); err != nil {
				return execution.Completion{Stopped: true, Error: err.Error()}
			}
		}
		return pass(r)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	receive := func() call {
		t.Helper()
		select {
		case c := <-started:
			return c
		case <-ctx.Done():
			t.Fatal("invocation never started")
			return call{}
		}
	}
	first := receive()
	if first.request.Task.ID != one.ID {
		t.Fatal("unexpected first task")
	}
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxParallelism = 2 })
	second := receive()
	if second.request.Task.ID != two.ID {
		t.Fatal("second independent task did not start")
	}
	applyPolicy(t, e, func(c *config.Config) {
		c.Policy.MaxParallelism = 1
		p := c.Profiles["review"]
		p.Model = "new-review-model"
		p.Reasoning = "medium"
		c.Profiles["review"] = p
	})
	settings, _, err := e.Store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := e.PreviewPolicy(ctx, settings.Config)
	if err != nil || len(preview.Active) != 2 {
		t.Fatal("active policy preview missing", err)
	}
	for _, a := range preview.Active {
		if a.Profile.Model != "test-model" {
			t.Fatal("active profile changed")
		}
	}
	close(first.release)
	waitTask(t, ctx, e, one.ID, func(t core.Task) bool { return t.NextRole == "audit" })
	v, err := e.Show(ctx, one.ID)
	if err != nil || !strings.Contains(v.Eligibility.WaitingFor, "global capacity") {
		t.Fatalf("lower limit ignored: %+v %v", v, err)
	}
	if second.ctx.Err() != nil {
		t.Fatal("lowering capacity cancelled admitted work")
	}
	if err = e.Stop(ctx, two.ID); err != nil {
		t.Fatal(err)
	}
	audit := receive()
	if audit.request.Task.ID != one.ID || audit.request.Attempt.Role != "audit" || audit.request.Attempt.Profile.Model != "new-review-model" {
		t.Fatal("next admission did not use new policy", audit.request.Attempt)
	}
	if err = e.Store.SetDrained(ctx, true); err != nil {
		t.Fatal(err)
	}
	close(audit.release)
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("drain did not settle")
	}
	if current(t, e, one.ID).Stage != core.AwaitingDelivery || current(t, e, two.ID).Stage != core.Stopped {
		t.Fatal("concurrent outcomes lost")
	}
}

func TestRepairRetryAndInvocationAllowancesSurvivePolicyEditsAndRestart(t *testing.T) {
	e := fixture(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	e.clock = func() time.Time { return now }
	applyPolicy(t, e, func(c *config.Config) {
		c.Policy.MaxRepairs = 2
		c.Policy.MaxTransientRetries = 2
		p := c.Profiles["build"]
		p.Model = "careful-model"
		c.Profiles["careful"] = p
		c.Policy.ImplementationLadder = []string{"careful"}
		ceiling := 6
		c.Policy.MaxInvocationsPerTask = &ceiling
	})
	task := approved(t, e)
	e.Executor = adapter(func(ctx context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
		if r.Attempt.Role == "implementer" {
			greeting := []string{"bonjour\n", "HELLO\n", "hello\n"}[r.Attempt.Repair]
			if err := os.WriteFile(filepath.Join(r.Dir, "greeting.txt"), []byte(greeting), 0600); err != nil {
				return execution.Completion{Stopped: true, Error: err.Error()}
			}
			return pass(r)
		}
		greeting, err := os.ReadFile(filepath.Join(r.ReadRoots[0], "greeting.txt"))
		if err != nil {
			return execution.Completion{Stopped: true, Error: err.Error()}
		}
		result := pass(r)
		if string(greeting) != "hello\n" {
			result.Result.Checks[0] = core.Check{ID: "change", Status: "defect", Evidence: "Approved greeting is hello; candidate contains " + string(greeting)}
		}
		return result
	})

	complete(t, e, claim(t, e))
	finish(t, e)
	reject := func() {
		t.Helper()
		a := claim(t, e)
		if a.Role != "audit" || a.Retry != 0 {
			t.Fatal("successful stage did not end retry episode", a)
		}
		f := e.execute(t.Context(), a)
		if f.completion.Result == nil || core.ReviewOutcome(f.completion.Result.Checks) != core.ConfirmedDefect {
			t.Fatal("incorrect candidate did not reach independent rejection", f.completion)
		}
		if err := e.saveCompletion(t.Context(), f); err != nil {
			t.Fatal(err)
		}
		finish(t, e)
	}
	reject()
	repair := claim(t, e)
	if repair.ProfileName != "careful" || repair.Repair != 1 {
		t.Fatal("repair did not select successor", repair)
	}
	if err := e.saveCompletion(t.Context(), finished{repair, execution.Completion{Stopped: true, Kind: core.Transient, Error: "HTTP 503"}}); err != nil {
		t.Fatal(err)
	}
	reopen(t, e)
	task = current(t, e, task.ID)
	if task.Usage.Invocations != 3 || task.Usage.Repairs != 1 || task.Pending == nil || !task.Pending.NotBefore.Equal(now.Add(30*time.Second)) {
		t.Fatal("durable recovery lost", task)
	}
	waiting(t, e, task.ID, "retry backoff")
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxRepairs = 0 })
	waiting(t, e, task.ID, "repair limit below consumed")
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxRepairs = 2 })
	applyPolicy(t, e, func(c *config.Config) {
		c.Policy.MaxTransientRetries = 0
		p := c.Profiles["careful"]
		p.Model = "edited-model"
		c.Profiles["careful"] = p
	})
	now = now.Add(time.Minute)
	waiting(t, e, task.ID, "transient retry limit")
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxTransientRetries = 1; n := 3; c.Policy.MaxInvocationsPerTask = &n })
	waiting(t, e, task.ID, "invocation ceiling")
	applyPolicy(t, e, func(c *config.Config) { n := 6; c.Policy.MaxInvocationsPerTask = &n })
	retried := claim(t, e)
	if retried.Profile.Model != repair.Profile.Model || retried.Repair != 1 || retried.Retry != 1 || retried.Invocation != 4 {
		t.Fatal("retry changed profile or consumed another repair", retried)
	}
	complete(t, e, retried)
	finish(t, e)
	reject()
	second := claim(t, e)
	if second.Repair != 2 || second.Profile.Model != "edited-model" || second.Invocation != 6 {
		t.Fatal("final ladder step or edited next profile lost", second)
	}
	complete(t, e, second)
	finish(t, e)
	waiting(t, e, task.ID, "invocation ceiling")
	applyPolicy(t, e, func(c *config.Config) { n := 7; c.Policy.MaxInvocationsPerTask = &n })
	complete(t, e, claim(t, e))
	finish(t, e)
	task = current(t, e, task.ID)
	if task.Stage != core.AwaitingDelivery || task.Usage.Invocations != 7 || task.Usage.Repairs != 2 || task.Usage.Escalations != 2 {
		t.Fatal("unexpected final allowances", task)
	}
}

func TestInterruptedRepairDoesNotResetAllowanceAndReplacementNeedsApproval(t *testing.T) {
	e := fixture(t)
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxRepairs = 1 })
	task := approved(t, e)
	complete(t, e, claim(t, e))
	finish(t, e)
	a := claim(t, e)
	f := e.execute(t.Context(), a)
	f.completion.Result.Checks[0].Status = "defect"
	if err := e.saveCompletion(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	finish(t, e)
	reserved := claim(t, e)
	reopen(t, e)
	if err := e.recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxRepairs = 0 })
	if _, err := e.Resume(t.Context(), task.ID, reserved.ID, true); err != nil {
		t.Fatal(err)
	}
	repeated := claim(t, e)
	if repeated.Repair != 1 || repeated.Invocation != 4 || repeated.ResumedFrom != reserved.ID {
		t.Fatal("resume granted a fresh repair", repeated)
	}
	if err := e.Stop(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	stopped := current(t, e, task.ID)
	if stopped.Usage.Repairs != 1 || stopped.Usage.Invocations != 4 {
		t.Fatal("stop erased consumption")
	}
	if _, err := e.Replace(t.Context(), task.ID, 1, brief+"Clarification.\n"); err != nil {
		t.Fatal(err)
	}
	replaced := current(t, e, task.ID)
	if replaced.Stage != core.Draft || replaced.Usage != (core.Usage{}) || replaced.Approval != nil {
		t.Fatal("replacement reused old authority")
	}
	history, err := e.Store.Attempts(t.Context(), task.ID)
	if err != nil || len(history) != 4 {
		t.Fatal("replacement erased prior attempts", err)
	}
}

func TestTransientEpisodeBackoffAndUnknownFailureDoNotEscalate(t *testing.T) {
	e := fixture(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	e.clock = func() time.Time { return now }
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxTransientRetries = 3; c.Policy.MaxRepairs = 2 })
	task := approved(t, e)
	for i, delay := range []time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute} {
		a := claim(t, e)
		if a.Retry != i || a.Repair != 0 || a.ProfileName != "build" {
			t.Fatal("transport failure escalated", a)
		}
		if err := e.saveCompletion(t.Context(), finished{a, execution.Completion{Stopped: true, Kind: core.Transient, Error: "changing upstream message"}}); err != nil {
			t.Fatal(err)
		}
		pending := current(t, e, task.ID).Pending
		if pending == nil || !pending.NotBefore.Equal(now.Add(delay)) {
			t.Fatal("retry deadline changed")
		}
		now = now.Add(delay)
		reopen(t, e)
	}
	a := claim(t, e)
	if err := e.saveCompletion(t.Context(), finished{a, execution.Completion{Stopped: true, Error: "unrecognized failure"}}); err != nil {
		t.Fatal(err)
	}
	task = current(t, e, task.ID)
	if task.Attention == nil || task.Attention.Kind != core.Interrupted || task.Pending != nil || task.Usage.Repairs != 0 {
		t.Fatal("unknown failure retried or escalated", task)
	}
}

func TestStatusRetainsTransientConsumption(t *testing.T) {
	c := config.Config{Policy: config.Policy{MaxTransientRetries: 3, MaxParallelism: 1}}
	for _, state := range []string{"manual resume", "interrupted", "stopped"} {
		t.Run(state, func(t *testing.T) {
			task := core.Task{ID: "task", Versions: []core.Version{{Number: 1}}, Approval: &core.Approval{}, NextRole: "implementer"}
			attempt := core.Attempt{ID: "previous", TaskID: "task", Version: 1, Retry: 3}
			switch state {
			case "manual resume":
				task.Pending = &core.Continuation{Kind: "resume", Retry: 3}
			case "interrupted":
				task.Attention = &core.Attention{Kind: core.Interrupted, Attempt: attempt.ID}
			case "stopped":
				task.Stage = core.Stopped
				task.Approval = nil
			}
			view := eligible(c, task, []core.Attempt{attempt}, false, time.Now())
			if view.RetriesRemaining != 0 {
				t.Fatal("status advertised a fresh retry", view)
			}
		})
	}
}
