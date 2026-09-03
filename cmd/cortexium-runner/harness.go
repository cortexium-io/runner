package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

const (
	conformanceRepository = "cortexium/harness-conformance"
	conformanceMarker     = "cortexium-runner-conformance-v1"
	conformanceWrite      = "runner-harness-write-ok\n"
	conformanceREADME     = "# Runner harness conformance\n\nmarker: " + conformanceMarker + "\n"
	conformanceBrowser    = `<!doctype html><html><body><main id="probe">runner-browser-conformance</main><script>document.body.dataset.runnerReady = "yes"; console.log("runner-browser-console-ok")</script></body></html>` + "\n"
)

var conformancePlannerSchema = []byte(`{
  "type": "object",
  "required": ["status", "marker"],
  "properties": {
    "status": {"type": "string", "const": "ready"},
    "marker": {"type": "string", "const": "cortexium-runner-conformance-v1"}
  },
  "additionalProperties": false
}`)

type harnessConformanceCheck struct {
	Ready   bool
	Skipped bool
	Detail  string
}

type harnessConformanceResult struct {
	Role          string
	Contract      string
	Harness       string
	Model         string
	Reasoning     string
	Access        string
	HarnessConfig string
	Executable    harnessConformanceCheck
	ContractCheck harnessConformanceCheck
	Browser       harnessConformanceCheck
}

func runHarness(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, "Usage: cortexium-runner harness check [--config PATH] [--browser] [--timeout DURATION]")
		fmt.Fprintln(stdout, "Run paid live conformance checks for every configured execution-role profile in a private temporary Git repository.")
		return nil
	}
	if args[0] != "check" {
		return fmt.Errorf("unknown harness command %q; use harness --help", args[0])
	}
	return runHarnessCheck(ctx, args[1:], stdout, nil)
}

