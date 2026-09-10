package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/execution"
)

func TestExecutionReportIdentifiesRetainedAcceptanceFailureWithoutPrivateDetails(t *testing.T) {
	output := integrityViolationOutput(retainedAcceptanceResumeFailure, errors.New("private-record-path token=secret"))
	report := formatExecutionReport("Retryable Runner blocker", output)
	if !strings.Contains(report, "retained QA acceptance record") || !strings.Contains(report, "No reviewer ran") || !strings.Contains(report, "retry: manual") {
		t.Fatalf("retained acceptance failure lost its specific recovery context: %q", report)
	}
	if strings.Contains(report, "private-record-path") || strings.Contains(report, "token=secret") || strings.Contains(report, "workspace integrity violation") {
		t.Fatalf("retained acceptance failure leaked details or reported workspace corruption: %q", report)
	}
}

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

func TestExecutionReportDistinguishesIncompleteReviewFromUnavailableCapability(t *testing.T) {
	for _, test := range []struct {
		class execution.FailureClass
		want  string
	}{
		{execution.FailureReviewIncomplete, "QA evidence incomplete"},
		{execution.FailureCapabilityUnavailable, "required local capability as unavailable"},
		{execution.FailureIntegrityUnverified, "does not establish a workspace change"},
	} {
		t.Run(string(test.class), func(t *testing.T) {
			private := "token=secret missing report /private/test-output.log"
			output := execution.Output{
				Outcome: execution.OutcomeNeedsInput, Summary: private, Blocker: &private,
				Verification: []string{private}, RemoteDetailSafe: true,
				FailureClass: test.class, RetryDisposition: execution.RetryManual,
			}
			report := formatExecutionReport("Retryable Runner blocker", output)
			if !strings.Contains(report, test.want) || !strings.Contains(report, "Failure: "+string(test.class)+"; retry: manual") {
				t.Fatalf("report lost its classification: %q", report)
			}
			if strings.Contains(report, "token=") || strings.Contains(report, "/private/") {
				t.Fatalf("report exposed model-authored evidence: %q", report)
			}
			if test.class == execution.FailureReviewIncomplete && strings.Contains(report, "capability") {
				t.Fatalf("incomplete review inferred a tooling failure: %q", report)
			}
		})
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
