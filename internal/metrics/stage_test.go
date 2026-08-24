package metrics

import (
	"context"
	"errors"
	"testing"
)

func TestAttemptTraceEmitsOnlyStructuredStageFields(t *testing.T) {
	var events []Event
	trace := NewAttemptTrace(func(event Event) error {
		events = append(events, event)
		return nil
	}, Event{AttemptID: "att_1", RunnerID: "runner", ItemTitle: "Task", Role: "implementer", Harness: "codex"})
	ctx := WithAttemptTrace(context.Background(), trace)
	finish := StartStage(ctx, StageHarnessRun)
	finish(StageOutcomeFailed, "timeout", "manual", Usage{Available: true, InputTokens: 12})

	if len(events) != 2 || events[0].Kind != EventStageStarted || events[1].Kind != EventStageCompleted {
		t.Fatalf("unexpected stage events: %#v", events)
	}
	completed := events[1]
	if completed.Stage != StageHarnessRun || completed.Outcome != StageOutcomeFailed || completed.FailureClass != "timeout" || completed.RetryDisposition != "manual" || completed.Usage.InputTokens != 12 {
		t.Fatalf("unexpected completed stage: %#v", completed)
	}
	if completed.Summary != "" || completed.WorkDone != nil || completed.Verification != nil {
		t.Fatalf("stage event admitted arbitrary attempt payload: %#v", completed)
	}
}

func TestAttemptTraceCollectsObserverErrorsWithoutPanicking(t *testing.T) {
	trace := NewAttemptTrace(func(Event) error { return errors.New("disk full") }, Event{AttemptID: "att_1"})
	finish := StartStage(WithAttemptTrace(context.Background(), trace), StageResultValidate)
	finish(StageOutcomeSucceeded, "", "", Usage{})
	if err := trace.Errors(); err == nil || err.Error() != "disk full\ndisk full" {
		t.Fatalf("observer errors = %v", err)
	}
}

func TestAttemptTraceSanitizesRecoveryEnumsAndRejectsFreeFormStageNames(t *testing.T) {
	var events []Event
	trace := NewAttemptTrace(func(event Event) error {
		events = append(events, event)
		return nil
	}, Event{AttemptID: "att_1"})
	ctx := WithAttemptTrace(context.Background(), trace)
	StartStage(ctx, "prompt=secret")(StageOutcomeSucceeded, "", "", Usage{})
	finish := StartStage(ctx, StageHarnessRun)
	finish("raw outcome", "token=secret", "retry everything", Usage{})

	if len(events) != 2 {
		t.Fatalf("free-form stage emitted telemetry: %#v", events)
	}
	completed := events[1]
	if completed.Outcome != StageOutcomeFailed || completed.FailureClass != "unknown" || completed.RetryDisposition != "none" {
		t.Fatalf("unsafe recovery fields were not sanitized: %#v", completed)
	}
}
