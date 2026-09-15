package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	runnermetrics "github.com/cortexium-io/runner/internal/metrics"
)

func TestMetricsRunIdentityIsRetainedAtRecordingNotExport(t *testing.T) {
	for _, mode := range []string{"cli", "service"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CORTEXIUM_RUNNER_STATE_DIR", t.TempDir())
			cfg := completeCLITestConfig(t.TempDir())
			profile := cfg.Roles[config.WorkRoleImplementer]
			profile.Description = "private-config-content-not-for-history"
			cfg.Roles[config.WorkRoleImplementer] = profile
			store, err := runnermetrics.NewDefaultStore(cfg.RunnerID)
			if err != nil {
				t.Fatal(err)
			}
			observe := metricsRunObserver(cfg, store.Append)
			if mode == "service" {
				observe = guidanceMetricsObserver(store, cfg, io.Discard)
			}
			// Changing even the shared map after attachment must not relabel this run.
			profile.Reasoning = "high"
			profile.Description = "changed configuration"
			cfg.Roles[config.WorkRoleImplementer] = profile
			if err := observe(runnermetrics.Event{Kind: runnermetrics.EventCompleted, AttemptID: "original", Outcome: "succeeded"}); err != nil {
				t.Fatal(err)
			}
			if err := metricsRunObserver(cfg, store.Append)(runnermetrics.Event{Kind: runnermetrics.EventStarted, AttemptID: "later"}); err != nil {
				t.Fatal(err)
			}
			if err := store.Append(runnermetrics.Event{Kind: runnermetrics.EventCompleted, AttemptID: "unknown"}); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(t.TempDir(), "runner.json")
			if err := config.SaveConfig(configPath, cfg); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if code := execute(t.Context(), []string{"metrics", "--config", configPath, "--json"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
				t.Fatalf("metrics CLI exit %d: %s", code, &stderr)
			}
			var view metricsOutput
			if err := json.Unmarshal(stdout.Bytes(), &view); err != nil || len(view.History) != 3 {
				t.Fatalf("metrics export: attempts=%d error=%v", len(view.History), err)
			}
			first, later := view.History[0].RunnerObserved.RunContext, view.History[1].RunnerObserved.RunContext
			if first == nil || later == nil || first.RunnerVersion != buildVersion() || first.BundledSkillsVersion == "" ||
				first.ConfigDigest == later.ConfigDigest || view.History[2].RunnerObserved.RunContext != nil {
				t.Fatalf("lost historical run identity: first=%+v later=%+v unknown=%+v", first, later, view.History[2].RunnerObserved.RunContext)
			}
			raw, err := os.ReadFile(store.Path())
			if err != nil || bytes.Contains(raw, []byte("private-config-content")) || bytes.Contains(raw, []byte("changed configuration")) {
				t.Fatalf("config contents leaked into history: %v", err)
			}
			if info, err := os.Stat(store.Path()); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("metrics history not private: %v", err)
			}
		})
	}
}

func TestMetricsRunObserverPreservesWriteFailures(t *testing.T) {
	want := errors.New("disk full")
	observe := metricsRunObserver(completeCLITestConfig(t.TempDir()), func(runnermetrics.Event) error { return want })
	if err := observe(runnermetrics.Event{}); !errors.Is(err, want) {
		t.Fatalf("metrics write failure hidden from admission controls: %v", err)
	}
}

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
	if view.Summary.ReviewVerdictCoveredAttempts != 3 || view.Summary.ReviewAcceptedAttempts != 1 || view.Summary.ReviewChangesRequestedAttempts != 1 || view.Summary.ReviewBlockedAttempts != 1 || len(view.History) != 4 {
		t.Fatalf("JSON omitted review coverage: %#v", view)
	}
	for _, attempt := range view.History {
		if attempt.AttemptID == "accepted" && (attempt.ModelReported.ReviewVerdict != "accept" || attempt.RunnerObserved.Outcome != "blocked") {
			t.Fatalf("JSON conflated QA and publication outcomes: %#v", attempt)
		}
	}
}

