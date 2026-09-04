package execution

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type StructuredHarnessResult struct {
	Message              string
	Usage                metrics.Usage
	DurationMilliseconds int64
	FailureClass         FailureClass
	RetryDisposition     RetryDisposition
	RetryAfter           string
}

// AutomaticRetryOutput restores the fixed, remotely safe failure report for a
// structured harness call. Structured planner and reviewer stages retain only
// typed recovery fields, so callers must not derive retry authority from the
// returned error text.
func (r StructuredHarnessResult) AutomaticRetryOutput() (Output, bool) {
	if r.RetryDisposition != RetryAutomatic {
		return Output{}, false
	}
	output, known := classifyHarnessFailure(nil, HarnessFailureEvidence{
		FailureClass: r.FailureClass, RetryDisposition: r.RetryDisposition, RetryAfter: r.RetryAfter,
	})
	if !known {
		return Output{}, false
	}
	output.Usage = r.Usage
	output.HarnessDurationMilliseconds = r.DurationMilliseconds
	return output, true
}

func RunPlannerWithUsage(ctx context.Context, kind string, cfg config.ExecutionConfig, workingDir, prompt string, schema []byte, run subprocess.Runner) (StructuredHarnessResult, error) {
	return runStructuredHarness(ctx, RolePlanner, kind, cfg, workingDir, prompt, schema, "prefer", metrics.StageHarnessRun, run)
}

// RunPlannerStageWithUsage performs the repository-aware half of the shared
// two-stage planning contract for any supported harness.
func RunPlannerStageWithUsage(ctx context.Context, kind string, cfg config.ExecutionConfig, workingDir, prompt string, schema []byte, run subprocess.Runner) (StructuredHarnessResult, error) {
	return runStructuredHarness(ctx, RolePlanner, kind, cfg, workingDir, prompt, schema, "require", metrics.StagePlannerOutline, run)
}

// RunPlannerSynthesisStageWithUsage exposes no repository or shell tools. It
// turns the Runner-validated outline into one fixed-key details response.
func RunPlannerSynthesisStageWithUsage(ctx context.Context, kind string, cfg config.ExecutionConfig, prompt string, schema []byte, run subprocess.Runner) (StructuredHarnessResult, error) {
	return runStructuredHarness(ctx, RoleSynthesis, kind, cfg, cfg.Harness.WorkingDir, prompt, schema, "require", metrics.StagePlannerDetails, run)
}

// RunProbeWithUsage invokes only the dedicated tool-free probe profile. It
// never borrows the capabilities of the role whose model settings are probed.
func RunProbeWithUsage(ctx context.Context, kind string, cfg config.ExecutionConfig, prompt string, schema []byte, run subprocess.Runner) (StructuredHarnessResult, error) {
	// A probe borrows model selection and timeout, never the configured role's
	// broader access. This is especially important for host-access Pi roles.
	cfg.RoleAccess = config.RoleAccessSandboxed
	cfg.HarnessConfigMode = config.HarnessConfigModeIsolated
	return runStructuredHarness(ctx, RoleProbe, kind, cfg, cfg.Harness.WorkingDir, prompt, schema, "prefer", metrics.StageHarnessRun, run)
}