func runHarnessCheck(ctx context.Context, args []string, stdout io.Writer, run subprocess.Runner) error {
	flags := newFlagSet("harness check", "cortexium-runner harness check [--config PATH] [--browser] [--timeout DURATION]", stdout)
	configPath := flags.String("config", "", "trusted operator config path; defaults to .cortexium/runner.json")
	browser := flags.Bool("browser", false, "also exercise configured implementer and reviewer browser access")
	timeout := flags.Duration("timeout", 5*time.Minute, "maximum duration of each live model call")
	proceed, err := parseFlags(flags, args, "harness check")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("harness check does not accept positional arguments")
	}
	if *timeout < time.Second {
		return errors.New("--timeout must be at least one second")
	}
	if run == nil {
		run = subprocess.OSRunner{}
	}

	*configPath = resolveRunnerConfigPath(*configPath, "")
	cfg, _, err := config.CheckTrustedConfig(*configPath)
	if err != nil {
		return fmt.Errorf("configuration is not ready: %w", err)
	}
	runtimeCfg, err := cfg.Resolve()
	if err != nil {
		return fmt.Errorf("resolve config for harness conformance: %w", err)
	}

	fixtureRoot, err := os.MkdirTemp("", "cortexium-runner-harness-check-")
	if err != nil {
		return fmt.Errorf("create private conformance directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(fixtureRoot) }()
	fixtureRepository := filepath.Join(fixtureRoot, "repository")
	if err := createHarnessConformanceFixture(ctx, run, fixtureRepository); err != nil {
		return err
	}

	fmt.Fprintln(stdout, "Harness conformance")
	fmt.Fprintf(stdout, "Configuration: %s\n", *configPath)
	fmt.Fprintln(stdout, "Live model calls: yes · Runner performs no GitHub operations and assigns a private temporary Git repository")
	if *browser {
		fmt.Fprintln(stdout, "Browser proof: enabled")
	} else {
		fmt.Fprintln(stdout, "Browser proof: not requested; use --browser to exercise it")
	}
	fmt.Fprintln(stdout)

	roleIDs := runtimeCfg.ExecutionRoleIDs()
	results := make([]harnessConformanceResult, 0, len(roleIDs))
	executables := map[string]harnessConformanceCheck{}
	for _, roleID := range roleIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		profile, ok := runtimeCfg.RoleProfile(roleID)
		if !ok {
			return fmt.Errorf("configured execution role %q has no resolved profile", roleID)
		}
		contract := runtimeCfg.RoleContract(roleID)
		execCfg := runtimeCfg.Execution(roleID, profile.Harness, fixtureRepository)
		if strings.TrimSpace(execCfg.Harness.Kind) == "" {
			return fmt.Errorf("configured execution role %q has no executable harness profile", roleID)
		}
		execCfg.WorkspaceBaseRef = "HEAD"
		execCfg.Harness.WorkspaceWriteRoot = filepath.Join(fixtureRoot, "worktrees")
		execCfg.Harness.TimeoutSeconds = int(timeout.Seconds())

		result := harnessConformanceResult{
			Role: roleID, Contract: contract, Harness: profile.Harness,
			Reasoning: strings.TrimSpace(profile.Reasoning),
			Access:    config.EffectiveRoleAccess(profile.Access), HarnessConfig: config.EffectiveHarnessConfigMode(profile.HarnessConfig),
		}
		if profile.Model != nil {
			result.Model = strings.TrimSpace(*profile.Model)
		}
		executableKey := strings.TrimSpace(execCfg.Harness.Command)
		if cached, exists := executables[executableKey]; exists {
			result.Executable = cached
		} else {
			result.Executable = inspectHarnessConformanceExecutable(ctx, run, execCfg.Harness)
			executables[executableKey] = result.Executable
		}
		if !result.Executable.Ready {
			result.ContractCheck = harnessConformanceCheck{Skipped: true, Detail: "not run because the configured executable is unavailable"}
			result.Browser = browserConformanceNotRun(contract, execCfg.SafeTools, *browser, "the live contract did not run")
			results = append(results, result)
			continue
		}

		writeProgress(stdout, fmt.Sprintf("Checking %s role %s through %s…", contract, roleID, profile.Harness))
		result.ContractCheck = checkHarnessRoleContract(ctx, run, execCfg, roleID, contract, fixtureRepository)
		if !result.ContractCheck.Ready {
			result.Browser = browserConformanceNotRun(contract, execCfg.SafeTools, *browser, "the live contract failed")
			results = append(results, result)
			continue
		}
		if !*browser || !execCfg.SafeTools || contract == config.WorkRolePlanner {
			result.Browser = browserConformanceNotRun(contract, execCfg.SafeTools, *browser, "")
			results = append(results, result)
			continue
		}

		writeProgress(stdout, fmt.Sprintf("Checking browser access for role %s through %s…", roleID, profile.Harness))
		result.Browser = checkHarnessBrowser(ctx, run, execCfg, roleID, contract, fixtureRepository)
		results = append(results, result)
	}

	ready := writeHarnessConformanceReport(stdout, results)
	if !ready {
		return errors.New("harness conformance failed; inspect the profile results above")
	}
	return nil
}

