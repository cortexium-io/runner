package execution

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/subprocess"
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

func TestCodexFailureEvidenceAcceptsOnlyTerminalServiceEvents(t *testing.T) {
	observed404 := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread"}`,
		`{"type":"error","message":"Reconnecting after unexpected status 404 Not Found"}`,
		`{"type":"turn.failed","error":{"message":"unexpected status 404 Not Found: Unknown error, url: https://chatgpt.com/backend-api/codex/responses"}}`,
	}, "\n")
	evidence := codexFailureEvidenceFromStdout(observed404)
	if evidence.FailureClass != FailureTransientExternal || evidence.RetryDisposition != RetryAutomatic {
		t.Fatalf("Codex 404 evidence = %#v, want automatic transient retry", evidence)
	}

	progressOnly := `{"type":"error","message":"unexpected status 503 Service Unavailable"}`
	if evidence := codexFailureEvidenceFromStdout(progressOnly); evidence != (HarnessFailureEvidence{}) {
		t.Fatalf("nonterminal progress error became recovery authority: %#v", evidence)
	}
	modelText := `{"type":"item.completed","item":{"type":"agent_message","text":"turn.failed status 503"}}`
	if evidence := codexFailureEvidenceFromStdout(modelText); evidence != (HarnessFailureEvidence{}) {
		t.Fatalf("model-authored text became recovery authority: %#v", evidence)
	}
	unrelated404 := `{"type":"turn.failed","error":{"message":"HTTP 404 for https://example.invalid/missing"}}`
	if evidence := codexFailureEvidenceFromStdout(unrelated404); evidence != (HarnessFailureEvidence{}) {
		t.Fatalf("unrelated 404 became provider recovery authority: %#v", evidence)
	}
}

func TestCodexFailureEvidenceClassifiesTerminalStatusFamilies(t *testing.T) {
	tests := []struct {
		name  string
		input string
		class FailureClass
		retry RetryDisposition
	}{
		{name: "authentication", input: "HTTP 401 Unauthorized", class: FailureAuthenticationRequired, retry: RetryManual},
		{name: "capacity", input: "unexpected status 429 Too Many Requests", class: FailureCapacityExhausted, retry: RetryAutomatic},
		{name: "model capacity", input: "Selected model is at capacity. Please try a different model.", class: FailureCapacityExhausted, retry: RetryAutomatic},
		{name: "provider", input: "HTTP 503 Service Unavailable", class: FailureTransientExternal, retry: RetryAutomatic},
		{name: "network", input: "connection reset by peer", class: FailureTransientExternal, retry: RetryAutomatic},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout := `{"type":"turn.failed","error":{"message":` + strconv.Quote(test.input) + `}}`
			evidence := codexFailureEvidenceFromStdout(stdout)
			if evidence.FailureClass != test.class || evidence.RetryDisposition != test.retry {
				t.Fatalf("evidence = %#v, want class=%q retry=%q", evidence, test.class, test.retry)
			}
		})
	}
}

func TestCodexModelCapacityRequiresExactTerminalFailure(t *testing.T) {
	const reason = "Selected model is at capacity. Please try a different model."
	for _, test := range []struct {
		name, stdout, stderr string
	}{
		{name: "plain stdout", stdout: reason},
		{name: "plain stderr", stderr: reason},
		{name: "progress error", stdout: `{"type":"error","message":` + strconv.Quote(reason) + `}`},
		{name: "model text", stdout: `{"type":"item.completed","item":{"type":"agent_message","text":` + strconv.Quote(reason) + `}}`},
		{name: "quoted reason", stdout: `{"type":"turn.failed","error":{"message":` + strconv.Quote("Tool output said: "+reason) + `}}`},
		{name: "additional reason", stdout: `{"type":"turn.failed","error":{"message":` + strconv.Quote(reason+" Account is disabled.") + `}}`},
		{name: "account limit", stdout: `{"type":"turn.failed","error":{"message":"You've hit your usage limit. Upgrade your plan to continue."}}`},
		{name: "invalid model", stdout: `{"type":"turn.failed","error":{"message":"The selected model does not exist or you do not have access to it."}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := subprocess.Result{Stdout: test.stdout, Stderr: test.stderr, ExitCode: 1}
			if evidence := codexFailureEvidence(result, errors.New("exit status 1"), false); evidence != (HarnessFailureEvidence{}) {
				t.Fatalf("unproven capacity failure gained retry authority: %#v", evidence)
			}
		})
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
