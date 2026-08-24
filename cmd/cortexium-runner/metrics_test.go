package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	runnermetrics "github.com/cortexium-io/runner/internal/metrics"
)

func TestMetricsCommandFiltersItemsAndReportsOnlyHarnessCost(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CORTEXIUM_RUNNER_STATE_DIR", stateDir)
	projectDir := t.TempDir()
	cfg := completeCLITestConfig(projectDir)
	configPath := filepath.Join(t.TempDir(), "runner.config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	store, err := runnermetrics.NewDefaultStore(cfg.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	cost := 0.75
	for _, event := range []runnermetrics.Event{
		{Kind: runnermetrics.EventStarted, AttemptID: "att_one", RunnerID: cfg.RunnerID, ItemID: "PVTI_one", ItemTitle: "Build shell", Role: "implementer", Harness: "claude", StartedAt: startedAt},
		{Kind: runnermetrics.EventStageStarted, AttemptID: "att_one", RunnerID: cfg.RunnerID, ItemID: "PVTI_one", ItemTitle: "Build shell", Role: "implementer", Harness: "claude", StageID: "stg_one", Stage: runnermetrics.StageHarnessRun, StartedAt: startedAt},
		{Kind: runnermetrics.EventStageCompleted, AttemptID: "att_one", RunnerID: cfg.RunnerID, ItemID: "PVTI_one", ItemTitle: "Build shell", Role: "implementer", Harness: "claude", StageID: "stg_one", Stage: runnermetrics.StageHarnessRun, StartedAt: startedAt, FinishedAt: startedAt.Add(50 * time.Second), DurationMilliseconds: 50000, Outcome: runnermetrics.StageOutcomeSucceeded},
		{Kind: runnermetrics.EventCompleted, AttemptID: "att_one", RunnerID: cfg.RunnerID, ItemID: "PVTI_one", ItemTitle: "Build shell", Role: "implementer", Harness: "claude", StartedAt: startedAt, FinishedAt: startedAt.Add(time.Minute), DurationMilliseconds: 60000, HarnessDurationMilliseconds: 50000, Outcome: "blocked", FailureClass: "capacity_exhausted", RetryDisposition: "manual", RetryAfter: "tomorrow", Summary: "Paused.", ResumedCheckpoint: true, Usage: runnermetrics.Usage{Available: true, InputTokens: 12, OutputTokens: 3, ReportedCostUSD: &cost}},
		{Kind: runnermetrics.EventStarted, AttemptID: "att_two", RunnerID: cfg.RunnerID, ItemID: "PVTI_two", ItemTitle: "Review shell", Role: "reviewer", Harness: "codex", StartedAt: startedAt},
	} {
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}

	var output bytes.Buffer
	if err := runMetrics([]string{"--config", configPath, "--item", "PVTI_one"}, &output); err != nil {
		t.Fatalf("metrics: %v", err)
	}
	for _, expected := range []string{"Recorded attempts: 1", "Harness invocations: 1", "saved-result resumes: 1", "resumed: exact saved implementation result", "12 input", "$0.7500", "Build shell", "capacity_exhausted", "retry manual", "stage: harness_run"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output omitted %q:\n%s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "Review shell") {
		t.Fatalf("item filter leaked another attempt:\n%s", output.String())
	}

	output.Reset()
	if err := runMetrics([]string{"--config", configPath, "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var decoded metricsOutput
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, output.String())
	}
	if decoded.Summary.Attempts != 2 || decoded.Summary.UnfinishedAttempts != 1 || decoded.Summary.HarnessInvocations != 1 || decoded.Summary.ResumedCheckpointAttempts != 1 || decoded.Summary.CostCoveredAttempts != 1 {
		t.Fatalf("unexpected JSON summary: %#v", decoded.Summary)
	}
}
