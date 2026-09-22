package execution

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func observedVerificationFixture() (VerificationReceipt, VerificationTarget) {
	digest := strings.Repeat("a", 64)
	wait, run, cleanup := int64(100), int64(200), int64(10)
	inputs := VerificationInputs{Selection: VerificationInputSelection{Policy: "trusted-entrypoint/full-candidate-v1", Paths: []string{"."}}, Requirements: digest, Executable: digest, Dependencies: digest, Configuration: digest, Environment: digest, Base: digest}
	r := VerificationReceipt{
		Version: 1, ExecutionID: "check-1", AttemptID: "attempt-1", Repository: "owner/repo", PlanID: "plan-1", PlanRevision: "revision-1",
		StartedAt: time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC), FinishedAt: time.Date(2026, 9, 22, 9, 0, 1, 0, time.UTC),
		SourceCommitOID: strings.Repeat("b", 40), SourceTreeOID: strings.Repeat("c", 40), SourceBaseOID: strings.Repeat("d", 40),
		Entrypoint: "complete", Command: []string{"npm", "run", "validate:complete"}, SettingsDigest: digest, Inputs: inputs,
		Boundary: VerificationComplete, Outcome: "passed", ReportDigest: digest, CleanupResolved: true, WaitMilliseconds: &wait, RunMilliseconds: &run, CleanupMilliseconds: &cleanup,
	}
	runStarted, runFinished := r.StartedAt.Add(100*time.Millisecond), r.StartedAt.Add(300*time.Millisecond)
	r.RunStartedAt, r.RunFinishedAt = &runStarted, &runFinished
	target := VerificationTarget{Repository: r.Repository, PlanID: r.PlanID, CandidateOID: r.SourceCommitOID, Entrypoint: r.Entrypoint, SettingsDigest: digest, Inputs: inputs, Boundary: VerificationComplete}
	return r, target
}

func TestVerificationReceiptReusesChecksWithoutRewritingHistory(t *testing.T) {
	r, target := observedVerificationFixture()
	pinned, err := r.Digest()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(r)
	// A new receipt/screenshot commit is a different full candidate, but it is
	// not an executable change when independent input assessment says so.
	target.CandidateOID = strings.Repeat("e", 40)
	result, err := AssessVerificationReceipt(r, pinned, target)
	if err != nil || !result.Applicable || !result.Historical || result.ExecutionID != r.ExecutionID || result.SourceCommitOID != r.SourceCommitOID {
		t.Fatalf("lost applicable history: %#v, %v", result, err)
	}
	after, _ := json.Marshal(r)
	if string(before) != string(after) {
		t.Fatal("applicability rewrote original execution")
	}
	target.RequireCurrentCandidate = true
	result, err = AssessVerificationReceipt(r, pinned, target)
	if err != nil || result.Applicable || result.Reason != "policy requires a current-candidate check" {
		t.Fatalf("waived current candidate gate: %#v, %v", result, err)
	}
}

func TestVerificationReceiptUnknownPhaseTimingIsNotZero(t *testing.T) {
	r, _ := observedVerificationFixture()
	r.CleanupMilliseconds = nil
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "cleanup_ms") {
		t.Fatal("unknown cleanup timing became a measurement")
	}
	zero := int64(0)
	r.WaitMilliseconds = &zero
	encoded, _ = json.Marshal(r)
	if !strings.Contains(string(encoded), `"wait_ms":0`) {
		t.Fatal("observed zero wait was lost")
	}
}

func TestVerificationReceiptRefusesChangedOrUnavailableInputs(t *testing.T) {
	changed := strings.Repeat("f", 64)
	for _, tc := range []struct {
		name   string
		change func(*VerificationTarget)
	}{
		{"requirements", func(v *VerificationTarget) { v.Inputs.Requirements = changed }},
		{"executable", func(v *VerificationTarget) { v.Inputs.Executable = changed }},
		{"dependencies", func(v *VerificationTarget) { v.Inputs.Dependencies = changed }},
		{"configuration", func(v *VerificationTarget) { v.Inputs.Configuration = changed }},
		{"environment", func(v *VerificationTarget) { v.Inputs.Environment = changed }},
		{"base assessment", func(v *VerificationTarget) { v.Inputs.Base = changed }},
		{"settings", func(v *VerificationTarget) { v.SettingsDigest = changed }},
		{"missing bindings", func(v *VerificationTarget) { v.Inputs.Environment = "" }},
		{"other repository", func(v *VerificationTarget) { v.Repository = "other/repo" }},
		{"other plan", func(v *VerificationTarget) { v.PlanID = "plan-2" }},
		{"different command selection", func(v *VerificationTarget) { v.Entrypoint = "focused" }},
		{"different boundary", func(v *VerificationTarget) { v.Boundary = VerificationFocused }},
		{"input selection changed", func(v *VerificationTarget) { v.Inputs.Selection.Paths = []string{"src"} }},
		{"missing input selection", func(v *VerificationTarget) { v.Inputs.Selection.Policy = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, target := observedVerificationFixture()
			pinned, err := r.Digest()
			if err != nil {
				t.Fatal(err)
			}
			tc.change(&target)
			result, err := AssessVerificationReceipt(r, pinned, target)
			if err != nil || result.Applicable || !result.Historical || result.Reason == "" {
				t.Fatalf("stale proof accepted: %#v,%v", result, err)
			}
		})
	}
}

