package metrics

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type traceContextKey struct{}

// AttemptTrace carries the already-sanitized attempt identity into Runner-owned
// stages. Stage records contain enums, timestamps, and usage only; callers
// cannot attach prompts, command arguments, diagnostics, or arbitrary detail.
type AttemptTrace struct {
	observer func(Event) error
	base     Event

	mu     sync.Mutex
	errors []error
}

func NewAttemptTrace(observer func(Event) error, base Event) *AttemptTrace {
	return &AttemptTrace{observer: observer, base: base}
}

func WithAttemptTrace(ctx context.Context, trace *AttemptTrace) context.Context {
	if trace == nil || trace.observer == nil {
		return ctx
	}
	return context.WithValue(ctx, traceContextKey{}, trace)
}

type FinishStage func(outcome, failureClass, retryDisposition string, usage Usage)

func StartStage(ctx context.Context, name string) FinishStage {
	trace, _ := ctx.Value(traceContextKey{}).(*AttemptTrace)
	name = strings.TrimSpace(name)
	if trace == nil || trace.observer == nil || !validStageName(name) {
		return func(string, string, string, Usage) {}
	}
	stageID := NewStageID()
	startedAt := time.Now().UTC()
	started := trace.stageEvent(EventStageStarted, stageID, name)
	started.StartedAt = startedAt
	trace.emit(started)
	var once sync.Once
	return func(outcome, failureClass, retryDisposition string, usage Usage) {
		once.Do(func() {
			outcome = strings.TrimSpace(outcome)
			if !validStageOutcome(outcome) {
				outcome = StageOutcomeFailed
			}
			failureClass = strings.TrimSpace(failureClass)
			if !validFailureClass(failureClass) {
				failureClass = "unknown"
			}
			retryDisposition = strings.TrimSpace(retryDisposition)
			if !validRetryDisposition(retryDisposition) {
				retryDisposition = "none"
			}
			finishedAt := time.Now().UTC()
			completed := trace.stageEvent(EventStageCompleted, stageID, name)
			completed.StartedAt = startedAt
			completed.FinishedAt = finishedAt
			completed.DurationMilliseconds = finishedAt.Sub(startedAt).Milliseconds()
			completed.Outcome = outcome
			completed.FailureClass = failureClass
			completed.RetryDisposition = retryDisposition
			completed.Usage = usage
			trace.emit(completed)
		})
	}
}

func (t *AttemptTrace) Errors() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return errors.Join(t.errors...)
}

func (t *AttemptTrace) stageEvent(kind, stageID, name string) Event {
	event := t.base
	event.Kind = kind
	event.StageID = stageID
	event.Stage = name
	event.FinishedAt = time.Time{}
	event.DurationMilliseconds = 0
	event.HarnessDurationMilliseconds = 0
	event.Outcome = ""
	event.FailureClass = ""
	event.RetryDisposition = ""
	event.RetryAfter = ""
	event.Summary = ""
	event.WorkDone = nil
	event.Verification = nil
	event.ResumedCheckpoint = false
	event.Usage = Usage{}
	return event
}

func (t *AttemptTrace) emit(event Event) {
	if err := t.observer(event); err != nil {
		t.mu.Lock()
		t.errors = append(t.errors, err)
		t.mu.Unlock()
	}
}
