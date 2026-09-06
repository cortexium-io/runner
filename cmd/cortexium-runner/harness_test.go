package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type harnessConformanceTestRunner struct {
	failPlanner  bool
	modelPrompts []string
	modelDirs    []string
}

func (r *harnessConformanceTestRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if filepath.Base(command) == "git" {
		return subprocess.OSRunner{}.Run(ctx, command, args, dir, timeout)
	}
	if len(args) == 1 && args[0] == "--version" {
		return subprocess.Result{Stdout: "codex-cli conformance-test\n"}, nil
	}
	if len(args) == 1 && args[0] == "--help" {
		return subprocess.Result{Stdout: strings.Join([]string{"--sandbox", "--ask-for-approval", "--disable", "--enable", "--config", "--strict-config"}, "\n")}, nil
	}
	if len(args) == 2 && args[0] == "exec" && args[1] == "--help" {
		return subprocess.Result{Stdout: strings.Join([]string{"--ephemeral", "--json", "--cd", "--output-last-message", "--output-schema", "--model", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check"}, "\n")}, nil
	}
	return subprocess.Result{}, errors.New("unexpected harness invocation without bounded input")
}

func (r *harnessConformanceTestRunner) RunBoundedInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return r.runModel(ctx, command, args, dir, timeout, input)
}

func (r *harnessConformanceTestRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return r.runModel(ctx, command, args, dir, timeout, input)
}

func (r *harnessConformanceTestRunner) RunLineFilteredInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string, _ subprocess.LineFilter) (subprocess.Result, error) {
	return r.runModel(ctx, command, args, dir, timeout, input)
}

func (r *harnessConformanceTestRunner) runModel(_ context.Context, command string, args []string, dir string, _ time.Duration, input io.Reader) (subprocess.Result, error) {
	if filepath.Base(command) != config.HarnessCodexCLI {
		return subprocess.Result{}, errors.New("unexpected command: " + command)
	}
	prompt, err := io.ReadAll(input)
	if err != nil {
		return subprocess.Result{}, err
	}
	text := string(prompt)
	r.modelPrompts = append(r.modelPrompts, text)
	r.modelDirs = append(r.modelDirs, dir)
	if r.failPlanner && strings.Contains(text, "return status ready") {
		return subprocess.Result{Stderr: "planner unavailable", ExitCode: 1}, errors.New("planner unavailable")
	}

	response := ""
	switch {
	case strings.Contains(text, "return status ready"):
		response = `{"status":"ready","marker":"cortexium-runner-conformance-v1"}`
	case strings.Contains(text, "Prove Runner implementer browser conformance"):
		if err := os.WriteFile(filepath.Join(dir, "browser-probe-dom.txt"), []byte(`<html><body data-runner-ready="yes">runner-browser-conformance</body></html>`), 0o600); err != nil {
			return subprocess.Result{}, err
		}
		response = `{"outcome":"succeeded","summary":"Browser fixture rendered.","work_done":["Rendered the fixture."],"verification":["Browser executed JavaScript.","DOM contains both markers.","git diff --check passed."],"blockers":[]}`
	case strings.Contains(text, "Prove Runner implementer conformance"):
		if err := os.WriteFile(filepath.Join(dir, "conformance-write.txt"), []byte(conformanceWrite), 0o600); err != nil {
			return subprocess.Result{}, err
		}
		response = `{"outcome":"succeeded","summary":"Conformance file written.","work_done":["Created conformance-write.txt."],"verification":["Exact content verified.","git diff --check passed."],"blockers":[]}`
	case strings.Contains(text, "Shared reviewer evidence-audit stage"):
		response = `{"criteria":{"P1":{"status":"passed","summary":"The fixture is correct.","evidence":["The expected marker is present."]}},"repository_rules":{"status":"passed","summary":"No rule violation exists.","evidence":["The fixture is unchanged."]},"maintainability":{"status":"passed","summary":"The fixture is minimal.","evidence":["Only two focused fixture files exist."]},"summary":"The known-good fixture passes."}`
	default:
		return subprocess.Result{}, errors.New("unexpected conformance prompt")
	}
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--output-last-message" {
			if err := os.WriteFile(args[index+1], []byte(response), 0o600); err != nil {
				return subprocess.Result{}, err
			}
			return subprocess.Result{Stdout: "{\"type\":\"message\"}\n"}, nil
		}
	}
	return subprocess.Result{}, errors.New("Codex conformance invocation omitted the structured result path")
}

func TestHarnessCheckExercisesEveryConfiguredRoleWithoutTouchingProject(t *testing.T) {
	project := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(configPath, completeCLITestConfig(project)); err != nil {
		t.Fatalf("save config: %v", err)
	}
	bin := t.TempDir()
	writeFakeCommand(t, bin, config.HarnessCodexCLI, "codex-cli conformance-test")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	runner := &harnessConformanceTestRunner{}

	var output bytes.Buffer
	if err := runHarnessCheck(t.Context(), []string{"--config", configPath}, &output, runner); err != nil {
		t.Fatalf("harness check: %v\n%s", err, output.String())
	}
	for _, expected := range []string{
		"planner (planner)", "implementer (implementer)", "reviewer (reviewer)",
		"authentication, structured output", "isolated worktree write", "shared review contract",
		"available to this profile but not exercised", "Harness conformance: passed",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("harness check output omitted %q:\n%s", expected, output.String())
		}
	}
	if len(runner.modelPrompts) != 3 {
		t.Fatalf("model calls = %d, want one per configured role", len(runner.modelPrompts))
	}
	entries, err := os.ReadDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("configured project was changed: %#v", entries)
	}
	for _, dir := range runner.modelDirs {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary model workspace still exists at %s: %v", dir, err)
		}
	}
}

