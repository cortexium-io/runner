package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

type CodexExecutor struct {
	cfg               config.HarnessConfig
	run               subprocess.Runner
	config            config.ExecutionConfig
	workspaceProvider workspace.Provider
}

type StructuredExecutionResult struct {
	Outcome          string            `json:"outcome"`
	Summary          string            `json:"summary"`
	WorkDone         []string          `json:"work_done"`
	Verification     []string          `json:"verification"`
	Blocker          *string           `json:"blocker"`
	ReviewAssessment *ReviewAssessment `json:"review_assessment"`
}

type executionContent struct {
	Outcome      string   `json:"outcome"`
	Summary      string   `json:"summary"`
	WorkDone     []string `json:"work_done"`
	Verification []string `json:"verification"`
	Blockers     []string `json:"blockers"`
}

const executionContentSchemaTemplate = `{
  "type": "object",
  "required": ["outcome", "summary", "work_done", "verification", "blockers"],
  "properties": {
    "outcome": {"type": "string", "enum": ["succeeded", "needs_input", "blocked"]},
    "summary": {"type": "string", "minLength": 1},
    "work_done": {"type": "array", "items": {"type": "string", "minLength": 1}},
    "verification": {"type": "array", %s"items": {"type": "string", "minLength": 1}},
    "blockers": {"type": "array", "maxItems": 1, "items": {"type": "string", "minLength": 1}}
  },
  "additionalProperties": false
}`

var executionContentSchema = executionContentSchemaForVerification(0)

func executionContentSchemaForVerification(approvedChecks int) []byte {
	limit := ""
	if approvedChecks > 0 {
		// Keep an empty array valid for blocked outcomes. Successful outcomes are
		// still required to cover every approved check by the engine.
		limit = fmt.Sprintf(`"maxItems": %d, `, approvedChecks)
	}
	return []byte(fmt.Sprintf(executionContentSchemaTemplate, limit))
}

func NewCodexExecutor(cfg config.ExecutionConfig, run subprocess.Runner) CodexExecutor {
	if run == nil {
		run = subprocess.OSRunner{}
	}
	harness := cfg.Harness
	return CodexExecutor{
		cfg:               harness,
		run:               run,
		config:            cfg,
		workspaceProvider: workspace.NewGitProviderWithLimits(run, snapshotLimits(cfg.ResourceLimits)),
	}
}

