package runner_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type primaryCompletionExecutor struct {
	runner.Executor
	mu       sync.Mutex
	mode     string
	ended    time.Time
	starts   int
	complete chan struct{}
}

func (e *primaryCompletionExecutor) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	e.mu.Lock()
	e.starts++
	e.ended = time.Now().UTC()
	if e.mode == "late" {
		e.ended = a.Deadline.Add(time.Nanosecond)
	} else if e.mode == "legacy" {
		e.ended = time.Time{}
	}
	e.mu.Unlock()
	o, err := e.Executor.Start(ctx, a)
	o.NextCheck = a.Deadline.Add(time.Second)
	return o, err
}

func (e *primaryCompletionExecutor) Stop(context.Context, string) error { return nil }

func (e *primaryCompletionExecutor) Observe(ctx context.Context, id string) (runner.Observation, error) {
	e.mu.Lock()
	ended := e.ended
	e.mu.Unlock()
	select {
	case <-e.complete:
		o, err := e.Executor.Observe(ctx, id)
		o.ExecutionEndedAt = ended
		return o, err
	default:
		return runner.Observation{
			State:            runner.Unknown,
			Reconciling:      true,
			ExecutionEndedAt: ended,
			NextCheck:        time.Now().Add(20 * time.Millisecond),
			Reason:           "primary work ended; output recovery pending",
		}, nil
	}
}

func TestPrimaryCompletionPreservesDeadlineDuringCleanupAndRestart(t *testing.T) {
	for _, mode := range []string{"on time", "late", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			s := snapshot(policy())
			project := s.Projects["demo"]
			process := project.Processes["demo"]
			process.AttemptTimeout = "200ms"
			project.Processes["demo"] = process
			s.Projects["demo"] = project
			executor := &primaryCompletionExecutor{mode: mode, complete: make(chan struct{})}
			wrap := func(e runner.Executor) runner.Executor { executor.Executor = e; return executor }
			dir := t.TempDir()
			r := openRig(t, dir, s, wrap)
			root := begin(t, r.core, "primary-completion", "demo")
			pending := await(t, r.core, root.ID, func(r runner.Run) bool {
				return r.Status == "Waiting" && r.Current().State == runner.Unknown && r.Current().StopRequested
			})
			if pending.Current().TimedOut == (mode == "on time") {
				r.close()
				t.Fatal("primary completion erased a real timeout or converted cleanup to execution time", pending.Current())
			}
			progress := runner.Summarize(pending, time.Now()).Progress
			if mode == "on time" && (progress.Reconciling != 1 || progress.Unknown != 0 || len(progress.Attention) != 0) {
				r.close()
				t.Fatal("automatic output recovery asked for owner intervention", progress)
			}
			r.close()
			reopened := openRig(t, dir, s, wrap)
			defer reopened.close()
			close(executor.complete)
			reopened.core.Wake()
			done := await(t, reopened.core, root.ID, released)
			want := "Failed"
			if mode == "on time" {
				want = "Completed"
			}
			executor.mu.Lock()
			starts := executor.starts
			executor.mu.Unlock()
			if done.Status != want || len(done.Attempts) != 1 || starts != 1 || !done.Attempts[0].ExecutionEndedAt.Equal(pending.Current().ExecutionEndedAt) {
				t.Fatalf("recovery changed execution identity, result or deadline: want=%s got=%s starts=%d", want, done.Status, starts)
			}
		})
	}
}
