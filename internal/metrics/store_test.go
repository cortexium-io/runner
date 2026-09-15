package metrics

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStoreValidatesReviewVerdictsOnAppendAndRead(t *testing.T) {
	for _, test := range []struct {
		name, kind, verdict string
		valid               bool
	}{
		{"unavailable", EventCompleted, "", true},
		{"accepted", EventCompleted, "accept", true},
		{"rejected", EventCompleted, "needs_changes", true},
		{"incomplete", EventCompleted, "blocked", true},
		{"free text", EventCompleted, "private diagnostic", false},
		{"premature", EventStarted, "accept", false},
		{"stage verdict", EventStageCompleted, "accept", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(privateMetricsPath(t))
			event := Event{Version: EventVersion, Kind: test.kind, AttemptID: "review", Outcome: "blocked", ReviewVerdict: test.verdict}
			if test.kind == EventStageCompleted {
				event.StageID, event.Stage = "stage", StageReviewerAudit
			}
			err := store.Append(event)
			if (err == nil) != test.valid {
				t.Fatalf("append validity=%t, want %t: %v", err == nil, test.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), test.verdict) {
				t.Fatal("invalid verdict leaked into the diagnostic")
			}
			// Also check records written outside Append, including invalid ones.
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.Path(), append(encoded, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			history, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if test.valid {
				if history.MalformedRecords != 0 || len(history.Attempts) != 1 || history.Attempts[0].ReviewVerdict != test.verdict {
					t.Fatalf("validated verdict not retained: %#v", history)
				}
			} else if history.MalformedRecords != 1 || len(history.Attempts) != 0 {
				t.Fatalf("invalid verdict admitted on read: %#v", history)
			}
		})
	}
}