func TestVerificationReceiptRejectsTamperingAndIncompleteProvenance(t *testing.T) {
	for _, change := range []func(*VerificationReceipt){
		func(r *VerificationReceipt) { r.Outcome = "failed" },
		func(r *VerificationReceipt) { r.Command = []string{"true"} },
		func(r *VerificationReceipt) { r.ReportDigest = strings.Repeat("f", 64) },
		func(r *VerificationReceipt) { r.AttemptID = "other-attempt" },
		func(r *VerificationReceipt) { r.SourceCommitOID = strings.Repeat("e", 40) },
		func(r *VerificationReceipt) { r.StartedAt = r.StartedAt.Add(time.Millisecond) },
		func(r *VerificationReceipt) { r.Inputs.Selection.Paths = []string{"src"} },
	} {
		r, target := observedVerificationFixture()
		pinned, err := r.Digest()
		if err != nil {
			t.Fatal(err)
		}
		change(&r)
		if _, err := AssessVerificationReceipt(r, pinned, target); err == nil {
			t.Fatal("tampered receipt accepted")
		}
	}
	r, target := observedVerificationFixture()
	pinned, _ := r.Digest()
	if _, err := AssessVerificationReceipt(r, "", target); err == nil {
		t.Fatal("missing protected digest accepted")
	}
	r.Inputs.Environment = ""
	if _, err := AssessVerificationReceipt(r, pinned, target); err == nil {
		t.Fatal("missing original environment accepted")
	}
}

func TestFailedOrUncleanExecutionNeverBecomesPassingEvidence(t *testing.T) {
	for _, outcome := range []string{"failed", "timeout", "canceled", "cleanup_unresolved", "passed"} {
		t.Run(outcome, func(t *testing.T) {
			r, target := observedVerificationFixture()
			r.Outcome = outcome
			if outcome == "passed" {
				r.CleanupResolved = false
			}
			pinned, err := r.Digest()
			if err != nil {
				t.Fatal(err)
			}
			result, err := AssessVerificationReceipt(r, pinned, target)
			if err != nil || result.Applicable || r.Outcome != outcome {
				t.Fatalf("failure history lost or accepted: %#v,%v", result, err)
			}
		})
	}
}

func TestVerificationReceiptRequiresObservedIntervalAndSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*VerificationReceipt)
	}{
		{"missing start", func(r *VerificationReceipt) { r.StartedAt = time.Time{} }},
		{"missing finish", func(r *VerificationReceipt) { r.FinishedAt = time.Time{} }},
		{"missing run endpoint", func(r *VerificationReceipt) { r.RunStartedAt = nil }},
		{"run outside invocation", func(r *VerificationReceipt) { end := r.FinishedAt.Add(time.Second); r.RunFinishedAt = &end }},
		{"reversed interval", func(r *VerificationReceipt) { r.FinishedAt = r.StartedAt.Add(-time.Second) }},
		{"negative duration", func(r *VerificationReceipt) { negative := int64(-1); r.RunMilliseconds = &negative }},
		{"missing selection", func(r *VerificationReceipt) { r.Inputs.Selection.Paths = nil }},
		{"escaping selection", func(r *VerificationReceipt) { r.Inputs.Selection.Paths = []string{"../other"} }},
		{"duplicate selection", func(r *VerificationReceipt) { r.Inputs.Selection.Paths = []string{"src", "src"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := observedVerificationFixture()
			tc.change(&r)
			if _, err := r.Digest(); err == nil {
				t.Fatal("unobserved or invalid provenance accepted")
			}
		})
	}
}
