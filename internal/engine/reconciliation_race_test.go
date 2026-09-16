package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
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

// Both fetches can race with deletion of a terminal PR's branch.
var reconciliationFetchBoundaries = []struct {
	name    string
	fetch   int
	warning string
}{
	{"base comparison", 1, "Pull request branch inspection failed."},
	{"worktree synchronization", 2, "Pull request worktree preparation failed."},
}

func newBranchFetchFailure(t *testing.T, fetch int) (*Engine, *disappearingPRBranchRunner, []github.WorkItem) {
	t.Helper()
	original, inner, _ := autoMergeReconciliationService(t, "PR Ready", true)
	runner := &disappearingPRBranchRunner{inner: inner, failAt: fetch}
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
	return service, runner, items
}

func TestReconciliationRecoversMergedPRAfterFetchFailure(t *testing.T) {
	for _, boundary := range reconciliationFetchBoundaries {
		t.Run(boundary.name, func(t *testing.T) {
			service, runner, items := newBranchFetchFailure(t, boundary.fetch)
			runner.state = "MERGED"

			warnings, changed, err := service.reconcilePullRequests(t.Context(), items)

			if !runner.failed || err != nil {
				t.Fatalf("fetch boundary reached=%t, error=%v", runner.failed, err)
			}
			if !changed || runner.inner.project.status != "Done" || runner.blockedUpdates != 0 || len(warnings) != 0 {
				t.Fatalf("merged PR: changed=%t status=%q blocked=%d warnings=%#v", changed, runner.inner.project.status, runner.blockedUpdates, warnings)
			}
		})
	}
}

func TestReconciliationBlocksClosedPRAfterFetchFailure(t *testing.T) {
	for _, boundary := range reconciliationFetchBoundaries {
		t.Run(boundary.name, func(t *testing.T) {
			service, runner, items := newBranchFetchFailure(t, boundary.fetch)
			runner.state = "CLOSED"

			warnings, changed, err := service.reconcilePullRequests(t.Context(), items)

			if !runner.failed || err != nil {
				t.Fatalf("fetch boundary reached=%t, error=%v", runner.failed, err)
			}
			project := runner.inner.project
			if !changed || project.status != "Blocked" || !strings.Contains(project.result, "closed without merge") || len(warnings) != 0 {
				t.Fatalf("closed PR: changed=%t status=%q result=%q warnings=%#v", changed, project.status, project.result, warnings)
			}
		})
	}
}

func TestReconciliationReportsOpenPRFetchFailure(t *testing.T) {
	for _, boundary := range reconciliationFetchBoundaries {
		t.Run(boundary.name, func(t *testing.T) {
			service, runner, items := newBranchFetchFailure(t, boundary.fetch)
			runner.state = "OPEN"

			warnings, changed, err := service.reconcilePullRequests(t.Context(), items)

			if !runner.failed || err != nil {
				t.Fatalf("fetch boundary reached=%t, error=%v", runner.failed, err)
			}
			if !changed || runner.inner.project.status != "Blocked" || len(warnings) != 1 || warnings[0].Summary != boundary.warning {
				t.Fatalf("open PR: changed=%t status=%q warnings=%#v; want %q", changed, runner.inner.project.status, warnings, boundary.warning)
			}
		})
	}
}

func TestReconciliationRejectsChangedMergedHeadAfterFetchFailure(t *testing.T) {
	for _, boundary := range reconciliationFetchBoundaries {
		t.Run(boundary.name, func(t *testing.T) {
			service, runner, items := newBranchFetchFailure(t, boundary.fetch)
			runner.state, runner.changedHead = "MERGED", true

			warnings, changed, err := service.reconcilePullRequests(t.Context(), items)

			if !runner.failed || err != nil {
				t.Fatalf("fetch boundary reached=%t, error=%v", runner.failed, err)
			}
			if changed || runner.inner.project.status != "" || len(warnings) != 1 || !strings.Contains(warnings[0].Summary, "no longer matches") {
				t.Fatalf("unreviewed head: changed=%t status=%q warnings=%#v", changed, runner.inner.project.status, warnings)
			}
		})
	}
}

func TestReconciliationPreservesInspectionErrorAfterFetchFailure(t *testing.T) {
	for _, boundary := range reconciliationFetchBoundaries {
		t.Run(boundary.name, func(t *testing.T) {
			service, runner, items := newBranchFetchFailure(t, boundary.fetch)
			runner.inspectionErr = errors.New("PR inspection unavailable")

			_, changed, err := service.reconcilePullRequests(t.Context(), items)

			if !runner.failed {
				t.Fatal("test did not reach the intended fetch boundary")
			}
			if !errors.Is(err, runner.inspectionErr) || !errors.Is(err, errPullRequestBranchFetch) || changed || runner.inner.project.status != "" {
				t.Fatalf("inspection failed: changed=%t status=%q error=%v", changed, runner.inner.project.status, err)
			}
		})
	}
}
