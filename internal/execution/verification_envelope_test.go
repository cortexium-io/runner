package execution

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func observedEnvelopeFixture() (VerificationEnvelope, VerificationEnvelopeTarget) {
	heavy, target := observedVerificationFixture()
	zero := 0
	guard := &VerificationCurrentCandidateCheckReceipt{
		ExecutionID: "current-2", AttemptID: "attempt-2", Repository: heavy.Repository,
		PlanID: heavy.PlanID, PlanRevision: heavy.PlanRevision, Entrypoint: heavy.Entrypoint,
		Boundary: heavy.Boundary, SourceCommitOID: strings.Repeat("e", 40), SourceTreeOID: strings.Repeat("f", 40),
		SourceBaseOID: heavy.SourceBaseOID, CandidateIntegrity: "current-candidate-integrity", SettingsDigest: heavy.SettingsDigest,
		Command: []string{"npm", "run", "validate:current"}, StartedAt: heavy.StartedAt, FinishedAt: heavy.FinishedAt,
		RunStartedAt: heavy.RunStartedAt, RunFinishedAt: heavy.RunFinishedAt, RunMilliseconds: heavy.RunMilliseconds,
		CleanupMilliseconds: heavy.CleanupMilliseconds, Outcome: "passed", ExitCode: &zero,
		ReportDigest: heavy.ReportDigest, CleanupResolved: true,
	}
	target.CandidateOID = guard.SourceCommitOID
	return VerificationEnvelope{Version: 1, Heavy: &heavy, CurrentCandidateCheck: guard}, VerificationEnvelopeTarget{
		VerificationTarget: target, PlanRevision: guard.PlanRevision, TreeOID: guard.SourceTreeOID,
		BaseOID: guard.SourceBaseOID, CandidateIntegrity: guard.CandidateIntegrity, CurrentCandidateCommand: guard.Command,
	}
}

func TestVerificationEnvelopeKeepsTwoDistinctExecutionIdentities(t *testing.T) {
	envelope, target := observedEnvelopeFixture()
	original, _ := json.Marshal(envelope)
	digest, err := envelope.Digest()
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := AssessVerificationEnvelope(envelope, digest, target)
	if err != nil || !assessment.Applicable || !assessment.Historical || assessment.SourceCommitOID != envelope.Heavy.SourceCommitOID {
		t.Fatalf("two-source evidence: %+v %v", assessment, err)
	}
	after, _ := json.Marshal(envelope)
	if string(original) != string(after) {
		t.Fatal("assessment rewrote history")
	}
	// Authenticated extraction preserves original heavy metadata, not guard time
	// or current candidate. A forged hash supplied alongside JSON is not enough.
	if err := AuthenticateVerificationEnvelope(envelope, digest); err != nil {
		t.Fatal(err)
	}
	heavy := *envelope.Heavy
	heavyDigest, _ := heavy.Digest()
	if _, err := AssessVerificationReceipt(heavy, heavyDigest, target.VerificationTarget); err != nil || !reflect.DeepEqual(heavy, *envelope.Heavy) {
		t.Fatal("original heavy extraction changed proof")
	}
	for _, mutate := range []func(*VerificationEnvelope){
		func(e *VerificationEnvelope) { e.Heavy.ReportDigest = strings.Repeat("b", 64) },
		func(e *VerificationEnvelope) { e.CurrentCandidateCheck.ExecutionID = "forged" },
		func(e *VerificationEnvelope) { e.CurrentCandidateCheck = nil },
	} {
		copy, _ := observedEnvelopeFixture()
		mutate(&copy)
		if err := AuthenticateVerificationEnvelope(copy, digest); err == nil {
			t.Fatal("tampered pair authenticated")
		}
	}
}

