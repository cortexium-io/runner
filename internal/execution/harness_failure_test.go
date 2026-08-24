package execution

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestClassifyClaudeSessionLimitRequiresStructuredAdapterEvidence(t *testing.T) {
	if output, known := classifyHarnessFailure(errors.New("exit status 1"), HarnessFailureEvidence{}); known || output.RemoteDetailSafe {
		t.Fatalf("spoofed text produced trusted recovery state: %#v", output)
	}

	stdout := `{"is_error":true,"result":"untrusted model text","error":{"type":"rate_limit_error","code":"session_limit","retry_after":"2026-08-13T10:40:00+02:00"}}`
	evidence := claudeFailureEvidenceFromStdout(stdout)
	output, known := classifyHarnessFailure(errors.New("exit status 1"), evidence)
	if !known || !output.RemoteDetailSafe || !output.DiscardDiagnostics || output.RetryDisposition != RetryManual || output.Outcome != OutcomeBlocked {
		t.Fatalf("classified output = %#v, known=%t", output, known)
	}
	if output.FailureClass != FailureCapacityExhausted || output.RetryDisposition != RetryManual || output.RetryAfter != "2026-08-13T10:40:00+02:00" {
		t.Fatalf("session-limit recovery classification = %#v", output)
	}
	if output.Summary != "The harness provider reported that capacity is unavailable until the structured retry time." {
		t.Fatalf("session-limit summary = %q", output.Summary)
	}
	if output.Blocker == nil || *output.Blocker != "Retry after the provider-reported structured retry time." {
		t.Fatalf("session-limit blocker = %#v", output.Blocker)
	}
	if strings.Contains(output.Summary, "input_tokens") || strings.Contains(*output.Blocker, "untrusted model text") {
		t.Fatalf("safe output exposed raw harness diagnostics: %#v", output)
	}
	unsafeRetry := claudeFailureEvidenceFromStdout(`{"error":{"type":"rate_limit_error","code":"provider_capacity","retry_after":"token=secret"}}`)
	if unsafeRetry.FailureClass != FailureCapacityExhausted || unsafeRetry.RetryAfter != "" {
		t.Fatalf("unvalidated structured retry field escaped adapter parsing: %#v", unsafeRetry)
	}
}

func TestClassifyHarnessFailureLeavesUnknownDiagnosticsLocal(t *testing.T) {
	for _, diagnostic := range []string{
		"provider failed with token=secret",
		"HTTP 429 rate limit",
		"Please log in",
		"permission denied",
	} {
		if output, known := classifyHarnessFailure(errors.New("exit status 1"), HarnessFailureEvidence{}); known || output.RemoteDetailSafe {
			t.Fatalf("free-form diagnostic %q was classified as remotely safe: %#v", diagnostic, output)
		}
	}
	if _, known := classifyHarnessFailure(errors.New("exit status 1"), HarnessFailureEvidence{}); known {
		t.Fatal("Claude-specific failure was applied to another harness")
	}
}

func TestClassifyClaudeAuthenticationRequiresTypedEnvelopeFields(t *testing.T) {
	evidence := claudeFailureEvidenceFromStdout(`{"is_error":true,"terminal_reason":"api_error","api_error_status":401,"result":"untrusted details"}`)
	output, known := classifyHarnessFailure(errors.New("exit status 1"), evidence)
	if !known || output.FailureClass != FailureAuthenticationRequired || output.RetryDisposition != RetryManual || !output.RemoteDetailSafe || !output.DiscardDiagnostics {
		t.Fatalf("authentication classification = %#v, known=%t", output, known)
	}
	if strings.Contains(output.Summary, "untrusted") || output.Blocker == nil || strings.Contains(*output.Blocker, "untrusted") {
		t.Fatalf("untrusted Claude result escaped typed classification: %#v", output)
	}
	for _, unsafe := range []string{
		`{"is_error":true,"terminal_reason":"api_error","result":"401 login required"}`,
		`{"is_error":true,"api_error_status":401,"result":"login required"}`,
		`{"terminal_reason":"api_error","api_error_status":401}`,
	} {
		if got := claudeFailureEvidenceFromStdout(unsafe); got.FailureClass != FailureNone {
			t.Fatalf("incomplete typed envelope was trusted: %s => %#v", unsafe, got)
		}
	}
}

func TestClassifyHarnessFailureUsesTypedRecoveryPolicies(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		class FailureClass
		retry RetryDisposition
	}{
		{name: "timeout", err: context.DeadlineExceeded, class: FailureTimeout, retry: RetryManual},
		{name: "canceled", err: context.Canceled, class: FailureCanceled, retry: RetryNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, known := classifyHarnessFailure(test.err, HarnessFailureEvidence{})
			if !known || !output.RemoteDetailSafe || !output.DiscardDiagnostics || output.FailureClass != test.class || output.RetryDisposition != test.retry {
				t.Fatalf("classification = %#v, known=%t", output, known)
			}
		})
	}
}
