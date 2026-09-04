package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	runnermetrics "github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/setup"
	"github.com/cortexium-io/runner/internal/subprocess"
	bundledskills "github.com/cortexium-io/runner/skills"
)

func TestInitInstallsRoleSkillsAndDoctorVerifiesReadiness(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("create bin: %v", err)
	}
	writeFakeCommand(t, bin, "codex", "codex-cli test-version")
	writeFakeGitHubProjectCommand(t, bin)
	writeFakeInitGitCommand(t, bin)
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	project := t.TempDir()
	command := exec.Command("git", "init", project)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}

	var before bytes.Buffer
	if err := run(t.Context(), []string{"doctor", "--project-dir", project}, strings.NewReader(""), &before); err == nil {
		t.Fatal("expected doctor to fail before required role skills are installed")
	}
	if !strings.Contains(before.String(), "Ready to run: no") {
		t.Fatalf("unexpected initial doctor output: %s", before.String())
	}

	configPath := filepath.Join(t.TempDir(), "runner.config.json")
	var initOutput bytes.Buffer
	if err := run(t.Context(), append([]string{"init", "--owner", "example", "--project-number", "7", "--repository", "example/repo", "--project-dir", project, "--config", configPath}, codexHarnessFlags()...), strings.NewReader(""), &initOutput); err != nil {
		t.Fatalf("init: %v\n%s", err, initOutput.String())
	}
	for _, skill := range []string{"runner-planner", "runner-implementer", "runner-reviewer"} {
		path := filepath.Join(home, ".codex", "skills", skill, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected installed skill %s: %v", skill, err)
		}
	}

	var after bytes.Buffer
	if err := run(t.Context(), []string{"doctor", "--config", configPath}, strings.NewReader(""), &after); err != nil {
		t.Fatalf("doctor after init: %v\n%s", err, after.String())
	}
	if !strings.Contains(after.String(), "Ready to run: yes") || !strings.Contains(after.String(), "authentication is managed by the harness and was not inspected") || !strings.Contains(after.String(), "planner=sandboxed/isolated") || !strings.Contains(after.String(), "implementer=sandboxed/isolated") || !strings.Contains(after.String(), "reviewer=sandboxed/isolated") {
		t.Fatalf("unexpected doctor output after init: %s", after.String())
	}
}

func TestInitCreatesStandaloneConfigAndCanSynchronizeIt(t *testing.T) {
	project := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("create bin: %v", err)
	}
	writeFakeGitHubProjectCommand(t, bin)
	writeFakeInitGitCommand(t, bin)
	writeFakeCommand(t, bin, "codex", "codex-cli test-version")
	writeFakeCommand(t, bin, "pi", "pi test-version")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	configPath := filepath.Join(t.TempDir(), "runner.config.json")
	var output bytes.Buffer
	dryRunConfig := filepath.Join(t.TempDir(), "runner.config.json")
	if err := run(t.Context(), append([]string{"init", "--owner", "example", "--project-number", "7", "--repository", "example/repo", "--project-dir", project, "--config", dryRunConfig, "--dry-run"}, codexHarnessFlags()...), strings.NewReader(""), &output); err != nil {
		t.Fatalf("init dry run: %v\n%s", err, output.String())
	}
	if _, err := os.Stat(dryRunConfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("init dry run created config: %v", err)
	}
	output.Reset()
	if err := run(t.Context(), []string{"init", "--owner", "example", "--project-number", "7", "--repository", "example/repo", "--project-dir", project, "--config", configPath, "--max-parallelism", "3", "--max-qa-rejections", "4", "--admission-window", "24h", "--max-admission-attempts", "12", "--max-admission-harness-time", "8h", "--max-admission-tokens", "1000000", "--max-admission-cost-usd", "25", "--auto-merge", "--autonomous-issues", "--trusted-issue-author", "maintainer", "--harness", "codex", "--model", "gpt-test", "--reasoning", "high", "--planning-support", "high", "--reviewer-harness", "pi", "--reviewer-access", "host", "--reviewer-harness-config", "inherit", "--reviewer-model", "qwen/local", "--reviewer-reasoning", "xhigh", "--base-update-review", "required"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("init: %v", err)
	}
	if expected := "Agent admission: rolling 24h0m0s window · 12 attempt(s) · 8h0m0s harness time · 1000000 reported tokens · $25.0000 reported cost"; !strings.Contains(output.String(), expected) {
		t.Fatalf("init output missing admission summary %q:\n%s", expected, output.String())
	}
	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load initialized config: %v", err)
	}
	workflow := loaded.EffectiveWorkflow()
	if loaded.GitHubProject.Owner != "example" || loaded.GitHubProject.Number != 7 || loaded.GitHubProject.TransitionField != config.RunnerTransitionFieldName || loaded.MaxParallelism != 3 || !loaded.GitHubProject.AutoMerge || loaded.GitHubProject.MergeMethod != config.MergeMethodMerge || loaded.Workflow == nil || workflow.Lanes["agent_qa"].MaxQARejections != 4 || workflow.Lanes["plan"].Name != "Plan" || loaded.Roles[config.WorkRolePlanner].Harness != config.HarnessCodexCLI || loaded.Roles[config.WorkRolePlanner].Model == nil || *loaded.Roles[config.WorkRolePlanner].Model != "gpt-test" || loaded.Roles[config.WorkRolePlanner].Reasoning != "high" || loaded.Roles[config.WorkRolePlanner].PlanningSupport != "" || loaded.Roles[config.WorkRoleImplementer].PlanningSupport != config.PlanningSupportHigh || loaded.Roles[config.WorkRoleReviewer].PlanningSupport != config.PlanningSupportHigh || loaded.Roles[config.WorkRoleReviewer].Harness != config.HarnessPiCLI || loaded.Roles[config.WorkRoleReviewer].Access != config.RoleAccessHost || loaded.Roles[config.WorkRoleReviewer].HarnessConfig != config.HarnessConfigModeInherit || loaded.Roles[config.WorkRoleReviewer].Model == nil || *loaded.Roles[config.WorkRoleReviewer].Model != "qwen/local" || loaded.Roles[config.WorkRoleReviewer].Reasoning != "xhigh" || loaded.GitHubProject.IntakeRepository != "example/repo" {
		t.Fatalf("unexpected initialized config %#v", loaded)
	}
	if loaded.AdmissionBudget == nil || loaded.AdmissionBudget.WindowSeconds != 86400 || loaded.AdmissionBudget.MaxAttempts != 12 || loaded.AdmissionBudget.MaxHarnessSeconds != 28800 || loaded.AdmissionBudget.MaxReportedTokens != 1000000 || loaded.AdmissionBudget.MaxReportedCostUSD == nil || *loaded.AdmissionBudget.MaxReportedCostUSD != 25 {
		t.Fatalf("unexpected admission budget %#v", loaded.AdmissionBudget)
	}
	if loaded.GitHubProject.AutonomousIssueIntake == nil || len(loaded.GitHubProject.AutonomousIssueIntake.TrustedAuthors) != 1 || loaded.GitHubProject.AutonomousIssueIntake.TrustedAuthors[0] != "maintainer" {
		t.Fatalf("unexpected autonomous issue policy %#v", loaded.GitHubProject.AutonomousIssueIntake)
	}
	runnerID := loaded.RunnerID
	output.Reset()
	if err := run(t.Context(), []string{"init", "--config", configPath, "--dry-run"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("synchronize existing init: %v\n%s", err, output.String())
	}
	loaded, err = config.LoadConfig(configPath)
	if err != nil || loaded.RunnerID != runnerID {
		t.Fatalf("existing config was replaced: %v %#v", err, loaded)
	}
}

func TestInitAcceptanceFreshEmptyRepositoryCreatesPrivateProject(t *testing.T) {
	project := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitLog := filepath.Join(t.TempDir(), "git-calls")
	ghLog := filepath.Join(t.TempDir(), "gh-calls")
	writeFakeEmptyInitGitCommand(t, bin)
	writeFakeGitHubProjectCommand(t, bin)
	writeFakeCommand(t, bin, "claude", "claude-cli test-version")
	writeFakeCommand(t, bin, "codex", "codex-cli test-version")
	t.Setenv("FAKE_INIT_GIT_LOG", gitLog)
	t.Setenv("GH_CALL_LOG", ghLog)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin)

	configPath := filepath.Join(t.TempDir(), "runner.config.json")
	var output bytes.Buffer
	err := run(t.Context(), []string{
		"init",
		"--create-project", "Fresh private Project",
		"--project-visibility", "private",
		"--repository", "example/repo",
		"--project-dir", project,
		"--config", configPath,
		"--bootstrap-base-branch",
		"--harness", config.HarnessClaudeCLI,
		"--implementer-harness", config.HarnessCodexCLI,
		"--model", "opus",
		"--reasoning", "xhigh",
		"--base-update-review", "required",
	}, strings.NewReader(""), &output)
	if err != nil {
		t.Fatalf("fresh empty-repository init: %v\n%s", err, output.String())
	}

	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load fresh config: %v", err)
	}
	if loaded.GitHubProject.Number != 8 || loaded.GitHubProject.Owner != "example" {
		t.Fatalf("fresh Project identity = %s/%d", loaded.GitHubProject.Owner, loaded.GitHubProject.Number)
	}
	for _, roleID := range config.BuiltinRoleIDs() {
		role := loaded.Roles[roleID]
		wantHarness := config.HarnessClaudeCLI
		if roleID == config.WorkRoleImplementer {
			wantHarness = config.HarnessCodexCLI
		}
		if role.Harness != wantHarness || role.Model == nil || *role.Model != "opus" || role.Reasoning != "xhigh" {
			t.Fatalf("fresh role %s = %#v", roleID, role)
		}
	}

	gitCalls, err := os.ReadFile(gitLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"mktree", "commit-tree", "update-ref refs/heads/main", "push --set-upstream origin"} {
		if !strings.Contains(string(gitCalls), expected) {
			t.Fatalf("fresh Git bootstrap omitted %q:\n%s", expected, gitCalls)
		}
	}
	ghCalls, err := os.ReadFile(ghLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"project create --owner example --title Fresh private Project --format json",
		"createProjectV2View",
		"deleteProjectV2View",
		"project edit 8 --owner example --visibility PRIVATE",
	} {
		if !strings.Contains(string(ghCalls), expected) {
			t.Fatalf("fresh Project setup omitted %q:\n%s", expected, ghCalls)
		}
	}
}

