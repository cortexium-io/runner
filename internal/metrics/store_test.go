package metrics

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestStoreFoldsDurableAttemptEventsAndIgnoresMalformedRecords(t *testing.T) {
	path := t.TempDir() + "/metrics.jsonl"
	store := NewStore(path)
	startedAt := time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC)
	start := Event{Kind: EventStarted, AttemptID: "att_1", RunnerID: "runner", ItemID: "item", ItemTitle: "Build it", Role: "implementer", Harness: "claude", StartedAt: startedAt}
	if err := store.Append(start); err != nil {
		t.Fatalf("append start: %v", err)
	}
	stageStart := start
	stageStart.Kind = EventStageStarted
	stageStart.StageID = "stg_1"
	stageStart.Stage = StageHarnessRun
	stageStart.StartedAt = startedAt.Add(time.Second)
	if err := store.Append(stageStart); err != nil {
		t.Fatalf("append stage start: %v", err)
	}
	stageFinish := stageStart
	stageFinish.Kind = EventStageCompleted
	stageFinish.FinishedAt = stageFinish.StartedAt.Add(90 * time.Second)
	stageFinish.DurationMilliseconds = (90 * time.Second).Milliseconds()
	stageFinish.Outcome = StageOutcomeSucceeded
	stageFinish.Usage = Usage{Available: true, InputTokens: 12, OutputTokens: 3}
	if err := store.Append(stageFinish); err != nil {
		t.Fatalf("append stage finish: %v", err)
	}
	cost := 1.25
	finish := start
	finish.Kind = EventCompleted
	finish.FinishedAt = startedAt.Add(2 * time.Minute)
	finish.DurationMilliseconds = (2 * time.Minute).Milliseconds()
	finish.HarnessDurationMilliseconds = (90 * time.Second).Milliseconds()
	finish.Outcome = "succeeded"
	finish.ResumedCheckpoint = true
	finish.Summary = "Done"
	finish.Usage = Usage{Available: true, InputTokens: 100, CacheReadInputTokens: 40, OutputTokens: 20, ReportedCostUSD: &cost}
	if err := store.Append(finish); err != nil {
		t.Fatalf("append finish: %v", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("not-json\n"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	history, err := store.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if history.MalformedRecords != 1 || len(history.Attempts) != 1 || !history.Attempts[0].Completed {
		t.Fatalf("unexpected history: %#v", history)
	}
	if len(history.Attempts[0].Stages) != 1 || !history.Attempts[0].Stages[0].Completed || history.Attempts[0].Stages[0].Name != StageHarnessRun || history.Attempts[0].Stages[0].Usage.InputTokens != 12 {
		t.Fatalf("stage history was not folded: %#v", history.Attempts[0].Stages)
	}
	summary := Summarize(history.Attempts)
	if summary.CompletedAttempts != 1 || summary.HarnessInvocations != 1 || summary.ResumedCheckpointAttempts != 1 || summary.HarnessDurationMilliseconds != 90000 || summary.RunnerDurationMilliseconds != 30000 || summary.CostCoveredAttempts != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if summary.Usage.ReportedCostUSD == nil || *summary.Usage.ReportedCostUSD != cost {
		t.Fatalf("reported cost lost: %#v", summary.Usage)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "prompt") || strings.Contains(string(content), "transcript") {
		t.Fatalf("store unexpectedly contains transcript-like fields: %s", content)
	}
}

func TestStoreRejectsStageWithoutStableIdentity(t *testing.T) {
	store := NewStore(t.TempDir() + "/metrics.jsonl")
	err := store.Append(Event{Kind: EventStageStarted, AttemptID: "att_1", Stage: StageHarnessRun})
	if err == nil || !strings.Contains(err.Error(), "stage_id") {
		t.Fatalf("invalid stage event was accepted: %v", err)
	}
}

func TestSummaryCountsEveryModelCallStageAsHarnessInvocation(t *testing.T) {
	stages := []string{StageHarnessRun, StagePlannerOutline, StagePlannerDetails, StageReviewerAudit, StageReviewerVerify}
	attempt := Attempt{Stages: make([]Stage, 0, len(stages))}
	for _, name := range stages {
		attempt.Stages = append(attempt.Stages, Stage{Name: name, Completed: true})
	}
	if got := Summarize([]Attempt{attempt}).HarnessInvocations; got != len(stages) {
		t.Fatalf("harness invocations = %d, want %d", got, len(stages))
	}
}

func TestStoreRejectsFreeFormStageAndRecoveryFields(t *testing.T) {
	store := NewStore(t.TempDir() + "/metrics.jsonl")
	for _, event := range []Event{
		{Kind: EventStageStarted, AttemptID: "attempt", StageID: "stage", Stage: "prompt=secret"},
		{Kind: EventStageCompleted, AttemptID: "attempt", StageID: "stage", Stage: StageHarnessRun, Outcome: "raw error"},
		{Kind: EventCompleted, AttemptID: "attempt", FailureClass: "token=secret"},
	} {
		if err := store.Append(event); err == nil {
			t.Fatalf("unsafe event was accepted: %#v", event)
		}
	}
}

func TestStoreRejectsInvalidNumericMetrics(t *testing.T) {
	store := NewStore(t.TempDir() + "/metrics.jsonl")
	for _, event := range []Event{
		{Kind: EventCompleted, AttemptID: "negative_duration", DurationMilliseconds: -1},
		{Kind: EventCompleted, AttemptID: "negative_tokens", Usage: Usage{Available: true, InputTokens: -1}},
		{Kind: EventCompleted, AttemptID: "negative_cost", Usage: Usage{ReportedCostUSD: floatPtr(-0.01)}},
	} {
		if err := store.Append(event); err == nil {
			t.Fatalf("invalid numeric metrics were accepted: %#v", event)
		}
	}
}

func TestStoreTreatsInvalidNumericHistoryAsMalformed(t *testing.T) {
	path := t.TempDir() + "/metrics.jsonl"
	if err := os.WriteFile(path, []byte(`{"version":1,"kind":"completed","attempt_id":"forged","usage":{"available":true,"input_tokens":-1}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	history, err := NewStore(path).Read()
	if err != nil {
		t.Fatal(err)
	}
	if history.MalformedRecords != 1 || len(history.Attempts) != 0 {
		t.Fatalf("invalid numeric history was trusted: %#v", history)
	}
}

func TestStorePreservesUnfinishedAttempt(t *testing.T) {
	store := NewStore(t.TempDir() + "/metrics.jsonl")
	if err := store.Append(Event{Kind: EventStarted, AttemptID: "att_running", RunnerID: "runner", ItemTitle: "Still running", Role: "reviewer", Harness: "codex", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	history, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Attempts) != 1 || history.Attempts[0].Completed || Summarize(history.Attempts).UnfinishedAttempts != 1 {
		t.Fatalf("unfinished attempt was not preserved: %#v", history)
	}
}

func floatPtr(value float64) *float64 { return &value }