func TestMetricItemFilterRefusesAmbiguousTitlesAndNeverUsesSubstrings(t *testing.T) {
	knownItems := []runnermetrics.Attempt{
		{Event: runnermetrics.Event{AttemptID: "one", ItemID: "PVTI_one", ItemTitle: "Shared history"}},
		{Event: runnermetrics.Event{AttemptID: "two", ItemID: "PVTI_two", ItemTitle: "Shared history"}},
	}
	filtered, err := filterMetricAttempts(knownItems, "PVTI_one")
	if err != nil || len(filtered) != 1 || filtered[0].AttemptID != "one" {
		t.Fatalf("exact item ID filter mixed attempts: %#v error=%v", filtered, err)
	}
	if filtered, err = filterMetricAttempts(knownItems, "Shared history"); err == nil || len(filtered) != 0 {
		t.Fatalf("ambiguous title was allowed to mix items: %#v error=%v", filtered, err)
	}
	if filtered, err = filterMetricAttempts(knownItems, "history"); err != nil || len(filtered) != 0 {
		t.Fatalf("substring selector unexpectedly matched an item: %#v error=%v", filtered, err)
	}

	for name, attempts := range map[string][]runnermetrics.Attempt{
		"two unavailable IDs": {
			{Event: runnermetrics.Event{AttemptID: "unknown-one", ItemTitle: "Unknown identity"}},
			{Event: runnermetrics.Event{AttemptID: "unknown-two", ItemTitle: "Unknown identity"}},
		},
		"known and unavailable IDs": {
			{Event: runnermetrics.Event{AttemptID: "known", ItemID: "PVTI_known", ItemTitle: "Incomplete identity"}},
			{Event: runnermetrics.Event{AttemptID: "unknown", ItemTitle: "Incomplete identity"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			filtered, err := filterMetricAttempts(attempts, attempts[0].ItemTitle)
			if err == nil || len(filtered) != 0 {
				t.Fatalf("title filter joined potentially distinct cards: %#v error=%v", filtered, err)
			}
		})
	}

	repeatedKnownID := []runnermetrics.Attempt{
		{Event: runnermetrics.Event{AttemptID: "known-one", ItemID: "PVTI_same", ItemTitle: "Known identity"}},
		{Event: runnermetrics.Event{AttemptID: "known-two", ItemID: "pvti_SAME", ItemTitle: "Known identity"}},
	}
	if filtered, err = filterMetricAttempts(repeatedKnownID, "Known identity"); err != nil || len(filtered) != 2 {
		t.Fatalf("attempts with one known card identity were rejected: %#v error=%v", filtered, err)
	}
	singleUnknown := []runnermetrics.Attempt{{Event: runnermetrics.Event{AttemptID: "single", ItemTitle: "Only match"}}}
	if filtered, err = filterMetricAttempts(singleUnknown, "Only match"); err != nil || len(filtered) != 1 {
		t.Fatalf("single exact title match was rejected: %#v error=%v", filtered, err)
	}
}

func TestMetricsCommandDoesNotRenderExactTitleCollisionsWithUnavailableItemIDs(t *testing.T) {
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
		{Kind: runnermetrics.EventCompleted, AttemptID: "unknown-one", ItemTitle: "Same title", WorkDone: []string{"FIRST_CARD_PRIVATE_EVIDENCE"}},
		{Kind: runnermetrics.EventCompleted, AttemptID: "unknown-two", ItemTitle: "Same title", WorkDone: []string{"SECOND_CARD_PRIVATE_EVIDENCE"}},
	} {
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}

	for _, mode := range []struct {
		name string
		args []string
	}{
		{name: "human", args: []string{"--config", configPath, "--item", "Same title"}},
		{name: "json", args: []string{"--config", configPath, "--item", "Same title", "--json"}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			var output bytes.Buffer
			err := runMetrics(mode.args, &output)
			if err == nil || !strings.Contains(err.Error(), "without one unambiguous card ID") {
				t.Fatalf("ambiguous title was not refused safely: output=%q error=%v", output.String(), err)
			}
			if output.Len() != 0 {
				t.Fatalf("ambiguous %s history disclosed retained evidence: %q", mode.name, output.String())
			}
		})
	}
}

