package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"
)

type HarnessFailureEvidence struct {
	FailureClass     FailureClass
	RetryDisposition RetryDisposition
	RetryAfter       string
}

func classifyHarnessFailure(runErr error, evidence HarnessFailureEvidence) (Output, bool) {
	if evidence.FailureClass == FailureAuthenticationRequired {
		return classifiedBlockedOutput(
			"The harness reported that authentication is required.",
			"Authenticate the configured harness, then retry this card.",
			evidence.FailureClass, RetryManual, "",
		), true
	}
	if evidence.FailureClass == FailureCapacityExhausted {
		summary := "The harness provider reported that capacity is unavailable."
		blocker := "Wait for provider capacity before retrying the harness."
		if evidence.RetryAfter != "" {
			summary = "The harness provider reported that capacity is unavailable until the structured retry time."
			blocker = "Retry after the provider-reported structured retry time."
		}
		return classifiedBlockedOutput(summary, blocker, evidence.FailureClass, evidence.RetryDisposition, evidence.RetryAfter), true
	}

	if errors.Is(runErr, context.DeadlineExceeded) {
		return classifiedBlockedOutput(
			"Harness execution reached its configured timeout.",
			"Inspect the retained work and retry manually after confirming the operation is safe to repeat.",
			FailureTimeout, RetryManual, "",
		), true
	}
	if errors.Is(runErr, context.Canceled) {
		return classifiedBlockedOutput(
			"Harness execution was canceled.",
			"Confirm why the Runner was stopped before deciding whether to retry.",
			FailureCanceled, RetryNone, "",
		), true
	}
	var executableErr *exec.Error
	if errors.As(runErr, &executableErr) {
		return classifiedBlockedOutput(
			"The configured harness executable could not be started.",
			"Check the configured harness command and local installation.",
			FailureInvalidConfiguration, RetryNone, "",
		), true
	}

	return Output{}, false
}

// claudeFailureEvidenceFromStdout accepts only a typed error object owned by
// the Claude CLI envelope. Model-authored result/structured_output text and raw
// stdout/stderr phrases are never considered recovery evidence.
func claudeFailureEvidenceFromStdout(stdout string) HarnessFailureEvidence {
	var envelope struct {
		IsError        bool            `json:"is_error"`
		TerminalReason string          `json:"terminal_reason"`
		APIErrorStatus int             `json:"api_error_status"`
		Error          json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(stdout)), &envelope) != nil {
		return HarnessFailureEvidence{}
	}
	if envelope.IsError && strings.TrimSpace(envelope.TerminalReason) == "api_error" && envelope.APIErrorStatus == 401 {
		return HarnessFailureEvidence{FailureClass: FailureAuthenticationRequired, RetryDisposition: RetryManual}
	}
	if len(envelope.Error) == 0 || string(envelope.Error) == "null" {
		return HarnessFailureEvidence{}
	}
	var providerError struct {
		Type       string `json:"type"`
		Code       string `json:"code"`
		RetryAfter string `json:"retry_after"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(envelope.Error)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&providerError) != nil || strings.TrimSpace(providerError.Type) != "rate_limit_error" {
		return HarnessFailureEvidence{}
	}
	switch strings.TrimSpace(providerError.Code) {
	case "session_limit", "provider_capacity":
	default:
		return HarnessFailureEvidence{}
	}
	retryAfter := ""
	if reported := strings.TrimSpace(providerError.RetryAfter); reported != "" {
		if parsed, err := time.Parse(time.RFC3339, reported); err == nil {
			retryAfter = parsed.Format(time.RFC3339)
		}
	}
	return HarnessFailureEvidence{FailureClass: FailureCapacityExhausted, RetryDisposition: RetryManual, RetryAfter: retryAfter}
}

func classifiedBlockedOutput(summary, blocker string, class FailureClass, retry RetryDisposition, retryAfter string) Output {
	return Output{
		Outcome:            OutcomeBlocked,
		Summary:            summary,
		WorkDone:           []string{},
		Blocker:            stringPtr(blocker),
		RemoteDetailSafe:   true,
		DiscardDiagnostics: true,
		FailureClass:       class,
		RetryDisposition:   retry,
		RetryAfter:         strings.TrimSpace(retryAfter),
	}
}
