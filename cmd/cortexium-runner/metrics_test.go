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
		{Kind: runnermetrics.EventCompleted, AttemptID: "att_one", RunnerID: cfg.RunnerID, ItemID: "PVTI_one", ItemTitle: "Build shell", Role: "implementer", Harness: "claude", StartedAt: startedAt, FinishedAt: startedAt.Add(time.Minute), DurationMilliseconds: 60000, HarnessDurationMilliseconds: 50000, Outcome: "blocked", FailureClass: "capacity_exhausted", FailureOperation: "publication_inspect_pull_request", PublicationAttempts: 3, RetryDisposition: "manual", RetryAfter: "tomorrow", Summary: "Paused.", ResumedCheckpoint: true, Usage: runnermetrics.Usage{Available: true, InputTokens: 12, OutputTokens: 3, ReportedCostUSD: &cost}},
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
	for _, expected := range []string{"Recorded attempts: 1", "Harness invocations: 1", "saved-result resumes: 1", "resumed: exact saved checkpoint", "12 input", "$0.7500", "Build shell", "capacity_exhausted", "retry manual", "publication_inspect_pull_request", "3 attempt(s)", "stage: harness_run", "Stage evidence: 1/1 attempts", "harness_run: 1/1 completed", "stage time is not test time"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output omitted %q:\n%s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "Review shell") {
		t.Fatalf("item filter leaked another attempt:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "Recorded QA verdicts: unavailable") {
		t.Fatalf("old metrics fabricated QA coverage:\n%s", output.String())
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
	if decoded.Summary.StageCoveredAttempts != 1 || len(decoded.Summary.Stages) != 1 || decoded.Summary.Stages[0].DurationMilliseconds != 50000 {
		t.Fatalf("JSON omitted measured stage coverage: %#v", decoded.Summary)
	}
}

func TestMetricsCommandReportsQAIndependentlyOfPublication(t *testing.T) {
	t.Setenv("CORTEXIUM_RUNNER_STATE_DIR", t.TempDir())
	cfg := completeCLITestConfig(t.TempDir())
	configPath := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := runnermetrics.NewDefaultStore(cfg.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []runnermetrics.Event{
		{Kind: runnermetrics.EventCompleted, AttemptID: "accepted", ReviewVerdict: "accept", Outcome: "blocked", FailureOperation: "publication_create_pull_request"},
		{Kind: runnermetrics.EventCompleted, AttemptID: "exhausted", ReviewVerdict: "needs_changes", Outcome: "blocked"},
		{Kind: runnermetrics.EventCompleted, AttemptID: "incomplete", ReviewVerdict: "blocked", Outcome: "needs_input"},
		{Kind: runnermetrics.EventCompleted, AttemptID: "resumed", Outcome: "succeeded", ResumedCheckpoint: true},
	} {
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := runMetrics([]string{"--config", configPath}, &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Recorded QA verdicts: 3 · 1 accepted · 1 changes requested · 1 blocked", "QA verdict: accept", "QA verdict: needs_changes", "QA verdict: blocked", "failed operation: publication_create_pull_request", "missing verdicts are not inferred"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("metrics omitted %q:\n%s", expected, output.String())
		}
	}
	output.Reset()
	if err := runMetrics([]string{"--config", configPath, "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var view metricsOutput
	if err := json.Unmarshal(output.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Summary.ReviewVerdictCoveredAttempts != 3 || view.Summary.ReviewAcceptedAttempts != 1 || view.Summary.ReviewChangesRequestedAttempts != 1 || view.Summary.ReviewBlockedAttempts != 1 || len(view.Attempts) != 4 {
		t.Fatalf("JSON omitted review coverage: %#v", view)
	}
	for _, attempt := range view.Attempts {
		if attempt.AttemptID == "accepted" && (attempt.ReviewVerdict != "accept" || attempt.Outcome != "blocked") {
			t.Fatalf("JSON conflated QA and publication outcomes: %#v", attempt)
		}
	}
}

func TestWriteMetricsRendersDetailedHistoryWithExplicitProvenanceAndUnknowns(t *testing.T) {
	candidate := runnermetrics.ObjectIdentity{CommitOID: strings.Repeat("a", 40), TreeOID: strings.Repeat("b", 40)}
	attempt := runnermetrics.Attempt{Completed: true, Event: runnermetrics.Event{
		Kind: runnermetrics.EventCompleted, AttemptID: "attempt", ItemID: "PVTI_history", ItemTitle: "Explain history", Role: "reviewer", Harness: "codex",
		StartedAt: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC), Outcome: "succeeded", Summary: "Accepted repair.", ModelReportedSummary: "The repair satisfies the request.",
		WorkDone: []string{"Inspected repair."}, Verification: []string{"Focused check passed."}, ReviewVerdict: "accept",
		ReviewDetails:   []runnermetrics.ReviewDetail{{Area: "acceptance", Name: "proof", Status: "passed", Summary: "The model reported coverage.", Evidence: []string{"reported check"}}},
		ApprovedRequest: &runnermetrics.ApprovedRequest{DelegatedContentDigest: "v1:" + strings.Repeat("c", 64), BodySnapshot: "Exact approved content"},
		CandidateOID:    candidate.CommitOID,
		Lineage: &runnermetrics.ObservedLineage{Repository: "owner/repo", Branch: "runner/history", Base: runnermetrics.ObjectIdentity{CommitOID: strings.Repeat("d", 40)}, Candidate: candidate, EvidenceCandidate: candidate, ReviewedCandidate: candidate,
			PublishedCandidate: runnermetrics.ObjectIdentity{CommitOID: strings.Repeat("e", 40), TreeOID: candidate.TreeOID}, PullRequestURL: "https://github.com/owner/repo/pull/7", PullRequestNumber: 7},
	}}
	view := metricsOutput{RunnerID: "runner", HistoryPath: "/protected/history", Summary: runnermetrics.Summarize([]runnermetrics.Attempt{attempt}), Attempts: []runnermetrics.Attempt{attempt}, DetailedHistory: true}
	var output bytes.Buffer
	writeMetrics(&output, view)
	for _, expected := range []string{
		"Runner-observed approval digest", "Exact approved content", "Runner-observed candidate commit", "Runner-observed published candidate commit",
		"Runner-observed pull request", "Runner-observed merge commit: unavailable", "model-reported work", "model-reported verification",
		"model-reported summary", "Runner-classified outcome", "model-reported review detail (Runner-validated)", "harness-reported usage: unavailable",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("detailed history omitted %q:\n%s", expected, output.String())
		}
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"runner_observed_approved_request", "runner_observed_lineage", "model_reported_summary", "model_reported_review_details"} {
		if !bytes.Contains(encoded, []byte(expected)) {
			t.Fatalf("JSON omitted provenance field %q: %s", expected, encoded)
		}
	}
	for _, forbidden := range []string{"raw_transcript", "hidden_reasoning", "command_environment", "credential"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("JSON exposed forbidden field %q: %s", forbidden, encoded)
		}
	}
}
