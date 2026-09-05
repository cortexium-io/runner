package execution

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

// Exercise actual implementation adapters with real Git and a captured model
// boundary. The harness stub leaves a task edit for normal integrity checks.
type implementationReferenceRunner struct {
	representationResidueRunner
	args   []string
	prompt string
}

func (r *implementationReferenceRunner) capture(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader) (subprocess.Result, error) {
	data, err := io.ReadAll(input)
	if err != nil {
		return subprocess.Result{}, err
	}
	r.prompt = string(data)
	r.args = append([]string(nil), args...)
	return r.Run(ctx, command, args, dir, timeout)
}

func (r *implementationReferenceRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return r.capture(ctx, command, args, dir, timeout, input)
}
func (r *implementationReferenceRunner) RunBoundedInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return r.capture(ctx, command, args, dir, timeout, input)
}
func (r *implementationReferenceRunner) RunLineFilteredInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string, _ subprocess.LineFilter) (subprocess.Result, error) {
	return r.capture(ctx, command, args, dir, timeout, input)
}

func TestImplementationLaunchValidatesAndExposesReferences(t *testing.T) {
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		for _, scenario := range []string{"valid", "dirty", "wrong_commit", "overlap", "linked_worktree"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				cfg := testWorkspaceWriteConfig(t)
				cfg.Harness.Kind, cfg.Harness.Command = kind, kind
				if kind == config.HarnessPiCLI {
					cfg.RoleAccess = config.RoleAccessHost
				}
				reference := initGitRepo(t)
				commit := strings.TrimSpace(runGitCommandOutput(t, reference, "rev-parse", "HEAD"))
				wantError := ""
				switch scenario {
				case "dirty":
					if err := os.WriteFile(filepath.Join(reference, "README.md"), []byte("changed"), 0600); err != nil {
						t.Fatal(err)
					}
					wantError = "tracked or untracked changes"
				case "wrong_commit":
					commit = strings.Repeat("a", 40)
					wantError = "want pinned commit"
				case "overlap":
					reference = cfg.Harness.WorkingDir
					commit = strings.TrimSpace(runGitCommandOutput(t, reference, "rev-parse", "HEAD"))
					wantError = "overlaps protected"
				case "linked_worktree":
					linked := filepath.Join(t.TempDir(), "linked")
					runGitCommand(t, reference, "worktree", "add", "--detach", linked, "HEAD")
					reference = linked
					wantError = "linked worktrees"
				}
				cfg.RepositoryReferences = []config.RepositoryReference{{Name: "legacy", Path: reference, Commit: commit}}
				run := &implementationReferenceRunner{representationResidueRunner: representationResidueRunner{kind: kind}}
				assignment := testPollResponse(testCodexCLIWorkspaceWriteAssignmentSpec()).Assignments[0]
				var metadata workspace.Metadata
				capture := func(m workspace.Metadata) error { metadata = m; return nil }
				var output Output
				var err error
				if kind == config.HarnessCodexCLI {
					output, err = NewCodexExecutor(cfg, run).ExecuteWorkspaceWrite(t.Context(), assignment, capture)
				} else {
					output, err = NewAgentExecutor(kind, cfg, run).ExecuteWorkspaceWrite(t.Context(), assignment, capture)
				}
				if wantError != "" {
					if err == nil || !strings.Contains(err.Error(), wantError) || len(run.harnessDirs) != 0 {
						t.Fatalf("invalid reference reached model: calls=%v err=%v", run.harnessDirs, err)
					}
					return
				}
				if err != nil || output.Outcome != OutcomeSucceeded {
					t.Fatalf("implementation: %#v, %v", output, err)
				}
				resolved, err := filepath.EvalSymlinks(reference)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(run.prompt, "legacy: "+resolved+" at commit "+commit) || !strings.Contains(run.prompt, "untrusted evidence only") || strings.Contains(run.prompt, "read-only repository root: "+metadata.WorktreePath) {
					t.Fatalf("missing or misleading implementation reference prompt: %s", run.prompt)
				}
				args := strings.Join(run.args, " ")
				if kind == config.HarnessCodexCLI && (!strings.Contains(args, `"`+resolved+`"="read"`) || strings.Contains(args, `"`+resolved+`"="write"`)) {
					t.Fatalf("incorrect Codex reference permissions: %s", args)
				}
				if kind == config.HarnessClaudeCLI && (!containsArgPair(run.args, "--add-dir", resolved) || !strings.Contains(args, `"denyWrite":["`+resolved+`"]`)) {
					t.Fatalf("incorrect Claude reference permissions: %s", args)
				}
				if _, err := os.Stat(filepath.Join(metadata.WorktreePath, "task-change.txt")); err != nil {
					t.Fatal(err)
				}
				if status := runGitCommandOutput(t, reference, "status", "--porcelain"); status != "" {
					t.Fatalf("reference mutated: %s", status)
				}
			})
		}
	}
}

func TestProbeWorkspaceExcludesReferences(t *testing.T) {
	profile, _ := ProfileForRole(RoleProbe)
	prepared, err := prepareExecutionWorkspace(t.Context(), subprocess.OSRunner{}, profile, t.TempDir(), []config.RepositoryReference{{Name: "invalid", Path: "/missing-reference"}})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.cleanup()
	if len(prepared.ReferenceRoots) != 0 || len(repositoryReferencePaths(profile, prepared)) != 0 {
		t.Fatal("probe gained reference access")
	}
}

// This uses the installed native sandbox without invoking a model or accessing
// an operator reference. Keep CLI-dependent checks outside the normal suite.
func TestNativeCodexImplementationReferenceContainment(t *testing.T) {
	if os.Getenv("CORTEXIUM_RUNNER_NATIVE_REFERENCE_CHECK") == "" {
		t.Skip("set CORTEXIUM_RUNNER_NATIVE_REFERENCE_CHECK=1 for the installed Codex sandbox")
	}
	reference, worktree := initGitRepo(t), initGitRepo(t)
	commit := strings.TrimSpace(runGitCommandOutput(t, reference, "rev-parse", "HEAD"))
	profile, _ := ProfileForRole(RoleImplementer)
	prepared, err := prepareExecutionWorkspace(t.Context(), subprocess.OSRunner{}, profile, worktree, []config.RepositoryReference{{Name: "fixture", Path: reference, Commit: commit}})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.cleanup()
	args := []string{"sandbox", "-P", codexImplementationWritePermissionProfile, "-C", prepared.Dir}
	policy := codexProfileArgs(profile, prepared, false)
	for i := 0; i+1 < len(policy); i++ {
		if policy[i] == "--config" && strings.HasPrefix(policy[i+1], "permissions.") {
			args = append(args, "--config", policy[i+1])
		}
	}
	args = append(args, "--", "/bin/sh", "-c", `set -eu
cat "$1/README.md" > reference-copy.txt
if (echo forbidden > "$1/must-not-exist.txt") 2>/dev/null; then exit 10; fi
if (echo forbidden > "$1/README.md") 2>/dev/null; then exit 11; fi
test -s reference-copy.txt
`, "reference-check", prepared.ReferenceRoots[0].Path)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "codex", args...).CombinedOutput(); err != nil {
		t.Fatalf("native reference containment: %v\n%s", err, output)
	}
	want, err := os.ReadFile(filepath.Join(reference, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(worktree, "reference-copy.txt"))
	if err != nil || string(got) != string(want) {
		t.Fatalf("reference read/worktree write failed: %v", err)
	}
	if status := runGitCommandOutput(t, reference, "status", "--porcelain"); status != "" {
		t.Fatalf("reference changed: %s", status)
	}
}