func (e CodexExecutor) Execute(ctx context.Context, assignment Assignment) (Output, error) {
	if err := validateExecutionHarness(config.HarnessCodexCLI, e.cfg); err != nil {
		return blockedOutputWithFailure(err.Error(), FailureInvalidConfiguration, RetryNone), err
	}
	role := RolePlanner
	if assignment.Spec.ReviewRequired {
		role = RoleReviewer
	}
	profile, err := ProfileForRole(role, e.config.RoleAccess)
	if err != nil {
		return blockedOutputWithFailure(err.Error(), FailureInvalidConfiguration, RetryNone), err
	}
	if err := ValidateHarnessProfile(config.HarnessCodexCLI, role, e.config.RoleAccess, e.config.HarnessConfigMode); err != nil {
		return blockedOutputWithFailure(err.Error(), FailureCapabilityUnavailable, RetryNone), err
	}
	if role == RoleReviewer {
		return executeSharedReviewer(ctx, config.HarnessCodexCLI, e.config, assignment, e.run)
	}
	if err := ensureHarnessAdvertisesProfile(ctx, e.run, strings.TrimSpace(e.cfg.Command), config.HarnessCodexCLI, role, e.config.RoleAccess, e.config.HarnessConfigMode); err != nil {
		return blockedOutputWithFailure(err.Error(), FailureCapabilityUnavailable, RetryNone), err
	}
	protectedRoots := append([]string{e.cfg.WorkspaceWriteRoot}, e.config.ReferenceProtectedRoots...)
	launchWorkspace, err := prepareExecutionWorkspace(ctx, e.run, profile, e.cfg.WorkingDir, e.config.RepositoryReferences, protectedRoots...)
	if err != nil {
		return blockedOutputWithFailure(err.Error(), FailureCapabilityUnavailable, RetryNone), err
	}
	defer launchWorkspace.cleanup()
	schema := executionContentSchemaForVerification(len(assignment.Spec.RequiredVerification))
	artifacts, err := newStructuredResultArtifacts("runner-codex-project", schema)
	if err != nil {
		return Output{}, err
	}
	defer artifacts.close()

	mcpArgs, err := codexMCPProfileArgsForConfig(ctx, e.run, strings.TrimSpace(e.cfg.Command), launchWorkspace.Dir, e.config.MCPServers, e.config.SafeTools, e.config.HarnessConfigMode)
	if err != nil {
		output := blockedOutputWithFailure("Configured Codex MCP capability is unavailable.", FailureCapabilityUnavailable, RetryManual)
		output.RemoteDetailSafe = true
		return output, err
	}
	args := e.args(profile, launchWorkspace, mcpArgs, artifacts.outputPath(), artifacts.schemaPath(), assignment)
	harnessStartedAt := time.Now()
	finishHarness := metrics.StartStage(ctx, metrics.StageHarnessRun)
	result, err := e.runCodex(ctx, args, launchWorkspace.Dir, strings.NewReader(e.projectPrompt(assignment, launchWorkspace)))
	harnessDuration := time.Since(harnessStartedAt).Milliseconds()
	usage := parseCodexUsage(result.Stdout)
	lastMessage, readErr := artifacts.readResult()
	if err == nil && readErr != nil {
		err = readErr
	}
	summary := summarizeCodexResult(result, lastMessage)
	if err != nil {
		if output, known := classifyHarnessFailure(err, HarnessFailureEvidence{}); known {
			finishStageFromOutput(finishHarness, output, err, usage)
			output.Usage = usage
			output.HarnessDurationMilliseconds = harnessDuration
			return output, fmt.Errorf("run codex cli: %w", err)
		}
		output := blockedOutputWithFailure("Codex CLI failed: "+summary, FailureUnknown, RetryNone)
		finishStageFromOutput(finishHarness, output, err, usage)
		output.Usage = usage
		output.HarnessDurationMilliseconds = harnessDuration
		return output, fmt.Errorf("run codex cli: %w", err)
	}
	finishStageFromOutput(finishHarness, Output{Outcome: OutcomeSucceeded}, nil, usage)
	structured, err := assembleExecutionContent(assignment, lastMessage)
	if err != nil {
		output := blockedOutputWithFailure("Codex CLI returned invalid execution content: "+err.Error(), FailureInvalidContract, RetryNone)
		output.Usage = usage
		output.HarnessDurationMilliseconds = harnessDuration
		return output, err
	}
	output := structuredExecutorOutput(structured)
	output.Usage = usage
	output.HarnessDurationMilliseconds = harnessDuration
	return output, nil
}