func TestInitExplainsHowToBootstrapAnEmptyRemoteRepository(t *testing.T) {
	project := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitLog := filepath.Join(t.TempDir(), "git-calls")
	writeFakeEmptyInitGitCommand(t, bin)
	writeFakeGitHubProjectCommand(t, bin)
	writeFakeCommand(t, bin, "codex", "codex-cli test-version")
	t.Setenv("FAKE_INIT_GIT_LOG", gitLog)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := run(t.Context(), append([]string{
		"init", "--owner", "example", "--project-number", "7", "--repository", "example/repo",
		"--project-dir", project, "--config", filepath.Join(t.TempDir(), "runner.config.json"),
	}, codexHarnessFlags()...), strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), `remote "origin" is empty`) || !strings.Contains(err.Error(), "--bootstrap-base-branch") {
		t.Fatalf("empty repository error = %v", err)
	}
	assertGitBootstrapNotApplied(t, gitLog)
}

func TestInitPreviewsAndAppliesEmptyRemoteBootstrap(t *testing.T) {
	for _, test := range []struct {
		name   string
		dryRun bool
	}{
		{name: "dry run", dryRun: true},
		{name: "apply"},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := t.TempDir()
			bin := filepath.Join(t.TempDir(), "bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			gitLog := filepath.Join(t.TempDir(), "git-calls")
			writeFakeEmptyInitGitCommand(t, bin)
			writeFakeGitHubProjectCommand(t, bin)
			writeFakeCommand(t, bin, "codex", "codex-cli test-version")
			t.Setenv("FAKE_INIT_GIT_LOG", gitLog)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			configPath := filepath.Join(t.TempDir(), "runner.config.json")
			args := append([]string{
				"init", "--owner", "example", "--project-number", "7", "--repository", "example/repo",
				"--project-dir", project, "--config", configPath, "--bootstrap-base-branch",
			}, codexHarnessFlags()...)
			if test.dryRun {
				args = append(args, "--dry-run")
			}
			var output bytes.Buffer
			if err := run(t.Context(), args, strings.NewReader(""), &output); err != nil {
				t.Fatalf("init empty repository: %v\n%s", err, output.String())
			}
			if !strings.Contains(output.String(), "Git base branch: create an empty initial commit on main and push it to origin") && test.dryRun {
				t.Fatalf("dry run omitted Git bootstrap plan: %s", output.String())
			}
			calls, err := os.ReadFile(gitLog)
			if err != nil {
				t.Fatal(err)
			}
			if test.dryRun {
				assertGitBootstrapNotApplied(t, gitLog)
				if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("dry run created config: %v", err)
				}
				return
			}
			for _, expected := range []string{"mktree", "commit-tree", "update-ref refs/heads/main", "push --set-upstream origin refs/heads/main:refs/heads/main"} {
				if !strings.Contains(string(calls), expected) {
					t.Fatalf("Git bootstrap omitted %q:\n%s", expected, calls)
				}
			}
			if !strings.Contains(output.String(), "Created empty initial commit") {
				t.Fatalf("init omitted applied bootstrap result: %s", output.String())
			}
			if _, err := os.Stat(configPath); err != nil {
				t.Fatalf("init did not create config: %v", err)
			}
		})
	}
}

func TestBaseBranchBootstrapCreatesEmptyCommitWithoutUsingIndex(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.git")
	project := filepath.Join(t.TempDir(), "project")
	runCLIGitTest(t, "", "init", "--bare", remote)
	runCLIGitTest(t, "", "init", "-b", "main", project)
	runCLIGitTest(t, project, "config", "user.name", "Runner Test")
	runCLIGitTest(t, project, "config", "user.email", "runner@example.test")
	runCLIGitTest(t, project, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(project, "staged.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLIGitTest(t, project, "add", "staged.txt")

	bootstrap := &baseBranchBootstrap{
		Root: project, RemoteName: "origin", BaseBranch: "main",
		CurrentBranch: "main", CreateInitialCommit: true,
	}
	var output bytes.Buffer
	if err := applyBaseBranchBootstrap(t.Context(), bootstrap, &output); err != nil {
		t.Fatalf("apply base branch bootstrap: %v\n%s", err, output.String())
	}
	if tree := runCLIGitTest(t, "", "--git-dir", remote, "ls-tree", "-r", "main"); tree != "" {
		t.Fatalf("initial commit unexpectedly contains files: %s", tree)
	}
	status := runCLIGitTest(t, project, "status", "--short")
	if !strings.Contains(status, "A  staged.txt") || !strings.Contains(status, "?? untracked.txt") {
		t.Fatalf("bootstrap changed staged or untracked work:\n%s", status)
	}
	if subject := runCLIGitTest(t, project, "log", "-1", "--format=%s"); subject != "chore: initialize repository for Cortexium Runner" {
		t.Fatalf("initial commit subject = %q", subject)
	}
}

func TestInitRefusesToBootstrapMissingBaseOnNonEmptyRemote(t *testing.T) {
	project := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitLog := filepath.Join(t.TempDir(), "git-calls")
	writeFakeEmptyInitGitCommand(t, bin)
	writeFakeGitHubProjectCommand(t, bin)
	writeFakeCommand(t, bin, "codex", "codex-cli test-version")
	t.Setenv("FAKE_INIT_GIT_LOG", gitLog)
	t.Setenv("FAKE_INIT_REMOTE_HEADS", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb refs/heads/develop\n")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := run(t.Context(), append([]string{
		"init", "--owner", "example", "--project-number", "7", "--repository", "example/repo",
		"--project-dir", project, "--config", filepath.Join(t.TempDir(), "runner.config.json"), "--bootstrap-base-branch",
	}, codexHarnessFlags()...), strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already contains branches (develop)") || !strings.Contains(err.Error(), "push the intended base branch explicitly") {
		t.Fatalf("non-empty remote bootstrap error = %v", err)
	}
	assertGitBootstrapNotApplied(t, gitLog)
}

func TestInitInteractivelyCollectsMissingRequiredChoices(t *testing.T) {
	project := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitLog := filepath.Join(t.TempDir(), "git-calls")
	writeFakeEmptyInitGitCommand(t, bin)
	writeFakeGitHubProjectCommand(t, bin)
	writeFakeCommand(t, bin, "codex", "codex-cli test-version")
	writeFakeCommand(t, bin, "claude", "claude-cli test-version")
	t.Setenv("FAKE_INIT_GIT_LOG", gitLog)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin)
	configPath := filepath.Join(project, ".cortexium", "runner.json")
	input := strings.NewReader("\nprivate\nclaude\n1\nxhigh\n2\n2\nrequired\n1\nyes\n")
	var output bytes.Buffer
	err := run(t.Context(), []string{
		"init", "--create-project", "Opus RTS", "--project-dir", project,
		"--interactive", "--dry-run",
	}, input, &output)
	if err != nil {
		t.Fatalf("interactive init: %v\n%s", err, output.String())
	}
	for _, expected := range []string{
		"Runner config path [" + configPath + "]",
		"New GitHub Project visibility [private/public]",
		"Default harness for all roles [codex/claude]",
		"Default model for all roles:",
		"1) Opus",
		"Choose an option [1-4]",
		"Default reasoning effort for all roles [low/medium/high/xhigh]",
		"How should the planner size downstream tasks?",
		"Regular coherent tasks (recommended)",
		"Smaller coherent tasks",
		"Split independently verifiable behavior for less capable downstream models",
		"planner: claude · model opus · reasoning xhigh",
		"implementer: claude · model opus · reasoning xhigh · access sandboxed · harness config isolated · task sizing small",
		"How many independent cards may Runner work on at the same time?",
		"1 concurrent card (recommended)",
		"2 concurrent cards",
		"Runs only cards whose declared dependencies are already Done",
		"Concurrent agent work: up to 2 independent card(s)",
		"When another PR changes main, Runner updates this branch. What should happen next?",
		"Re-run implementation and Agent QA (recommended)",
		"Continue to human PR review without re-running agents",
		"After Agent QA passes, what should Runner do with the pull request?",
		"Wait for a human to merge (recommended)",
		"Merge automatically after GitHub requirements pass",
		`Git remote "origin" is empty. Should Runner create and push an empty initial commit on main? [yes/no]`,
		"Git base branch: create an empty initial commit on main and push it to origin",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("interactive init output missing %q:\n%s", expected, output.String())
		}
	}
	assertGitBootstrapNotApplied(t, gitLog)
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interactive dry run created config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".gitignore")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interactive dry run changed .gitignore: %v", err)
	}
}

