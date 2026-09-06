package execution

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

// Captured before Codex emitted any JSONL session events; no model ran.
const observedCodexBrowserStartupTimeout = `Reading prompt from stdin...
2026-09-06T08:15:05.452276Z ERROR codex_core::session: Failed to create session: required MCP servers failed to initialize: runner_browser: MCP client startup timed out after 60s
Error: thread/start: thread/start failed: error creating thread: Fatal error: Failed to initialize session: required MCP servers failed to initialize: runner_browser: MCP client startup timed out after 60s (code -32603)
`

type codexStartupFailureRunner struct {
	subprocess.OSRunner
	calls          int
	activeCheckout string
}

func (r *codexStartupFailureRunner) RunBoundedHeadTailInput(ctx context.Context, _ string, _ []string, dir string, timeout time.Duration, input io.Reader, limit int, marker string) (subprocess.Result, error) {
	r.calls++
	if r.activeCheckout != "" {
		if err := os.WriteFile(filepath.Join(r.activeCheckout, "unexpected.txt"), []byte("changed"), 0o600); err != nil {
			return subprocess.Result{}, err
		}
	}
	// Exercise actual bounded capture, exit status and process teardown, without
	// invoking a model, downloading a package, or sleeping for the real timeout.
	return r.OSRunner.RunBoundedHeadTailInput(ctx, "sh", []string{"-c", `printf '%s' "$1" >&2; exit 1`, "startup-fixture", observedCodexBrowserStartupTimeout}, dir, timeout, input, limit, marker)
}

func TestCodexBrowserStartupRecoveryAcrossExecutionEntrypoints(t *testing.T) {
	for _, entrypoint := range []string{"executor", "implementer", "planner", "reviewer"} {
		t.Run(entrypoint, func(t *testing.T) {
			cfg := testWorkspaceWriteConfig(t)
			cfg.SafeTools = true
			runner := &codexStartupFailureRunner{}
			executor := NewCodexExecutor(cfg, runner)
			var output Output
			var err error
			switch entrypoint {
			case "executor":
				output, err = executor.Execute(t.Context(), Assignment{Spec: testCodexCLIAssignmentSpec()})
			case "implementer":
				output, err = executor.ExecuteWorkspaceWrite(t.Context(), Assignment{Spec: testCodexCLIWorkspaceWriteAssignmentSpec()}, nil)
			case "planner":
				var result StructuredHarnessResult
				result, err = RunPlannerWithUsage(t.Context(), config.HarnessCodexCLI, cfg, cfg.Harness.WorkingDir, "Plan this work.", []byte(`{"type":"object"}`), runner)
				var automatic bool
				output, automatic = result.AutomaticRetryOutput()
				if !automatic {
					t.Fatalf("planner lost automatic recovery: %#v, %v", result, err)
				}
			case "reviewer":
				output, err = executor.Execute(t.Context(), reviewerAssignment())
			}
			if err == nil || runner.calls != 1 || output.FailureClass != FailureBrowserStartup || output.RetryDisposition != RetryAutomatic {
				t.Fatalf("startup failure was not recoverable: calls=%d output=%#v err=%v", runner.calls, output, err)
			}
			if output.Outcome != OutcomeBlocked || output.ReviewAssessment != nil || !output.RemoteDetailSafe || !strings.Contains(output.Summary, "runner_browser") {
				t.Fatalf("startup failure became a review verdict or lost its fixed diagnostic: %#v", output)
			}
			if output.DiscardDiagnostics || !strings.Contains(err.Error(), strings.TrimSpace(observedCodexBrowserStartupTimeout)) {
				t.Fatalf("lost the local startup diagnostic: output=%#v err=%v", output, err)
			}
		})
	}
}

