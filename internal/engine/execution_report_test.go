package engine

import (
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/execution"
)

func TestExecutionReportSuppressesSchemaValidModelAndCLIPayloads(t *testing.T) {
	secretPayload := `raw CLI payload token=secret session_id=private stack trace prompt text`
	blocker := secretPayload
	output := execution.Output{
		Outcome:          execution.OutcomeBlocked,
		Summary:          secretPayload,
		WorkDone:         []string{secretPayload},
		Verification:     []string{secretPayload},
		Blocker:          &blocker,
		FailureClass:     execution.FailureIntegrityViolation,
		RetryDisposition: execution.RetryNone,
	}
	report := formatExecutionReport("Runner blocked", output)
	if strings.Contains(report, secretPayload) || strings.Contains(report, "token=secret") || strings.Contains(report, "session_id") {
		t.Fatalf("remote execution report exposed raw model/CLI content: %q", report)
	}
	if !strings.Contains(report, "workspace integrity violation") || !strings.Contains(report, "Failure: integrity_violation") {
		t.Fatalf("remote execution report lost its bounded Runner classification: %q", report)
	}
	if strings.ContainsAny(report, "\r\n") {
		t.Fatalf("Project execution report is not a single readable line: %q", report)
	}
}

func TestExecutionReportBoundsStructuredRetryField(t *testing.T) {
	output := execution.Output{
		Outcome: execution.OutcomeBlocked, FailureClass: execution.FailureCapacityExhausted,
		RetryDisposition: execution.RetryManual, RetryAfter: strings.Repeat("x", 500),
	}
	report := formatExecutionReport("Retryable Runner blocker", output)
	if len(report) > 600 || strings.Contains(report, strings.Repeat("x", 201)) {
		t.Fatalf("remote retry field was not bounded: bytes=%d report=%q", len(report), report)
	}
}

func TestExecutionReportPublishesOnlyRunnerSanitizedCandidateCorrection(t *testing.T) {
	correction := "Candidate failed `git diff --cached --check` because it contains trailing whitespace. Correct every reported line before retrying."
	output := execution.Output{
		Outcome: execution.OutcomeBlocked, Summary: "Implementation candidate needs correction before QA.", Blocker: &correction,
		RemoteDetailSafe: true, FailureClass: execution.FailureCandidateValidation, RetryDisposition: execution.RetryManual,
	}
	report := formatExecutionReport("Retryable Runner blocker", output)
	if !strings.Contains(report, correction) || !strings.Contains(report, "Failure: candidate_validation; retry: manual") {
		t.Fatalf("candidate correction was not published: %q", report)
	}
	secret := "PRIVATE-CANDIDATE-CONTENT"
	output.Blocker = &secret
	output.RemoteDetailSafe = false
	report = formatExecutionReport("Runner blocked", output)
	if strings.Contains(report, secret) || !strings.Contains(report, "needs correction before QA") {
		t.Fatalf("untrusted candidate detail was published: %q", report)
	}
}