func TestInitNonInteractiveRequiresExplicitConfig(t *testing.T) {
	err := run(t.Context(), []string{"init", "--non-interactive"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "non-interactive init requires an explicit --config path") {
		t.Fatalf("missing non-interactive config error = %v", err)
	}
}

func TestInitRoleAccessDefaultsSafeAndRequiresExplicitPiHost(t *testing.T) {
	if access, err := resolveInitRoleAccess(nil, config.WorkRoleReviewer, config.HarnessCodexCLI, ""); err != nil || access != config.RoleAccessSandboxed {
		t.Fatalf("Codex reviewer safe default = %q, %v", access, err)
	}
	if _, err := resolveInitRoleAccess(nil, config.WorkRoleImplementer, config.HarnessPiCLI, ""); err == nil || !strings.Contains(err.Error(), "--implementer-access host") {
		t.Fatalf("Pi implementer omitted explicit host requirement: %v", err)
	}
	if access, err := resolveInitRoleAccess(nil, config.WorkRoleReviewer, config.HarnessPiCLI, config.RoleAccessHost); err != nil || access != config.RoleAccessHost {
		t.Fatalf("explicit Pi reviewer host access = %q, %v", access, err)
	}
}

func TestInitHarnessConfigurationDefaultsIsolatedAndAcceptsExplicitInheritance(t *testing.T) {
	planner, implementer, reviewer := "", "", ""
	if err := applyInitHarnessConfigDefaults("", &planner, &implementer, &reviewer); err != nil {
		t.Fatal(err)
	}
	if planner != config.HarnessConfigModeIsolated || implementer != config.HarnessConfigModeIsolated || reviewer != config.HarnessConfigModeIsolated {
		t.Fatalf("safe harness configuration defaults = %q/%q/%q", planner, implementer, reviewer)
	}
	planner, implementer, reviewer = "", config.HarnessConfigModeIsolated, ""
	if err := applyInitHarnessConfigDefaults(config.HarnessConfigModeInherit, &planner, &implementer, &reviewer); err != nil {
		t.Fatal(err)
	}
	if planner != config.HarnessConfigModeInherit || implementer != config.HarnessConfigModeIsolated || reviewer != config.HarnessConfigModeInherit {
		t.Fatalf("explicit harness configuration defaults were not preserved: %q/%q/%q", planner, implementer, reviewer)
	}
	if err := applyInitHarnessConfigDefaults("ambient", &planner, &implementer, &reviewer); err == nil {
		t.Fatal("unknown harness configuration mode was accepted")
	}
}

func TestInitPlanningSupportDefaultsRemainOperatorControlled(t *testing.T) {
	implementer, reviewer := "", ""
	if err := applyInitPlanningSupportDefaults(config.PlanningSupportHigh, &implementer, &reviewer); err != nil {
		t.Fatal(err)
	}
	if implementer != config.PlanningSupportHigh || reviewer != config.PlanningSupportHigh {
		t.Fatalf("shared planning support was not applied: %q/%q", implementer, reviewer)
	}
	implementer, reviewer = config.PlanningSupportStandard, ""
	if err := applyInitPlanningSupportDefaults(config.PlanningSupportHigh, &implementer, &reviewer); err != nil {
		t.Fatal(err)
	}
	if implementer != config.PlanningSupportStandard || reviewer != config.PlanningSupportHigh {
		t.Fatalf("role-specific planning support was overwritten: %q/%q", implementer, reviewer)
	}
	if err := applyInitPlanningSupportDefaults("automatic", &implementer, &reviewer); err == nil || !strings.Contains(err.Error(), "standard or high") {
		t.Fatalf("unknown planning support was accepted: %v", err)
	}
}

func TestRoleEditChangesOnlyTheSelectedAccessBoundary(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.json")
	cfg := completeCLITestConfig(t.TempDir())
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"role", "edit", "reviewer", "--config", configPath, "--access", "host"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("edit reviewer access: %v\n%s", err, output.String())
	}
	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Roles[config.WorkRoleReviewer].Access != config.RoleAccessHost || loaded.Roles[config.WorkRoleImplementer].Access != config.RoleAccessSandboxed {
		t.Fatalf("role access edit widened another role: %#v", loaded.Roles)
	}
	output.Reset()
	if err := run(t.Context(), []string{"role", "show", "reviewer", "--config", configPath}, strings.NewReader(""), &output); err != nil || !strings.Contains(output.String(), "Access: host") {
		t.Fatalf("role show omitted resolved access: %v\n%s", err, output.String())
	}
}

func TestRoleEditChangesOnlyTheSelectedHarnessConfiguration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.json")
	cfg := completeCLITestConfig(t.TempDir())
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"role", "edit", "implementer", "--config", configPath, "--harness-config", "inherit"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("edit implementer harness configuration: %v\n%s", err, output.String())
	}
	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Roles[config.WorkRoleImplementer].HarnessConfig != config.HarnessConfigModeInherit || loaded.Roles[config.WorkRoleReviewer].HarnessConfig != config.HarnessConfigModeIsolated {
		t.Fatalf("role harness configuration edit changed another role: %#v", loaded.Roles)
	}
	output.Reset()
	if err := run(t.Context(), []string{"role", "show", "implementer", "--config", configPath}, strings.NewReader(""), &output); err != nil || !strings.Contains(output.String(), "Harness configuration: inherit") {
		t.Fatalf("role show omitted resolved harness configuration: %v\n%s", err, output.String())
	}
}

func TestRoleEditAllChangesBuiltinHarnessConfigurationAtomically(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.json")
	cfg := completeCLITestConfig(t.TempDir())
	cfg.Roles["security_reviewer"] = config.RoleConfig{Extends: config.WorkRoleReviewer}
	cfg.Roles["isolated_reviewer"] = config.RoleConfig{Extends: config.WorkRoleReviewer, HarnessConfig: config.HarnessConfigModeIsolated}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"role", "edit", "--all", "--config", configPath, "--harness-config", "inherit"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("edit all built-in harness configurations: %v\n%s", err, output.String())
	}
	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range config.BuiltinRoleIDs() {
		definition := loaded.Roles[id]
		resolved, ok := loaded.RoleProfile(id)
		if !ok || definition.HarnessConfig != config.HarnessConfigModeInherit || resolved.HarnessConfig != config.HarnessConfigModeInherit || resolved.Access != config.RoleAccessSandboxed {
			t.Fatalf("built-in role %s did not inherit ambient configuration within its existing access boundary: definition=%#v resolved=%#v", id, definition, resolved)
		}
		if !strings.Contains(output.String(), id+": codex/sandboxed/inherit") {
			t.Fatalf("bulk edit output omitted effective role %s:\n%s", id, output.String())
		}
	}
	inherited, ok := loaded.RoleProfile("security_reviewer")
	if !ok || inherited.HarnessConfig != config.HarnessConfigModeInherit {
		t.Fatalf("custom role did not inherit the edited reviewer policy: %#v", inherited)
	}
	isolated, ok := loaded.RoleProfile("isolated_reviewer")
	if !ok || isolated.HarnessConfig != config.HarnessConfigModeIsolated {
		t.Fatalf("custom explicit harness configuration was overwritten: %#v", isolated)
	}
	if !strings.Contains(output.String(), "Custom roles keep explicit overrides") {
		t.Fatalf("bulk edit output did not explain custom role behavior:\n%s", output.String())
	}
}

func TestRoleEditAllRejectsUnsafeCombinationWithoutReplacingConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.json")
	cfg := completeCLITestConfig(t.TempDir())
	enabled := true
	cfg.Harnesses = append(cfg.Harnesses, config.HarnessConfig{Kind: config.HarnessPiCLI, Command: "pi", Enabled: &enabled})
	planner := cfg.Roles[config.WorkRolePlanner]
	planner.Harness = config.HarnessPiCLI
	cfg.Roles[config.WorkRolePlanner] = planner
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"role", "edit", "--all", "--config", configPath, "--harness-config", "inherit"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "Pi cannot safely inherit ambient configuration") {
		t.Fatalf("unsafe all-role inheritance was not rejected clearly: %v", err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed all-role edit partially replaced the config")
	}
}

func TestRoleEditAllAcceptsOnlyHarnessConfiguration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(configPath, completeCLITestConfig(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"role", "edit", "--all", "--config", configPath},
		{"role", "edit", "--all", "implementer", "--config", configPath, "--harness-config", "inherit"},
		{"role", "edit", "--all", "--config", configPath, "--harness-config", "inherit", "--access", "host"},
	} {
		if err := run(t.Context(), args, strings.NewReader(""), io.Discard); err == nil {
			t.Fatalf("invalid all-role edit was accepted: %#v", args)
		}
	}
}

func TestRoleEditChangesOnlySelectedPlanningSupport(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.json")
	cfg := completeCLITestConfig(t.TempDir())
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"role", "edit", "reviewer", "--config", configPath, "--planning-support", "high"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("edit reviewer planning support: %v\n%s", err, output.String())
	}
	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Roles[config.WorkRoleReviewer].PlanningSupport != config.PlanningSupportHigh || loaded.Roles[config.WorkRoleImplementer].PlanningSupport != config.PlanningSupportStandard {
		t.Fatalf("role planning support edit changed another role: %#v", loaded.Roles)
	}
	output.Reset()
	if err := run(t.Context(), []string{"role", "show", "reviewer", "--config", configPath}, strings.NewReader(""), &output); err != nil || !strings.Contains(output.String(), "Planner task sizing: small") {
		t.Fatalf("role show omitted planning support: %v\n%s", err, output.String())
	}
	if err := run(t.Context(), []string{"role", "edit", "reviewer", "--config", configPath, "--clear-planning-support"}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("clear reviewer planning support: %v", err)
	}
	loaded, err = config.LoadConfig(configPath)
	if err != nil || loaded.Roles[config.WorkRoleReviewer].PlanningSupport != "" {
		t.Fatalf("planning support override was not cleared: config=%#v err=%v", loaded, err)
	}
}

func TestRoleEditConfiguresPiReasoningPreservationPerRole(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.json")
	cfg := completeCLITestConfig(t.TempDir())
	enabled := true
	cfg.Harnesses = append(cfg.Harnesses, config.HarnessConfig{
		Kind: config.HarnessPiCLI, Command: "pi", Enabled: &enabled, WorkspaceWriteRoot: filepath.Join(t.TempDir(), "worktrees"),
	})
	reviewer := cfg.Roles[config.WorkRoleReviewer]
	reviewer.Harness = config.HarnessPiCLI
	reviewer.Access = config.RoleAccessHost
	cfg.Roles[config.WorkRoleReviewer] = reviewer
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := run(t.Context(), []string{"role", "edit", "reviewer", "--config", configPath, "--preserve-reasoning"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("enable Pi reasoning preservation: %v\n%s", err, output.String())
	}
	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if setting := loaded.Roles[config.WorkRoleReviewer].PreserveReasoning; setting == nil || !*setting || loaded.Roles[config.WorkRoleImplementer].PreserveReasoning != nil {
		t.Fatalf("reasoning preservation changed the wrong role: %#v", loaded.Roles)
	}
	output.Reset()
	if err := run(t.Context(), []string{"role", "show", "reviewer", "--config", configPath}, strings.NewReader(""), &output); err != nil || !strings.Contains(output.String(), "Preserve reasoning across Pi turns: true") {
		t.Fatalf("role show omitted Pi reasoning preservation: %v\n%s", err, output.String())
	}
	if err := run(t.Context(), []string{"role", "edit", "reviewer", "--config", configPath, "--no-preserve-reasoning"}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("disable Pi reasoning preservation: %v", err)
	}
	loaded, err = config.LoadConfig(configPath)
	if err != nil || loaded.Roles[config.WorkRoleReviewer].PreserveReasoning == nil || *loaded.Roles[config.WorkRoleReviewer].PreserveReasoning {
		t.Fatalf("explicit false was not persisted: config=%#v err=%v", loaded, err)
	}
	if err := run(t.Context(), []string{"role", "edit", "reviewer", "--config", configPath, "--clear-preserve-reasoning"}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("clear Pi reasoning-preservation override: %v", err)
	}
	loaded, err = config.LoadConfig(configPath)
	if err != nil || loaded.Roles[config.WorkRoleReviewer].PreserveReasoning != nil {
		t.Fatalf("reasoning-preservation override was not cleared: config=%#v err=%v", loaded, err)
	}
}

func TestRoleEditGrantsAndClearsOnlySelectedMCPServers(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.json")
	cfg := completeCLITestConfig(t.TempDir())
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"role", "edit", "reviewer", "--config", configPath, "--mcp-server", "chrome_dev_tools"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("grant reviewer MCP server: %v\n%s", err, output.String())
	}
	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(loaded.Roles[config.WorkRoleReviewer].MCPServers, ",") != "chrome_dev_tools" || len(loaded.Roles[config.WorkRoleImplementer].MCPServers) != 0 {
		t.Fatalf("MCP grant widened another role: %#v", loaded.Roles)
	}
	output.Reset()
	if err := run(t.Context(), []string{"role", "show", "reviewer", "--config", configPath}, strings.NewReader(""), &output); err != nil || !strings.Contains(output.String(), "MCP servers: chrome_dev_tools") {
		t.Fatalf("role show omitted MCP grant: %v\n%s", err, output.String())
	}
	if err := run(t.Context(), []string{"role", "edit", "reviewer", "--config", configPath, "--clear-mcp-servers"}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("clear reviewer MCP servers: %v", err)
	}
	loaded, err = config.LoadConfig(configPath)
	if err != nil || len(loaded.Roles[config.WorkRoleReviewer].MCPServers) != 0 {
		t.Fatalf("MCP grant was not cleared: config=%#v err=%v", loaded, err)
	}
}