func createHarnessConformanceFixture(ctx context.Context, run subprocess.Runner, repository string) error {
	if err := os.Mkdir(repository, 0o700); err != nil {
		return fmt.Errorf("create conformance repository: %w", err)
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.name", "Cortexium Runner"},
		{"config", "user.email", "runner-conformance@example.invalid"},
	} {
		if _, err := subprocess.RunGit(ctx, run, args, repository, 30*time.Second); err != nil {
			return fmt.Errorf("initialize conformance repository with git %s: %w", strings.Join(args, " "), err)
		}
	}
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte(conformanceREADME), 0o600); err != nil {
		return fmt.Errorf("write conformance README: %w", err)
	}
	if err := os.WriteFile(filepath.Join(repository, "browser-probe.html"), []byte(conformanceBrowser), 0o600); err != nil {
		return fmt.Errorf("write conformance browser fixture: %w", err)
	}
	for _, args := range [][]string{
		{"add", "README.md", "browser-probe.html"},
		{"-c", "core.hooksPath=", "-c", "commit.gpgSign=false", "commit", "-m", "Create Runner harness conformance fixture"},
	} {
		if _, err := subprocess.RunGit(ctx, run, args, repository, 30*time.Second); err != nil {
			return fmt.Errorf("commit conformance fixture with git %s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

func inspectHarnessConformanceExecutable(ctx context.Context, run subprocess.Runner, harness config.HarnessConfig) harnessConformanceCheck {
	command := strings.TrimSpace(harness.Command)
	path, err := exec.LookPath(command)
	if err != nil {
		return harnessConformanceCheck{Detail: fmt.Sprintf("%s executable was not found in PATH", command)}
	}
	result, err := run.Run(ctx, path, []string{"--version"}, "", 5*time.Second)
	if err != nil {
		return harnessConformanceCheck{Detail: fmt.Sprintf("%s was found at %s but --version failed: %s", command, path, compactConformanceError(err))}
	}
	version := firstConformanceLine(result.Stdout, result.Stderr)
	detail := "found at " + path
	if version != "" {
		detail += " · " + version
	}
	return harnessConformanceCheck{Ready: true, Detail: detail}
}

func checkHarnessRoleContract(ctx context.Context, run subprocess.Runner, cfg config.ExecutionConfig, roleID, contract, repository string) harnessConformanceCheck {
	var err error
	switch contract {
	case config.WorkRolePlanner:
		err = checkHarnessPlanner(ctx, run, cfg, repository)
	case config.WorkRoleImplementer:
		err = checkHarnessImplementer(ctx, run, cfg, roleID, repository)
	case config.WorkRoleReviewer:
		err = checkHarnessReviewer(ctx, run, cfg, roleID, repository)
	default:
		err = fmt.Errorf("unsupported Runner role contract %q", contract)
	}
	if err != nil {
		return harnessConformanceCheck{Detail: compactConformanceError(err)}
	}
	detail := map[string]string{
		config.WorkRolePlanner:     "authentication, structured output, and read-only repository access succeeded",
		config.WorkRoleImplementer: "authentication, structured output, isolated worktree write, and integrity verification succeeded",
		config.WorkRoleReviewer:    "authentication, shared review contract, and read-only repository access succeeded",
	}[contract]
	return harnessConformanceCheck{Ready: true, Detail: detail}
}

func checkHarnessPlanner(ctx context.Context, run subprocess.Runner, cfg config.ExecutionConfig, repository string) error {
	prompt := "Use the runner-planner skill. Inspect README.md, make no changes, and return status ready with the exact marker " + conformanceMarker + "."
	result, err := execution.RunPlannerWithUsage(ctx, cfg.Harness.Kind, cfg, repository, prompt, conformancePlannerSchema, run)
	if err != nil {
		return err
	}
	canonical, err := execution.CanonicalizeStructuredResult(result.Message, "status", "marker")
	if err != nil {
		return fmt.Errorf("validate planner structured result: %w", err)
	}
	var decoded struct {
		Status string `json:"status"`
		Marker string `json:"marker"`
	}
	decoder := json.NewDecoder(strings.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("decode planner structured result: %w", err)
	}
	if decoded.Status != "ready" || decoded.Marker != conformanceMarker {
		return errors.New("planner returned an unexpected status or fixture marker")
	}
	return verifyHarnessConformanceRepository(ctx, run, repository)
}

func checkHarnessImplementer(ctx context.Context, run subprocess.Runner, cfg config.ExecutionConfig, roleID, repository string) error {
	assignment := execution.Assignment{Spec: execution.Spec{
		ID: "harness_check_" + roleID, ItemID: "harness_check_" + roleID,
		Repository: conformanceRepository, DelegatedContentDigest: "v1:harness-check:" + roleID,
		Task: execution.Task{
			Title:        "Prove Runner implementer conformance",
			Instructions: "Use the runner-implementer skill. Create conformance-write.txt containing exactly runner-harness-write-ok followed by a newline. Verify its exact content and run git diff --check. Make no other change.",
		},
		RequiredVerification: []string{"conformance-write.txt has the exact requested content", "git diff --check passes"},
	}}
	var prepared workspace.Metadata
	output, err := executeHarnessWorkspaceWrite(ctx, run, cfg, assignment, func(metadata workspace.Metadata) error {
		prepared = metadata
		return nil
	})
	if err != nil {
		return err
	}
	if output.Outcome != execution.OutcomeSucceeded {
		return fmt.Errorf("implementer returned outcome %q", output.Outcome)
	}
	content, err := os.ReadFile(filepath.Join(prepared.WorktreePath, "conformance-write.txt"))
	if err != nil {
		return fmt.Errorf("read implementer conformance artifact: %w", err)
	}
	if string(content) != conformanceWrite {
		return fmt.Errorf("implementer wrote unexpected conformance content %q", content)
	}
	return verifyHarnessConformanceRepository(ctx, run, repository)
}

func checkHarnessReviewer(ctx context.Context, run subprocess.Runner, cfg config.ExecutionConfig, roleID, repository string) error {
	criterion := "README.md contains the exact marker " + conformanceMarker
	assignment := execution.Assignment{Spec: execution.Spec{
		ID: "harness_check_" + roleID, ItemID: "harness_check_" + roleID,
		Repository: conformanceRepository, DelegatedContentDigest: "v1:harness-check:" + roleID,
		Task: execution.Task{
			Title:        "Prove Runner reviewer conformance",
			Instructions: "Use the runner-reviewer skill. Inspect README.md without changing the repository. Accept only if the required criterion, repository instructions, and maintainability checks pass.",
		},
		RequiredVerification: []string{criterion}, ReviewRequired: true,
	}}
	output, err := executeHarnessReadOnly(ctx, run, cfg, assignment)
	if err != nil {
		return err
	}
	if output.Outcome != execution.OutcomeSucceeded || output.ReviewAssessment == nil || output.ReviewAssessment.Verdict != "accept" {
		return fmt.Errorf("reviewer did not accept the known-good fixture; outcome=%q", output.Outcome)
	}
	return verifyHarnessConformanceRepository(ctx, run, repository)
}

func checkHarnessBrowser(ctx context.Context, run subprocess.Runner, cfg config.ExecutionConfig, roleID, contract, repository string) harnessConformanceCheck {
	var err error
	switch contract {
	case config.WorkRoleImplementer:
		err = checkHarnessImplementerBrowser(ctx, run, cfg, roleID, repository)
	case config.WorkRoleReviewer:
		err = checkHarnessReviewerBrowser(ctx, run, cfg, roleID, repository)
	default:
		return harnessConformanceCheck{Skipped: true, Detail: "not applicable to the planner contract"}
	}
	if err != nil {
		return harnessConformanceCheck{Detail: compactConformanceError(err)}
	}
	return harnessConformanceCheck{Ready: true, Detail: "the configured role rendered the temporary browser fixture successfully"}
}

func checkHarnessImplementerBrowser(ctx context.Context, run subprocess.Runner, cfg config.ExecutionConfig, roleID, repository string) error {
	browserInstruction := "Use only the navigate and evaluate tools in Runner's runner_browser MCP server. Call navigate directly; do not inspect list_mcp_resources and do not launch a browser through shell commands."
	if cfg.Harness.Kind == config.HarnessPiCLI {
		browserInstruction = "Use only the runner_browser_navigate and runner_browser_evaluate tools granted by Runner. Do not launch a browser through shell commands."
	}
	assignment := execution.Assignment{Spec: execution.Spec{
		ID: "harness_browser_" + roleID, ItemID: "harness_browser_" + roleID,
		Repository: conformanceRepository, DelegatedContentDigest: "v1:harness-browser:" + roleID,
		Task: execution.Task{
			Title:        "Prove Runner implementer browser conformance",
			Instructions: "Use the runner-implementer skill. " + browserInstruction + " Start a temporary localhost static server for the assigned repository, load browser-probe.html, and save the browser-rendered DOM to browser-probe-dom.txt. Verify that it contains runner-browser-conformance and data-runner-ready=\"yes\". Stop the server, remove temporary diagnostics, run git diff --check, and make no other change.",
		},
		RequiredVerification: []string{"the browser rendered the fixture and executed its JavaScript", "browser-probe-dom.txt contains both expected markers", "git diff --check passes"},
	}}
	var prepared workspace.Metadata
	output, err := executeHarnessWorkspaceWrite(ctx, run, cfg, assignment, func(metadata workspace.Metadata) error {
		prepared = metadata
		return nil
	})
	if err != nil {
		return err
	}
	if output.Outcome != execution.OutcomeSucceeded {
		return fmt.Errorf("implementer browser check returned outcome %q", output.Outcome)
	}
	content, err := os.ReadFile(filepath.Join(prepared.WorktreePath, "browser-probe-dom.txt"))
	if err != nil {
		return fmt.Errorf("read browser conformance artifact: %w", err)
	}
	for _, marker := range []string{"runner-browser-conformance", `data-runner-ready="yes"`} {
		if !strings.Contains(string(content), marker) {
			return fmt.Errorf("browser conformance artifact omitted %q", marker)
		}
	}
	return verifyHarnessConformanceRepository(ctx, run, repository)
}

func checkHarnessReviewerBrowser(ctx context.Context, run subprocess.Runner, cfg config.ExecutionConfig, roleID, repository string) error {
	browserInstruction := "Use only the navigate and evaluate tools in Runner's runner_browser MCP server. Call navigate directly; do not inspect list_mcp_resources and do not launch a browser through shell commands."
	if cfg.Harness.Kind == config.HarnessPiCLI {
		browserInstruction = "Use only the runner_browser_navigate and runner_browser_evaluate tools granted by Runner. Do not launch a browser through shell commands."
	}
	assignment := execution.Assignment{Spec: execution.Spec{
		ID: "harness_browser_" + roleID, ItemID: "harness_browser_" + roleID,
		Repository: conformanceRepository, DelegatedContentDigest: "v1:harness-browser:" + roleID,
		Task: execution.Task{
			Title:        "Prove Runner reviewer browser conformance",
			Instructions: "Use the runner-reviewer skill. " + browserInstruction + " Start a temporary localhost static server for the assigned repository, load browser-probe.html, and inspect the rendered DOM and console. Then stop the server and remove temporary artifacts without changing the repository.",
		},
		RequiredVerification: []string{"the page renders runner-browser-conformance, JavaScript sets data-runner-ready to yes, and the console contains runner-browser-console-ok"},
		ReviewRequired:       true,
	}}
	output, err := executeHarnessReadOnly(ctx, run, cfg, assignment)
	if err != nil {
		return err
	}
	if output.Outcome != execution.OutcomeSucceeded || output.ReviewAssessment == nil || output.ReviewAssessment.Verdict != "accept" {
		return fmt.Errorf("reviewer browser check did not accept the known-good fixture; outcome=%q", output.Outcome)
	}
	return verifyHarnessConformanceRepository(ctx, run, repository)
}

func executeHarnessReadOnly(ctx context.Context, run subprocess.Runner, cfg config.ExecutionConfig, assignment execution.Assignment) (execution.Output, error) {
	if cfg.Harness.Kind == config.HarnessCodexCLI {
		return execution.NewCodexExecutor(cfg, run).Execute(ctx, assignment)
	}
	return execution.NewAgentExecutor(cfg.Harness.Kind, cfg, run).Execute(ctx, assignment)
}

func executeHarnessWorkspaceWrite(ctx context.Context, run subprocess.Runner, cfg config.ExecutionConfig, assignment execution.Assignment, prepared func(workspace.Metadata) error) (execution.Output, error) {
	if cfg.Harness.Kind == config.HarnessCodexCLI {
		return execution.NewCodexExecutor(cfg, run).ExecuteWorkspaceWrite(ctx, assignment, prepared)
	}
	return execution.NewAgentExecutor(cfg.Harness.Kind, cfg, run).ExecuteWorkspaceWrite(ctx, assignment, prepared)
}

func verifyHarnessConformanceRepository(ctx context.Context, run subprocess.Runner, repository string) error {
	for name, expected := range map[string]string{"README.md": conformanceREADME, "browser-probe.html": conformanceBrowser} {
		content, err := os.ReadFile(filepath.Join(repository, name))
		if err != nil {
			return fmt.Errorf("read conformance fixture %s: %w", name, err)
		}
		if string(content) != expected {
			return fmt.Errorf("live role changed the conformance fixture %s", name)
		}
	}
	result, err := subprocess.RunGit(ctx, run, []string{"status", "--porcelain", "--untracked-files=all"}, repository, 30*time.Second)
	if err != nil {
		return fmt.Errorf("inspect conformance repository after live role: %w", err)
	}
	if strings.TrimSpace(result.Stdout) != "" {
		return fmt.Errorf("live role changed the source conformance repository: %s", strings.TrimSpace(result.Stdout))
	}
	return nil
}

func browserConformanceNotRun(contract string, safeTools, requested bool, reason string) harnessConformanceCheck {
	if contract == config.WorkRolePlanner {
		return harnessConformanceCheck{Skipped: true, Detail: "not applicable to the planner contract"}
	}
	if !safeTools {
		return harnessConformanceCheck{Skipped: true, Detail: "not configured for this role"}
	}
	if strings.TrimSpace(reason) != "" {
		return harnessConformanceCheck{Skipped: true, Detail: "not exercised because " + reason}
	}
	if !requested {
		return harnessConformanceCheck{Skipped: true, Detail: "available to this profile but not exercised; rerun with --browser"}
	}
	return harnessConformanceCheck{Skipped: true, Detail: "not exercised"}
}

func writeHarnessConformanceReport(output io.Writer, results []harnessConformanceResult) bool {
	ready := len(results) > 0
	fmt.Fprintln(output, "Results")
	for _, result := range results {
		profile := result.Harness
		if result.Model != "" {
			profile += " / " + result.Model
		}
		fmt.Fprintf(output, "  %s (%s) · %s\n", result.Role, result.Contract, profile)
		policy := result.Access + "/" + result.HarnessConfig
		if result.Access == config.RoleAccessHost && result.HarnessConfig == config.HarnessConfigModeInherit {
			policy += " · unrestricted host execution"
		} else if result.Access == config.RoleAccessHost {
			policy += " · host execution"
		} else if result.Harness == config.HarnessPiCLI {
			policy += " · fixed read-only Pi profile · no native OS sandbox"
		} else if result.HarnessConfig == config.HarnessConfigModeInherit {
			policy += " · native harness sandbox · ambient configuration enabled"
		} else {
			policy += " · native harness sandbox"
		}
		if result.Harness == config.HarnessPiCLI && result.Access == config.RoleAccessHost {
			policy += " · Pi has no native OS sandbox"
		}
		fmt.Fprintf(output, "    Policy: %s\n", policy)
		if result.Reasoning != "" {
			fmt.Fprintf(output, "    Reasoning: %s\n", result.Reasoning)
		}
		writeHarnessConformanceCheck(output, "Executable", result.Executable)
		writeHarnessConformanceCheck(output, "Live contract", result.ContractCheck)
		writeHarnessConformanceCheck(output, "Browser", result.Browser)
		if !result.Executable.Ready || !result.ContractCheck.Ready || (!result.Browser.Skipped && !result.Browser.Ready) {
			ready = false
		}
	}
	fmt.Fprintln(output)
	if ready {
		writeStateLine(output, toneSuccess, "Harness conformance: passed")
	} else {
		writeStateLine(output, toneFailure, "Harness conformance: failed")
	}
	return ready
}

func writeHarnessConformanceCheck(output io.Writer, label string, check harnessConformanceCheck) {
	marker := "✗"
	tone := toneFailure
	if check.Ready {
		marker = "✓"
		tone = toneSuccess
	} else if check.Skipped {
		marker = "○"
		tone = toneMuted
	}
	writeStateLine(output, tone, "    %s %s: %s", marker, label, check.Detail)
}

func firstConformanceLine(values ...string) string {
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				return line
			}
		}
	}
	return ""
}

func compactConformanceError(err error) string {
	if err == nil {
		return "unknown failure"
	}
	value := strings.Join(strings.Fields(err.Error()), " ")
	if len(value) > 1000 {
		value = value[:1000] + "..."
	}
	return value
}
