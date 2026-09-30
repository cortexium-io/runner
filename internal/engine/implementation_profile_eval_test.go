package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

type implementationProfileFixture struct {
	name, source, tests, task, oracleFailure string
}

// Reuse the requirement-derived record fixtures, not a new benchmark framework.
func implementationProfileFixtures(t *testing.T) []implementationProfileFixture {
	t.Helper()
	reviews := reviewerEvalScenarios(t, false)
	stub := "package records\n\nimport \"errors\"\n\ntype Record struct { Tenant, Title string }\n\nfunc Update(records map[string]Record, tenant, id, title string) error { return errors.New(\"not implemented\") }\n"
	return []implementationProfileFixture{
		{"implement_record_update", stub, reviews[0].tests, "Implement Update in records.go to satisfy the documented record-editing contract.", "TestUpdatePreservesOwnership"},
		{"repair_tenant_access", reviews[1].source, reviews[1].tests, "Fix the unauthorized cross-tenant record update. Preserve valid edits and record ownership; add focused regression coverage.", "TestUpdateRejectsInvalidRequests/foreign_tenant"},
		{"repair_ownership_loss", reviews[2].source, reviews[2].tests, "Fix the loss of tenant ownership during a valid title edit. Preserve the existing access checks and invalid-request behavior; add focused regression coverage.", "TestUpdatePreservesOwnership"},
	}
}

func implementationProfileRepository(t *testing.T, fixture implementationProfileFixture) string {
	t.Helper()
	repo := t.TempDir()
	reviewerFixtureGit(t, repo, "init", "-b", "main")
	reviewerFixtureGit(t, repo, "config", "user.name", "Test User")
	reviewerFixtureGit(t, repo, "config", "user.email", "test@example.com")
	writeReviewerEvalFile(t, repo, "go.mod", "module example.com/records\n\ngo 1.25.0\n")
	writeReviewerEvalFile(t, repo, "README.md", strings.Join(recordUpdateProofs, " ")+"\nThe tenant argument is authenticated upstream. Store titles as supplied. Concurrent map access is outside this component's contract.\n")
	writeReviewerEvalFile(t, repo, "records.go", fixture.source)
	writeReviewerEvalFile(t, repo, "records_test.go", fixture.tests)
	reviewerFixtureGit(t, repo, "add", ".")
	reviewerFixtureGit(t, repo, "commit", "-m", "Prepare implementation comparison fixture")
	return repo
}

func TestImplementationProfileFixturesExposeSeededFaults(t *testing.T) {
	for _, fixture := range implementationProfileFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			repo := implementationProfileRepository(t, fixture)
			writeReviewerEvalFile(t, repo, "records_test.go", reviewerEvalScenarios(t, false)[0].tests)
			output, _, err := runReviewerFixtureTests(t.Context(), repo)
			if err == nil || !strings.Contains(output, fixture.oracleFailure) {
				t.Fatalf("reference assertions did not expose %s: %v\n%s", fixture.name, err, output)
			}
		})
	}
}