func TestVerificationEnvelopeRequiresBothExactCurrentGuardAndApplicableHeavy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*VerificationEnvelope, *VerificationEnvelopeTarget)
	}{
		{"missing heavy", func(e *VerificationEnvelope, _ *VerificationEnvelopeTarget) { e.Heavy = nil }},
		{"missing guard", func(e *VerificationEnvelope, _ *VerificationEnvelopeTarget) { e.CurrentCandidateCheck = nil }},
		{"failed guard", func(e *VerificationEnvelope, _ *VerificationEnvelopeTarget) {
			e.CurrentCandidateCheck.Outcome = "failed"
			code := 7
			e.CurrentCandidateCheck.ExitCode = &code
		}},
		{"failed heavy", func(e *VerificationEnvelope, _ *VerificationEnvelopeTarget) { e.Heavy.Outcome = "failed" }},
		{"candidate", func(_ *VerificationEnvelope, v *VerificationEnvelopeTarget) { v.CandidateOID = strings.Repeat("1", 40) }},
		{"tree", func(_ *VerificationEnvelope, v *VerificationEnvelopeTarget) { v.TreeOID = strings.Repeat("1", 40) }},
		{"base", func(_ *VerificationEnvelope, v *VerificationEnvelopeTarget) { v.BaseOID = strings.Repeat("1", 40) }},
		{"integrity", func(_ *VerificationEnvelope, v *VerificationEnvelopeTarget) { v.CandidateIntegrity = "different" }},
		{"missing integrity", func(_ *VerificationEnvelope, v *VerificationEnvelopeTarget) { v.CandidateIntegrity = "" }},
		{"revision", func(_ *VerificationEnvelope, v *VerificationEnvelopeTarget) { v.PlanRevision = "amended" }},
		{"repository", func(e *VerificationEnvelope, _ *VerificationEnvelopeTarget) {
			e.CurrentCandidateCheck.Repository = "other/repository"
		}},
		{"plan", func(e *VerificationEnvelope, _ *VerificationEnvelopeTarget) {
			e.CurrentCandidateCheck.PlanID = "other-plan"
		}},
		{"entrypoint", func(e *VerificationEnvelope, _ *VerificationEnvelopeTarget) {
			e.CurrentCandidateCheck.Entrypoint = "other"
		}},
		{"settings", func(e *VerificationEnvelope, _ *VerificationEnvelopeTarget) {
			e.CurrentCandidateCheck.SettingsDigest = strings.Repeat("1", 64)
		}},
		{"command", func(_ *VerificationEnvelope, v *VerificationEnvelopeTarget) {
			v.CurrentCandidateCommand = []string{"true"}
		}},
		{"disabled policy", func(_ *VerificationEnvelope, v *VerificationEnvelopeTarget) { v.CurrentCandidateCommand = nil }},
		{"changed executable", func(_ *VerificationEnvelope, v *VerificationEnvelopeTarget) {
			v.Inputs.Executable = strings.Repeat("1", 64)
		}},
		{"exact heavy policy", func(_ *VerificationEnvelope, v *VerificationEnvelopeTarget) { v.RequireCurrentCandidate = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, target := observedEnvelopeFixture()
			tc.change(&e, &target)
			digest, err := e.Digest()
			if err != nil {
				t.Fatal(err)
			}
			got, err := AssessVerificationEnvelope(e, digest, target)
			if err != nil || got.Applicable || got.Reason == "" {
				t.Fatalf("invalid proof accepted: %+v %v", got, err)
			}
		})
	}
}

func TestVerificationEnvelopeRefusesInventedOrContradictoryGuard(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*VerificationCurrentCandidateCheckReceipt)
	}{
		{"missing run", func(r *VerificationCurrentCandidateCheckReceipt) { r.RunStartedAt = nil }},
		{"missing exit", func(r *VerificationCurrentCandidateCheckReceipt) { r.ExitCode = nil }},
		{"nonzero pass", func(r *VerificationCurrentCandidateCheckReceipt) { n := 7; r.ExitCode = &n }},
		{"unknown exit", func(r *VerificationCurrentCandidateCheckReceipt) { n := -1; r.ExitCode = &n }},
		{"unresolved cleanup", func(r *VerificationCurrentCandidateCheckReceipt) { r.CleanupResolved = false }},
		{"missing authority", func(r *VerificationCurrentCandidateCheckReceipt) { r.PlanRevision = "" }},
		{"bad source", func(r *VerificationCurrentCandidateCheckReceipt) { r.SourceTreeOID = "unknown" }},
		{"bad command", func(r *VerificationCurrentCandidateCheckReceipt) { r.Command = []string{"sh", "bad\x00"} }},
		{"negative timing", func(r *VerificationCurrentCandidateCheckReceipt) { n := int64(-1); r.RunMilliseconds = &n }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := observedEnvelopeFixture()
			tc.change(e.CurrentCandidateCheck)
			if _, err := e.Digest(); err == nil {
				t.Fatal("invalid guard attestation accepted")
			}
		})
	}
	if _, err := (VerificationEnvelope{Version: 1}).Digest(); err == nil {
		t.Fatal("never-run envelope became evidence")
	}
}

func TestVerificationReceiptOptionalExitPreservesHistoricalDigest(t *testing.T) {
	r, _ := observedVerificationFixture()
	before, _ := r.Digest()
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), "exit_code") {
		t.Fatal("unavailable historical exit invented")
	}
	var decoded VerificationReceipt
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	after, _ := decoded.Digest()
	if before != after {
		t.Fatal("historical digest changed")
	}
	nonzero := 7
	r.ExitCode = &nonzero
	if _, err := r.Digest(); err == nil {
		t.Fatal("nonzero passing check allowed")
	}
}