func (e CodexExecutor) ExecuteWorkspaceWrite(ctx context.Context, assignment Assignment, onPrepared func(workspace.Metadata) error) (Output, error) {
	if err := validateExecutionHarness(config.HarnessCodexCLI, e.cfg); err != nil {
		return blockedOutputWithFailure(err.Error(), FailureInvalidConfiguration, RetryNone), err
	}
	profile, err := ProfileForRole(RoleImplementer, e.config.RoleAccess)
	if err != nil {
		return blockedOutputWithFailure(err.Error(), FailureInvalidConfiguration, RetryNone), err
	}
	if err := ValidateHarnessProfile(config.HarnessCodexCLI, RoleImplementer, e.config.RoleAccess, e.config.HarnessConfigMode); err != nil {
		return blockedOutputWithFailure(err.Error(), FailureCapabilityUnavailable, RetryNone), err
	}
	if err := ensureHarnessAdvertisesProfile(ctx, e.run, strings.TrimSpace(e.cfg.Command), config.HarnessCodexCLI, RoleImplementer, e.config.RoleAccess, e.config.HarnessConfigMode); err != nil {
		return blockedOutputWithFailure(err.Error(), FailureCapabilityUnavailable, RetryNone), err
	}
	packet := assignment.Spec

	finishWorkspace := metrics.StartStage(ctx, metrics.StageWorkspacePrepare)
	metadata, err := e.workspaceProvider.Prepare(ctx, workspace.Request{
		WorkingDir: e.cfg.WorkingDir, WorktreeRoot: e.cfg.WorkspaceWriteRoot,
		WorkID: packet.ID, ItemID: packet.ItemID, DelegatedContentDigest: packet.DelegatedContentDigest,
		Repository: packet.Repository, BranchPrefix: "runner", BaseRef: e.config.WorkspaceBaseRef, QuarantineMismatch: true,
	})
	if err != nil {
		output := blockedOutputWithFailure("Codex CLI workspace-write setup failed: "+err.Error(), FailureInvalidConfiguration, RetryNone)
		finishStageFromOutput(finishWorkspace, output, err, metrics.Usage{})
		return output, err
	}
	if onPrepared != nil {
		if err := onPrepared(metadata); err != nil {
			output := blockedOutputWithFailure("Codex CLI workspace-write journal update failed: "+err.Error(), FailureIntegrityViolation, RetryNone)
			finishStageFromOutput(finishWorkspace, output, err, metrics.Usage{})
			return output, err
		}
	}
	finishStageFromOutput(finishWorkspace, Output{Outcome: OutcomeSucceeded}, nil, metrics.Usage{})

	schema := executionContentSchemaForVerification(len(assignment.Spec.RequiredVerification))
	artifacts, err := newStructuredResultArtifacts("runner-codex-worktree", schema)
	if err != nil {
		return blockedOutput("Create Codex result files failed: " + err.Error()), err
	}
	defer artifacts.close()

	launchWorkspace, err := prepareProfileWorkspace(profile, metadata.WorktreePath)
	if err != nil {
		return blockedOutputWithFailure(err.Error(), FailureCapabilityUnavailable, RetryNone), err
	}
	defer launchWorkspace.cleanup()
	mcpArgs, err := codexMCPProfileArgsForConfig(ctx, e.run, strings.TrimSpace(e.cfg.Command), launchWorkspace.Dir, e.config.MCPServers, e.config.SafeTools, e.config.HarnessConfigMode)
	if err != nil {
		output := blockedOutputWithFailure("Configured Codex MCP capability is unavailable.", FailureCapabilityUnavailable, RetryManual)
		output.RemoteDetailSafe = true
		return output, err
	}
	args := e.profileWorkspaceWriteArgs(profile, launchWorkspace, mcpArgs, artifacts.outputPath(), artifacts.schemaPath(), assignment)
	harnessStartedAt := time.Now()
	finishHarness := metrics.StartStage(ctx, metrics.StageHarnessRun)
	result, runErr := e.runCodex(ctx, args, metadata.WorktreePath, strings.NewReader(e.workspaceWritePrompt(assignment)))
	harnessDuration := time.Since(harnessStartedAt).Milliseconds()
	usage := parseCodexUsage(result.Stdout)
	lastMessage, readErr := artifacts.readResult()
	if runErr == nil && readErr != nil {
		runErr = readErr
	}
	summary := summarizeCodexResult(result, lastMessage)
	var structured StructuredExecutionResult
	var structuredErr error
	if runErr == nil {
		finishStageFromOutput(finishHarness, Output{Outcome: OutcomeSucceeded}, nil, usage)
		structured, structuredErr = assembleExecutionContent(assignment, lastMessage)
	} else if classified, known := classifyHarnessFailure(runErr, HarnessFailureEvidence{}); known {
		finishStageFromOutput(finishHarness, classified, runErr, usage)
	} else {
		finishStageFromOutput(finishHarness, blockedOutputWithFailure("Harness execution failed.", FailureUnknown, RetryNone), runErr, usage)
	}
	verifyCtx, cancelVerify := postHarnessContext(ctx, e.timeout())
	defer cancelVerify()
	finishVerify := metrics.StartStage(verifyCtx, metrics.StageWorkspaceVerify)
	verifyErr := newWorkspaceVerifier(e.run, e.timeout(), snapshotLimits(e.config.ResourceLimits)).Verify(verifyCtx, metadata)
	if verifyErr != nil {
		output := blockedOutputWithFailure("Codex CLI workspace-write integrity verification failed.", FailureIntegrityViolation, RetryNone)
		finishStageFromOutput(finishVerify, output, verifyErr, metrics.Usage{})
		output.Usage = usage
		output.HarnessDurationMilliseconds = harnessDuration
		causes := []error{verifyErr}
		if runErr != nil {
			causes = append([]error{fmt.Errorf("run codex cli workspace-write: %w", runErr)}, causes...)
		}
		if structuredErr != nil {
			causes = append([]error{structuredErr}, causes...)
		}
		return output, errors.Join(causes...)
	}
	finishStageFromOutput(finishVerify, Output{Outcome: OutcomeSucceeded}, nil, metrics.Usage{})

	if runErr != nil {
		if output, known := classifyHarnessFailure(runErr, HarnessFailureEvidence{}); known {
			output.Usage = usage
			output.HarnessDurationMilliseconds = harnessDuration
			return output, fmt.Errorf("run codex cli workspace-write: %w", runErr)
		}
		output := blockedOutputWithFailure("Codex CLI workspace-write failed: "+summary, FailureUnknown, RetryNone)
		output.Usage = usage
		output.HarnessDurationMilliseconds = harnessDuration
		return output, fmt.Errorf("run codex cli workspace-write: %w", runErr)
	}
	if structuredErr != nil {
		output := blockedOutputWithFailure("Codex CLI workspace-write returned invalid execution content: "+structuredErr.Error(), FailureInvalidContract, RetryNone)
		output.Usage = usage
		output.HarnessDurationMilliseconds = harnessDuration
		return output, structuredErr
	}
	output := structuredExecutorOutput(structured)
	output.Usage = usage
	output.HarnessDurationMilliseconds = harnessDuration
	return output, nil
}