func TestCodexBrowserStartupEvidenceRequiresExactPreSessionEnvelope(t *testing.T) {
	exitErr := &exec.ExitError{}
	fatal := strings.Split(strings.TrimSpace(observedCodexBrowserStartupTimeout), "\n")[2]
	for _, stderr := range []string{observedCodexBrowserStartupTimeout, fatal, "Reading prompt from stdin...\n" + fatal} {
		evidence := codexFailureEvidence(subprocess.Result{Stderr: stderr, ExitCode: 1}, exitErr, true)
		if evidence.FailureClass != FailureBrowserStartup || evidence.RetryDisposition != RetryAutomatic {
			t.Fatalf("known pre-session envelope was not recognized: %#v", evidence)
		}
	}

	for _, test := range []struct {
		name   string
		stdout string
		stderr string
	}{
		{name: "session started", stdout: `{"type":"thread.started","thread_id":"started"}`, stderr: observedCodexBrowserStartupTimeout},
		{name: "model text", stdout: `{"type":"item.completed","item":{"type":"agent_message","text":"MCP client startup timed out after 60s"}}`, stderr: observedCodexBrowserStartupTimeout},
		{name: "nonterminal error", stdout: `{"type":"error","message":"MCP client startup timed out after 60s"}`, stderr: observedCodexBrowserStartupTimeout},
		{name: "malformed stdout", stdout: "{", stderr: observedCodexBrowserStartupTimeout},
		{name: "truncated stdout", stdout: harnessTruncationMarker, stderr: observedCodexBrowserStartupTimeout},
		{name: "truncated stderr", stderr: harnessTruncationMarker + observedCodexBrowserStartupTimeout},
		{name: "tool output", stderr: "tool said: " + fatal},
		{name: "additional diagnostic", stderr: "token=private\n" + fatal},
		{name: "diagnostic after fatal", stderr: fatal + "\noperation failed"},
		{name: "another server", stderr: strings.ReplaceAll(observedCodexBrowserStartupTimeout, "runner_browser", "operator_browser")},
		{name: "multiple failed servers", stderr: strings.ReplaceAll(observedCodexBrowserStartupTimeout, "runner_browser:", "another_server: denied, runner_browser:")},
		{name: "invalid timestamp", stderr: strings.ReplaceAll(observedCodexBrowserStartupTimeout, "2026-09-06T08:15:05.452276Z", "not-a-timestamp")},
		{name: "timeout phrase only", stderr: "runner_browser: MCP client startup timed out after 60s"},
		{name: "different startup failure", stderr: strings.ReplaceAll(fatal, "timed out after 60s", "connection closed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if evidence := codexFailureEvidence(subprocess.Result{Stdout: test.stdout, Stderr: test.stderr, ExitCode: 1}, exitErr, true); evidence != (HarnessFailureEvidence{}) {
				t.Fatalf("unproven pre-session timeout gained recovery authority: %#v", evidence)
			}
		})
	}
	for _, test := range []struct {
		name      string
		err       error
		exitCode  int
		safeTools bool
	}{
		{name: "success", exitCode: 0, safeTools: true},
		{name: "generic error", err: errors.New("exit status 1"), exitCode: 1, safeTools: true},
		{name: "cleanup failed", err: errors.Join(exitErr, errors.New("tear down process group")), exitCode: 1, safeTools: true},
		{name: "canceled", err: context.Canceled, exitCode: 1, safeTools: true},
		{name: "attempt timed out", err: context.DeadlineExceeded, exitCode: 1, safeTools: true},
		{name: "signal exit", err: exitErr, exitCode: -1, safeTools: true},
		{name: "browser not granted", err: exitErr, exitCode: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if evidence := codexFailureEvidence(subprocess.Result{Stderr: observedCodexBrowserStartupTimeout, ExitCode: test.exitCode}, test.err, test.safeTools); evidence != (HarnessFailureEvidence{}) {
				t.Fatalf("unsafe process state gained recovery authority: %#v", evidence)
			}
		})
	}
}

func TestCodexBrowserStartupRecoveryDoesNotOverrideIntegrityFailure(t *testing.T) {
	cfg := testWorkspaceWriteConfig(t)
	cfg.SafeTools = true
	runner := &codexStartupFailureRunner{activeCheckout: cfg.Harness.WorkingDir}
	output, err := NewCodexExecutor(cfg, runner).ExecuteWorkspaceWrite(t.Context(), Assignment{Spec: testCodexCLIWorkspaceWriteAssignmentSpec()}, nil)
	if err == nil || output.FailureClass != FailureIntegrityViolation || output.RetryDisposition != RetryNone {
		t.Fatalf("startup recovery hid integrity failure: output=%#v err=%v", output, err)
	}
	if !strings.Contains(err.Error(), "active checkout changed") || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected workspace-integrity evidence, got %v", err)
	}
}