// This separately authorized, once-only comparison has one shared budget. Normal
// tests never spend model quota. No planning, model review, or live Project runs.
func TestLiveImplementationProfileComparison(t *testing.T) {
	if os.Getenv("CORTEXIUM_RUNNER_IMPLEMENTATION_COMPARISON") != "1" {
		t.Skip("implementation profile comparison requires explicit admission")
	}
	candidate := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_EVAL_CANDIDATE"))
	if candidate == "" || candidate != strings.TrimSpace(reviewerFixtureGit(t, ".", "rev-parse", "HEAD")) || strings.TrimSpace(reviewerFixtureGit(t, ".", "status", "--porcelain", "--untracked-files=all")) != "" {
		t.Fatal("comparison requires the exact clean committed candidate")
	}
	settings := evalSettings{Candidate: candidate, Run: 1, ArtifactPath: os.Getenv("CORTEXIUM_RUNNER_EVAL_ARTIFACT"), MaxAttempts: 9, MaxTokens: 900000, CaseTimeout: 5 * time.Minute, AggregateTime: 45 * time.Minute}
	if !filepath.IsAbs(settings.ArtifactPath) {
		t.Fatal("comparison requires an absolute, new private artifact path outside the candidate")
	}
	for _, harness := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI} {
		if _, err := exec.LookPath(harness); err != nil {
			t.Fatalf("comparison harness %s is not installed", harness)
		}
		version, err := exec.CommandContext(t.Context(), harness, "--version").Output()
		if err != nil {
			t.Fatalf("comparison harness %s version check failed", harness)
		}
		t.Logf("comparison harness %s: %s", harness, strings.TrimSpace(string(version)))
	}
	// Exercise the installed Codex sandbox before admitting any model work.
	// This exact test selection cannot execute a live model comparison.
	root := strings.TrimSpace(reviewerFixtureGit(t, ".", "rev-parse", "--show-toplevel"))
	if err := implementationProfileToolchainPreflight(t.Context(), subprocess.OSRunner{}, root, settings.ArtifactPath); err != nil {
		t.Fatal(err)
	}
	coordinator, err := newEvalCoordinator(settings, os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	passed := false
	defer func() {
		coordinator.finish(passed)
		if err := coordinator.Close(); err != nil {
			t.Error(err)
		}
	}()
	profiles := []struct{ harness, model, effort string }{
		{config.HarnessCodexCLI, "gpt-6-sol", "high"},
		{config.HarnessCodexCLI, "gpt-6.1-sol", "medium"},
		{config.HarnessClaudeCLI, "claude-opus-5-5", "medium"},
	}
	for caseIndex, fixture := range implementationProfileFixtures(t) {
		// Rotate the starting arm; avoid always giving the same model a warm host.
		for offset := range len(profiles) {
			profile := profiles[(caseIndex+offset)%len(profiles)]
			armSettings := settings
			armSettings.CodexModel, armSettings.ClaudeModel, armSettings.Reasoning = profile.model, profile.model, profile.effort
			caseID := profile.model + "/" + profile.effort + "/" + fixture.name
			result := coordinator.runCase(t.Context(), profile.harness, config.WorkRoleImplementer, caseID, func(ctx context.Context) evalCaseResult {
				return runImplementationProfileFixture(ctx, t, profile.harness, armSettings, fixture)
			})
			if result.Err != nil || result.AdmissionStop != "" {
				t.Errorf("comparison stopped at %s (stage=%s class=%s admission=%s)", caseID, result.FailureStage, result.FailureClass, result.AdmissionStop)
				return // No retry, alternative model, or budget reset.
			}
		}
	}
	passed = len(coordinator.attempts) == 9 && !t.Failed()
}

func implementationProfileToolchainPreflight(ctx context.Context, run subprocess.Runner, root, artifactPath string) error {
	path := filepath.Join(filepath.Dir(artifactPath), "toolchain-preflight.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("private toolchain preflight evidence could not be preserved")
	}
	result, runErr := run.Run(ctx, "env", []string{
		"CORTEXIUM_RUNNER_IMPLEMENTATION_PREFLIGHT=1", "go", "test", "./internal/execution",
		"-run", "^TestImplementationProfileToolchainPreflight$", "-count=1", "-timeout=90s", "-v",
	}, root, 90*time.Second)
	_, writeErr := file.WriteString(result.Stdout + result.Stderr)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return errors.New("private toolchain preflight evidence could not be preserved")
	}
	if runErr != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout, "--- PASS: TestImplementationProfileToolchainPreflight (") {
		return fmt.Errorf("toolchain preflight did not pass; no model work admitted (private evidence: %s)", path)
	}
	return nil
}

type implementationPreflightRunner func(string, []string, string, time.Duration) (subprocess.Result, error)

func (run implementationPreflightRunner) Run(_ context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	return run(command, args, dir, timeout)
}

func TestImplementationProfilePreflightRequiresAnExecutedPass(t *testing.T) {
	const passed = "--- PASS: TestImplementationProfileToolchainPreflight (0.01s)\n"
	for _, scenario := range []struct {
		name, output string
		exitCode     int
		err          error
		wantPass     bool
	}{
		{name: "executed pass", output: passed, wantPass: true},
		{name: "skipped", output: "--- SKIP: TestImplementationProfileToolchainPreflight (0.00s)\n"},
		{name: "missing test", output: "testing: warning: no tests to run\nPASS\n"},
		{name: "failed process", output: passed, exitCode: 1, err: errors.New("process failed")},
		{name: "nonzero status", output: passed, exitCode: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			run := implementationPreflightRunner(func(command string, args []string, root string, timeout time.Duration) (subprocess.Result, error) {
				calls++
				if command != "env" || strings.Join(args, " ") != "CORTEXIUM_RUNNER_IMPLEMENTATION_PREFLIGHT=1 go test ./internal/execution -run ^TestImplementationProfileToolchainPreflight$ -count=1 -timeout=90s -v" || root != "/candidate" || timeout != 90*time.Second {
					t.Fatal("preflight escaped its exact no-model test selection or deadline")
				}
				return subprocess.Result{Stdout: scenario.output, ExitCode: scenario.exitCode}, scenario.err
			})
			artifact := filepath.Join(t.TempDir(), "comparison.jsonl")
			err := implementationProfileToolchainPreflight(t.Context(), run, "/candidate", artifact)
			if (err == nil) != scenario.wantPass || calls != 1 {
				t.Fatalf("preflight result = %v, calls = %d", err, calls)
			}
			log := filepath.Join(filepath.Dir(artifact), "toolchain-preflight.log")
			info, statErr := os.Stat(log)
			if statErr != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("preflight evidence is not private")
			}
			if err := implementationProfileToolchainPreflight(t.Context(), run, "/candidate", artifact); err == nil {
				t.Fatal("preflight allowed existing evidence to be overwritten")
			}
			if calls != 1 {
				t.Fatal("preflight reran despite an existing evidence record")
			}
			content, readErr := os.ReadFile(log)
			if readErr != nil || string(content) != scenario.output {
				t.Fatal("preflight changed retained evidence")
			}
		})
	}
}

