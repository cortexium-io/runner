package engine

import (
	"fmt"
	"strings"

	"github.com/cortexium-io/runner/internal/execution"
)

func formatExecutionReport(title string, output execution.Output) string {
	parts := make([]string, 0, 3)
	if title = strings.TrimSpace(title); title != "" {
		parts = append(parts, title)
	}
	if summary := remoteDiagnosticSummary(output); summary != "" {
		parts = append(parts, summary)
	}
	if recovery := recoveryClassification(output); recovery != "" {
		parts = append(parts, recovery)
	}
	return strings.Join(parts, " — ")
}

func remoteDiagnosticSummary(output execution.Output) string {
	switch output.FailureClass {
	case execution.FailureNone:
		if output.Outcome == execution.OutcomeSucceeded {
			return "Runner accepted the harness result. Detailed harness evidence is retained locally."
		}
		return "Runner did not accept a remotely publishable harness diagnostic. Details are retained locally."
	case execution.FailureTransientExternal:
		return "Runner classified a transient external failure."
	case execution.FailureCapacityExhausted:
		return "Runner classified provider capacity as unavailable from structured adapter evidence."
	case execution.FailureTimeout:
		return "Runner classified the harness attempt as timed out."
	case execution.FailureCanceled:
		return "Runner classified the harness attempt as canceled."
	case execution.FailureInvalidContract:
		return "Runner rejected an invalid structured-result contract."
	case execution.FailureCapabilityUnavailable:
		return "Runner classified a required local capability as unavailable."
	case execution.FailureNeedsInput:
		return "Runner classified the attempt as requiring operator input."
	case execution.FailurePermissionDenied:
		return "Runner classified a local permission failure."
	case execution.FailureAuthenticationRequired:
		return "Runner classified a local authentication requirement."
	case execution.FailureInvalidConfiguration:
		return "Runner classified the local harness configuration as invalid."
	case execution.FailureIntegrityViolation:
		return "Runner detected a workspace integrity violation and blocked continuation."
	default:
		return "Runner classified an unknown local failure. Details are retained locally."
	}
}

func recoveryClassification(output execution.Output) string {
	if output.FailureClass == execution.FailureNone && output.RetryDisposition == "" && strings.TrimSpace(output.RetryAfter) == "" {
		return ""
	}
	var report strings.Builder
	fmt.Fprintf(&report, "Failure: %s", boundedFailureClass(output.FailureClass))
	if retry := boundedRetryDisposition(output.RetryDisposition); retry != "" {
		fmt.Fprintf(&report, "; retry: %s", retry)
	}
	if retryAfter := strings.TrimSpace(output.RetryAfter); output.FailureClass == execution.FailureCapacityExhausted && retryAfter != "" {
		fmt.Fprintf(&report, "; retry after: %s", boundedRemoteField(retryAfter, 200))
	}
	return report.String()
}

func boundedFailureClass(class execution.FailureClass) string {
	switch class {
	case execution.FailureTransientExternal, execution.FailureCapacityExhausted, execution.FailureTimeout,
		execution.FailureCanceled, execution.FailureInvalidContract, execution.FailureCapabilityUnavailable,
		execution.FailureNeedsInput, execution.FailurePermissionDenied, execution.FailureAuthenticationRequired,
		execution.FailureInvalidConfiguration, execution.FailureIntegrityViolation:
		return string(class)
	default:
		return string(execution.FailureUnknown)
	}
}

func boundedRetryDisposition(retry execution.RetryDisposition) string {
	switch retry {
	case execution.RetryManual, execution.RetryNone:
		return string(retry)
	default:
		return ""
	}
}

func boundedRemoteField(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}
