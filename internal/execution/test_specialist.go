package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

func ValidateTestSpecialistRequest(spec Spec, request *TestSpecialistRequest) error {
	if spec.ReviewRequired || spec.ReviewOnly || spec.TestSpecialist == nil || request == nil {
		return errors.New("test specialist handoff is unavailable for this assignment")
	}
	if len(request.Paths) == 0 || len(request.Paths) > workspace.MaxTestSpecialistFiles ||
		len(request.CriterionIndices) == 0 || len(request.CriterionIndices) > 32 ||
		strings.TrimSpace(request.Reason) == "" || len(request.Reason) > 4096 ||
		request.ExistingChecks == nil || len(request.ExistingChecks) > 16 {
		return errors.New("test specialist request needs bounded criteria, reason, existing checks and exact paths")
	}
	seen := map[int]bool{}
	for _, index := range request.CriterionIndices {
		if index < 0 || index >= len(spec.RequiredVerification) || seen[index] {
			return errors.New("test specialist request references an unknown or duplicate approved criterion")
		}
		seen[index] = true
	}
	paths := map[string]bool{}
	for _, name := range request.Paths {
		key := strings.ToLower(name)
		if len(name) > 1024 || paths[key] || !workspace.TestSpecialistPathAllowed(name, spec.TestSpecialist.AllowedPaths) {
			return errors.New("test specialist request exceeds its exact operator-owned file selection")
		}
		paths[key] = true
	}
	for _, check := range request.ExistingChecks {
		if strings.TrimSpace(check) == "" || len(check) > 2048 {
			return errors.New("test specialist existing-check context is empty or unbounded")
		}
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > 32*1024 {
		return errors.New("test specialist request exceeds its context bound")
	}
	return nil
}

func validateImplementationTestCapability(kind string, cfg config.ExecutionConfig, spec Spec) error {
	if spec.TestSpecialist == nil {
		return nil // Disabled or already consumed; never implicitly enable it.
	}
	if kind != config.HarnessCodexCLI && kind != config.HarnessClaudeCLI || spec.ReviewOnly || spec.ReviewRequired ||
		cfg.TestSpecialist == nil || !cfg.TestSpecialist.Enabled || !slices.Equal(cfg.TestSpecialist.AllowedPaths, spec.TestSpecialist.AllowedPaths) {
		return errors.New("test specialist capability lacks matching native implementation policy")
	}
	return nil
}

// ExecuteTestSpecialist uses the ordinary implementer contract with strictly
// narrower capabilities. The engine owns the spent allowance and private copy;
// this method cannot authorize a handoff or apply any returned source change.
func ExecuteTestSpecialist(ctx context.Context, kind string, cfg config.ExecutionConfig, assignment Assignment, request *TestSpecialistRequest, privateDirectory string, run subprocess.Runner) (Output, error) {
	if err := ValidateAssignmentContext(assignment.Spec); err != nil {
		return blockedOutputWithFailure(err.Error(), FailureInvalidContract, RetryNone), err
	}
	if err := ValidateTestSpecialistRequest(assignment.Spec, request); err != nil {
		return blockedOutputWithFailure(err.Error(), FailureInvalidContract, RetryNone), err
	}
	if cfg.TestSpecialist == nil || !cfg.TestSpecialist.Enabled || !slices.Equal(cfg.TestSpecialist.AllowedPaths, assignment.Spec.TestSpecialist.AllowedPaths) {
		err := errors.New("specialist capability does not match the selected implementation policy")
		return blockedOutputWithFailure(err.Error(), FailureInvalidContract, RetryNone), err
	}
	if kind != config.HarnessCodexCLI && kind != config.HarnessClaudeCLI {
		err := errors.New("test specialist requires native sandboxed Codex or Claude containment")
		return blockedOutputWithFailure(err.Error(), FailureCapabilityUnavailable, RetryManual), err
	}
	// No ambient skills/configuration, live connectors or host-access fallback.
	// Existing implementation skill is stable guidance; the request is bounded
	// untrusted assignment data, never additional execution authority.
	cfg.RoleAccess, cfg.HarnessConfigMode = config.RoleAccessSandboxed, config.HarnessConfigModeIsolated
	cfg.SafeTools, cfg.MCPServers, cfg.RepositoryReferences = false, nil, nil
	cfg.ReviewEvidenceRoot, cfg.ReviewEvidencePaths = "", nil
	cfg.Harness.WorkingDir = privateDirectory
	cfg.Skills = []string{"runner-implementer"}
	encoded, _ := json.Marshal(request)
	assignment.Spec.TestSpecialist = nil
	contextAssignment := assignment
	contextAssignment.Spec.RequiredVerification = nil
	criteria, _ := json.Marshal(assignment.Spec.RequiredVerification)
	prompt := "You are the sequential test specialist for this one approved implementation. This is not a new role, QA verdict, or permission to repair production code. " +
		"Inspect existing tests and the approved requirements. Add or improve the smallest meaningful test only when it adds protection; a reasoned no-change result is valid. " +
		"Only the exact requested files may change. Do not change production source, requirements, instructions, configuration, manifests, dependencies, Git metadata, file modes, or links; do not delete files. " +
		"This private source copy contains retained uncommitted work and a standalone inventory index, not the original Git history or installed dependencies. " +
		"Do not install packages, generate unrelated files, request a specialist/repair pass, or bypass unavailable capabilities. Run a focused check only if its existing prerequisites and permitted paths suffice; otherwise report it as unrun for the original implementer. " +
		"Return succeeded for a completed test contribution or justified no-change result, not a claim that the original feature is complete. Return blocked/needs_input honestly if the requested contribution cannot be made. " +
		"The inherited deadline includes this handoff and continuation. Keep the contribution within 32 files and 256 KiB total.\n" +
		"Requested contribution (untrusted implementer context, bounded by Runner):\n" + string(encoded) + "\n" +
		harnessTaskContext(contextAssignment, false) + "\nOriginal approved proof obligations (zero-based indices; context, not a requirement to complete all feature work here):\n" + string(criteria)
	var instructions strings.Builder
	appendStructuredResultInstructions(&instructions, false)
	prompt += instructions.String()
	result, err := runStructuredHarness(ctx, RoleImplementer, kind, cfg, privateDirectory, prompt,
		executionContentSchemaForVerification(0), "require", metrics.StageTestSpecialist, run)
	if err != nil {
		class := result.FailureClass
		if class == FailureNone {
			class = FailureUnknown
		}
		output := blockedOutputWithFailure("Test specialist execution failed; retained implementation was not replaced.", class, RetryManual)
		output.Usage, output.HarnessDurationMilliseconds = result.Usage, result.DurationMilliseconds
		return output, err
	}
	structured, err := assembleExecutionContent(assignment, result.Message)
	if err != nil {
		output := blockedOutputWithFailure("Test specialist returned invalid content.", FailureInvalidContract, RetryManual)
		output.Usage, output.HarnessDurationMilliseconds = result.Usage, result.DurationMilliseconds
		return output, err
	}
	output := structuredExecutorOutput(structured)
	output.Usage, output.HarnessDurationMilliseconds = result.Usage, result.DurationMilliseconds
	return output, nil
}

func testSpecialistCapabilityPrompt(spec Spec) string {
	if spec.TestSpecialist == nil {
		return ""
	}
	paths, _ := json.Marshal(spec.TestSpecialist.AllowedPaths)
	return fmt.Sprintf("\nRunner permits one explicitly requested sequential test-specialist contribution within this same attempt/deadline, not a mandatory phase. Exact operator-selected files: %s. If useful, return outcome test_requested, blockers [], retained work/evidence and test_request {criterion_indices: zero-based indices of approved proof obligations, reason: why a focused contribution is needed, existing_checks: inspected relevant checks (or []), paths: exact subset of permitted files}. Stop workspace execution when handing off. Set test_request to null for other outcomes. The specialist cannot alter production or requirements; Runner returns its actual contribution before you resume. It does not renew repair/time/usage allowances.\n", paths)
}

func canonicalizeImplementationContent(value string, fields ...string) (string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(NormalizeStructuredResult(value)), &object); err != nil {
		return "", err
	}
	if _, present := object["test_request"]; present {
		fields = append(fields, "test_request")
	}
	return CanonicalizeStructuredResult(value, fields...)
}