// Use the production role resolver rather than the zero-value development-tool
// profile of a directly constructed ExecutionConfig. Keep fixture identity local.
func implementationProfileExecutionConfig(harness, repo, worktrees string, settings evalSettings) config.ExecutionConfig {
	model := settings.modelForHarness(harness)
	runtime := config.RuntimeConfig{
		Harnesses: []config.HarnessConfig{{Kind: harness, Command: harness, WorkspaceWriteRoot: worktrees}},
		Roles: map[string]config.RoleConfig{config.WorkRoleImplementer: {
			Harness: harness, Model: &model, Reasoning: settings.Reasoning,
			TimeoutSeconds: int(settings.CaseTimeout.Seconds()), Skills: []string{"runner-implementer"},
		}},
		RoleContracts: map[string]string{config.WorkRoleImplementer: config.WorkRoleImplementer},
	}
	cfg := runtime.Execution(config.WorkRoleImplementer, harness, repo)
	cfg.WorkspaceBaseRef = "HEAD"
	return cfg
}

func TestImplementationProfileExecutionUsesDefaultDevelopmentTools(t *testing.T) {
	settings := evalSettings{CodexModel: "explicit-codex", ClaudeModel: "explicit-claude", Reasoning: "high", CaseTimeout: 5 * time.Minute}
	for _, harness := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI} {
		t.Run(harness, func(t *testing.T) {
			cfg := implementationProfileExecutionConfig(harness, "/fixture", "/worktrees", settings)
			if !cfg.SafeTools || cfg.RoleAccess != config.RoleAccessSandboxed || cfg.HarnessConfigMode != config.HarnessConfigModeIsolated {
				t.Fatalf("comparison lost the default bounded development profile: %#v", cfg)
			}
			if cfg.Harness.Kind != harness || cfg.Harness.Command != harness || cfg.Harness.Model == nil || *cfg.Harness.Model != settings.modelForHarness(harness) || cfg.Harness.ReasoningEffort != "high" {
				t.Fatalf("comparison changed the explicit harness/model/effort: %#v", cfg.Harness)
			}
			if cfg.WorkspaceBaseRef != "HEAD" || cfg.Harness.WorkingDir != "/fixture" || cfg.Harness.WorkspaceWriteRoot != "/worktrees" || cfg.Harness.TimeoutSeconds != 300 || strings.Join(cfg.Skills, ",") != "runner-implementer" {
				t.Fatalf("comparison changed its assigned workspace, deadline or skill: %#v", cfg)
			}
		})
	}
}