func TestInitInteractiveDoesNotOfferBootstrapWhenRemoteBaseExists(t *testing.T) {
	project := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeInitGitCommand(t, bin)
	writeFakeGitHubProjectCommand(t, bin)
	writeFakeCommand(t, bin, "codex", "codex-cli test-version")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin)

	input := strings.NewReader("private\n1\n1\n1\nhigh\n1\n1\nrequired\n1\n")
	var output bytes.Buffer
	err := run(t.Context(), []string{
		"init", "--create-project", "Existing base", "--project-dir", project,
		"--config", filepath.Join(t.TempDir(), "runner.config.json"), "--interactive", "--dry-run",
	}, input, &output)
	if err != nil {
		t.Fatalf("interactive init with existing remote base: %v\n%s", err, output.String())
	}
	if strings.Contains(output.String(), `Git remote "origin" is empty`) {
		t.Fatalf("init offered an empty-remote action after finding origin/main:\n%s", output.String())
	}
}

func TestInitInteractiveAndNonInteractiveAreMutuallyExclusive(t *testing.T) {
	err := run(t.Context(), []string{"init", "--config", filepath.Join(t.TempDir(), "runner.json"), "--interactive", "--non-interactive"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("interactive mode conflict error = %v", err)
	}
}

func TestInitNonInteractiveCreationRequiresExplicitVisibility(t *testing.T) {
	err := run(t.Context(), []string{"init", "--config", filepath.Join(t.TempDir(), "runner.json"), "--create-project", "Test", "--non-interactive"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--project-visibility must be explicitly set") {
		t.Fatalf("missing Project visibility error = %v", err)
	}
}

func TestInitPromptsForDefaultConfigBeforeInteractiveHarnessDiscovery(t *testing.T) {
	var output bytes.Buffer
	err := run(t.Context(), []string{"init", "--interactive"}, strings.NewReader(""), &output)
	if err == nil || !strings.Contains(err.Error(), "input ended before setup was complete") {
		t.Fatalf("empty interactive input error = %v", err)
	}
	if !strings.Contains(output.String(), "Runner config path [") || !strings.Contains(output.String(), filepath.Join(".cortexium", "runner.json")) {
		t.Fatalf("config prompt missing from output: %s", output.String())
	}
}

func TestConfigDestinationPreflightRejectsOtherWritableParent(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("secure operator-config provenance is supported on macOS and Linux")
	}
	parent := filepath.Join(t.TempDir(), "unsafe-operator-dir")
	if err := os.Mkdir(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	err := preflightConfigDestination(filepath.Join(parent, "runner.json"))
	if err == nil || !strings.Contains(err.Error(), "permits replacement by another local user") {
		t.Fatalf("unsafe config parent was accepted: %v", err)
	}
}

func assertGitBootstrapNotApplied(t *testing.T, logPath string) {
	t.Helper()
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"mktree", "commit-tree", "update-ref", "push --set-upstream"} {
		if strings.Contains(string(calls), forbidden) {
			t.Fatalf("Git bootstrap unexpectedly ran %q:\n%s", forbidden, calls)
		}
	}
}

func runCLIGitTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	if directory != "" {
		command.Dir = directory
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestInitCompletesLocalAndGitHubPreflightBeforeMutation(t *testing.T) {
	project := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeInitGitCommand(t, bin)
	writeFakeCommand(t, bin, "codex", "codex-cli test-version")
	t.Setenv("HOME", t.TempDir())
	logPath := filepath.Join(t.TempDir(), "gh-calls")
	ghPath := filepath.Join(bin, "gh")
	ghScript := `#!/bin/sh
printf '%s\n' "$*" >> "$GH_CALL_LOG"
if [ "$1 $2" = "auth status" ]; then
  printf '%s\n' 'not authenticated' >&2
  exit 1
fi
exit 0
`
	if err := os.WriteFile(ghPath, []byte(ghScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_CALL_LOG", logPath)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := run(t.Context(), append([]string{"init", "--owner", "example", "--create-project", "Test", "--project-visibility", "private", "--repository", "example/repo", "--project-dir", project, "--config", filepath.Join(t.TempDir(), "runner.json")}, codexHarnessFlags()...), strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("expected GitHub authentication preflight failure, got %v", err)
	}
	calls, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.TrimSpace(string(calls)) != "auth status --hostname github.com" || strings.Contains(string(calls), "project create") {
		t.Fatalf("init mutated GitHub before preflight completed: %s", calls)
	}
}

func TestUnknownCommandReturnsClearError(t *testing.T) {
	err := run(t.Context(), []string{"wat"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), `unknown command "wat"`) {
		t.Fatalf("unknown command error = %v", err)
	}
}

func TestRemovedCommandNamespacesAreNotAccepted(t *testing.T) {
	for _, command := range []string{"check", "setup", "project", "version"} {
		err := run(t.Context(), []string{command}, strings.NewReader(""), io.Discard)
		if err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("removed command %q error = %v", command, err)
		}
	}
}

func TestCLIHelpVersionAndExitContract(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		want string
	}{
		{name: "no arguments", code: 0, want: "Usage:"},
		{name: "root help", args: []string{"--help"}, code: 0, want: "Usage:"},
		{name: "version", args: []string{"--version"}, code: 0, want: "cortexium-runner "},
		{name: "command help", args: []string{"run", "--help"}, code: 0, want: "Usage: cortexium-runner run"},
		{name: "update help", args: []string{"update", "--help"}, code: 0, want: "Usage: cortexium-runner update"},
		{name: "plan help", args: []string{"plan", "--help"}, code: 0, want: "Usage: cortexium-runner plan"},
		{name: "role help", args: []string{"role", "add", "--help"}, code: 0, want: "Usage: cortexium-runner role add"},
		{name: "workflow help", args: []string{"workflow", "explain", "--help"}, code: 0, want: "Usage: cortexium-runner workflow explain"},
		{name: "unknown command", args: []string{"unknown"}, code: 1, want: "error: unknown command"},
		{name: "invalid flag", args: []string{"run", "--unknown"}, code: 1, want: "error: parse run flags"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := execute(t.Context(), test.args, strings.NewReader(""), &stdout, &stderr)
			if code != test.code {
				t.Fatalf("exit code = %d, want %d; stdout=%s stderr=%s", code, test.code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String()+stderr.String(), test.want) {
				t.Fatalf("output missing %q: stdout=%s stderr=%s", test.want, stdout.String(), stderr.String())
			}
		})
	}
}

func TestExecuteEscapesTerminalControlInErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := execute(t.Context(), []string{"unknown\x1b[2J\r\u202e"}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	rendered := stderr.String()
	for _, control := range []string{"\x1b", "\r", "\u202e"} {
		if strings.Contains(rendered, control) {
			t.Fatalf("stderr retained %q in %q", control, rendered)
		}
	}
	for _, escaped := range []string{`\x1b`, `\r`, `\u202e`} {
		if !strings.Contains(rendered, escaped) {
			t.Fatalf("stderr omitted %q in %q", escaped, rendered)
		}
	}
}

func TestEveryCommandHelpReturnsSuccess(t *testing.T) {
	commands := [][]string{
		{"init", "--help"}, {"doctor", "--help"}, {"update", "--help"}, {"plan", "--help"}, {"approve", "--help"}, {"retry", "--help"}, {"status", "--help"}, {"run", "--help"},
		{"harness", "--help"}, {"harness", "check", "--help"},
		{"workflow", "--help"}, {"workflow", "validate", "--help"}, {"workflow", "explain", "--help"},
		{"role", "--help"}, {"role", "list", "--help"}, {"role", "show", "--help"}, {"role", "add", "--help"}, {"role", "edit", "--help"}, {"role", "remove", "--help"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var output bytes.Buffer
			if err := run(t.Context(), args, strings.NewReader(""), &output); err != nil {
				t.Fatalf("help failed: %v", err)
			}
			if !strings.Contains(output.String(), "Usage:") {
				t.Fatalf("help omitted usage: %s", output.String())
			}
		})
	}
}

