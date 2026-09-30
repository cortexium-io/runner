package engine

import (
	"context"
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

func runImplementationProfileFixture(ctx context.Context, t *testing.T, harness string, settings evalSettings, fixture implementationProfileFixture) evalCaseResult {
	t.Helper()
	repo := implementationProfileRepository(t, fixture)
	model := settings.modelForHarness(harness)
	cfg := config.ExecutionConfig{WorkspaceBaseRef: "HEAD", Skills: []string{"runner-implementer"},
		Harness: config.HarnessConfig{Kind: harness, Command: harness, Model: &model, ReasoningEffort: settings.Reasoning,
			WorkingDir: repo, WorkspaceWriteRoot: filepath.Join(t.TempDir(), "worktrees"), TimeoutSeconds: int(settings.CaseTimeout.Seconds())}}
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