func runStructuredHarness(ctx context.Context, role RoleContract, kind string, cfg config.ExecutionConfig, workingDir, prompt string, schema []byte, piConstrainedSamplingStrict, stageName string, run subprocess.Runner) (StructuredHarnessResult, error) {
	if run == nil {
		run = subprocess.OSRunner{}
	}
	if len(schema) == 0 {
		return failedStructuredHarnessResult(FailureInvalidContract, RetryNone), fmt.Errorf("%s structured output schema is required", role)
	}
	if err := validateExecutionHarness(kind, cfg.Harness); err != nil {
		return failedStructuredHarnessResult(FailureInvalidConfiguration, RetryNone), err
	}
	profile, err := ProfileForRole(role, cfg.RoleAccess)
	if err != nil {
		return failedStructuredHarnessResult(FailureInvalidConfiguration, RetryNone), err
	}
	if err := ValidateHarnessProfile(kind, role, cfg.RoleAccess, cfg.HarnessConfigMode); err != nil {
		return failedStructuredHarnessResult(FailureCapabilityUnavailable, RetryNone), err
	}
	if err := ensureHarnessAdvertisesProfile(ctx, run, strings.TrimSpace(cfg.Harness.Command), kind, role, cfg.RoleAccess, cfg.HarnessConfigMode); err != nil {
		return failedStructuredHarnessResult(FailureCapabilityUnavailable, RetryNone), err
	}
	protectedRoots := append([]string{cfg.Harness.WorkspaceWriteRoot}, cfg.ReferenceProtectedRoots...)
	workspace, err := prepareExecutionWorkspace(ctx, run, profile, workingDir, cfg.RepositoryReferences, protectedRoots...)
	if err != nil {
		return failedStructuredHarnessResult(FailureCapabilityUnavailable, RetryNone), err
	}
	defer workspace.cleanup()
	if role == RolePlanner || role == RoleReviewer {
		prompt += trustedSkillInstructions(cfg)
	}
	if kind == config.HarnessCodexCLI {
		prompt += codexMCPPromptForConfig(cfg.MCPServers, cfg.SafeTools, cfg.HarnessConfigMode)
	} else if kind == config.HarnessClaudeCLI {
		prompt += runnerBrowserPrompt(cfg.SafeTools)
	}
	prompt += profileRepositoryInstruction(workspace)
	switch kind {
	case config.HarnessCodexCLI:
		artifacts, err := newStructuredResultArtifacts("runner-plan", schema)
		if err != nil {
			return failedStructuredHarnessResult(FailureCapabilityUnavailable, RetryNone), err
		}
		defer artifacts.close()
		harness := cfg.Harness
		command := strings.TrimSpace(harness.Command)
		timeout := harnessTimeout(harness)
		mcpArgs, err := codexMCPProfileArgsForConfig(ctx, run, command, workspace, cfg.MCPServers, cfg.SafeTools, cfg.HarnessConfigMode)
		if err != nil {
			return failedStructuredHarnessResult(FailureCapabilityUnavailable, RetryManual), err
		}
		args := codexProfileArgsForConfig(profile, workspace, cfg.SafeTools, cfg.HarnessConfigMode, command)
		if harness.Model != nil && strings.TrimSpace(*harness.Model) != "" {
			args = append(args, "--model", strings.TrimSpace(*harness.Model))
		}
		if effort := strings.TrimSpace(harness.ReasoningEffort); effort != "" {
			args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
		}
		args = append(args, codexExecArgsForConfig(profile, workspace, cfg.HarnessConfigMode)...)
		// Codex 0.153 must receive invocation MCP overrides after
		// exec --ignore-user-config or its stdio handshake can stall.
		args = append(args, mcpArgs...)
		args = append(args, "--output-last-message", artifacts.outputPath(), "--output-schema", artifacts.schemaPath())
		startedAt := time.Now()
		finishHarness := metrics.StartStage(ctx, stageName)
		result, runErr := subprocess.RunBoundedHeadTailInput(ctx, run, command, args, workspace.Dir, timeout, strings.NewReader(prompt), maxHarnessDiagnosticBytes, harnessTruncationMarker)
		duration := time.Since(startedAt).Milliseconds()
		usage := parseCodexUsage(result.Stdout)
		if runErr != nil {
			output, known := classifyHarnessFailure(runErr, codexFailureEvidenceFromStdout(result.Stdout))
			if !known {
				output = blockedOutputWithFailure("Harness execution failed.", FailureUnknown, RetryNone)
			}
			finishStageFromOutput(finishHarness, output, runErr, usage)
			return structuredHarnessFailure(output, usage, duration), fmt.Errorf("run Codex %s: %w", role, commandFailure(runErr, result))
		}
		finishStageFromOutput(finishHarness, Output{Outcome: OutcomeSucceeded}, nil, usage)
		data, err := artifacts.readResult()
		if err != nil {
			failed := failedStructuredHarnessResult(FailureInvalidContract, RetryManual)
			failed.Usage = usage
			failed.DurationMilliseconds = duration
			return failed, fmt.Errorf("read Codex %s result: %w", role, err)
		}
		return StructuredHarnessResult{Message: strings.TrimSpace(data), Usage: usage, DurationMilliseconds: duration}, nil
	case config.HarnessClaudeCLI, config.HarnessPiCLI:
		executor := NewAgentExecutor(kind, cfg, run)
		args := []string{}
		piDirectNative := kind == config.HarnessPiCLI && role == RoleSynthesis && piUsesNativeStructuredOutput(executor.cfg.Model)
		if kind == config.HarnessClaudeCLI {
			args = claudeProfileArgsForConfig(profile, workspace, cfg.SafeTools, cfg.HarnessConfigMode)
			args = append(args, "--json-schema", string(schema))
			args = appendHarnessModelArgs(args, executor.cfg, true)
		} else {
			if piDirectNative {
				args = piDirectNativeProfileArgsForConfig(profile, cfg.HarnessConfigMode)
			} else {
				args = piProfileArgsForModelAndConfig(profile, executor.cfg.Model, cfg.HarnessConfigMode)
			}
			args = appendHarnessModelArgs(args, executor.cfg, false)
			if effort := strings.TrimSpace(executor.cfg.ReasoningEffort); effort != "" {
				args = append(args, "--thinking", effort)
			}
		}
		startedAt := time.Now()
		finishHarness := metrics.StartStage(ctx, stageName)
		result, lastMessage, usage, failureEvidence, runErr := executor.runHarnessWithPiTransport(ctx, args, workspace.Dir, strings.NewReader(prompt), schema, piConstrainedSamplingStrict, piDirectNative)
		duration := time.Since(startedAt).Milliseconds()
		if runErr != nil {
			output, known := classifyHarnessFailure(runErr, failureEvidence)
			if !known {
				output = blockedOutputWithFailure("Harness execution failed.", FailureUnknown, RetryNone)
			}
			finishStageFromOutput(finishHarness, output, runErr, usage)
			return structuredHarnessFailure(output, usage, duration), fmt.Errorf("run %s %s: %w", kind, role, commandFailure(runErr, result))
		}
		finishStageFromOutput(finishHarness, Output{Outcome: OutcomeSucceeded}, nil, usage)
		return StructuredHarnessResult{Message: lastMessage, Usage: usage, DurationMilliseconds: duration}, nil
	default:
		return failedStructuredHarnessResult(FailureInvalidConfiguration, RetryNone), fmt.Errorf("unsupported %s harness %q", role, kind)
	}
}

func failedStructuredHarnessResult(class FailureClass, retry RetryDisposition) StructuredHarnessResult {
	return StructuredHarnessResult{FailureClass: class, RetryDisposition: retry}
}

func structuredHarnessFailure(output Output, usage metrics.Usage, duration int64) StructuredHarnessResult {
	return StructuredHarnessResult{
		Usage: usage, DurationMilliseconds: duration,
		FailureClass: output.FailureClass, RetryDisposition: output.RetryDisposition, RetryAfter: output.RetryAfter,
	}
}

func harnessTimeout(cfg config.HarnessConfig) time.Duration {
	return time.Duration(cfg.TimeoutSeconds) * time.Second
}

func commandFailure(err error, result subprocess.Result) error {
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout)
	}
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, truncate(detail, 1000))
}
