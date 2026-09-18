package execution

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

type codexCapacityFailureRunner struct {
	subprocess.OSRunner
	args [][]string
	dirs []string
}

func (r *codexCapacityFailureRunner) RunBoundedHeadTailInput(_ context.Context, _ string, args []string, dir string, _ time.Duration, _ io.Reader, _ int, _ string) (subprocess.Result, error) {
	r.args = append(r.args, append([]string(nil), args...))
	r.dirs = append(r.dirs, dir)
	return subprocess.Result{
		Stdout:   `{"type":"turn.failed","error":{"message":"Selected model is at capacity. Please try a different model."}}`,
		ExitCode: 1,
	}, errors.New("exit status 1")
}

func TestCodexModelCapacityRetainsImplementationWorkspaceAndProfile(t *testing.T) {
	cfg := testWorkspaceWriteConfig(t)
	runner := &codexCapacityFailureRunner{}
	executor := NewCodexExecutor(cfg, runner)
	assignment := Assignment{Spec: testCodexCLIWorkspaceWriteAssignmentSpec()}
	var retainedCommit string

	// Two caller-controlled attempts: the adapter must not silently fall back
	// or retry internally, and the retry must reuse the retained worktree.
	for attempt := 1; attempt <= 2; attempt++ {
		output, err := executor.ExecuteWorkspaceWrite(t.Context(), assignment, nil)
		if err == nil || output.FailureClass != FailureCapacityExhausted || output.RetryDisposition != RetryAutomatic {
			t.Fatalf("capacity failure did not reach the retry policy: output=%#v err=%v", output, err)
		}
		if !output.RemoteDetailSafe || !output.DiscardDiagnostics || output.ReviewAssessment != nil || len(runner.args) != attempt {
			t.Fatalf("capacity failure became a review or internal fallback: calls=%d output=%#v", len(runner.args), output)
		}
		args := runner.args[attempt-1]
		if !containsArgPair(args, "--model", "configured-model") || !containsArgPair(args, "-c", `model_reasoning_effort="medium"`) {
			t.Fatalf("capacity retry changed the configured profile: %v", args)
		}
		if runner.dirs[attempt-1] == cfg.Harness.WorkingDir || runner.dirs[attempt-1] != runner.dirs[0] {
			t.Fatalf("capacity retry lost the isolated workspace: %v", runner.dirs)
		}
		if info, err := os.Stat(runner.dirs[attempt-1]); err != nil || !info.IsDir() {
			t.Fatalf("capacity failure discarded the implementation workspace: %v", err)
		}
		path := filepath.Join(runner.dirs[0], "retained.txt")
		if attempt == 1 {
			if err := os.WriteFile(path, []byte("retained candidate\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runGitCommand(t, runner.dirs[0], "add", "retained.txt")
			runGitCommand(t, runner.dirs[0], "commit", "-m", "Retained candidate")
			retainedCommit = runGitCommandOutput(t, runner.dirs[0], "rev-parse", "HEAD")
		} else {
			content, err := os.ReadFile(path)
			if err != nil || string(content) != "retained candidate\n" || runGitCommandOutput(t, runner.dirs[0], "rev-parse", "HEAD") != retainedCommit {
				t.Fatalf("capacity retry discarded retained work: content=%q error=%v", content, err)
			}
		}
	}
}