func assembleExecutionContent(assignment Assignment, value string) (StructuredExecutionResult, error) {
	if assignment.Spec.ReviewRequired {
		return StructuredExecutionResult{}, errors.New("ordinary execution content is not valid for a reviewer assignment")
	}
	canonical, err := CanonicalizeStructuredResult(value, "outcome", "summary", "work_done", "verification", "blockers")
	if err != nil {
		return StructuredExecutionResult{}, fmt.Errorf("canonicalize execution content: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var content executionContent
	if err := decoder.Decode(&content); err != nil {
		return StructuredExecutionResult{}, fmt.Errorf("decode execution content: %w", err)
	}
	if content.WorkDone == nil || content.Verification == nil || content.Blockers == nil {
		return StructuredExecutionResult{}, errors.New("execution content must explicitly include every array")
	}
	content.Outcome = strings.TrimSpace(content.Outcome)
	content.Summary = strings.TrimSpace(content.Summary)
	for index := range content.WorkDone {
		content.WorkDone[index] = strings.TrimSpace(content.WorkDone[index])
	}
	for index := range content.Verification {
		content.Verification[index] = strings.TrimSpace(content.Verification[index])
		if content.Verification[index] == "" {
			return StructuredExecutionResult{}, fmt.Errorf("execution content verification[%d] is empty", index)
		}
	}
	for index := range content.Blockers {
		content.Blockers[index] = strings.TrimSpace(content.Blockers[index])
		if content.Blockers[index] == "" {
			return StructuredExecutionResult{}, fmt.Errorf("execution content blockers[%d] is empty", index)
		}
	}
	if len(content.Blockers) > 1 {
		return StructuredExecutionResult{}, errors.New("execution content supports at most one blocker")
	}
	if content.Outcome == OutcomeSucceeded && len(content.Blockers) != 0 {
		return StructuredExecutionResult{}, errors.New("successful execution content requires blockers to be empty")
	}
	if (content.Outcome == OutcomeNeedsInput || content.Outcome == OutcomeBlocked) && len(content.Blockers) != 1 {
		return StructuredExecutionResult{}, errors.New("needs_input or blocked execution content requires exactly one blocker")
	}
	var blocker *string
	if len(content.Blockers) == 1 {
		blocker = stringPtr(content.Blockers[0])
	}
	result := StructuredExecutionResult{
		Outcome: content.Outcome, Summary: content.Summary,
		WorkDone: content.WorkDone, Verification: content.Verification, Blocker: blocker,
	}
	if err := validateSemanticResultEvidence(result.Outcome, result.Summary, result.WorkDone, result.Blocker); err != nil {
		return StructuredExecutionResult{}, err
	}
	if result.Outcome == OutcomeSucceeded && len(result.Verification) == 0 {
		return StructuredExecutionResult{}, errors.New("successful execution content requires concrete verification evidence")
	}
	if err := validateReviewAssessmentForAssignment(assignment, result.Outcome, nil); err != nil {
		return StructuredExecutionResult{}, err
	}
	return result, nil
}

func parseStructuredExecutionResult(value string) (StructuredExecutionResult, error) {
	value = NormalizeStructuredResult(value)
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	var result StructuredExecutionResult
	if err := decoder.Decode(&result); err != nil {
		return StructuredExecutionResult{}, fmt.Errorf("decode structured result: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &fields); err != nil {
		return StructuredExecutionResult{}, fmt.Errorf("inspect structured result fields: %w", err)
	}
	if _, ok := fields["review_assessment"]; !ok {
		return StructuredExecutionResult{}, errors.New("structured result review_assessment field is required; use null for non-reviewer assignments")
	}
	if _, ok := fields["verification"]; !ok {
		return StructuredExecutionResult{}, errors.New("structured result verification field is required")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return StructuredExecutionResult{}, errors.New("structured result contains more than one JSON value")
		}
		return StructuredExecutionResult{}, fmt.Errorf("decode structured result trailer: %w", err)
	}
	result.Outcome = strings.TrimSpace(result.Outcome)
	result.Summary = strings.TrimSpace(result.Summary)
	for i, item := range result.WorkDone {
		result.WorkDone[i] = strings.TrimSpace(item)
	}
	for i, item := range result.Verification {
		result.Verification[i] = strings.TrimSpace(item)
		if result.Verification[i] == "" {
			return StructuredExecutionResult{}, fmt.Errorf("structured result verification[%d] is empty", i)
		}
	}
	if result.Blocker != nil {
		trimmed := strings.TrimSpace(*result.Blocker)
		if trimmed == "" {
			result.Blocker = nil
		} else {
			result.Blocker = &trimmed
		}
	}
	if err := validateSemanticResultEvidence(result.Outcome, result.Summary, result.WorkDone, result.Blocker); err != nil {
		return StructuredExecutionResult{}, err
	}
	if result.Outcome == OutcomeSucceeded && len(result.Verification) == 0 {
		return StructuredExecutionResult{}, errors.New("successful structured result requires concrete verification evidence")
	}
	return result, nil
}

func structuredExecutorOutput(result StructuredExecutionResult) Output {
	output := Output{
		Outcome:          result.Outcome,
		Summary:          result.Summary,
		WorkDone:         append([]string{}, result.WorkDone...),
		Verification:     append([]string{}, result.Verification...),
		Blocker:          result.Blocker,
		ReviewAssessment: result.ReviewAssessment,
	}
	return output
}

func blockedOutput(summary string) Output {
	return blockedOutputWithFailure(summary, FailureUnknown, RetryNone)
}

func blockedOutputWithFailure(summary string, class FailureClass, retry RetryDisposition) Output {
	return Output{
		Outcome: OutcomeBlocked, Summary: summary, WorkDone: []string{}, Blocker: stringPtr(summary),
		FailureClass: class, RetryDisposition: retry,
	}
}

func (e CodexExecutor) args(profile ExecutionProfile, workspace profileWorkspace, mcpArgs []string, outputPath string, schemaPath string, assignment Assignment) []string {
	args := codexProfileArgsForConfig(profile, workspace, e.config.SafeTools, e.config.HarnessConfigMode, e.cfg.Command)
	args = append(args, mcpArgs...)
	if model := e.modelID(); model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", strings.TrimSpace(e.cfg.ReasoningEffort)))
	args = append(args, codexExecArgsForConfig(profile, workspace, e.config.HarnessConfigMode)...)
	args = append(args,
		"--output-last-message", outputPath,
		"--output-schema", schemaPath,
	)
	return args
}

func (e CodexExecutor) profileWorkspaceWriteArgs(profile ExecutionProfile, workspace profileWorkspace, mcpArgs []string, outputPath string, schemaPath string, assignment Assignment) []string {
	args := codexProfileArgsForConfig(profile, workspace, e.config.SafeTools, e.config.HarnessConfigMode, e.cfg.Command)
	args = append(args, mcpArgs...)
	if model := e.modelID(); model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", strings.TrimSpace(e.cfg.ReasoningEffort)))
	args = append(args, codexExecArgsForConfig(profile, workspace, e.config.HarnessConfigMode)...)
	args = append(args,
		"--output-last-message", outputPath,
		"--output-schema", schemaPath,
	)
	return args
}

func (e CodexExecutor) modelID() string {
	if e.cfg.Model != nil && strings.TrimSpace(*e.cfg.Model) != "" {
		return strings.TrimSpace(*e.cfg.Model)
	}
	return ""
}

func (e CodexExecutor) projectPrompt(assignment Assignment, workspace profileWorkspace) string {
	return buildCodexPrompt(assignment) + trustedSkillInstructions(e.config) + codexMCPPromptForConfig(e.config.MCPServers, e.config.SafeTools, e.config.HarnessConfigMode) + profileRepositoryInstruction(workspace)
}

func (e CodexExecutor) workspaceWritePrompt(assignment Assignment) string {
	return buildWorkspaceWriteCodexPrompt(assignment) + trustedSkillInstructions(e.config) + codexMCPPromptForConfig(e.config.MCPServers, e.config.SafeTools, e.config.HarnessConfigMode)
}

func (e CodexExecutor) runCodex(ctx context.Context, args []string, workingDir string, input io.Reader) (subprocess.Result, error) {
	return subprocess.RunBoundedHeadTailInput(ctx, e.run, strings.TrimSpace(e.cfg.Command), args, workingDir, e.timeout(), input, maxHarnessDiagnosticBytes, harnessTruncationMarker)
}

func (e CodexExecutor) timeout() time.Duration {
	return time.Duration(e.cfg.TimeoutSeconds) * time.Second
}

func validateExecutionHarness(kind string, harness config.HarnessConfig) error {
	if strings.TrimSpace(harness.Kind) != kind {
		return fmt.Errorf("execution harness kind must be %q", kind)
	}
	if strings.TrimSpace(harness.Command) == "" {
		return fmt.Errorf("%s command is required", kind)
	}
	if harness.TimeoutSeconds <= 0 {
		return fmt.Errorf("%s timeout_seconds must be positive", kind)
	}
	return nil
}

func buildCodexPrompt(assignment Assignment) string {
	packet := assignment.Spec
	var b strings.Builder
	b.WriteString("You are executing one approved local Runner assignment through Codex CLI.\n")
	b.WriteString("Runner has applied its fixed read-only execution profile. This assignment expects an analysis or review result rather than implementation changes.\n")
	b.WriteString("If the Runner-provided capabilities are insufficient, return a blocked outcome and identify the missing capability.\n\n")
	b.WriteString("Title: ")
	b.WriteString(packet.Task.Title)
	b.WriteString("\n\nApproved resolved instructions:\n")
	b.WriteString(resolvedInstructions(assignment))
	if len(packet.ContextRefs) > 0 {
		b.WriteString("\n\nContext references:\n")
		for _, ref := range packet.ContextRefs {
			b.WriteString("- ")
			b.WriteString(ref)
			b.WriteByte('\n')
		}
	}
	appendStructuredResultInstructions(&b)
	return b.String()
}

func buildWorkspaceWriteCodexPrompt(assignment Assignment) string {
	packet := assignment.Spec
	var b strings.Builder
	b.WriteString("You are executing one approved local Runner assignment through Codex CLI.\n")
	b.WriteString("Runner has applied its fixed implementer profile in an isolated Git worktree.\n")
	b.WriteString("If the Runner-provided capabilities are insufficient, return a blocked outcome and identify the missing capability.\n\n")
	b.WriteString("Title: ")
	b.WriteString(packet.Task.Title)
	b.WriteString("\n\nApproved resolved instructions:\n")
	b.WriteString(resolvedInstructions(assignment))
	if len(packet.ContextRefs) > 0 {
		b.WriteString("\n\nContext references:\n")
		for _, ref := range packet.ContextRefs {
			b.WriteString("- ")
			b.WriteString(ref)
			b.WriteByte('\n')
		}
	}
	if len(packet.RequiredVerification) > 0 {
		b.WriteString("\n\nRunner-owned proof obligations:\n")
		for _, verification := range packet.RequiredVerification {
			b.WriteString("- ")
			b.WriteString(verification)
			b.WriteByte('\n')
		}
		appendVerificationOwnershipInstructions(&b, len(packet.RequiredVerification))
	}
	appendStructuredResultInstructions(&b)
	return b.String()
}

func appendStructuredResultInstructions(b *strings.Builder) {
	b.WriteString("\nReturn the structured outcome through the harness's required structured-output mechanism. Include a concise summary, concrete work_done entries, and a verification entry for each check actually performed. Never describe an unrun check as verification. Set blockers to [] for succeeded. Set blockers to exactly one non-empty reason for needs_input or blocked.")
	b.WriteString("\nWhen the harness provides a dedicated Runner finalization tool, follow that tool's own completion instructions exactly. Otherwise, the entire final response must be exactly one JSON object. Do not use Markdown, a code fence, headings, bullets, or commentary outside that object.")
	b.WriteString("\nDo not return blocker or review_assessment fields. Runner derives its internal result from this execution content.")
}

func resolvedInstructions(assignment Assignment) string {
	packet := assignment.Spec
	var b strings.Builder
	b.WriteString(strings.TrimSpace(packet.Task.Instructions))
	if strings.TrimSpace(packet.DelegatedContentDigest) != "" {
		b.WriteString("\n\nDelegated content identity: ")
		b.WriteString(strings.TrimSpace(packet.DelegatedContentDigest))
	}
	if strings.TrimSpace(packet.ApprovedBodySnapshot) != "" {
		b.WriteString("\n\nSource description and acceptance criteria (approved body snapshot):\n--- BEGIN APPROVED BODY SNAPSHOT ---\n")
		b.WriteString(strings.TrimSpace(packet.ApprovedBodySnapshot))
		b.WriteString("\n--- END APPROVED BODY SNAPSHOT ---")
	}
	return b.String()
}

func parsePorcelainFilesByStatus(output string, statusFilter string) []string {
	seen := map[string]bool{}
	var files []string
	for _, line := range strings.Split(output, "\n") {
		if len(line) < 4 {
			continue
		}
		status := strings.TrimSpace(line[:2])
		if statusFilter != "" && status != statusFilter {
			continue
		}
		file := strings.TrimSpace(line[3:])
		if idx := strings.LastIndex(file, " -> "); idx >= 0 {
			file = strings.TrimSpace(file[idx+4:])
		}
		if file == "" || seen[file] {
			continue
		}
		seen[file] = true
		files = append(files, file)
	}
	return files
}

func summarizeCodexResult(result subprocess.Result, lastMessage string) string {
	if strings.TrimSpace(lastMessage) != "" {
		return strings.TrimSpace(lastMessage)
	}
	if strings.TrimSpace(result.Stdout) != "" {
		return truncate(strings.TrimSpace(result.Stdout), 4000)
	}
	if strings.TrimSpace(result.Stderr) != "" {
		return truncate(strings.TrimSpace(result.Stderr), 4000)
	}
	if result.ExitCode != 0 {
		return fmt.Sprintf("codex exited with status %d", result.ExitCode)
	}
	return "codex completed without captured output"
}

func compactText(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}
