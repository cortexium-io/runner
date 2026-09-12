package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPromptContextRejectsFreeTextAtHistoryBoundary(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "history.jsonl"))
	event := Event{Version: EventVersion, Kind: EventCompleted, AttemptID: "attempt",
		PromptContexts: []PromptContext{{Layout: "stable-first-v1", GuidanceDigest: "raw prompt content"}}}
	if err := store.Append(event); err == nil {
		t.Fatal("history accepted free text as a context digest")
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	history, err := store.Read()
	if err != nil || history.MalformedRecords != 1 || len(history.Attempts) != 0 {
		t.Fatalf("malformed context was not excluded on read: %#v %v", history, err)
	}
}

func TestPromptContextIsPinnedToStageAndAggregatedForAttempt(t *testing.T) {
	var events []Event
	trace := NewAttemptTrace(func(event Event) error { events = append(events, event); return nil }, Event{AttemptID: "attempt"})
	ctx := WithAttemptTrace(t.Context(), trace)
	first := PromptContext{Layout: "stable-first-v1", GuidanceDigest: "sha256:" + strings.Repeat("a", 64)}
	second := PromptContext{Layout: "stable-first-v1", GuidanceDigest: "sha256:" + strings.Repeat("b", 64)}
	RecordPromptContext(ctx, PromptContext{Layout: "prompt=secret", GuidanceDigest: "secret"})
	RecordPromptContext(ctx, first)
	finish := StartStage(ctx, StagePlannerOutline)
	RecordPromptContext(ctx, second)
	finish(StageOutcomeSucceeded, "", "", Usage{})
	StartStage(ctx, StagePlannerDetails)(StageOutcomeSucceeded, "", "", Usage{})
	if len(events) != 4 || !reflect.DeepEqual(events[1].PromptContexts, []PromptContext{first}) ||
		!reflect.DeepEqual(events[3].PromptContexts, []PromptContext{second}) ||
		!reflect.DeepEqual(trace.PromptContexts(), []PromptContext{first, second}) {
		t.Fatalf("context changed mid-stage or unsafe payload was retained: %#v", events)
	}
}

func TestAttemptTraceEmitsOnlyStructuredStageFields(t *testing.T) {
	var events []Event
	trace := NewAttemptTrace(func(event Event) error {
		events = append(events, event)
		return nil
	}, Event{AttemptID: "att_1", RunnerID: "runner", ItemTitle: "Task", Role: "implementer", Harness: "codex", ReviewVerdict: "accept"})
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
	if completed.Summary != "" || completed.WorkDone != nil || completed.Verification != nil || completed.ReviewVerdict != "" || events[0].ReviewVerdict != "" {
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

func TestCandidateValidationIsAStableFailureClass(t *testing.T) {
	if !validFailureClass("candidate_validation") {
		t.Fatal("candidate validation failure class is not accepted by metrics")
	}
}

func TestBrowserStartupIsAStableFailureClass(t *testing.T) {
	if !validFailureClass("browser_startup") {
		t.Fatal("browser startup failure class is not accepted by metrics")
	}
}

func TestAutomaticRetryIsAStableDisposition(t *testing.T) {
	if !validRetryDisposition("automatic") {
		t.Fatal("automatic retry disposition is not accepted by metrics")
	}
}