func TestUpdateRejectsCheckWithExactVersion(t *testing.T) {
	err := run(t.Context(), []string{"update", "--check", "--version", "v0.2.0"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--check and --version cannot be used together") {
		t.Fatalf("combined update modes error = %v", err)
	}
}

func TestStatusReportsWorkAndPollingStateInsteadOfReadiness(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CORTEXIUM_RUNNER_STATE_DIR", filepath.Join(home, "runner-state"))
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeGitHubProjectCommand(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := filepath.Join(t.TempDir(), "runner.config.json")
	cfg := completeCLITestConfig("/project")
	cfg.AdmissionBudget = &config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxAttempts: 1}
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := runnermetrics.NewDefaultStore(cfg.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(runnermetrics.Event{Kind: runnermetrics.EventStarted, AttemptID: "att_budget", RunnerID: cfg.RunnerID, ItemTitle: "Recent task", Role: "implementer", Harness: "codex", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"status", "--config", path}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"Process: stopped", "Concurrent agent work: up to 1 independent card(s)", "Agent admission: paused", "rolling admission budget reached 1 attempts", "Pull request merge: human merge required", "Next poll: not scheduled", "Current cards: 1", "Active work: 0", "Blocked work: 0"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("status missing %q: %s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "Kanban board ready") {
		t.Fatalf("status still duplicates doctor readiness: %s", output.String())
	}
	file, err := os.OpenFile(store.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("not-json\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(t.Context(), []string{"status", "--config", path}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("status with malformed admission history: %v", err)
	}
	for _, want := range []string{"Agent admission: paused", "history contains 1 malformed record(s)", "Metrics warning: ignored 1 malformed history record(s)"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("status did not fail closed for malformed history; missing %q: %s", want, output.String())
		}
	}
}

func TestBlockedWorkSectionShowsConciseReasonAndNextAction(t *testing.T) {
	var output bytes.Buffer
	writeWorkSection(&output, "Blocked work", []github.WorkItem{{
		Title: "Review feature", Status: "Blocked",
		Result: "Reason: Claude Code session limit reached.\n\nBlocker: Wait for reset.\n\nNext action: Move this card to Agent QA after the blocker clears.\nFourth line must not be shown.",
	}}, true, "/operator/runner.json", "Ready")
	for _, expected := range []string{
		"Review feature [Blocked]",
		"    Reason: Claude Code session limit reached.",
		"    Blocker: Wait for reset.",
		"    Next action: Move this card to Agent QA after the blocker clears.",
		"    Board retry: move this card to Ready to retry through implementation.",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("blocked status omitted %q:\n%s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "Fourth line") {
		t.Fatalf("blocked status included excessive detail:\n%s", output.String())
	}
}

func TestRoleCLIManagesInheritedCustomProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.config.json")
	cfg := completeCLITestConfig("/project")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"role", "add", "security_reviewer", "--config", path, "--extends", "reviewer", "--harness", "pi", "--model", "qwen/local"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("add role: %v", err)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("load role config: %v", err)
	}
	profile, ok := loaded.RoleProfile("security_reviewer")
	if !ok || loaded.RoleContract("security_reviewer") != config.WorkRoleReviewer || profile.Harness != config.HarnessPiCLI || profile.Model == nil || *profile.Model != "qwen/local" || strings.Join(profile.Skills, ",") != "runner-reviewer" {
		t.Fatalf("custom role did not inherit reviewer defaults: %#v", profile)
	}
	output.Reset()
	if err := run(t.Context(), []string{"role", "show", "security_reviewer", "--config", path}, strings.NewReader(""), &output); err != nil || !strings.Contains(output.String(), "Contract: reviewer") {
		t.Fatalf("show role: %v\n%s", err, output.String())
	}
	if err := run(t.Context(), []string{"role", "remove", "security_reviewer", "--config", path}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("remove role: %v", err)
	}
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if _, ok := loaded.RoleProfile("security_reviewer"); ok {
		t.Fatal("removed custom role still resolves")
	}
}

func TestRoleCLIConfiguresAndClearsImplementerLadder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.config.json")
	cfg := completeCLITestConfig("/project")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := run(t.Context(), []string{"role", "add", "implementer_luna", "--config", path, "--extends", "implementer", "--model", "gpt-luna"}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("add ladder role: %v", err)
	}
	if err := run(t.Context(), []string{"role", "edit", "implementer", "--config", path, "--next-implementer", "implementer_luna"}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("configure ladder: %v", err)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(loaded.ImplementerLadder, ",") != "implementer,implementer_luna" {
		t.Fatalf("unexpected ladder %#v", loaded.ImplementerLadder)
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"role", "show", "implementer_luna", "--config", path}, strings.NewReader(""), &output); err != nil || !strings.Contains(output.String(), "Implementer ladder: 2/2") {
		t.Fatalf("show ladder role: %v\n%s", err, output.String())
	}
	if err := run(t.Context(), []string{"role", "remove", "implementer_luna", "--config", path}, strings.NewReader(""), io.Discard); err == nil || !strings.Contains(err.Error(), "implementer_ladder") {
		t.Fatalf("removed an active ladder role: %v", err)
	}
	if err := run(t.Context(), []string{"role", "edit", "implementer", "--config", path, "--clear-implementer-ladder"}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("clear ladder: %v", err)
	}
	if err := run(t.Context(), []string{"role", "remove", "implementer_luna", "--config", path}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("remove unused ladder role: %v", err)
	}
}

func TestRunRejectsInvalidPollingIntervalsBeforeLoadingConfig(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"run", "--poll-interval", "0s"}, want: "--poll-interval must be positive"},
		{args: []string{"run", "--max-idle-interval", "0s"}, want: "--max-idle-interval must be positive"},
		{args: []string{"run", "--poll-interval", "1m", "--max-idle-interval", "30s"}, want: "--max-idle-interval must be greater than or equal to --poll-interval"},
	}
	for _, test := range tests {
		err := run(t.Context(), test.args, strings.NewReader(""), io.Discard)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("run %v error = %v, want %q", test.args, err, test.want)
		}
	}
}

func TestRunDefaultsToContinuousMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "runner.config.json")
	if err := config.SaveConfig(configPath, completeCLITestConfig(t.TempDir())); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := run(ctx, []string{"run", "--config", configPath}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("bare run did not use cancellable continuous mode: %v", err)
	}
}

func TestRunOncePerformsOneCycleAndExits(t *testing.T) {
	home := t.TempDir()
	bin := t.TempDir()
	writeFakeGitHubProjectCommand(t, bin)
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	configPath := filepath.Join(t.TempDir(), "runner.config.json")
	if err := config.SaveConfig(configPath, completeCLITestConfig(t.TempDir())); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"run", "--config", configPath, "--once"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("run once: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "idle: no ready GitHub Project items") {
		t.Fatalf("one-cycle run did not exit after reporting idle: %s", output.String())
	}
	if !strings.Contains(output.String(), "Running one GitHub Project cycle") {
		t.Fatalf("one-cycle run did not announce background work: %s", output.String())
	}
}

func TestRunRejectsRemovedLoopFlag(t *testing.T) {
	if err := run(t.Context(), []string{"run", "--loop"}, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("removed --loop flag was still accepted")
	}
}

func TestApprovePreviewsExactAuthenticatedAssertionAndApprovesByDefault(t *testing.T) {
	project := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("create bin: %v", err)
	}
	writeFakeGitHubProjectCommand(t, bin)
	writeFakeInitGitCommand(t, bin)
	writeFakeCommand(t, bin, "codex", "codex-cli test-version")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	callLog := filepath.Join(t.TempDir(), "gh-calls.log")
	t.Setenv("GH_CALL_LOG", callLog)
	config := filepath.Join(t.TempDir(), "runner.config.json")
	var initOutput bytes.Buffer
	if err := run(t.Context(), append([]string{"init", "--owner", "example", "--project-number", "7", "--repository", "example/repo", "--project-dir", project, "--config", config}, codexHarnessFlags()...), strings.NewReader(""), &initOutput); err != nil {
		t.Fatalf("init: %v", err)
	}
	beforeJSON, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read GitHub calls before JSON preview: %v", err)
	}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"approve", "--config", config, "--item", "PVTI_approval", "--json"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("JSON approval preview: %v", err)
	}
	if !strings.Contains(output.String(), `"applied": false`) || !strings.Contains(output.String(), `"approval"`) {
		t.Fatalf("JSON approval was not a read-only preview: %s", output.String())
	}
	afterJSON, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read GitHub calls after JSON preview: %v", err)
	}
	if strings.Count(string(afterJSON), "project item-edit") != strings.Count(string(beforeJSON), "project item-edit") {
		t.Fatalf("JSON approval preview mutated Project state:\n%s", afterJSON)
	}
	output.Reset()
	if err := run(t.Context(), []string{"approve", "--config", config, "--item", "PVTI_approval", "--dry-run"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("preview approval: %v", err)
	}
	if !strings.Contains(output.String(), "Dry run only") || !strings.Contains(output.String(), "v2:") || !strings.Contains(output.String(), "Role: planner") || !strings.Contains(output.String(), "Review public request") {
		t.Fatalf("unexpected approval preview: %s", output.String())
	}
	output.Reset()
	if err := run(t.Context(), []string{"approve", "--config", config, "--item", "PVTI_approval"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("approve item: %v", err)
	}
	if !strings.Contains(output.String(), "moved it to Backlog") || !strings.Contains(output.String(), "Public intake label: remove needs-assessment") {
		t.Fatalf("unexpected applied approval output: %s", output.String())
	}
	const assertionLabel = "Authenticated assertion: "
	assertionStart := strings.Index(output.String(), assertionLabel)
	if assertionStart < 0 {
		t.Fatalf("applied approval did not display its assertion: %s", output.String())
	}
	assertionLine := strings.SplitN(output.String()[assertionStart+len(assertionLabel):], "\n", 2)[0]
	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read GitHub calls: %v", err)
	}
	if !strings.Contains(string(calls), "--field-id F_approval --text "+assertionLine) {
		t.Fatalf("displayed assertion was not the assertion written by apply: assertion=%q calls=%s", assertionLine, calls)
	}
	approvalCalls := string(calls[len(beforeJSON):])
	lockAt := strings.Index(approvalCalls, "--field-id F_transition --text v1")
	backlogAt := strings.Index(approvalCalls, "--single-select-option-id O_backlog")
	approvalAt := strings.Index(approvalCalls, "--field-id F_approval --text "+assertionLine)
	unlockAt := strings.Index(approvalCalls, "--field-id F_transition --clear")
	if lockAt < 0 || backlogAt <= lockAt || approvalAt <= backlogAt || unlockAt <= approvalAt {
		t.Fatalf("approval did not remain transition-locked through its final status and authority writes: %s", approvalCalls)
	}
	if strings.Contains(approvalCalls, "--single-select-option-id O_assessment") {
		t.Fatalf("approval used Needs assessment as a transactional hop: %s", approvalCalls)
	}
}

