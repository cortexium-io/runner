package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type disappearingPRBranchRunner struct {
	inner          *autoMergeReconciliationRunner
	failAt         int
	fetches        int
	failed         bool
	state          string
	changedHead    bool
	inspectionErr  error
	blockedUpdates int
}

func (r *disappearingPRBranchRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" && len(args) > 0 && args[0] == "fetch" && strings.Contains(strings.Join(args, " "), "refs/heads/cortexium/task:") {
		r.fetches++
		if r.fetches == r.failAt {
			r.failed = true
			return subprocess.Result{ExitCode: 128, Stderr: "fatal: couldn't find remote ref refs/heads/cortexium/task"}, errors.New("exit status 128")
		}
	}
	if command == "gh" && projectUpdateSelects(args, "PVTI_auto_merge_reconcile", "O_blocked") {
		r.blockedUpdates++
	}
	isView := command == "gh" && len(args) > 1 && args[0] == "pr" && args[1] == "view"
	if r.failed && isView && r.inspectionErr != nil {
		return subprocess.Result{}, r.inspectionErr
	}
	result, err := r.inner.Run(ctx, command, args, dir, timeout)
	if r.failed && isView {
		result.Stdout = strings.ReplaceAll(result.Stdout, `"state":"OPEN"`, fmt.Sprintf(`"state":%q`, r.state))
		if r.changedHead {
			result.Stdout = strings.ReplaceAll(result.Stdout, r.inner.head, "different-reviewed-head")
		}
	}
	return result, err
}

func TestReconciliationRechecksTerminalPRAfterBranchFetchFailure(t *testing.T) {
	for _, boundary := range []struct {
		name    string
		fetch   int
		warning string
	}{
		{"base comparison", 1, "Pull request branch inspection failed."},
		{"worktree synchronization", 2, "Pull request worktree preparation failed."},
	} {
		for _, state := range []string{"MERGED", "CLOSED", "OPEN", "mismatched merged head", "inspection unavailable"} {
			t.Run(boundary.name+"/"+state, func(t *testing.T) {
				original, inner, project := autoMergeReconciliationService(t, "PR Ready", true)
				runner := &disappearingPRBranchRunner{inner: inner, failAt: boundary.fetch, state: state}
				if state == "mismatched merged head" {
					runner.state, runner.changedHead = "MERGED", true
				}
				if state == "inspection unavailable" {
					runner.inspectionErr = errors.New("PR inspection unavailable")
				}
				service, err := New(completeEngineTestConfig(config.Config{
					ProjectDir:    original.cfg.ProjectDir,
					GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo", AutoMerge: true},
				}), runner)
				if err != nil {
					t.Fatal(err)
				}
				items, err := service.source.LifecycleItems(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				warnings, changed, err := service.reconcilePullRequests(t.Context(), items)
				if !runner.failed {
					t.Fatal("test did not reach the intended fetch boundary")
				}
				if state == "inspection unavailable" {
					if !errors.Is(err, runner.inspectionErr) || !errors.Is(err, errPullRequestBranchFetch) || changed || project.status != "" {
						t.Fatalf("lost failure context or changed state without evidence: changed=%t status=%q err=%v", changed, project.status, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				switch state {
				case "MERGED":
					if !changed || project.status != "Done" || runner.blockedUpdates != 0 || len(warnings) != 0 {
						t.Fatalf("merged PR did not go directly to Done: changed=%t status=%q blocked=%d warnings=%#v", changed, project.status, runner.blockedUpdates, warnings)
					}
				case "CLOSED":
					if !changed || project.status != "Blocked" || !strings.Contains(project.result, "closed without merge") || len(warnings) != 0 {
						t.Fatalf("closed PR did not use its terminal event: status=%q result=%q warnings=%#v", project.status, project.result, warnings)
					}
				case "OPEN":
					if !changed || project.status != "Blocked" || len(warnings) != 1 || warnings[0].Summary != boundary.warning {
						t.Fatalf("open PR fetch failure was ignored: status=%q warnings=%#v", project.status, warnings)
					}
				default:
					if changed || project.status != "" || len(warnings) != 1 || !strings.Contains(warnings[0].Summary, "no longer matches") {
						t.Fatalf("unreviewed merged head was accepted: changed=%t status=%q warnings=%#v", changed, project.status, warnings)
					}
				}
			})
		}
	}
}