func TestWriteMetricsPreservesSummaryForRetainedProvenanceFields(t *testing.T) {
	started := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	attempts := []runnermetrics.Attempt{
		{Completed: true, Event: runnermetrics.Event{AttemptID: "model", ItemTitle: "Model report", Role: "implementer", Harness: "codex", StartedAt: started, Outcome: "succeeded", ModelReportedSummary: "model summary"}},
		{Completed: true, Event: runnermetrics.Event{AttemptID: "runner", ItemTitle: "Runner observation", Role: "runner", Harness: "runner", StartedAt: started, Outcome: "succeeded", RunnerObservation: "runner summary"}},
		{Completed: true, Event: runnermetrics.Event{AttemptID: "refused", ItemTitle: "Refused review", Role: "reviewer", Harness: "codex", StartedAt: started, Outcome: "blocked", ModelReportedSummary: "unvalidated acceptance claim", RunnerObservation: "Runner refused changed review workspace"}},
	}
	var output bytes.Buffer
	writeMetrics(&output, metricsOutput{RunnerID: "runner", Summary: runnermetrics.Summarize(attempts), Attempts: attempts})
	if !strings.Contains(output.String(), "model summary") || !strings.Contains(output.String(), "runner summary") {
		t.Fatalf("existing metrics view lost retained summaries:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "Runner refused changed review workspace") || !strings.Contains(output.String(), "rationale: unvalidated acceptance claim") ||
		strings.Index(output.String(), "Runner-observed:") > strings.Index(output.String(), "Model-reported:") {
		t.Fatalf("retained summaries lost their separate provenance:\n%s", output.String())
	}
}

func TestMetricsHistoryJoinsTwoAttemptsChronologicallyWithExactProvenance(t *testing.T) {
	item := github.WorkItem{ID: "PVTI_history", Title: "Explain retained attempts", Body: "Exact approved request", Repository: "owner/repo"}
	snapshot := github.DelegatedContentSnapshotFor(item)
	digest := sha256.Sum256([]byte(snapshot))
	approval := runnermetrics.NewApprovedRequest(fmt.Sprintf("v1:%x", digest), snapshot)
	complete := true
	firstCandidate := runnermetrics.ObjectIdentity{CommitOID: strings.Repeat("a", 40), TreeOID: strings.Repeat("b", 40)}
	secondCandidate := runnermetrics.ObjectIdentity{CommitOID: strings.Repeat("c", 40), TreeOID: strings.Repeat("d", 40)}
	started := time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC)
	attempts := []runnermetrics.Attempt{
		{
			Completed: true,
			Event: runnermetrics.Event{
				Kind: runnermetrics.EventCompleted, AttemptID: "attempt-one", RunnerID: "runner", ProjectOwner: "owner", ProjectNumber: 8,
				Repository: item.Repository, ItemID: item.ID, ItemTitle: item.Title, Role: "implementer", Harness: "codex", StartedAt: started,
				FinishedAt: started.Add(time.Minute), DurationMilliseconds: 60000, Outcome: "rejected", RunnerObservation: "Runner retained the rejected candidate.",
				ModelReportedSummary: "The first implementation needs repair.", WorkDone: []string{"Implemented the first candidate."}, Verification: []string{"Focused verification failed."},
				ReviewVerdict: "needs_changes", ReviewFindings: []runnermetrics.ReviewFinding{{Area: "acceptance", Summary: "Observed behavior did not match."}},
				ModelReportComplete: &complete, ApprovedRequest: approval, CandidateOID: firstCandidate.CommitOID,
				Lineage: &runnermetrics.ObservedLineage{Repository: item.Repository, Branch: "runner/history", Base: runnermetrics.ObjectIdentity{CommitOID: strings.Repeat("1", 40)}, Candidate: firstCandidate, EvidenceCandidate: firstCandidate, ReviewedCandidate: firstCandidate},
			},
		},
		{
			Completed: true,
			Event: runnermetrics.Event{Kind: runnermetrics.EventCompleted, AttemptID: "foreign", RunnerID: "runner", ItemID: "PVTI_foreign", ItemTitle: "Foreign item", Role: "implementer", Harness: "codex", StartedAt: started.Add(30 * time.Minute),
				Outcome: "succeeded", WorkDone: []string{"FOREIGN_ITEM_EVIDENCE", "DO_NOT_LEAK_PROMPT", "DO_NOT_LEAK_TRANSCRIPT", "DO_NOT_LEAK_CREDENTIAL"}, ModelReportComplete: &complete},
		},
		{
			Completed: true,
			Event: runnermetrics.Event{
				Kind: runnermetrics.EventCompleted, AttemptID: "attempt-two", RunnerID: "runner", ProjectOwner: "owner", ProjectNumber: 8,
				Repository: item.Repository, ItemID: item.ID, ItemTitle: item.Title, Role: "reviewer", Harness: "codex", StartedAt: started.Add(time.Hour),
				FinishedAt: started.Add(time.Hour + time.Minute), DurationMilliseconds: 60000, Outcome: "succeeded", RunnerObservation: "Runner observed publication and final merge.",
				ModelReportedSummary: "The repaired implementation satisfies the request.", WorkDone: []string{"Reviewed the repaired candidate."}, Verification: []string{"Focused verification passed."},
				ReviewVerdict: "accept", ReviewDetails: []runnermetrics.ReviewDetail{{Area: "acceptance", Name: "history", Status: "passed", Summary: "Two attempts remain separate.", Evidence: []string{"Deterministic fixture passed."}}},
				ModelReportComplete: &complete, Usage: runnermetrics.Usage{Available: true, InputTokens: 9, OutputTokens: 4}, ApprovedRequest: approval, CandidateOID: secondCandidate.CommitOID,
				Lineage: &runnermetrics.ObservedLineage{Repository: item.Repository, Branch: "runner/history", Base: runnermetrics.ObjectIdentity{CommitOID: strings.Repeat("2", 40)}, Candidate: secondCandidate, EvidenceCandidate: secondCandidate, ReviewedCandidate: secondCandidate, RebasedCandidate: runnermetrics.ObjectIdentity{CommitOID: strings.Repeat("e", 40), TreeOID: secondCandidate.TreeOID}, PublishedCandidate: runnermetrics.ObjectIdentity{CommitOID: strings.Repeat("f", 40), TreeOID: secondCandidate.TreeOID}, PullRequestURL: "https://github.com/owner/repo/pull/8", PullRequestNumber: 8, Merge: runnermetrics.ObjectIdentity{CommitOID: strings.Repeat("3", 40)}},
			},
		},
	}
	// Store.Read returns newest first; the item-history projection reverses that
	// order without allowing the intervening foreign item into the view.
	attempts[0], attempts[2] = attempts[2], attempts[0]
	before, err := json.Marshal(attempts)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := filterMetricAttempts(attempts, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	sortMetricAttemptsChronologically(filtered)
	view := metricsOutput{RunnerID: "runner", Summary: runnermetrics.Summarize(filtered), Attempts: filtered, History: metricAttemptHistories(filtered), MalformedRecords: 1}

	var human bytes.Buffer
	writeMetrics(&human, view)
	rendered := human.String()
	for _, expected := range []string{
		"Chronological history (oldest first)", "1. 2026-09-13", "attempt attempt-one", "2. 2026-09-13", "attempt attempt-two",
		"Runner-observed:", "Model-reported:", approval.DelegatedContentDigest, "Exact approved request", "The first implementation needs repair.",
		"review finding [acceptance]", "review outcome: needs_changes", "verification evidence candidate: commit " + firstCandidate.CommitOID,
		"later rebased candidate: commit " + strings.Repeat("e", 40), "later published candidate: commit " + strings.Repeat("f", 40),
		"pull request: https://github.com/owner/repo/pull/8 · number 8", "merge: commit " + strings.Repeat("3", 40),
		"usage: unavailable; not zero", "History warning: ignored 1 malformed record(s)", "untrusted read-only evidence",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("human history omitted %q:\n%s", expected, rendered)
		}
	}
	if strings.Index(rendered, "attempt attempt-one") > strings.Index(rendered, "attempt attempt-two") {
		t.Fatalf("attempts were not chronological:\n%s", rendered)
	}
	for _, forbidden := range []string{"FOREIGN_ITEM_EVIDENCE", "DO_NOT_LEAK_PROMPT", "DO_NOT_LEAK_TRANSCRIPT", "DO_NOT_LEAK_CREDENTIAL"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("human history disclosed %q:\n%s", forbidden, rendered)
		}
	}

	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(view); err != nil {
		t.Fatal(err)
	}
	var decoded metricsOutput
	if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil {
		t.Fatalf("decode JSON history: %v\n%s", err, encoded.String())
	}
	if len(decoded.History) != 2 || decoded.History[0].AttemptID != "attempt-one" || decoded.History[1].AttemptID != "attempt-two" {
		t.Fatalf("JSON attempts were mixed or misordered: %#v", decoded.History)
	}
	first, second := decoded.History[0], decoded.History[1]
	if first.RunnerObserved.ApprovedRequest == nil || first.RunnerObserved.ApprovedRequest.Snapshot != snapshot ||
		first.ModelReported.Rationale != "The first implementation needs repair." || first.ModelReported.ReviewVerdict != "needs_changes" || first.ModelReported.Usage != nil ||
		!strings.Contains(strings.Join(first.Unavailable, ","), "model_reported.usage") {
		t.Fatalf("JSON lost first-attempt identity or provenance: %#v", first)
	}
	if second.RunnerObserved.Lineage == nil || second.RunnerObserved.Lineage.EvidenceCandidate != secondCandidate ||
		second.RunnerObserved.Lineage.PublishedCandidate.CommitOID != strings.Repeat("f", 40) || second.ModelReported.Usage == nil || second.ModelReported.Usage.InputTokens != 9 {
		t.Fatalf("JSON lost second-attempt lineage or report: %#v", second)
	}
	for _, forbidden := range []string{"FOREIGN_ITEM_EVIDENCE", "DO_NOT_LEAK_PROMPT", "DO_NOT_LEAK_TRANSCRIPT", "DO_NOT_LEAK_CREDENTIAL"} {
		if strings.Contains(encoded.String(), forbidden) {
			t.Fatalf("JSON history disclosed %q:\n%s", forbidden, encoded.String())
		}
	}
	after, err := json.Marshal(attempts)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("read-only history view changed retained state: error=%v", err)
	}
}

