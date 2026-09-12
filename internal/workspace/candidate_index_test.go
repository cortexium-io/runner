//go:build !windows

package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

type indexLockRunner struct {
	calls int
	after func()
}

func (r *indexLockRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	result, err := (subprocess.OSRunner{}).Run(ctx, command, args, dir, timeout)
	if command == "git" && slices.Contains(args, "update-index") {
		r.calls++
		if r.after != nil {
			r.after()
		}
	}
	return result, err
}

func TestConstructCandidateHandlesIndexLockContention(t *testing.T) {
	for _, mode := range []string{"released", "persistent", "canceled", "deleted path"} {
		t.Run(mode, func(t *testing.T) {
			repo := initGitRepo(t)
			provider := NewGitProvider(subprocess.OSRunner{})
			metadata, err := provider.Prepare(t.Context(), boundRequest(Request{
				WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"),
				WorkID: "index_lock", BranchPrefix: "runner", BaseRef: "HEAD",
			}))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "deleted path" {
				if err := os.Remove(filepath.Join(metadata.WorktreePath, "README.md")); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(metadata.gitDirectory, "index.lock")
			lockBytes := []byte("owned by another Git process\n")
			if err := os.WriteFile(lockPath, lockBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			runner := &indexLockRunner{}
			runner.after = func() {
				if runner.calls != 1 {
					return
				}
				if mode == "released" || mode == "deleted path" {
					// The test lock owner releases it after Git's first refusal.
					// No scheduling race or sleep controls this interleaving.
					if err := os.Remove(lockPath); err != nil {
						t.Fatal(err)
					}
				} else if mode == "canceled" {
					cancel()
				}
			}
			candidate, err := NewGitProvider(runner).ConstructCandidate(ctx, metadata, "Candidate")
			if mode == "released" || mode == "deleted path" {
				if err != nil || candidate.CommitOID == "" || runner.calls < 2 {
					t.Fatalf("released index lock did not resume candidate construction: candidate=%#v calls=%d err=%v", candidate, runner.calls, err)
				}
				if got := runGitTest(t, metadata.WorktreePath, "--no-optional-locks", "status", "--porcelain"); got != "" {
					t.Fatalf("constructed candidate is dirty: %q", got)
				}
				return
			}
			if mode == "persistent" && (!errors.Is(err, ErrCandidateIndexBusy) || runner.calls != 3) {
				t.Fatalf("persistent lock was not bounded/classified: calls=%d err=%v", runner.calls, err)
			}
			if mode == "canceled" && (!errors.Is(err, context.Canceled) || runner.calls != 1) {
				t.Fatalf("cancellation retried Git or lost its error: calls=%d err=%v", runner.calls, err)
			}
			if retained, err := os.ReadFile(lockPath); err != nil || string(retained) != string(lockBytes) {
				t.Fatalf("Runner changed another process's index lock: %q %v", retained, err)
			}
		})
	}
}

type indexFailureRunner struct {
	result subprocess.Result
	err    error
	calls  int
}

func (r *indexFailureRunner) Run(context.Context, string, []string, string, time.Duration) (subprocess.Result, error) {
	r.calls++
	return r.result, r.err
}

func TestCandidateIndexRetryRefusesOtherErrors(t *testing.T) {
	profile := subprocess.PrivilegedGitProfile{
		WorkTree: "/repo", GitDirectory: "/repo/.git", CommonDirectory: "/repo/.git",
		IndexFile: "/repo/.git/index", ObjectDirectory: "/repo/.git/objects",
	}
	lockDiagnostic := "fatal: Unable to create '/repo/.git/index.lock': File exists.\n"
	for _, test := range []struct {
		name   string
		stderr string
		exit   int
		err    error
	}{
		{"other index", "fatal: Unable to create '/other/index.lock': File exists.\n", 128, errors.New("exit status 128")},
		{"permission", "fatal: Unable to create '/repo/.git/index.lock': Permission denied\n", 128, errors.New("exit status 128")},
		{"prefixed diagnostic", "untrusted output\n" + lockDiagnostic, 128, errors.New("exit status 128")},
		{"other exit", lockDiagnostic, 1, errors.New("exit status 1")},
		{"cleanup failure", lockDiagnostic, 128, errors.Join(errors.New("exit status 128"), errors.New("process cleanup failed"))},
		{"capture overflow", lockDiagnostic, 128, errors.Join(errors.New("exit status 128"), &subprocess.CaptureLimitError{Command: "git", Stream: "stdout", Limit: 1})},
		{"timeout", lockDiagnostic, 128, context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &indexFailureRunner{result: subprocess.Result{ExitCode: test.exit, Stderr: test.stderr}, err: test.err}
			err := NewGitProvider(runner).updateCandidateIndex(t.Context(), profile, "--force-remove", "--", "feature.txt")
			if err == nil || errors.Is(err, ErrCandidateIndexBusy) || runner.calls != 1 {
				t.Fatalf("unrecognized failure entered lock recovery: calls=%d err=%v", runner.calls, err)
			}
		})
	}
}
