package execution

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type codexTerminalFailureRunner struct{ subprocess.OSRunner }

func (r codexTerminalFailureRunner) RunBoundedHeadTailInput(ctx context.Context, _ string, _ []string, dir string, timeout time.Duration, input io.Reader, limit int, marker string) (subprocess.Result, error) {
	// More than the capture limit, followed by the actual CLI failure. No model,
	// browser or network call is needed to reproduce the lost-diagnostic path.
	script := `awk 'BEGIN { for (i=0; i<20000; i++) print "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"opening progress\"}}" }'
printf '%s\n' '{"type":"turn.failed","error":{"message":"Synthetic terminal failure: transport stopped."}}'
printf '%s\n' 'Reading prompt from stdin...' >&2
exit 1`
	return r.OSRunner.RunBoundedHeadTailInput(ctx, "sh", []string{"-c", script}, dir, timeout, input, limit, marker)
}

func TestCodexTerminalDiagnosticSurvivesAcrossExecutionEntrypoints(t *testing.T) {
	for _, entrypoint := range []string{"executor", "implementer", "planner", "reviewer"} {
		t.Run(entrypoint, func(t *testing.T) {
			cfg := testWorkspaceWriteConfig(t)
			runner := codexTerminalFailureRunner{}
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
				output.FailureClass, output.RetryDisposition = result.FailureClass, result.RetryDisposition
			case "reviewer":
				output, err = executor.Execute(t.Context(), reviewerAssignment())
			}
			if err == nil || !strings.Contains(err.Error(), "Synthetic terminal failure: transport stopped.") || strings.Contains(err.Error(), "opening progress") {
				t.Fatalf("terminal diagnostic lost or replaced by progress: %v", err)
			}
			if output.FailureClass != FailureUnknown || output.RetryDisposition != RetryNone || output.RemoteDetailSafe || strings.Contains(output.Summary, "Synthetic") {
				t.Fatalf("local diagnostics changed remote reporting or recovery policy: %#v", output)
			}
		})
	}
}

func TestCodexFailureDiagnosticSelectionAndBounds(t *testing.T) {
	terminal := `{"type":"turn.failed","error":{"message":"Actual terminal reason"}}`
	for _, test := range []struct {
		name, stdout, stderr, want string
	}{
		{"terminal wins", terminal, "startup noise", "Actual terminal reason"},
		{"stderr tail", "tool output", strings.Repeat("noise", 1000) + "final stderr reason", "final stderr reason"},
		{"stdout tail", strings.Repeat("noise", 1000) + "final stdout reason", "", "final stdout reason"},
		{"malformed event", "{malformed", "real stderr reason", "real stderr reason"},
		{"startup stderr does not hide stdout", strings.Repeat("noise", 1000) + "final stdout reason", "Reading prompt from stdin...", "final stdout reason"},
		{"missing output", "", "", "exit failure"},
		{"unicode tail", strings.Repeat("例", 2000) + " final reason", "", "final reason"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("exit failure")
			err := codexCommandFailure(cause, subprocess.Result{Stdout: test.stdout, Stderr: test.stderr, ExitCode: 1})
			if !errors.Is(err, cause) || !strings.Contains(err.Error(), test.want) || len(err.Error()) > len(cause.Error())+2+4000 || !utf8.ValidString(err.Error()) {
				t.Fatalf("unbounded or incomplete local diagnostic: %v", err)
			}
		})
	}
}

func TestCodexFailureDoesNotAcceptPartialModelResult(t *testing.T) {
	runner := &structuredResultCommandRunner{
		rawResult:     testStructuredResult("Everything succeeded."),
		commandResult: subprocess.Result{ExitCode: 1, Stdout: `{"type":"turn.failed","error":{"message":"Finalization failed"}}`},
		err:           errors.New("exit status 1"),
	}
	output, err := NewCodexExecutor(testCodexConfig(t), runner).Execute(t.Context(), Assignment{Spec: testCodexCLIAssignmentSpec()})
	if err == nil || !strings.Contains(err.Error(), "Finalization failed") || strings.Contains(err.Error(), "Everything succeeded") || output.Outcome != OutcomeBlocked || output.FailureClass != FailureUnknown {
		t.Fatalf("partial model result hid the process failure: output=%#v err=%v", output, err)
	}
}