func TestRetryPreviewsAndReturnsBlockedItemToRecordedLane(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("create bin: %v", err)
	}
	writeFakeGitHubProjectCommand(t, bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	callLog := filepath.Join(t.TempDir(), "gh-calls.log")
	t.Setenv("GH_CALL_LOG", callLog)
	item := github.WorkItem{
		ID: "PVTI_retry", Title: "Retry browser review", Body: "Acceptance criteria",
		URL: "https://github.com/example/repo/issues/2", Repository: "example/repo",
		Status: "Blocked", Result: "Previous browser blocker.", Phase: "agent_qa",
	}
	stateDir := t.TempDir()
	t.Setenv("CORTEXIUM_RUNNER_STATE_DIR", stateDir)
	authorityKeyDigest := sha256.Sum256([]byte("runner-cli-test-approval-authority"))
	authorityKey := authorityKeyDigest[:]
	runnerDigest := sha256.Sum256([]byte("runner"))
	authorityPath := filepath.Join(stateDir, "approval-authority", hex.EncodeToString(runnerDigest[:12])+".key")
	if err := os.MkdirAll(filepath.Dir(authorityPath), 0o700); err != nil {
		t.Fatalf("create approval authority directory: %v", err)
	}
	if err := os.WriteFile(authorityPath, authorityKey, 0o600); err != nil {
		t.Fatalf("write approval authority: %v", err)
	}
	cfg := completeCLITestConfig(t.TempDir())
	assertion := signCLITestActionAssertion(cfg.ResolveProject(), item, config.WorkRoleReviewer, "blocked", authorityKey)
	t.Setenv("FAKE_GH_RETRY_APPROVAL", assertion)
	configPath := filepath.Join(t.TempDir(), "runner.config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var output bytes.Buffer
	if err := run(t.Context(), []string{"retry", "--config", configPath, "--item", item.Title, "--dry-run"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("preview retry: %v", err)
	}
	if !strings.Contains(output.String(), "Retry destination: Agent QA") || !strings.Contains(output.String(), "Dry run only") {
		t.Fatalf("unexpected retry preview: %s", output.String())
	}

	output.Reset()
	if err := run(t.Context(), []string{"retry", "--config", configPath, "--item", item.ID}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("apply retry: %v", err)
	}
	if !strings.Contains(output.String(), "Moved the item to Agent QA") {
		t.Fatalf("unexpected retry output: %s", output.String())
	}
	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read gh calls: %v", err)
	}
	if !strings.Contains(string(calls), "singleSelectOptionId") || !strings.Contains(string(calls), "=O_qa") {
		t.Fatalf("retry did not target Agent QA: %s", calls)
	}
	retryCalls := string(calls)
	lockAt := strings.Index(retryCalls, "--field-id F_transition --text v1")
	approvalAt := strings.Index(retryCalls, "=F_approval")
	qaAt := strings.Index(retryCalls, "=O_qa")
	unlockAt := strings.Index(retryCalls, "--field-id F_transition --clear")
	if lockAt < 0 || approvalAt <= lockAt || qaAt <= approvalAt || unlockAt <= qaAt {
		t.Fatalf("retry did not remain transition-locked until Agent QA was authoritative: %s", retryCalls)
	}
	if strings.Contains(retryCalls, "=O_assessment") {
		t.Fatalf("retry used Needs assessment as a transactional hop: %s", retryCalls)
	}

	output.Reset()
	const correction = "Keep task-owned files; leave unrelated operator changes untouched."
	if err := run(t.Context(), []string{"retry", "--config", configPath, "--item", item.ID, "--feedback", correction, "--dry-run"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("preview corrected retry: %v", err)
	}
	if !strings.Contains(output.String(), "Replacement feedback: "+correction) || !strings.Contains(output.String(), "QA failures: reset to 0") {
		t.Fatalf("corrected retry preview omitted its state changes: %s", output.String())
	}
}

func signCLITestActionAssertion(project config.ProjectConfig, item github.WorkItem, role, state string, key []byte) string {
	authorityDigest := sha256.Sum256(key)
	dependencies := make([]string, 0, len(item.Dependencies))
	for _, dependency := range item.Dependencies {
		if dependency = strings.TrimSpace(dependency); dependency != "" {
			dependencies = append(dependencies, dependency)
		}
	}
	result := strings.TrimSpace(item.Result)
	if len(result) > 1000 {
		result = result[:1000]
	}
	payload := struct {
		Version                   string   `json:"version"`
		Authority                 string   `json:"authority"`
		ProjectOwner              string   `json:"project_owner"`
		ProjectNumber             int      `json:"project_number"`
		State                     string   `json:"state"`
		Role                      string   `json:"role"`
		ItemID                    string   `json:"item_id"`
		DelegatedContentDigest    string   `json:"delegated_content_digest"`
		Body                      string   `json:"body"`
		URL                       string   `json:"url,omitempty"`
		Repository                string   `json:"repository,omitempty"`
		Dependencies              []string `json:"dependencies,omitempty"`
		Result                    string   `json:"result,omitempty"`
		Phase                     string   `json:"phase,omitempty"`
		Activity                  string   `json:"activity,omitempty"`
		QAFailures                int      `json:"qa_failures,omitempty"`
		Branch                    string   `json:"branch,omitempty"`
		PullRequest               string   `json:"pull_request,omitempty"`
		QACommit                  string   `json:"qa_commit,omitempty"`
		PlanningSourceID          string   `json:"planning_source_id,omitempty"`
		PlanningSourceLane        string   `json:"planning_source_lane,omitempty"`
		PlanningSourceFingerprint string   `json:"planning_source_fingerprint,omitempty"`
		PlanningDestination       string   `json:"planning_destination,omitempty"`
		PlanningBatchFingerprint  string   `json:"planning_batch_fingerprint,omitempty"`
		PlanningBatchSize         int      `json:"planning_batch_size,omitempty"`
		PlanningItemIndex         int      `json:"planning_item_index,omitempty"`
	}{
		Version: "v2", Authority: hex.EncodeToString(authorityDigest[:12]), ProjectOwner: strings.TrimSpace(project.Owner), ProjectNumber: project.Number,
		State: strings.TrimSpace(state), Role: strings.TrimSpace(role), ItemID: strings.TrimSpace(item.ID),
		DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Body: strings.TrimSpace(item.Body), URL: strings.TrimSpace(item.URL),
		Repository: strings.TrimSpace(item.Repository), Dependencies: dependencies, Result: result, Phase: strings.TrimSpace(item.Phase), Activity: strings.TrimSpace(item.Activity),
		QAFailures: item.QAFailures, Branch: strings.TrimSpace(item.Branch), PullRequest: strings.TrimSpace(item.PullRequest), QACommit: strings.TrimSpace(item.QACommit),
		PlanningSourceID: strings.TrimSpace(item.PlanningSourceID), PlanningSourceLane: strings.TrimSpace(item.PlanningSourceLane),
		PlanningSourceFingerprint: strings.TrimSpace(item.PlanningSourceFingerprint), PlanningDestination: strings.TrimSpace(item.PlanningDestination),
		PlanningBatchFingerprint: strings.TrimSpace(item.PlanningBatchFingerprint), PlanningBatchSize: item.PlanningBatchSize, PlanningItemIndex: item.PlanningItemIndex,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	return strings.Join([]string{
		"v2", payload.Authority, base64.RawURLEncoding.EncodeToString([]byte(payload.State)),
		base64.RawURLEncoding.EncodeToString([]byte(payload.Role)), hex.EncodeToString(mac.Sum(nil)),
	}, ":")
}

func TestInitPreviewsAndSynchronizesCurrentProjectConfiguration(t *testing.T) {
	project := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("create bin: %v", err)
	}
	writeFakeGitHubProjectCommand(t, bin)
	writeFakeInitGitCommand(t, bin)
	writeFakeCommand(t, bin, "codex", "codex-cli test-version")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	config := filepath.Join(t.TempDir(), "runner.config.json")
	var output bytes.Buffer
	if err := run(t.Context(), append([]string{"init", "--owner", "example", "--project-number", "7", "--repository", "example/repo", "--project-dir", project, "--config", config}, codexHarnessFlags()...), strings.NewReader(""), &output); err != nil {
		t.Fatalf("init: %v", err)
	}

	output.Reset()
	if err := run(t.Context(), []string{"init", "--config", config, "--dry-run"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("preview Project configuration: %v", err)
	}
	for _, expected := range []string{"Initialization dry run", "Current Project: compatible", "Needs assessment", "Plan", "Agent QA", "PR Ready"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("configuration preview missing %q: %s", expected, output.String())
		}
	}

	output.Reset()
	if err := run(t.Context(), []string{"init", "--config", config, "--prune", "--dry-run"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("dry-run Project pruning: %v", err)
	}
	if !strings.Contains(output.String(), "Prune: no extra Status options") || !strings.Contains(output.String(), "Initialization dry run") {
		t.Fatalf("unexpected prune dry-run output: %s", output.String())
	}

	output.Reset()
	if err := run(t.Context(), []string{"init", "--config", config}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("synchronize Project configuration: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "Synchronized GitHub Project example/7") {
		t.Fatalf("unexpected configuration output: %s", output.String())
	}

	output.Reset()
	if err := run(t.Context(), []string{"init", "--config", config, "--prune"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("synchronize Project pruning: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "Synchronized and pruned GitHub Project example/7") {
		t.Fatalf("unexpected prune synchronization output: %s", output.String())
	}
}

func TestAddEnqueuesPlanAndReadyWorkWhileRunnerLockIsHeld(t *testing.T) {
	bin := t.TempDir()
	writeFakeGitHubProjectCommand(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	callLog := filepath.Join(t.TempDir(), "gh-calls")
	t.Setenv("GH_CALL_LOG", callLog)
	cfg := completeCLITestConfig(t.TempDir())
	configPath := filepath.Join(t.TempDir(), "runner.config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	projectLock, err := github.AcquireProcessLock(*cfg.GitHubProject)
	if err != nil {
		t.Fatal(err)
	}
	defer projectLock.Release()

	var output bytes.Buffer
	if err := run(t.Context(), []string{"add", "plan", "--config", configPath, "--title", "Shape exports", "--body", "Define the export goal and constraints.", "--dry-run"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("preview planner request: %v", err)
	}
	if !strings.Contains(output.String(), `Would add "Shape exports" to Plan`) {
		t.Fatalf("unexpected add preview: %s", output.String())
	}

	for _, test := range []struct {
		mode   string
		title  string
		body   string
		status string
		action string
	}{
		{mode: "plan", title: "Shape exports", body: "Define the export goal and constraints.", status: "O_plan", action: "ask the planner"},
		{mode: "ready", title: "Fix export header", body: "Correct the header and cover it with a focused test.", status: "O_ready", action: "implement it"},
	} {
		output.Reset()
		if err := run(t.Context(), []string{"add", test.mode, "--config", configPath, "--title", test.title, "--body", test.body}, strings.NewReader(""), &output); err != nil {
			t.Fatalf("add %s work: %v", test.mode, err)
		}
		if !strings.Contains(output.String(), "Added PVTI_added") || !strings.Contains(output.String(), test.action) {
			t.Fatalf("unexpected %s receipt: %s", test.mode, output.String())
		}
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	logged := string(calls)
	if strings.Count(logged, "project item-create") != 2 {
		t.Fatalf("dry run mutated GitHub or add omitted a card:\n%s", logged)
	}
	for _, expected := range []string{
		"project item-create 7 --owner example --title Shape exports --body Define the export goal and constraints.",
		"--single-select-option-id O_plan",
		"project item-create 7 --owner example --title Fix export header --body Correct the header and cover it with a focused test.",
		"--single-select-option-id O_ready",
	} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("add omitted %q:\n%s", expected, logged)
		}
	}
}

func writeFakeGitHubProjectCommand(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "gh")
	content := `#!/bin/sh
[ -z "${GH_CALL_LOG:-}" ] || printf '%s\n' "$*" >> "$GH_CALL_LOG"
case "$1 $2" in
	"project create") printf '%s\n' '{"id":"PVT_created","number":8,"url":"https://github.com/users/example/projects/8"}' ;;
	"project item-create") printf '%s\n' '{"id":"PVTI_added"}' ;;
  "project view") printf '%s\n' '{"id":"PVT_test","number":7}' ;;
	"api graphql")
		case "$*" in
			*"fields(first:100,after:"*) printf '%s\n' '{"data":{"node":{"fields":{"nodes":[{"__typename":"ProjectV2SingleSelectField","id":"F_status","name":"Status","dataType":"SINGLE_SELECT","options":[{"id":"O_assessment","name":"Needs assessment"},{"id":"O_backlog","name":"Backlog"},{"id":"O_plan","name":"Plan"},{"id":"O_ready","name":"Ready"},{"id":"O_running","name":"In Progress"},{"id":"O_qa","name":"Agent QA"},{"id":"O_pr_ready","name":"PR Ready"},{"id":"O_blocked","name":"Blocked"},{"id":"O_done","name":"Done"}]},{"__typename":"ProjectV2Field","id":"F_result","name":"Runner Result","dataType":"TEXT"},{"__typename":"ProjectV2Field","id":"F_approval","name":"Runner Approval","dataType":"TEXT"},{"__typename":"ProjectV2Field","id":"F_phase","name":"Runner Phase","dataType":"TEXT"},{"__typename":"ProjectV2Field","id":"F_transition","name":"Runner Transition","dataType":"TEXT"},{"__typename":"ProjectV2Field","id":"F_activity","name":"Runner Activity","dataType":"TEXT"},{"__typename":"ProjectV2Field","id":"F_qa","name":"QA Failures","dataType":"NUMBER"},{"__typename":"ProjectV2Field","id":"F_branch","name":"Runner Branch","dataType":"TEXT"},{"__typename":"ProjectV2Field","id":"F_pr","name":"Pull Request","dataType":"TEXT"},{"__typename":"ProjectV2Field","id":"F_qa_commit","name":"QA Commit","dataType":"TEXT"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}' ;;
			*"node(id:\$item_id)"*)
				printf '{"data":{"node":{"id":"PVTI_retry","status":{"name":"Blocked"},"approval":{"text":"%s"},"result":{"text":"Previous browser blocker."},"phase":{"text":"agent_qa"},"content":{"title":"Retry browser review","body":"Acceptance criteria","repository":{"nameWithOwner":"example/repo"},"url":"https://github.com/example/repo/issues/2"}}}}\n' "$FAKE_GH_RETRY_APPROVAL" ;;
			*"items(first:100,after:"*)
				if [ -n "${FAKE_GH_RETRY_APPROVAL:-}" ]; then
					printf '{"data":{"node":{"items":{"nodes":[{"id":"PVTI_retry","status":{"name":"Blocked"},"approval":{"text":"%s"},"result":{"text":"Previous browser blocker."},"phase":{"text":"agent_qa"},"content":{"title":"Retry browser review","body":"Acceptance criteria","repository":{"nameWithOwner":"example/repo"},"url":"https://github.com/example/repo/issues/2"}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}\n' "$FAKE_GH_RETRY_APPROVAL"
				else
					printf '%s\n' '{"data":{"node":{"items":{"nodes":[{"id":"PVTI_approval","status":{"name":"Needs assessment"},"approval":{"text":""},"content":{"title":"Review public request","body":"Acceptance criteria","repository":{"nameWithOwner":"example/repo"},"url":"https://github.com/example/repo/issues/1"}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}'
				fi ;;
			*"createProjectV2View"*) printf '%s\n' '{"data":{"createProjectV2View":{"projectV2View":{"id":"PVTV_runner","name":"Board","layout":"BOARD_LAYOUT","configuration":{"visibleFields":{"nodes":[{"id":"F_title"},{"id":"F_activity"},{"id":"F_qa"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}}' ;;
			*"deleteProjectV2View"*) printf '%s\n' '{"data":{"deleteProjectV2View":{"projectV2View":{"id":"PVTV_board"}}}}' ;;
			*) printf '%s\n' '{"data":{"node":{"views":{"nodes":[{"id":"PVTV_board","name":"Board","layout":"BOARD_LAYOUT","configuration":{"visibleFields":{"nodes":[{"id":"F_title"},{"id":"F_activity"},{"id":"F_qa"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}]}}}}' ;;
		esac ;;
  "repo view") printf '%s\n' '{"nameWithOwner":"example/repo","hasIssuesEnabled":true}' ;;
  "api repos/example/repo") printf '%s\n' '{"allow_auto_merge":true,"allow_merge_commit":true,"allow_rebase_merge":true,"allow_squash_merge":true,"permissions":{"push":true}}' ;;
  "api repos/example/repo/branches/main") printf '%s\n' '{"name":"main","protected":false}' ;;
  "api repos/example/repo/rules/branches/main") printf '%s\n' '[]' ;;
  "label list") printf '%s\n' '[{"name":"needs-assessment"}]' ;;
  "issue list") printf '%s\n' '[]' ;;
  "issue view") printf '%s\n' '{"labels":[{"name":"needs-assessment"}]}' ;;
  "issue edit") exit 0 ;;
  "project item-edit") exit 0 ;;
  *) printf '%s\n' 'gh test-version' ;;
esac
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
}

func writeFakeInitGitCommand(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "git")
	content := `#!/bin/sh
if [ "$1" = "-C" ]; then
  project_dir="$2"
  shift 2
fi
case "$1 $2" in
  "rev-parse --show-toplevel") (cd "$project_dir" && pwd) ;;
	"rev-parse --verify") printf '%s\n' 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' ;;
	"config --get") printf '%s\n' 'https://github.com/example/repo.git' ;;
	"remote get-url") printf '%s\n' 'https://github.com/example/repo.git' ;;
  "ls-remote --heads") printf '%s\n' 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa refs/heads/main' ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
}

func writeFakeEmptyInitGitCommand(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "git")
	content := `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_INIT_GIT_LOG"
if [ "$1" = "-C" ]; then
  project_dir="$2"
  shift 2
fi
case "$1 $2" in
  "rev-parse --show-toplevel") (cd "$project_dir" && pwd) ;;
  "rev-parse --verify") exit 1 ;;
  "config --get") printf '%s\n' 'https://github.com/example/repo.git' ;;
  "remote get-url") printf '%s\n' 'https://github.com/example/repo.git' ;;
  "ls-remote --heads") printf '%b' "$FAKE_INIT_REMOTE_HEADS" ;;
  "symbolic-ref --quiet") printf '%s\n' 'main' ;;
  "symbolic-ref HEAD") exit 0 ;;
  "mktree ") printf '%s\n' '4b825dc642cb6eb9a060e54bf8d69288fbee4904' ;;
  "commit-tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904") printf '%s\n' 'cccccccccccccccccccccccccccccccccccccccc' ;;
  "update-ref refs/heads/main") exit 0 ;;
  "push --set-upstream") exit 0 ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake empty-repository git: %v", err)
	}
}

func TestRootHelpDescribesStandaloneWorkflow(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), []string{"--help"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("help: %v", err)
	}
	for _, expected := range []string{"cortexium-runner doctor", "cortexium-runner update", "cortexium-runner add plan|ready", "cortexium-runner plan", "cortexium-runner approve", "cortexium-runner retry", "--once", "--max-idle-interval DURATION", "No hosted control plane"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("help missing %q: %s", expected, output.String())
		}
	}
}

func TestDoctorOfflineUsesOnlyLocalConfigAndEmbeddedSkills(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.config.json")
	if err := config.SaveConfig(configPath, completeCLITestConfig("/project")); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	var output bytes.Buffer
	if err := run(t.Context(), []string{"doctor", "--offline", "--config", configPath}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("offline doctor: %v\n%s", err, output.String())
	}
	for _, expected := range []string{"config.v5", "skills.embedded", "Configuration ready: yes"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("check output missing %q: %s", expected, output.String())
		}
	}
}

func TestDoctorFixAutoDetectsProjectLocalDefaultConfig(t *testing.T) {
	project := t.TempDir()
	command := exec.Command("git", "init", "--quiet", project)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	configPath := filepath.Join(project, filepath.FromSlash(defaultRunnerConfigPath))
	if err := config.SaveConfig(configPath, completeCLITestConfig(project)); err != nil {
		t.Fatalf("write default config: %v", err)
	}
	t.Chdir(project)
	t.Setenv("HOME", t.TempDir())
	var output bytes.Buffer
	if err := run(t.Context(), []string{"doctor", "--offline", "--fix"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("auto-detected doctor fix: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "Configuration ready: yes") {
		t.Fatalf("doctor did not use default config: %s", output.String())
	}
}

func TestDoctorRejectsLiveProbeInOfflineMode(t *testing.T) {
	err := run(t.Context(), []string{"doctor", "--offline", "--probe-harnesses"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("offline live probe error = %v", err)
	}
}

func TestDoctorCapabilityExplainsBlockedTool(t *testing.T) {
	detail := "Chrome or Chromium 149+ is required for Runner's loopback-only browser; found major version 140"
	version := "Google Chrome 140.0.7339.208"
	var output bytes.Buffer
	writeDoctorCapability(&output, []setup.CapabilityState{{
		ID: "chrome", Type: config.CapabilityTypeLocalTool, Status: setup.CapabilityBlocked,
		Version: &version, Detail: &detail,
	}}, config.CapabilityTypeLocalTool, "chrome", "isolated Chrome/Chromium")

	if got := output.String(); !strings.Contains(got, version) || !strings.Contains(got, detail) {
		t.Fatalf("blocked capability output omitted diagnosis: %q", got)
	}
}

func TestDoctorReportsPinnedRepositoryReferences(t *testing.T) {
	var output strings.Builder
	writeDoctorReport(&output, setup.InspectionReport{
		RepositoryReferences: []setup.RepositoryReferenceInspection{{
			Name: "legacy-frontend", Path: "/references/legacy-frontend",
			Commit: "714128eaeb8e3805431f8fdeaa49a570e2830cea", Status: setup.CapabilityAvailable,
			Detail: "clean Git checkout matches the configured immutable commit",
		}},
	}, nil)
	for _, expected := range []string{"Repository references", "legacy-frontend", "/references/legacy-frontend", "714128eaeb8e3805431f8fdeaa49a570e2830cea", "clean Git checkout"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("doctor reference output omitted %q: %s", expected, output.String())
		}
	}
}

func TestDoctorFixReplacesOnlyDifferingBundledRoleSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(t.TempDir(), "custom-runner.json")
	if err := config.SaveConfig(configPath, completeCLITestConfig("/project")); err != nil {
		t.Fatalf("write config: %v", err)
	}
	plannerPath := filepath.Join(home, ".codex", "skills", "runner-planner", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(plannerPath), 0o755); err != nil {
		t.Fatalf("create skill directory: %v", err)
	}
	if err := os.WriteFile(plannerPath, []byte("outdated runner planner\n"), 0o644); err != nil {
		t.Fatalf("write differing skill: %v", err)
	}

	var initOutput bytes.Buffer
	err := installInitSkills(completeCLITestConfig("/project"), configPath, false, false, &initOutput)
	if err == nil || !strings.Contains(err.Error(), "cortexium-runner doctor --config \""+configPath+"\" --fix") {
		t.Fatalf("init skill error did not contain exact recovery command: %v\n%s", err, initOutput.String())
	}
	content, readErr := os.ReadFile(plannerPath)
	if readErr != nil || string(content) != "outdated runner planner\n" {
		t.Fatalf("non-fix path changed the differing skill: %q error=%v", content, readErr)
	}

	var output bytes.Buffer
	if err := run(t.Context(), []string{"doctor", "--config", configPath, "--offline", "--fix"}, strings.NewReader(""), &output); err != nil {
		t.Fatalf("doctor fix: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "replaced: runner-planner for codex") || !strings.Contains(output.String(), "Configuration ready: yes") {
		t.Fatalf("doctor fix output is not actionable: %s", output.String())
	}
	want, ok := (bundledskills.EmbeddedCatalog{}).Get("runner-planner")
	if !ok {
		t.Fatal("bundled planner skill is missing")
	}
	content, readErr = os.ReadFile(plannerPath)
	if readErr != nil || string(content) != string(want.Content) {
		t.Fatalf("doctor fix did not restore the bundled planner: error=%v", readErr)
	}
}

func TestDoctorFixRejectsJSONOutput(t *testing.T) {
	err := run(t.Context(), []string{"doctor", "--fix", "--json"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("doctor fix JSON error = %v", err)
	}
}

type liveProbeTestRunner struct {
	calls int
}

func (r *liveProbeTestRunner) Run(_ context.Context, command string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	r.calls++
	if command == "codex" {
		for index := 0; index+1 < len(args); index++ {
			if args[index] == "--output-last-message" {
				if err := os.WriteFile(args[index+1], []byte(`{"status":"ready","token":"cortexium-runner-live-probe-v1"}`), 0o600); err != nil {
					return subprocess.Result{}, err
				}
				return subprocess.Result{}, nil
			}
		}
	}
	return subprocess.Result{Stdout: `{"structured_output":{"status":"ready","token":"cortexium-runner-live-probe-v1"}}`}, nil
}

func (r *liveProbeTestRunner) RunBoundedInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, _ io.Reader, _ int, _ string) (subprocess.Result, error) {
	return r.Run(ctx, command, args, dir, timeout)
}

func (r *liveProbeTestRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, _ io.Reader, _ int, _ string) (subprocess.Result, error) {
	return r.Run(ctx, command, args, dir, timeout)
}

func TestLiveHarnessProbeGroupsRolesUsingTheSameExecutionProfile(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	enabled := true
	cfg.Harnesses = []config.HarnessConfig{{
		Kind: config.HarnessClaudeCLI, Command: "claude", Enabled: &enabled,
		WorkspaceWriteRoot: filepath.Join(t.TempDir(), "worktrees"),
	}, {
		Kind: config.HarnessCodexCLI, Command: "codex", Enabled: &enabled,
		WorkspaceWriteRoot: filepath.Join(t.TempDir(), "worktrees"),
	}}
	cfg.Roles = config.RoleTemplate(config.HarnessClaudeCLI)
	implementer := cfg.Roles[config.WorkRoleImplementer]
	implementer.Harness = config.HarnessCodexCLI
	cfg.Roles[config.WorkRoleImplementer] = implementer
	runner := &liveProbeTestRunner{}

	probes, err := probeConfiguredHarnesses(t.Context(), cfg, runner)
	if err != nil {
		t.Fatalf("probe configured harnesses: %v", err)
	}
	if len(probes) != 2 || !probes[0].Ready || !probes[1].Ready || runner.calls != 2 {
		t.Fatalf("unexpected probes %#v after %d calls", probes, runner.calls)
	}
	rolesByHarness := map[string]string{}
	for _, probe := range probes {
		rolesByHarness[probe.Harness] = strings.Join(probe.Roles, ",")
	}
	if rolesByHarness[config.HarnessClaudeCLI] != "planner,reviewer" || rolesByHarness[config.HarnessCodexCLI] != "implementer" {
		t.Fatalf("unexpected grouped roles %#v", rolesByHarness)
	}
}

func TestInitSelectsEveryExplicitlyConfiguredRoleHarness(t *testing.T) {
	workflow := config.WorkflowTemplate(true)
	cfg := config.Config{
		GitHubProject: &config.GitHubProjectConfig{Owner: "example", Number: 1},
		Workflow:      &workflow,
		Roles: map[string]config.RoleConfig{
			config.WorkRolePlanner:     {Harness: config.HarnessCodexCLI, Skills: []string{"runner-planner"}},
			config.WorkRoleImplementer: {Harness: config.HarnessCodexCLI, Skills: []string{"runner-implementer"}},
			config.WorkRoleReviewer:    {Harness: config.HarnessClaudeCLI, Skills: []string{"runner-reviewer"}},
		},
	}
	harnesses, err := configuredHarnesses(cfg)
	if err != nil {
		t.Fatalf("select harnesses: %v", err)
	}
	if strings.Join(harnesses, ",") != config.HarnessCodexCLI+","+config.HarnessClaudeCLI {
		t.Fatalf("unexpected init harnesses %#v", harnesses)
	}
}

func TestInitInfersRolesOnlyWhenOneHarnessIsAvailable(t *testing.T) {
	bin := t.TempDir()
	writeFakeCommand(t, bin, "claude", "claude-cli test-version")
	t.Setenv("PATH", bin)

	selected, err := selectInitHarnesses("", "", "")
	if err != nil {
		t.Fatalf("select one available harness: %v", err)
	}
	for _, role := range config.BuiltinRoleIDs() {
		if selected[role] != config.HarnessClaudeCLI {
			t.Fatalf("role %s selected %q, want claude", role, selected[role])
		}
	}

	writeFakeCommand(t, bin, "pi", "pi test-version")
	if _, err := selectInitHarnesses("", "", ""); err == nil || !strings.Contains(err.Error(), "multiple harnesses") {
		t.Fatalf("multiple available harnesses did not require explicit role selection: %v", err)
	}
}

func codexHarnessFlags() []string {
	return []string{
		"--harness", config.HarnessCodexCLI,
		"--reasoning", "high",
		"--base-update-review", "required",
	}
}

func completeCLITestConfig(projectDir string) config.Config {
	enabled := true
	workflow := config.WorkflowTemplate(true)
	return config.Config{
		ConfigVersion:  config.ConfigVersion,
		RunnerID:       "runner",
		ProjectDir:     projectDir,
		MaxParallelism: 1,
		Harnesses: []config.HarnessConfig{{
			Kind: config.HarnessCodexCLI, Command: "codex", Enabled: &enabled,
			WorkspaceWriteRoot: filepath.Join(filepath.Dir(projectDir), ".runner-worktrees"),
		}},
		Roles:    config.RoleTemplate(config.HarnessCodexCLI),
		Workflow: &workflow,
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "example", Number: 7, IntakeRepository: "example/repo", IntakeLabel: "needs-assessment",
			ResultField: "Runner Result", ApprovalField: "Runner Approval", PhaseField: "Runner Phase", TransitionField: config.RunnerTransitionFieldName,
			QAFailuresField: "QA Failures", BranchField: "Runner Branch", PullRequestField: "Pull Request",
			QACommitField: "QA Commit", BaseBranch: "main", RemoteName: "origin", MergeMethod: config.MergeMethodMerge,
		},
	}
}

func TestInitDoesNotRequireDefaultCLIWhenHarnessUsesTrustedCommandOverride(t *testing.T) {
	cfg := config.Config{Harnesses: []config.HarnessConfig{{Kind: config.HarnessCodexCLI, Command: "/opt/team/codex-wrapper"}}}
	tools := respectConfiguredHarnessCommands([]string{"codex", "gh", "git"}, cfg)
	if strings.Join(tools, ",") != "gh,git" {
		t.Fatalf("init retained the replaced default CLI: %#v", tools)
	}
}

func writeFakeCommand(t *testing.T, dir, name, version string) {
	t.Helper()
	path := filepath.Join(dir, name)
	help := ""
	switch name {
	case "codex":
		help = "--ephemeral\\n--json\\n--cd\\n--output-last-message\\n--output-schema\\n--config\\n--model\\n--sandbox\\n--ask-for-approval\\n--disable\\n--enable\\n--strict-config\\n--ignore-user-config\\n--ignore-rules\\n--skip-git-repo-check"
	case "claude":
		help = "--print\\n--output-format\\n--json-schema\\n--no-session-persistence\\n--model\\n--effort\\n--permission-mode\\n--settings\\n--tools\\n--allowedTools\\n--safe-mode\\n--setting-sources\\n--strict-mcp-config\\n--mcp-config\\n--disable-slash-commands\\n--no-chrome\\n--add-dir\\n--dangerously-skip-permissions"
	case "pi":
		help = "--print\\n--no-session\\n--no-extensions\\n--no-skills\\n--no-prompt-templates\\n--no-themes\\n--no-context-files\\n--no-tools\\n--tools\\n--mode\\n--append-system-prompt\\n--extension\\n--model\\n--thinking\\n--approve\\n--no-approve"
	}
	content := "#!/bin/sh\nif [ \"$1\" = \"--help\" ] || { [ \"$1\" = \"exec\" ] && [ \"$2\" = \"--help\" ]; }; then printf '%b\\n' '" + help + "'; else printf '%s\\n' '" + version + "'; fi\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}