func TestHarnessCheckCanExerciseConfiguredBrowserProfiles(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(configPath, completeCLITestConfig(t.TempDir())); err != nil {
		t.Fatalf("save config: %v", err)
	}
	bin := t.TempDir()
	writeFakeCommand(t, bin, config.HarnessCodexCLI, "codex-cli conformance-test")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	runner := &harnessConformanceTestRunner{}

	var output bytes.Buffer
	if err := runHarnessCheck(t.Context(), []string{"--config", configPath, "--browser"}, &output, runner); err != nil {
		t.Fatalf("browser harness check: %v\n%s", err, output.String())
	}
	if calls := len(runner.modelPrompts); calls != 5 {
		t.Fatalf("model calls = %d, want three role calls plus two browser calls", calls)
	}
	if count := strings.Count(output.String(), "configured role rendered the temporary browser fixture successfully"); count != 2 {
		t.Fatalf("browser success count = %d, want two:\n%s", count, output.String())
	}
	browserPrompts := 0
	for _, prompt := range runner.modelPrompts {
		if strings.Contains(prompt, "browser conformance") {
			browserPrompts++
			for _, expected := range []string{"navigate", "evaluate", "ALL_TOOLS", "tools object"} {
				if !strings.Contains(prompt, expected) {
					t.Fatalf("browser conformance prompt omitted %q:\n%s", expected, prompt)
				}
			}
		}
	}
	if browserPrompts != 2 {
		t.Fatalf("browser conformance prompts = %d, want two", browserPrompts)
	}
}

func TestBrowserConformanceOutcomeErrorRetainsModelDetail(t *testing.T) {
	blocker := "runner_browser was not present in either callable tool catalog"
	err := browserConformanceOutcomeError("browser check failed", execution.Output{
		Outcome: execution.OutcomeBlocked,
		Summary: "Browser verification could not run.",
		Blocker: &blocker,
	})
	if !strings.Contains(err.Error(), `outcome="blocked"`) || !strings.Contains(err.Error(), blocker) {
		t.Fatalf("browser conformance error omitted useful detail: %v", err)
	}
}

func TestHarnessCheckContinuesAfterIndependentRoleFailure(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(configPath, completeCLITestConfig(t.TempDir())); err != nil {
		t.Fatalf("save config: %v", err)
	}
	bin := t.TempDir()
	writeFakeCommand(t, bin, config.HarnessCodexCLI, "codex-cli conformance-test")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	runner := &harnessConformanceTestRunner{failPlanner: true}

	var output bytes.Buffer
	err := runHarnessCheck(t.Context(), []string{"--config", configPath}, &output, runner)
	if err == nil || !strings.Contains(err.Error(), "conformance failed") {
		t.Fatalf("harness check error = %v", err)
	}
	if len(runner.modelPrompts) != 3 || !strings.Contains(output.String(), "planner unavailable") || !strings.Contains(output.String(), "Harness conformance: failed") {
		t.Fatalf("independent profiles did not complete after failure: calls=%d\n%s", len(runner.modelPrompts), output.String())
	}
}

func TestHarnessCheckRejectsTooShortTimeout(t *testing.T) {
	err := runHarnessCheck(t.Context(), []string{"--timeout", "500ms"}, io.Discard, &harnessConformanceTestRunner{})
	if err == nil || !strings.Contains(err.Error(), "at least one second") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestHarnessCLIReportsBrowserStartupFailureForEveryRole(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(configPath, completeCLITestConfig(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	var help []string
	for _, role := range []execution.RoleContract{execution.RolePlanner, execution.RoleImplementer, execution.RoleReviewer} {
		root, command, err := execution.RequiredHarnessFlags(config.HarnessCodexCLI, role, config.RoleAccessSandboxed, config.HarnessConfigModeIsolated)
		if err != nil {
			t.Fatal(err)
		}
		help = append(help, root...)
		help = append(help, command...)
	}
	const fatal = "Error: thread/start: thread/start failed: error creating thread: Fatal error: Failed to initialize session: required MCP servers failed to initialize: runner_browser: MCP client startup timed out after 60s (code -32603)"
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*--version*) printf '%s\\n' 'codex-cli startup-fixture'; exit 0;;\n" +
		"*--help*) printf '%s\\n' '" + strings.Join(help, " ") + "'; exit 0;;\nesac\n" +
		"printf '%s\\n' '" + fatal + "' >&2\nexit 1\n"
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, config.HarnessCodexCLI), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var output bytes.Buffer
	err := run(t.Context(), []string{"harness", "check", "--config", configPath}, strings.NewReader(""), &output)
	if err == nil || !strings.Contains(err.Error(), "conformance failed") {
		t.Fatalf("startup failure did not fail the CLI check: %v\n%s", err, output.String())
	}
	if count := strings.Count(output.String(), fatal); count != 3 {
		t.Fatalf("expected one startup diagnostic per role, got %d:\n%s", count, output.String())
	}
}