func runImplementationProfileFixture(ctx context.Context, t *testing.T, harness string, settings evalSettings, fixture implementationProfileFixture) evalCaseResult {
	t.Helper()
	repo := implementationProfileRepository(t, fixture)
	model := settings.modelForHarness(harness)
	cfg := implementationProfileExecutionConfig(harness, repo, filepath.Join(t.TempDir(), "worktrees"), settings)
	assignment := execution.Assignment{Spec: execution.Spec{
		ID: "implementation_profile_fixture", ItemID: "implementation_profile_fixture", Repository: "owner/repo", DelegatedContentDigest: "v1:implementation-profile:" + fixture.name,
		Task: execution.Task{Title: fixture.name, Instructions: fixture.task + " " + strings.Join(recordUpdateProofs, " ") +
			" Use runner-implementer. Read README.md. Change only records.go and records_test.go. Run the focused Go tests and git diff --check. Do not add services, dependencies, browser tests, CI, or unrelated cleanup; do not push or publish."},
		RequiredVerification: append(append([]string{}, recordUpdateProofs...), "Focused Go tests and git diff --check pass."),
	}}
	var prepared workspace.Metadata
	capture := func(metadata workspace.Metadata) error { prepared = metadata; return nil }
	var output execution.Output
	var err error
	if harness == config.HarnessCodexCLI {
		output, err = execution.NewCodexExecutor(cfg, nil).ExecuteWorkspaceWrite(ctx, assignment, capture)
	} else {
		output, err = execution.NewAgentExecutor(harness, cfg, nil).ExecuteWorkspaceWrite(ctx, assignment, capture)
	}
	result := evalCaseResult{Outcome: output.Outcome, FailureClass: string(output.FailureClass), RetryDisposition: string(output.RetryDisposition), Usage: output.Usage, HarnessDurationMilliseconds: output.HarnessDurationMilliseconds}
	resultPath := filepath.Join(filepath.Dir(settings.ArtifactPath), model+"-"+settings.Reasoning+"-"+fixture.name+".result.json")
	if saveErr := saveImplementationProfileOutput(resultPath, output, err); saveErr != nil {
		result.Err, result.FailureStage = errors.New("private implementation handoff could not be preserved"), "implementation_execution"
		return result
	}
	if err != nil || output.Outcome != execution.OutcomeSucceeded {
		result.Err, result.FailureStage = errors.New("implementation assignment did not succeed"), "implementation_execution"
		return result
	}
	// Validate scope and untouched scaffolding; the hidden oracle cannot be edited
	// or replaced by the implementer, even if it weakens the visible tests.
	for _, name := range []string{"go.mod", "README.md"} {
		before, beforeErr := os.ReadFile(filepath.Join(repo, name))
		after, afterErr := os.ReadFile(filepath.Join(prepared.WorktreePath, name))
		if beforeErr != nil || afterErr != nil || string(before) != string(after) {
			result.Err = fmt.Errorf("fixture changed protected scaffolding %s", name)
		}
	}
	changed := reviewerFixtureGit(t, prepared.WorktreePath, "diff", "--name-only", prepared.BaseRevision)
	changed += reviewerFixtureGit(t, prepared.WorktreePath, "ls-files", "--others", "--exclude-standard")
	for _, name := range strings.Split(strings.TrimSpace(changed), "\n") {
		if name == "" {
			continue
		}
		if name != "records.go" && name != "records_test.go" {
			result.Err = errors.New("fixture made an out-of-scope change")
		}
	}
	check := exec.CommandContext(ctx, "git", "diff", "--check", prepared.BaseRevision)
	check.Dir = prepared.WorktreePath
	if err := check.Run(); err != nil {
		result.Err = errors.New("fixture diff check failed")
	}
	patch := reviewerFixtureGit(t, prepared.WorktreePath, "diff", prepared.BaseRevision)
	patchPath := filepath.Join(filepath.Dir(settings.ArtifactPath), model+"-"+settings.Reasoning+"-"+fixture.name+".diff")
	patchFile, patchErr := os.OpenFile(patchPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if patchErr == nil {
		_, patchErr = patchFile.WriteString(patch)
		patchErr = errors.Join(patchErr, patchFile.Close())
	}
	if patchErr != nil {
		result.Err = errors.New("private candidate diff could not be preserved")
	}
	oracle := t.TempDir()
	writeReviewerEvalFile(t, oracle, "go.mod", "module example.com/records\n\ngo 1.25.0\n")
	source, readErr := os.ReadFile(filepath.Join(prepared.WorktreePath, "records.go"))
	if readErr != nil {
		result.Err = errors.New("fixture source is unavailable")
	} else {
		writeReviewerEvalFile(t, oracle, "records.go", string(source))
		writeReviewerEvalFile(t, oracle, "records_test.go", reviewerEvalScenarios(t, false)[0].tests)
		_, duration, testErr := runReviewerFixtureTests(ctx, oracle)
		result.FixtureTestDurationMS = duration.Milliseconds()
		if testErr != nil {
			result.Err = errors.New("independent reference assertions failed")
		}
	}
	if result.Err != nil {
		result.Outcome, result.FailureClass, result.FailureStage = execution.OutcomeBlocked, string(execution.FailureInvalidContract), "fixture_content"
	}
	return result
}

// Keep untrusted handoffs/diagnostics private, including unsuccessful assignments.
// The aggregate artifact remains sanitized and existing evidence is never replaced.
func saveImplementationProfileOutput(path string, output execution.Output, executionErr error) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	diagnostic := ""
	if executionErr != nil {
		diagnostic = executionErr.Error()
	}
	err = json.NewEncoder(file).Encode(struct {
		Output     execution.Output `json:"output"`
		Diagnostic string           `json:"diagnostic,omitempty"`
	}{output, diagnostic})
	return errors.Join(err, file.Close())
}

func TestImplementationProfileOutputPreservesBlockedEvidencePrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	blocker := "Required proof could not be established."
	output := execution.Output{Outcome: execution.OutcomeBlocked, Blocker: &blocker, WorkDone: []string{"Retained implementation"}}
	if err := saveImplementationProfileOutput(path, output, errors.New("private diagnostic")); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(content), blocker) || !strings.Contains(string(content), "private diagnostic") {
		t.Fatal("blocked handoff was discarded")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("handoff is not private")
	}
	if err := saveImplementationProfileOutput(path, execution.Output{}, nil); err == nil {
		t.Fatal("existing handoff was overwritten")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(content) {
		t.Fatal("retained handoff changed")
	}
}
