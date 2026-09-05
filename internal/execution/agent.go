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

type AgentExecutor struct {
	kind              string
	cfg               config.HarnessConfig
	config            config.ExecutionConfig
	run               subprocess.Runner
	workspaceProvider workspace.Provider
}

func NewAgentExecutor(kind string, cfg config.ExecutionConfig, run subprocess.Runner) AgentExecutor {
	if run == nil {
		run = subprocess.OSRunner{}
	}
	return AgentExecutor{
		kind: kind, cfg: cfg.Harness, config: cfg, run: run,
		workspaceProvider: workspace.NewGitProviderWithLimits(run, snapshotLimits(cfg.ResourceLimits)),
	}
}

func (e AgentExecutor) Execute(ctx context.Context, assignment Assignment) (Output, error) {
	if !config.ValidHarnessKind(e.kind) || e.kind == config.HarnessCodexCLI {
		return Output{}, fmt.Errorf("unsupported generic harness kind %q", e.kind)
	}
	if err := validateExecutionHarness(e.kind, e.cfg); err != nil {
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
	if err := ValidateHarnessProfile(e.kind, role, e.config.RoleAccess, e.config.HarnessConfigMode); err != nil {
		return blockedOutputWithFailure(err.Error(), FailureCapabilityUnavailable, RetryNone), err
	}
	if role == RoleReviewer {
		return executeSharedReviewer(ctx, e.kind, e.config, assignment, e.run)
	}
	if err := ensureHarnessAdvertisesProfile(ctx, e.run, strings.TrimSpace(e.cfg.Command), e.kind, role, e.config.RoleAccess, e.config.HarnessConfigMode); err != nil {
		return blockedOutputWithFailure(err.Error(), FailureCapabilityUnavailable, RetryNone), err
	}
	protectedRoots := append([]string{e.cfg.WorkspaceWriteRoot}, e.config.ReferenceProtectedRoots...)
	launchWorkspace, err := prepareExecutionWorkspace(ctx, e.run, profile, e.cfg.WorkingDir, e.config.RepositoryReferences, protectedRoots...)
	if err != nil {
		return blockedOutputWithFailure(err.Error(), FailureCapabilityUnavailable, RetryNone), err
	}
	defer launchWorkspace.cleanup()
	prompt := buildHarnessPrompt(assignment, false, e.displayName())
	harnessStartedAt := time.Now()
	finishHarness := metrics.StartStage(ctx, metrics.StageHarnessRun)
	schema := executionContentSchemaForVerification(len(assignment.Spec.RequiredVerification))
	result, lastMessage, usage, failureEvidence, err := e.runHarness(ctx, e.profileProjectArgs(profile, launchWorkspace, schema), launchWorkspace.Dir, strings.NewReader(prompt+trustedSkillInstructions(e.config)+runnerBrowserPrompt(e.config.SafeTools)+profileRepositoryInstruction(launchWorkspace)), schema, "prefer")
	harnessDuration := time.Since(harnessStartedAt).Milliseconds()
	if err != nil {
		if output, known := classifyHarnessFailure(err, failureEvidence); known {
			finishStageFromOutput(finishHarness, output, err, usage)
			output.Usage = usage
			output.HarnessDurationMilliseconds = harnessDuration
			return output, fmt.Errorf("run %s: %w", e.kind, err)
		}
		summary := summarizeHarnessResult(result, lastMessage)
		output := blockedOutputWithFailure("Harness failed: "+summary, FailureUnknown, RetryNone)
		finishStageFromOutput(finishHarness, output, err, usage)
		output.Usage = usage
		output.HarnessDurationMilliseconds = harnessDuration
		return output, fmt.Errorf("run %s: %w", e.kind, err)
	}
	finishStageFromOutput(finishHarness, Output{Outcome: OutcomeSucceeded}, nil, usage)
	structured, err := assembleExecutionContent(assignment, lastMessage)
	if err != nil {
		output := blockedOutputWithFailure("Harness returned invalid execution content: "+err.Error(), FailureInvalidContract, RetryNone)
		output.Usage = usage
		output.HarnessDurationMilliseconds = harnessDuration
		return output, err
	}
	output := structuredExecutorOutput(structured)
	output.Usage = usage
	output.HarnessDurationMilliseconds = harnessDuration
	return output, nil
}

// ExecuteWorkspaceWrite runs Claude Code or Pi CLI in the same Runner-owned
// issue worktree used by Codex. The configured role access controls native
// isolation; Runner owns workspace identity and verifies both the task
// worktree and active checkout after the harness exits.
func (e AgentExecutor) ExecuteWorkspaceWrite(ctx context.Context, assignment Assignment, onPrepared func(workspace.Metadata) error) (Output, error) {
	if e.kind != config.HarnessClaudeCLI && e.kind != config.HarnessPiCLI {
		return Output{}, fmt.Errorf("unsupported implementation harness %q", e.kind)
	}
	if err := validateExecutionHarness(e.kind, e.cfg); err != nil {
		return blockedOutputWithFailure(err.Error(), FailureInvalidConfiguration, RetryNone), err
	}
	profile, err := ProfileForRole(RoleImplementer, e.config.RoleAccess)
	if err != nil {
		return blockedOutputWithFailure(err.Error(), FailureInvalidConfiguration, RetryNone), err
	}
	if err := ValidateHarnessProfile(e.kind, RoleImplementer, e.config.RoleAccess, e.config.HarnessConfigMode); err != nil {
		return blockedOutputWithFailure(err.Error(), FailureCapabilityUnavailable, RetryNone), err
	}
	if err := ensureHarnessAdvertisesProfile(ctx, e.run, strings.TrimSpace(e.cfg.Command), e.kind, RoleImplementer, e.config.RoleAccess, e.config.HarnessConfigMode); err != nil {
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
		output := blockedOutputWithFailure(e.displayName()+" workspace-write setup failed: "+err.Error(), FailureInvalidConfiguration, RetryNone)
		finishStageFromOutput(finishWorkspace, output, err, metrics.Usage{})
		return output, err
	}
	if onPrepared != nil {
		if err := onPrepared(metadata); err != nil {
			output := blockedOutputWithFailure(e.displayName()+" workspace-write journal update failed: "+err.Error(), FailureIntegrityViolation, RetryNone)
			finishStageFromOutput(finishWorkspace, output, err, metrics.Usage{})
			return output, err
		}
	}
	finishStageFromOutput(finishWorkspace, Output{Outcome: OutcomeSucceeded}, nil, metrics.Usage{})

	launchProfile := profile
	if e.kind == config.HarnessClaudeCLI && !requiresFullHarnessAccess(profile) {
		// Claude Code 2.1.222 can generate a macOS sandbox profile larger
		// than ARG_MAX when its process cwd is an existing linked worktree.
		// Keep the task worktree as the writable repository root, but launch
		// the CLI from Runner's private neutral directory.
		launchProfile.Workspace = WorkspaceNeutral
	}
	protectedRoots := append([]string{e.cfg.WorkingDir, e.cfg.WorkspaceWriteRoot}, e.config.ReferenceProtectedRoots...)
	launchWorkspace, err := prepareExecutionWorkspace(ctx, e.run, launchProfile, metadata.WorktreePath, e.config.RepositoryReferences, protectedRoots...)
	if err != nil {
		return blockedOutputWithFailure(err.Error(), FailureCapabilityUnavailable, RetryNone), err
	}
	defer launchWorkspace.cleanup()
	prompt := buildHarnessPrompt(assignment, true, e.displayName()) + trustedSkillInstructions(e.config) + runnerBrowserPrompt(e.config.SafeTools) + profileReferenceInstruction(launchWorkspace)
	if launchWorkspace.Dir != metadata.WorktreePath {
		prompt += "\n\nAssigned worktree: " + metadata.WorktreePath
		if len(launchWorkspace.ReferenceRoots) == 0 {
			prompt += "\nRun every repository read, edit, command, and verification inside that exact directory.\n"
		} else {
			prompt += "\nRun implementation edits, commands, and verification inside that exact directory. Source inspection may also use the Runner-approved read-only references listed above.\n"
		}
	}
	harnessStartedAt := time.Now()
	finishHarness := metrics.StartStage(ctx, metrics.StageHarnessRun)
	schema := executionContentSchemaForVerification(len(assignment.Spec.RequiredVerification))
	result, lastMessage, usage, failureEvidence, runErr := e.runHarness(
		ctx,
		e.profileProjectArgs(profile, launchWorkspace, schema),
		launchWorkspace.Dir,
		strings.NewReader(prompt),
		schema,
		"prefer",
	)
	harnessDuration := time.Since(harnessStartedAt).Milliseconds()
	summary := summarizeHarnessResult(result, lastMessage)
	var structured StructuredExecutionResult
	var structuredErr error
	if runErr == nil {
		finishStageFromOutput(finishHarness, Output{Outcome: OutcomeSucceeded}, nil, usage)
		structured, structuredErr = assembleExecutionContent(assignment, lastMessage)
	} else if classified, known := classifyHarnessFailure(runErr, failureEvidence); known {
		finishStageFromOutput(finishHarness, classified, runErr, usage)
	} else {
		finishStageFromOutput(finishHarness, blockedOutputWithFailure("Harness execution failed.", FailureUnknown, RetryNone), runErr, usage)
	}

	verifyCtx, cancelVerify := postHarnessContext(ctx, e.timeout())
	defer cancelVerify()
	finishVerify := metrics.StartStage(verifyCtx, metrics.StageWorkspaceVerify)
	verifyErr := newWorkspaceVerifier(e.run, e.timeout(), snapshotLimits(e.config.ResourceLimits)).Verify(verifyCtx, metadata)
	if verifyErr != nil {
		output := blockedOutputWithFailure(e.displayName()+" workspace-write integrity verification failed.", FailureIntegrityViolation, RetryNone)
		finishStageFromOutput(finishVerify, output, verifyErr, metrics.Usage{})
		output.Usage = usage
		output.HarnessDurationMilliseconds = harnessDuration
		causes := []error{verifyErr}
		if runErr != nil {
			causes = append([]error{fmt.Errorf("run %s workspace-write: %w", e.kind, runErr)}, causes...)
		}
		if structuredErr != nil {
			causes = append([]error{structuredErr}, causes...)
		}
		return output, errors.Join(causes...)
	}
	finishStageFromOutput(finishVerify, Output{Outcome: OutcomeSucceeded}, nil, metrics.Usage{})

	if runErr != nil {
		if output, known := classifyHarnessFailure(runErr, failureEvidence); known {
			output.Usage = usage
			output.HarnessDurationMilliseconds = harnessDuration
			return output, fmt.Errorf("run %s workspace-write: %w", e.kind, runErr)
		}
		output := blockedOutputWithFailure(e.displayName()+" workspace-write failed: "+summary, FailureUnknown, RetryNone)
		output.Usage = usage
		output.HarnessDurationMilliseconds = harnessDuration
		return output, fmt.Errorf("run %s workspace-write: %w", e.kind, runErr)
	}
	if structuredErr != nil {
		output := blockedOutputWithFailure(e.displayName()+" workspace-write returned invalid execution content: "+structuredErr.Error(), FailureInvalidContract, RetryNone)
		output.Usage = usage
		output.HarnessDurationMilliseconds = harnessDuration
		return output, structuredErr
	}
	output := structuredExecutorOutput(structured)
	output.Usage = usage
	output.HarnessDurationMilliseconds = harnessDuration
	return output, nil
}

func (e AgentExecutor) runHarness(ctx context.Context, args []string, workingDir string, input io.Reader, schema []byte, piConstrainedSamplingStrict string) (subprocess.Result, string, metrics.Usage, HarnessFailureEvidence, error) {
	return e.runHarnessWithPiTransport(ctx, args, workingDir, input, schema, piConstrainedSamplingStrict, false)
}

func (e AgentExecutor) runHarnessWithPiTransport(ctx context.Context, args []string, workingDir string, input io.Reader, schema []byte, piConstrainedSamplingStrict string, piDirectNative bool) (subprocess.Result, string, metrics.Usage, HarnessFailureEvidence, error) {
	var cleanups []func()
	var artifactVerifiers []func() error
	cleanupArtifacts := func() {
		for index := len(cleanups) - 1; index >= 0; index-- {
			cleanups[index]()
		}
	}
	piProvenance := ""
	structuredPi := e.kind == config.HarnessPiCLI && len(schema) > 0
	piNativeStructured := structuredPi && piUsesNativeStructuredOutput(e.cfg.Model)
	if piDirectNative && !piNativeStructured {
		return subprocess.Result{}, "", metrics.Usage{}, HarnessFailureEvidence{}, errors.New("direct native structured output requires an LM Studio Pi model")
	}
	if structuredPi {
		var err error
		var channel *piStructuredResultChannel
		qwenThinkingControls := piUsesQwenThinkingControls(e.cfg.Model)
		if piDirectNative {
			channel, err = createPiDirectNativeStructuredResultExtension(schema, qwenThinkingControls)
		} else if piNativeStructured {
			channel, err = createPiNativeStructuredResultExtension(schema, e.cfg.ReasoningEffort, e.config.PreserveReasoning, qwenThinkingControls)
		} else {
			ctx, err = subprocess.WithEnvironmentVariable(ctx, "PI_EXPERIMENTAL", "1")
			if err == nil {
				channel, err = createPiStructuredResultExtension(schema, piConstrainedSamplingStrict)
			}
		}
		if err != nil {
			return subprocess.Result{}, "", metrics.Usage{}, HarnessFailureEvidence{}, err
		}
		cleanups = append(cleanups, func() { _ = channel.Close() })
		artifactVerifiers = append(artifactVerifiers, channel.Verify)
		piProvenance = channel.provenance
		args, err = addPiStructuredResultExtension(args, channel.path)
		if err != nil {
			cleanupArtifacts()
			return subprocess.Result{}, "", metrics.Usage{}, HarnessFailureEvidence{}, err
		}
	}
	if e.kind == config.HarnessPiCLI && e.config.SafeTools && piInvocationAllowsBrowser(args, inheritsHarnessConfiguration(e.config.HarnessConfigMode)) {
		channel, browserErr := createPiBrowserExtension()
		if browserErr != nil {
			cleanupArtifacts()
			return subprocess.Result{}, "", metrics.Usage{}, HarnessFailureEvidence{}, browserErr
		}
		cleanups = append(cleanups, func() { _ = channel.Close() })
		artifactVerifiers = append(artifactVerifiers, channel.Verify)
		var addErr error
		args, addErr = addPiBrowserExtensionForConfig(args, channel.path, e.config.HarnessConfigMode)
		if addErr != nil {
			cleanupArtifacts()
			return subprocess.Result{}, "", metrics.Usage{}, HarnessFailureEvidence{}, addErr
		}
	}
	defer cleanupArtifacts()
	command := strings.TrimSpace(e.cfg.Command)
	var result subprocess.Result
	var err error
	if e.kind == config.HarnessPiCLI {
		filter := keepPiTextEventLine
		if structuredPi {
			filter = keepPiStructuredEventLine
			if piNativeStructured {
				filter = keepPiNativeStructuredEventLine
			}
		}
		result, err = subprocess.RunLineFilteredInput(ctx, e.run, command, args, workingDir, e.timeout(), input, maxHarnessResultBytes, harnessTruncationMarker, filter)
	} else {
		result, err = subprocess.RunBoundedInput(ctx, e.run, command, args, workingDir, e.timeout(), input, maxHarnessResultBytes, harnessTruncationMarker)
	}
	for _, verify := range artifactVerifiers {
		if artifactErr := verify(); err == nil && artifactErr != nil {
			err = fmt.Errorf("verify Pi Runner extension: %w", artifactErr)
		}
	}
	lastMessage, usage, parseErr := extractHarnessResultAndUsage(e.kind, result.Stdout, piProvenance, piNativeStructured, piDirectNative)
	failureEvidence := HarnessFailureEvidence{}
	if e.kind == config.HarnessClaudeCLI {
		failureEvidence = claudeFailureEvidenceFromStdout(result.Stdout)
		if err == nil && failureEvidence.FailureClass != FailureNone {
			err = errors.New("Claude Code reported a structured provider failure")
		}
	}
	if err == nil && parseErr != nil {
		err = parseErr
	}
	return result, lastMessage, usage, failureEvidence, err
}

func (e AgentExecutor) profileProjectArgs(profile ExecutionProfile, workspace profileWorkspace, schema []byte) []string {
	switch e.kind {
	case config.HarnessClaudeCLI:
		args := claudeProfileArgsForConfig(profile, workspace, e.config.SafeTools, e.config.HarnessConfigMode)
		args = append(args, "--json-schema", string(schema))
		args = appendHarnessModelArgs(args, e.cfg, true)
		return args
	case config.HarnessPiCLI:
		args := piProfileArgsForModelAndConfig(profile, e.cfg.Model, e.config.HarnessConfigMode)
		args = appendHarnessModelArgs(args, e.cfg, false)
		if effort := strings.TrimSpace(e.cfg.ReasoningEffort); effort != "" {
			args = append(args, "--thinking", effort)
		}
		return args
	default:
		return nil
	}
}

func appendHarnessModelArgs(args []string, cfg config.HarnessConfig, supportsEffort bool) []string {
	if cfg.Model != nil && strings.TrimSpace(*cfg.Model) != "" {
		args = append(args, "--model", strings.TrimSpace(*cfg.Model))
	}
	if supportsEffort && strings.TrimSpace(cfg.ReasoningEffort) != "" {
		args = append(args, "--effort", strings.TrimSpace(cfg.ReasoningEffort))
	}
	return args
}

func extractHarnessResultAndUsage(kind, stdout, piProvenance string, piNativeStructured, piDirectNative bool) (string, metrics.Usage, error) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return "", metrics.Usage{}, errors.New("harness returned no final output")
	}
	if kind == config.HarnessClaudeCLI {
		var envelope claudeResultEnvelope
		if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
			return "", metrics.Usage{}, fmt.Errorf("decode Claude result envelope: %w", err)
		}
		usage := usageFromClaudeEnvelope(envelope)
		if len(envelope.StructuredOutput) > 0 && string(envelope.StructuredOutput) != "null" {
			return string(envelope.StructuredOutput), usage, nil
		}
		if strings.TrimSpace(envelope.Result) != "" {
			return strings.TrimSpace(envelope.Result), usage, nil
		}
		return "", usage, errors.New("Claude result envelope contains no result")
	}
	if kind == config.HarnessPiCLI {
		usage, usageErr := usageFromPiEventStream(trimmed)
		var result string
		var err error
		if piProvenance == "" {
			result, err = extractPiTextResult(trimmed)
		} else if piDirectNative {
			result, err = extractPiDirectNativeStructuredResult(trimmed)
		} else if piNativeStructured {
			result, err = extractPiNativeStructuredResult(trimmed, piProvenance)
		} else {
			result, err = extractPiStructuredResult(trimmed, piProvenance)
		}
		if err != nil {
			if usageErr != nil {
				return result, metrics.Usage{}, errors.Join(err, usageErr)
			}
			return result, usage, err
		}
		// Usage is operational evidence, not part of the ordinary structured
		// result contract. Evaluation admission fails closed when it requires
		// unavailable counters; normal Pi work must not fail because a Pi event
		// version omitted or reshaped optional usage fields.
		if usageErr != nil {
			return result, metrics.Usage{}, nil
		}
		return result, usage, nil
	}
	return trimmed, metrics.Usage{}, nil
}