func TestStoreFoldsDurableAttemptEventsAndIgnoresMalformedRecords(t *testing.T) {
	path := privateMetricsPath(t)
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
	finish.PublicationAttempts = 2
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
	if history.MalformedRecords != 1 || len(history.Attempts) != 1 || !history.Attempts[0].Completed || history.Attempts[0].PublicationAttempts != 2 {
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
	store := NewStore(privateMetricsPath(t))
	err := store.Append(Event{Kind: EventStageStarted, AttemptID: "att_1", Stage: StageHarnessRun})
	if err == nil || !strings.Contains(err.Error(), "stage_id") {
		t.Fatalf("invalid stage event was accepted: %v", err)
	}
}

func TestStoreRetainsIncompleteReviewClassification(t *testing.T) {
	store := NewStore(privateMetricsPath(t))
	event := Event{
		Kind: EventCompleted, AttemptID: "incomplete_review", Role: "reviewer", Outcome: "needs_input",
		FailureClass: "review_incomplete", RetryDisposition: "manual",
		Summary:      "Review checks: 8 passed, 0 failed, 1 blocked.",
		Verification: []string{"Historical test names were missing; current verification remained inconclusive."},
	}
	if err := store.Append(event); err != nil {
		t.Fatal(err)
	}
	history, err := store.Read()
	if err != nil || history.MalformedRecords != 0 || len(history.Attempts) != 1 {
		t.Fatalf("incomplete review was lost from metrics: %#v, %v", history, err)
	}
	got := history.Attempts[0]
	if got.FailureClass != event.FailureClass || got.RetryDisposition != "manual" || len(got.Verification) != 1 || got.Verification[0] != event.Verification[0] {
		t.Fatalf("incomplete review changed in durable metrics: %#v", got)
	}
}

func TestStoreRetainsManualRecoveryClassifications(t *testing.T) {
	for _, class := range []string{"needs_input", "agent_blocked", "integrity_unverified"} {
		t.Run(class, func(t *testing.T) {
			store := NewStore(privateMetricsPath(t))
			event := Event{Kind: EventCompleted, AttemptID: "paused", Outcome: "blocked", FailureClass: class, RetryDisposition: "manual"}
			if err := store.Append(event); err != nil {
				t.Fatal(err)
			}
			history, err := store.Read()
			if err != nil || history.MalformedRecords != 0 || len(history.Attempts) != 1 || !history.Attempts[0].Completed ||
				history.Attempts[0].FailureClass != class || history.Attempts[0].RetryDisposition != "manual" {
				t.Fatalf("manual recovery was lost from durable metrics: %#v %v", history, err)
			}
		})
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
	store := NewStore(privateMetricsPath(t))
	for _, event := range []Event{
		{Kind: EventStageStarted, AttemptID: "attempt", StageID: "stage", Stage: "prompt=secret"},
		{Kind: EventStageCompleted, AttemptID: "attempt", StageID: "stage", Stage: StageHarnessRun, Outcome: "raw error"},
		{Kind: EventCompleted, AttemptID: "attempt", FailureClass: "token=secret"},
		{Kind: EventCompleted, AttemptID: "attempt", FailureOperation: "token=secret"},
	} {
		if err := store.Append(event); err == nil {
			t.Fatalf("unsafe event was accepted: %#v", event)
		}
	}
}

func TestStoreRejectsInvalidNumericMetrics(t *testing.T) {
	store := NewStore(privateMetricsPath(t))
	for _, event := range []Event{
		{Kind: EventCompleted, AttemptID: "negative_duration", DurationMilliseconds: -1},
		{Kind: EventCompleted, AttemptID: "too_many_publication_attempts", PublicationAttempts: 4},
		{Kind: EventCompleted, AttemptID: "negative_tokens", Usage: Usage{Available: true, InputTokens: -1}},
		{Kind: EventCompleted, AttemptID: "negative_cost", Usage: Usage{ReportedCostUSD: floatPtr(-0.01)}},
	} {
		if err := store.Append(event); err == nil {
			t.Fatalf("invalid numeric metrics were accepted: %#v", event)
		}
	}
}

func TestStoreTreatsInvalidNumericHistoryAsMalformed(t *testing.T) {
	path := privateMetricsPath(t)
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
	store := NewStore(privateMetricsPath(t))
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

func privateMetricsPath(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "metrics.jsonl")
}

func TestStoreRejectsMalformedRunIdentityOnWriteAndRead(t *testing.T) {
	for name, identity := range map[string]*RunContext{
		"missing versions":  {ConfigDigest: "sha256:" + strings.Repeat("a", 64)},
		"raw configuration": {RunnerVersion: "dev", BundledSkillsVersion: "1.8.13", ConfigDigest: "private config contents"},
		"oversized version": {RunnerVersion: strings.Repeat("x", 129), BundledSkillsVersion: "1.8.13", ConfigDigest: "sha256:" + strings.Repeat("a", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			store := NewStore(privateMetricsPath(t))
			event := Event{Version: EventVersion, Kind: EventCompleted, AttemptID: "invalid", RunContext: identity}
			if err := store.Append(event); err == nil {
				t.Fatal("accepted invalid run identity")
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
				t.Fatalf("read accepted invalid identity: %#v error=%v", history, err)
			}
		})
	}
}

func floatPtr(value float64) *float64 { return &value }

func approvedRequestFixture(body string) *ApprovedRequest {
	snapshot, _ := json.Marshal(struct {
		Version string `json:"version"`
		Body    string `json:"body"`
	}{Version: "v1", Body: body})
	digest := sha256.Sum256(snapshot)
	return NewApprovedRequest("v1:"+hex.EncodeToString(digest[:]), string(snapshot))
}

func TestStoreRetainsImmutableAttemptEvidenceAcrossReplacementCleanupAndRestart(t *testing.T) {
	path := privateMetricsPath(t)
	store := NewStore(path)
	approval := approvedRequestFixture("Exact approved body")
	firstCandidate := ObjectIdentity{CommitOID: strings.Repeat("b", 40), TreeOID: strings.Repeat("c", 40)}
	secondCandidate := ObjectIdentity{CommitOID: strings.Repeat("d", 40), TreeOID: strings.Repeat("e", 40)}
	started := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	first := Event{
		Kind: EventCompleted, AttemptID: "attempt-rejected", ItemID: "PVTI_history", ItemTitle: "Retain history", Role: "reviewer", Harness: "codex",
		StartedAt: started, FinishedAt: started.Add(time.Minute), Outcome: "rejected", ReviewVerdict: "needs_changes",
		Summary: "Repair the mismatch.", WorkDone: []string{"Reviewed the first candidate."}, Verification: []string{"Focused check failed."},
		ReviewFindings:  []ReviewFinding{{Area: "acceptance", Summary: "Candidate mismatched the approved identity."}},
		ApprovedRequest: approval, CandidateOID: firstCandidate.CommitOID,
		Lineage: &ObservedLineage{Repository: "owner/repo", Branch: "runner/history", Base: ObjectIdentity{CommitOID: strings.Repeat("1", 40)}, Candidate: firstCandidate, EvidenceCandidate: firstCandidate, ReviewedCandidate: firstCandidate},
	}
	second := Event{
		Kind: EventCompleted, AttemptID: "attempt-repair", ItemID: first.ItemID, ItemTitle: first.ItemTitle, Role: "reviewer", Harness: "codex",
		StartedAt: started.Add(time.Hour), FinishedAt: started.Add(time.Hour + time.Minute), Outcome: "succeeded", ReviewVerdict: "accept",
		Summary: "Repair accepted and published.", WorkDone: []string{"Reviewed the repaired candidate."}, Verification: []string{"Focused repair and adjacent regression checks passed."},
		ApprovedRequest: approval, CandidateOID: secondCandidate.CommitOID,
		Lineage: &ObservedLineage{Repository: "owner/repo", Branch: "runner/history", Base: ObjectIdentity{CommitOID: strings.Repeat("2", 40)}, Candidate: secondCandidate, EvidenceCandidate: secondCandidate, ReviewedCandidate: secondCandidate, PublishedCandidate: ObjectIdentity{CommitOID: strings.Repeat("f", 40), TreeOID: secondCandidate.TreeOID}, PullRequestURL: "https://github.com/owner/repo/pull/8", PullRequestNumber: 8, Merge: ObjectIdentity{CommitOID: strings.Repeat("3", 40)}},
	}
	for _, event := range []Event{first, second} {
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	replacement := first
	replacement.Outcome = "succeeded"
	replacement.Summary = "forged replacement"
	if err := store.Append(replacement); err != nil {
		t.Fatal(err)
	}

	history, err := NewStore(path).Read()
	if err != nil {
		t.Fatal(err)
	}
	if history.MalformedRecords != 1 || len(history.Attempts) != 2 {
		t.Fatalf("immutable attempts were lost after restart: %#v", history)
	}
	if history.Attempts[0].AttemptID != second.AttemptID || history.Attempts[1].AttemptID != first.AttemptID || history.Attempts[1].Outcome != "rejected" {
		t.Fatalf("attempt replacement changed retained history: %#v", history.Attempts)
	}
	if history.Attempts[0].ApprovedRequest == nil || *history.Attempts[0].ApprovedRequest != *approval || history.Attempts[0].Lineage.PublishedCandidate.CommitOID != second.Lineage.PublishedCandidate.CommitOID {
		t.Fatalf("approval or lineage was not retained: %#v", history.Attempts[0])
	}
	if !slices.Equal(history.Attempts[1].Verification, first.Verification) || history.Attempts[1].ReviewFindings[0] != first.ReviewFindings[0] {
		t.Fatalf("replaced operational evidence was not preserved: %#v", history.Attempts[1])
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("history is not owner-only: info=%v err=%v", info, err)
	}
}

func TestStoreRefusesMismatchedMalformedOversizedAndSubstitutedHistoryWithoutCorruption(t *testing.T) {
	path := privateMetricsPath(t)
	store := NewStore(path)
	candidate := ObjectIdentity{CommitOID: strings.Repeat("a", 40), TreeOID: strings.Repeat("b", 40)}
	valid := Event{Version: EventVersion, Kind: EventCompleted, AttemptID: "valid", Outcome: "succeeded", CandidateOID: candidate.CommitOID,
		ApprovedRequest: approvedRequestFixture("approved"),
		Lineage:         &ObservedLineage{Repository: "owner/repo", Candidate: candidate, EvidenceCandidate: candidate}}
	if err := store.Append(valid); err != nil {
		t.Fatal(err)
	}
	changed := valid
	changed.AttemptID = "changed"
	changed.Lineage = &ObservedLineage{Repository: "owner/repo", Candidate: ObjectIdentity{CommitOID: strings.Repeat("d", 40), TreeOID: strings.Repeat("e", 40)}, EvidenceCandidate: candidate}
	changed.CandidateOID = changed.Lineage.Candidate.CommitOID
	if err := store.Append(changed); err == nil {
		t.Fatal("changed candidate inherited a prior evidence binding")
	}
	excessive := valid
	excessive.AttemptID = "excessive"
	excessive.Verification = make([]string, maxEvidenceEntries+1)
	for index := range excessive.Verification {
		excessive.Verification[index] = "bounded"
	}
	if err := store.Append(excessive); err == nil {
		t.Fatal("excessive evidence was accepted")
	}
	broken := valid
	broken.AttemptID = "broken"
	broken.ApprovedRequest = NewApprovedRequest("v1:broken", "unresolvable")
	encoded, _ := json.Marshal(broken)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write(append(encoded, '\n'))
	_, _ = file.Write(append(bytes.Repeat([]byte{'x'}, maxEventBytes+1), '\n'))
	_ = file.Close()
	history, err := store.Read()
	if err != nil || len(history.Attempts) != 1 || history.MalformedRecords != 2 || history.Attempts[0].AttemptID != valid.AttemptID {
		t.Fatalf("invalid input corrupted prior history: %#v %v", history, err)
	}

	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(valid); err == nil {
		t.Fatal("substituted history leaf was accepted")
	}
	content, _ := os.ReadFile(external)
	if string(content) != "unchanged" {
		t.Fatalf("substitution target was modified: %q", content)
	}
}

func TestRetainedAttemptEvidenceValidationSeparatesClaimsAndObservedIdentity(t *testing.T) {
	candidate := ObjectIdentity{CommitOID: strings.Repeat("a", 40), TreeOID: strings.Repeat("b", 40)}
	event := Event{
		Kind: EventCompleted, AttemptID: "validated", CandidateOID: candidate.CommitOID,
		Summary: "Runner classification", ModelReportedSummary: "Model rationale", WorkDone: []string{"Model action"}, Verification: []string{"Model verification claim"},
		Usage:           Usage{Available: false},
		ReviewDetails:   []ReviewDetail{{Area: "acceptance", Name: "proof", Status: "passed", Summary: "Model review", Evidence: []string{"reported evidence"}}},
		ApprovedRequest: approvedRequestFixture("approved"),
		Lineage:         &ObservedLineage{Repository: "owner/repo", Candidate: candidate, EvidenceCandidate: candidate},
	}
	if !validRetainedAttemptEvidence(event) {
		t.Fatal("valid bounded provenance was rejected")
	}
	started := Event{Kind: EventStarted, AttemptID: "unfinished", ApprovedRequest: event.ApprovedRequest}
	if !validRetainedAttemptEvidence(started) {
		t.Fatal("unfinished attempt lost its exact approved request identity")
	}
	changed := event
	changed.Lineage = &ObservedLineage{Repository: "owner/repo", Candidate: ObjectIdentity{CommitOID: strings.Repeat("d", 40), TreeOID: strings.Repeat("e", 40)}, EvidenceCandidate: candidate}
	changed.CandidateOID = changed.Lineage.Candidate.CommitOID
	if validRetainedAttemptEvidence(changed) {
		t.Fatal("prior candidate receipt certified a changed candidate")
	}
	broken := event
	broken.ApprovedRequest = NewApprovedRequest("v1:missing", "approved")
	if validRetainedAttemptEvidence(broken) {
		t.Fatal("broken approval digest was presented as inspectable")
	}
	tamperedBody := event
	tamperedBody.ApprovedRequest = NewApprovedRequest(event.ApprovedRequest.DelegatedContentDigest, `{"version":"v1","body":"different content"}`)
	if validRetainedAttemptEvidence(tamperedBody) {
		t.Fatal("approved body no longer matched its retained content digest")
	}
	oversized := event
	oversized.Verification = []string{strings.Repeat("x", maxEvidenceTextBytes+1)}
	if validRetainedAttemptEvidence(oversized) {
		t.Fatal("oversized model report was retained")
	}
	stage := event
	stage.Kind = EventStageCompleted
	if validRetainedAttemptEvidence(stage) {
		t.Fatal("attempt-only private evidence leaked into a stage record")
	}
}

func TestStoreRejectsCrossItemAttemptAndStageJoins(t *testing.T) {
	store := NewStore(privateMetricsPath(t))
	base := Event{Kind: EventStarted, AttemptID: "shared", RunnerID: "runner", ProjectOwner: "owner", ProjectNumber: 8,
		Repository: "owner/repo", ItemID: "item-a", ItemTitle: "A", Role: "implementer", Harness: "codex", StartedAt: time.Now().UTC()}
	if err := store.Append(base); err != nil {
		t.Fatal(err)
	}
	foreignStage := base
	foreignStage.Kind, foreignStage.StageID, foreignStage.Stage = EventStageStarted, "foreign", StageHarnessRun
	foreignStage.ItemID, foreignStage.ItemTitle = "item-b", "B"
	if err := store.Append(foreignStage); err != nil {
		t.Fatal(err)
	}
	validStage := base
	validStage.Kind, validStage.StageID, validStage.Stage = EventStageStarted, "valid", StageHarnessRun
	if err := store.Append(validStage); err != nil {
		t.Fatal(err)
	}
	completed := base
	completed.Kind, completed.Outcome = EventCompleted, "succeeded"
	if err := store.Append(completed); err != nil {
		t.Fatal(err)
	}
	history, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if history.MalformedRecords != 1 || len(history.Attempts) != 1 || history.Attempts[0].ItemID != "item-a" || len(history.Attempts[0].Stages) != 1 || history.Attempts[0].Stages[0].StageID != "valid" {
		t.Fatalf("cross-item records mixed into one attempt: %#v", history)
	}
}

func TestStoreRejectsApprovalReplacementWithinAttempt(t *testing.T) {
	store := NewStore(privateMetricsPath(t))
	started := Event{Kind: EventStarted, AttemptID: "approval", ItemID: "item", ApprovedRequest: approvedRequestFixture("first")}
	if err := store.Append(started); err != nil {
		t.Fatal(err)
	}
	completed := started
	completed.Kind, completed.Outcome = EventCompleted, "succeeded"
	completed.ApprovedRequest = approvedRequestFixture("second")
	if err := store.Append(completed); err != nil {
		t.Fatal(err)
	}
	history, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if history.MalformedRecords != 1 || len(history.Attempts) != 1 || history.Attempts[0].Completed || *history.Attempts[0].ApprovedRequest != *started.ApprovedRequest {
		t.Fatalf("approval replacement changed immutable attempt identity: %#v", history)
	}
}

func TestStoreReadsWhileAnotherStoreAppends(t *testing.T) {
	path := privateMetricsPath(t)
	writer, reader := NewStore(path), NewStore(path)
	work := make([]string, 32)
	for index := range work {
		work[index] = strings.Repeat("x", 8*1024)
	}
	if err := writer.Append(Event{Kind: EventCompleted, AttemptID: "initial", WorkDone: work}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-start
		for index := range 128 {
			if err := writer.Append(Event{Kind: EventCompleted, AttemptID: strconv.Itoa(index)}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	close(start)
	var readErr error
	for range 128 {
		if _, readErr = reader.Read(); readErr != nil {
			break
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatalf("ordinary concurrent append broke a history read: %v", readErr)
	}
	history, err := reader.Read()
	if err != nil || history.MalformedRecords != 0 || len(history.Attempts) != 129 {
		t.Fatalf("concurrent history was incomplete: attempts=%d malformed=%d err=%v", len(history.Attempts), history.MalformedRecords, err)
	}
}