func TestMetricHistoryShowsIncompleteAndLegacyFactsHonestly(t *testing.T) {
	incomplete := false
	attempt := runnermetrics.Attempt{Completed: true, Event: runnermetrics.Event{
		AttemptID: "incomplete", Summary: "Older summary with unknown authorship", ModelReportComplete: &incomplete,
		CandidateOID: strings.Repeat("a", 40), Lineage: &runnermetrics.ObservedLineage{Candidate: runnermetrics.ObjectIdentity{CommitOID: strings.Repeat("a", 40)}},
	}}
	history := metricAttemptHistoryFor(attempt)
	unavailable := strings.Join(history.Unavailable, ",")
	for _, expected := range []string{
		"provenance_unavailable.legacy_summary.source", "runner_observed.started_at", "runner_observed.finished_at",
		"runner_observed.approved_request", "runner_observed.lineage.evidence_candidate.commit_oid",
		"model_reported.rationale", "model_reported.actions", "model_reported.verification", "model_reported.usage",
	} {
		if !strings.Contains(unavailable, expected) {
			t.Fatalf("incomplete JSON projection omitted %q: %#v", expected, history)
		}
	}
	if history.ProvenanceUnavailable == nil || history.ProvenanceUnavailable.Summary != attempt.Summary || history.ModelReported.Usage != nil {
		t.Fatalf("legacy or unavailable fact was misrepresented: %#v", history)
	}
	var output bytes.Buffer
	writeMetrics(&output, metricsOutput{RunnerID: "runner", Summary: runnermetrics.Summarize([]runnermetrics.Attempt{attempt}), Attempts: []runnermetrics.Attempt{attempt}})
	for _, expected := range []string{
		"unavailable · attempt incomplete", "approval digest: unavailable", "verification evidence candidate: commit unavailable",
		"completeness: incomplete", "usage: unavailable; not zero", "Provenance unavailable (legacy summary): Older summary with unknown authorship",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("incomplete human history omitted %q:\n%s", expected, output.String())
		}
	}
}