func buildHarnessPrompt(assignment Assignment, workspaceWrite bool, displayName string) string {
	var b strings.Builder
	b.WriteString(buildHarnessTaskPrompt(assignment, workspaceWrite, displayName))
	appendStructuredResultInstructions(&b)
	return b.String()
}

func buildHarnessTaskPrompt(assignment Assignment, workspaceWrite bool, displayName string) string {
	packet := assignment.Spec
	var b strings.Builder
	b.WriteString("You are executing one approved local Runner assignment through ")
	b.WriteString(displayName)
	b.WriteString(".\n")
	if workspaceWrite {
		b.WriteString("Runner has applied its fixed implementer profile in an isolated Git worktree.\n")
	} else {
		b.WriteString("Runner has applied its fixed read-only execution profile. This assignment expects an analysis or review result rather than implementation changes.\n")
	}
	b.WriteString("If the Runner-provided capabilities are insufficient, report the exact missing capability through the requested structured content.\n\n")
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
	if workspaceWrite && len(packet.RequiredVerification) > 0 {
		b.WriteString("\n\nRunner-owned proof obligations:\n")
		for _, verification := range packet.RequiredVerification {
			b.WriteString("- ")
			b.WriteString(verification)
			b.WriteByte('\n')
		}
		appendVerificationOwnershipInstructions(&b, len(packet.RequiredVerification))
	}
	return b.String()
}

