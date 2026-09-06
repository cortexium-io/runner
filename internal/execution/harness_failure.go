package execution

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

type HarnessFailureEvidence struct {
	FailureClass     FailureClass
	RetryDisposition RetryDisposition
	RetryAfter       string
}

func classifyHarnessFailure(runErr error, evidence HarnessFailureEvidence) (Output, bool) {
	if evidence.FailureClass == FailureBrowserStartup {
		output := classifiedBlockedOutput(
			"Runner's runner_browser MCP server timed out before the Codex session started.",
			"Runner can retry browser startup. If retries are exhausted, check the local browser capability before retrying manually.",
			FailureBrowserStartup, RetryAutomatic, "",
		)
		// This narrow startup envelope contains only fixed CLI text and a
		// timestamp. Keep the local diagnostic; GitHub still receives a template.
		output.DiscardDiagnostics = false
		return output, true
	}
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

func codexFailureEvidence(result subprocess.Result, runErr error, safeTools bool) HarnessFailureEvidence {
	if evidence := codexFailureEvidenceFromStdout(result.Stdout); evidence.FailureClass != FailureNone {
		return evidence
	}
	// Codex emits no JSONL event when thread/start fails. Accept only its exact
	// pre-session fatal envelope for our required, pinned browser, with a plain
	// exit-status error after successful process teardown. A joined cleanup
	// error, cancellation, nonempty/truncated stdout, or any additional stderr
	// text fails closed. Once a session emits output this exception cannot apply.
	if _, exited := runErr.(*exec.ExitError); !exited || result.ExitCode != 1 || !safeTools || result.Stdout != "" {
		return HarnessFailureEvidence{}
	}
	reason := fmt.Sprintf("required MCP servers failed to initialize: %s: MCP client startup timed out after %ds", runnerBrowserMCPServer, runnerBrowserStartupTimeoutSeconds)
	fatal := "Error: thread/start: thread/start failed: error creating thread: Fatal error: Failed to initialize session: " + reason + " (code -32603)"
	lines := strings.Split(strings.TrimSpace(result.Stderr), "\n")
	if lines[len(lines)-1] != fatal {
		return HarnessFailureEvidence{}
	}
	lines = lines[:len(lines)-1]
	if len(lines) > 0 && lines[0] == "Reading prompt from stdin..." {
		lines = lines[1:]
	}
	if len(lines) == 1 {
		timestamp, message, found := strings.Cut(lines[0], " ERROR codex_core::session: ")
		if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil || !found || message != "Failed to create session: "+reason {
			return HarnessFailureEvidence{}
		}
		lines = lines[1:]
	}
	if len(lines) != 0 {
		return HarnessFailureEvidence{}
	}
	return HarnessFailureEvidence{FailureClass: FailureBrowserStartup, RetryDisposition: RetryAutomatic}
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
