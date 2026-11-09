package runner_test

import (
	"context"
	"sync"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

type infrastructureFixture struct {
	runner.Executor
	mu        sync.Mutex
	failures  int
	retryable bool
}

func (e *infrastructureFixture) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	e.mu.Lock()
	fail := e.failures > 0
	if fail {
		e.failures--
	}
	e.mu.Unlock()
	if fail {
		return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{
			Kind:      "failed",
			Reason:    "pre-dispatch connection unavailable",
			Retryable: e.retryable,
		}}, nil
	}
	return e.Executor.Start(ctx, a)
}

func TestSafeInfrastructureRetryStaysWithinOriginalPolicy(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		policy, failures, total int
		retryable               bool
		want                    string
		attempts                int
	}{
		{"disabled", 0, 1, 3, true, "Failed", 1},
		{"safe reconnect", 1, 1, 3, true, "Completed", 2},
		{"infrastructure bound", 1, 2, 3, true, "Failed", 2},
		{"original total bound", 1, 1, 1, true, "Failed", 1},
		{"domain failure is separate", 1, 1, 3, false, "Failed", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := snapshot(policy())
			p := s.Projects["demo"]
			proc := p.Processes["demo"]
			proc.InfrastructureRetries, proc.MaxAttempts, proc.MaxAttemptsPerStep = tc.policy, tc.total, 1
			p.Processes["demo"] = proc
			s.Projects["demo"] = p
			r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
				return &infrastructureFixture{Executor: e, failures: tc.failures, retryable: tc.retryable}
			})
			defer r.close()
			run := begin(t, r.core, "bounded-infrastructure", "demo")
			end := await(t, r.core, run.ID, released)
			if end.Status != tc.want || len(end.Attempts) != tc.attempts || end.Process.MaxAttempts != tc.total || end.VisitExtraAttempts != 0 {
				t.Fatalf("retry changed a policy bound: %+v", end)
			}
		})
	}
}

type cleanupFixture struct {
	runner.Executor
	mu      sync.Mutex
	blocked bool
	calls   int
}

func (e *cleanupFixture) ReleaseRun(context.Context, runner.Run) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.blocked {
		return &runner.CleanupBlockedError{Reason: "workspace differs from retained candidate; preserve it"}
	}
	return nil
}
func (e *cleanupFixture) ForgetRun(context.Context, string) error { return nil }

func TestParkedCleanupSurvivesRestartAndOwnerRetrySpendsNoExecution(t *testing.T) {
	s := snapshot(policy())
	dir := t.TempDir()
	fixture := &cleanupFixture{blocked: true}
	wrap := func(e runner.Executor) runner.Executor { fixture.Executor = e; return fixture }
	r := openRig(t, dir, s, wrap)
	run := begin(t, r.core, "park-integrity", "demo")
	parked := await(t, r.core, run.ID, func(r runner.Run) bool { return r.Terminal() && r.CleanupBlocked })
	r.close()
	r = openRig(t, dir, s, wrap)
	defer r.close()
	saved, err := r.core.Get(run.ID)
	if err != nil || !saved.CleanupBlocked || saved.Revision != parked.Revision {
		t.Fatal("parked cleanup lost its durable condition", err)
	}
	fixture.mu.Lock()
	calls := fixture.calls
	fixture.blocked = false
	fixture.mu.Unlock()
	if calls != 1 {
		t.Fatal("unchanged parked resources were retried")
	}
	if _, err = r.core.Retry(runner.RetryRequest{RequestID: "invalid-cleanup-grant", RunID: run.ID, CleanupOnly: true, ExtraAttempts: 1}); err == nil {
		t.Fatal("cleanup-only action granted execution allowance")
	}
	req := runner.RetryRequest{RequestID: "revalidate-cleanup", RunID: run.ID, CleanupOnly: true}
	if _, err = r.core.Retry(req); err != nil {
		t.Fatal(err)
	}
	clean := await(t, r.core, run.ID, func(r runner.Run) bool { return r.ResourcesReleased })
	if clean.Status != parked.Status || len(clean.Attempts) != len(parked.Attempts) || clean.Process.MaxAttempts != parked.Process.MaxAttempts || clean.CleanupBlocked {
		t.Fatal("cleanup retry changed settled execution", clean)
	}
	if _, err = r.core.Retry(req); err != nil {
		t.Fatal("cleanup request replay was not idempotent", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.calls != 2 {
		t.Fatal("cleanup retry repeated or did not revalidate", fixture.calls)
	}
}