func appendVerificationOwnershipInstructions(b *strings.Builder, approvedChecks int) {
	b.WriteString("These obligations define what must be proved, not how. After inspecting the repository, choose the smallest reliable proof method for each obligation and meaningful changed-behavior failure. Reuse existing focused tests and commands before creating anything new; add or update durable tests when that is the simplest reliable regression protection. Do not create a second test framework, overlapping coverage, a repository scratch script, or a custom verification harness.\n")
	b.WriteString("Do not substitute broader checks, repeat expensive passing evidence, or invent unrelated verification work. Stop when the approved behavior is complete and every obligation has reliable evidence. ")
	entryLabel := "entries"
	if approvedChecks == 1 {
		entryLabel = "entry"
	}
	fmt.Fprintf(b, "For a successful result, return exactly %d verification evidence %s: one for each obligation, in the same order. Combine related observations for the same obligation into its single entry.\n", approvedChecks, entryLabel)
}

func (e AgentExecutor) timeout() time.Duration {
	return time.Duration(e.cfg.TimeoutSeconds) * time.Second
}

func (e AgentExecutor) displayName() string {
	if e.kind == config.HarnessClaudeCLI {
		return "Claude Code"
	}
	return "Pi CLI"
}

func summarizeHarnessResult(result subprocess.Result, lastMessage string) string {
	parts := compactText([]string{lastMessage, result.Stderr, result.Stdout})
	if len(parts) == 0 {
		return fmt.Sprintf("exit code %d with no output", result.ExitCode)
	}
	return truncate(strings.Join(parts, " | "), 1000)
}
