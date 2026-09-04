package execution

import (
	"bufio"
	"bytes"
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
	if evidence.FailureClass == FailureTransientExternal {
		return classifiedBlockedOutput(
			"The harness provider reported a transient service failure.",
			"Runner can retry after a short provider recovery delay.",
			evidence.FailureClass, evidence.RetryDisposition, evidence.RetryAfter,
		), true
	}
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

// codexFailureEvidenceFromStdout accepts only the terminal failure event from
// Codex CLI's --json stream. Progress errors and model-authored result content
// are deliberately ignored. Codex currently exposes the provider reason as a
// message rather than typed HTTP fields, so matching remains limited to fixed
// statuses and the authenticated Codex service endpoint.
func codexFailureEvidenceFromStdout(stdout string) HarnessFailureEvidence {
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	scanner.Buffer(make([]byte, 64*1024), maxHarnessDiagnosticBytes)
	var terminalMessage string
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type != "turn.failed" || event.Error == nil {
			continue
		}
		terminalMessage = strings.ToLower(strings.TrimSpace(event.Error.Message))
	}
	if terminalMessage == "" {
		return HarnessFailureEvidence{}
	}
	if codexFailureHasHTTPStatus(terminalMessage, "401") {
		return HarnessFailureEvidence{FailureClass: FailureAuthenticationRequired, RetryDisposition: RetryManual}
	}
	if codexFailureHasHTTPStatus(terminalMessage, "429") {
		return HarnessFailureEvidence{FailureClass: FailureCapacityExhausted, RetryDisposition: RetryAutomatic}
	}
	for _, status := range []string{"500", "502", "503", "504"} {
		if codexFailureHasHTTPStatus(terminalMessage, status) {
			return HarnessFailureEvidence{FailureClass: FailureTransientExternal, RetryDisposition: RetryAutomatic}
		}
	}
	if codexFailureHasHTTPStatus(terminalMessage, "404") && strings.Contains(terminalMessage, "chatgpt.com/backend-api/codex/") {
		return HarnessFailureEvidence{FailureClass: FailureTransientExternal, RetryDisposition: RetryAutomatic}
	}
	for _, marker := range []string{"connection reset", "connection refused", "service unavailable", "temporarily unavailable", "tls handshake timeout", "unexpected eof"} {
		if strings.Contains(terminalMessage, marker) {
			return HarnessFailureEvidence{FailureClass: FailureTransientExternal, RetryDisposition: RetryAutomatic}
		}
	}
	return HarnessFailureEvidence{}
}

func codexFailureHasHTTPStatus(message, status string) bool {
	return strings.Contains(message, "status "+status) || strings.Contains(message, "http "+status) || strings.Contains(message, "http error: "+status)
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
