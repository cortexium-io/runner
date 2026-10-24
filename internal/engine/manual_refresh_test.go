package engine

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
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

// Inspect the actual disposable remote, including after publication. Returning
// the local HEAD here would accidentally certify an unpublished refresh.
type manualRefreshRunner struct {
	inner               *autoMergeReconciliationRunner
	reviewer            *candidateInspectingReviewer
	remote              string
	config              config.Config
	reviews, creates    int
	reject              bool
	failTransitionClear bool
	failTransitionStart bool
}

func (r *manualRefreshRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *manualRefreshRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "codex" {
		r.reviews++
		if r.reject {
			return (reviewerRejectRunner{project: r.inner.project}).Run(ctx, command, args, dir, timeout)
		}
		return r.reviewer.Run(ctx, command, args, dir, timeout)
	}
	if command == "gh" {
		joined := strings.Join(args, " ")
		if strings.HasPrefix(joined, "pr view ") {
			r.inner.head = runnerGitRevision(ctx, r.remote, timeout, "refs/heads/cortexium/task")
			r.inner.baseRevision = runnerGitRevision(ctx, r.remote, timeout, "refs/heads/main")
		}
		if strings.HasPrefix(joined, "pr list ") {
			return subprocess.Result{Stdout: `[{"url":"https://github.com/owner/repo/pull/12","number":12,"headRefName":"cortexium/task","baseRefName":"main"}]`}, nil
		}
		if strings.HasPrefix(joined, "pr create ") {
			r.creates++
			return subprocess.Result{}, errors.New("must retain the original pull request")
		}
		if r.failTransitionClear && strings.Contains(joined, "--field-id F_transition") && strings.Contains(joined, "--clear") {
			r.failTransitionClear = false
			return subprocess.Result{}, errors.New("interrupted after committing the QA transition")
		}
		if r.failTransitionStart && strings.Contains(joined, "--field-id F_transition") && strings.Contains(joined, "--text") {
			r.failTransitionStart = false
			return subprocess.Result{}, context.Canceled
		}
	}
	return r.inner.Run(ctx, command, args, dir, timeout)
}

func manualRefreshService(t *testing.T, method string) (*Engine, *manualRefreshRunner, github.WorkItem, workspace.Metadata) {
	t.Helper()
	original, inner, project := autoMergeReconciliationService(t, "PR Ready", false)
	items, err := original.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	item := items[0]
	item.QAFailures = 2
	item.Approval = testApproval(item)
	project.itemsJSON = `{"items":[` + projectItemJSON(item) + `]}`
	project.qaFailures = item.QAFailures
	cfg := completeEngineTestConfig(config.Config{ProjectDir: original.cfg.ProjectDir,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo", AutoMerge: false, MergeMethod: method}})
	runner := &manualRefreshRunner{inner: inner, reviewer: &candidateInspectingReviewer{project: project}, remote: filepath.Join(filepath.Dir(cfg.ProjectDir), "origin.git"), config: cfg}
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	content := github.DelegatedContentFor(item)
	metadata, err := service.workspaceForItem(t.Context(), item, content.Digest, cfg.ProjectDir)
	if err != nil {
		t.Fatal(err)
	}
	candidate := workspace.Candidate{CommitOID: item.QACommit, TreeOID: strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD^{tree}"))}
	if err := service.saveVerificationEvidence(item, content, metadata, candidate, approvedVerificationContract(content.BodySnapshot), []string{"Original candidate check passed"}); err != nil {
		t.Fatal(err)
	}
	return service, runner, item, metadata
}

func reconcileManualRefresh(t *testing.T, service *Engine) (bool, error) {
	t.Helper()
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		return false, err
	}
	_, changed, err := service.reconcilePullRequests(t.Context(), items)
	return changed, err
}

func TestManualPublishedRefreshRetainsCountersAndRequiresFreshQA(t *testing.T) {
	for _, method := range []string{config.MergeMethodMerge, config.MergeMethodRebase, config.MergeMethodSquash} {
		t.Run(method, func(t *testing.T) {
			service, runner, item, metadata := manualRefreshService(t, method)
			project := runner.inner.project
			advanceRemoteBase(t, service.cfg.ProjectDir, "new-base.txt", "independent work\n")
			if changed, err := reconcileManualRefresh(t, service); err != nil || !changed || project.status != "Agent QA" || project.phase != "agent_qa" {
				t.Fatalf("manual candidate did not return to fresh QA: changed=%t status=%q phase=%q err=%v", changed, project.status, project.phase, err)
			}
			refreshed := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD"))
			if refreshed == item.QACommit || runner.reviews != 0 || project.qaFailures != 2 {
				t.Fatalf("refresh reused acceptance or reset history: head=%s reviews=%d count=%d", refreshed, runner.reviews, project.qaFailures)
			}
			if remote := strings.TrimSpace(runGitTest(t, runner.remote, "rev-parse", "refs/heads/cortexium/task")); remote != item.QACommit {
				t.Fatal("refreshed candidate was pushed before fresh QA")
			}
			// Recreate the coordinator at the local-refresh/QA boundary. Repeated
			// reconciliation must neither discard the candidate nor replay rework.
			restarted, err := New(runner.config, runner)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if changed, err := reconcileManualRefresh(t, restarted); err != nil || changed {
					t.Fatalf("queued QA was reconciled again: changed=%t err=%v", changed, err)
				}
			}
			current, err := restarted.workspaceForItem(t.Context(), item, github.DelegatedContentFor(item).Digest, service.cfg.ProjectDir)
			if err != nil {
				t.Fatal(err)
			}
			candidate := workspace.Candidate{CommitOID: refreshed, TreeOID: strings.TrimSpace(runGitTest(t, current.WorktreePath, "rev-parse", "HEAD^{tree}"))}
			entries, err := restarted.loadVerificationEvidence(item, github.DelegatedContentFor(item), current, candidate, approvedVerificationContract(item.Body))
			if err != nil || len(entries) != 1 || entries[0].SourceCommitOID != item.QACommit {
				t.Fatalf("original evidence was lost or relabelled: %+v err=%v", entries, err)
			}
			results, err := restarted.RunCycle(t.Context())
			if err != nil || len(results) != 1 || results[0].Outcome != execution.OutcomeSucceeded || project.status != "PR Ready" {
				t.Fatalf("fresh QA/publication failed: results=%+v status=%q err=%v", results, project.status, err)
			}
			if runner.reviewer.head != refreshed || runner.reviews != 1 || project.qaCommit != refreshed || project.qaFailures != 2 || project.pullRequest != item.PullRequest || runner.creates != 0 || runner.inner.mergeRequests != 0 {
				t.Fatalf("publication did not retain exact review/PR/history/manual policy: reviews=%d reviewed=%s QA=%s count=%d PR=%s creates=%d merges=%d", runner.reviews, runner.reviewer.head, project.qaCommit, project.qaFailures, project.pullRequest, runner.creates, runner.inner.mergeRequests)
			}
			if changed, err := reconcileManualRefresh(t, restarted); err != nil || changed || runner.reviews != 1 || project.qaFailures != 2 {
				t.Fatalf("up-to-date manual PR was not stable: changed=%t err=%v", changed, err)
			}
		})
	}
}

func TestManualPublishedRefreshRecoversInterruptedTransition(t *testing.T) {
	service, runner, item, metadata := manualRefreshService(t, config.MergeMethodMerge)
	// A retained spent implementation checkpoint must not be deleted or renewed
	// by a base refresh, even when the QA transition finishes after a restart.
	snapshot, err := service.workspaceSnapshotState(t.Context(), metadata.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	output := execution.Output{Outcome: execution.OutcomeSucceeded, Summary: "Prior implementation", WorkDone: []string{"Feature implemented"}, Verification: []string{"Original candidate check passed"}}
	if err := service.saveImplementationCheckpoint(item, github.DelegatedContentFor(item), "prior context", metadata, snapshot, workspace.Candidate{CommitOID: snapshot.Head, TreeOID: snapshot.Tree}, output, time.Now().Add(-time.Hour), true); err != nil {
		t.Fatal(err)
	}
	checkpoint := service.implementationCheckpointPath(item.ID)
	before, err := os.ReadFile(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	advanceRemoteBase(t, service.cfg.ProjectDir, "new-base.txt", "independent work\n")
	runner.failTransitionClear = true
	if _, err := reconcileManualRefresh(t, service); err == nil {
		t.Fatal("interrupted transition unexpectedly succeeded")
	}
	project := runner.inner.project
	if project.transition == "" || project.status != "Agent QA" || project.qaFailures != 2 || runner.reviews != 0 {
		t.Fatalf("interruption lost the protected QA transition: status=%q transition=%q count=%d", project.status, project.transition, project.qaFailures)
	}
	restarted, err := New(runner.config, runner)
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := restarted.source.RecoverInterrupted(t.Context()); err != nil || recovered != 1 {
		t.Fatalf("recover QA transition: recovered=%d err=%v", recovered, err)
	}
	if changed, err := reconcileManualRefresh(t, restarted); err != nil || changed || project.transition != "" || project.qaFailures != 2 || runner.reviews != 0 {
		t.Fatalf("recovery replayed work or changed history: changed=%t err=%v", changed, err)
	}
	after, err := os.ReadFile(checkpoint)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refresh/recovery changed the spent implementation allowance")
	}
}

func TestManualPublishedRefreshRejectionExhaustsExistingBudget(t *testing.T) {
	service, runner, item, _ := manualRefreshService(t, config.MergeMethodMerge)
	advanceRemoteBase(t, service.cfg.ProjectDir, "new-base.txt", "independent work\n")
	if _, err := reconcileManualRefresh(t, service); err != nil {
		t.Fatal(err)
	}
	runner.reject = true
	results, err := service.RunCycle(t.Context())
	project := runner.inner.project
	if err != nil || len(results) != 1 || project.status != "Blocked" || project.phase != "ready" || project.qaFailures != 3 || runner.reviews != 1 || runner.inner.mergeRequests != 0 {
		t.Fatalf("fresh rejection did not exhaust the existing budget: results=%+v status=%q phase=%q count=%d err=%v", results, project.status, project.phase, project.qaFailures, err)
	}
	if remote := strings.TrimSpace(runGitTest(t, runner.remote, "rev-parse", "refs/heads/cortexium/task")); remote != item.QACommit {
		t.Fatal("rejected candidate was published")
	}
}

func TestManualPublishedRefreshRecoversBeforeTransition(t *testing.T) {
	service, runner, item, metadata := manualRefreshService(t, config.MergeMethodMerge)
	advanceRemoteBase(t, service.cfg.ProjectDir, "new-base.txt", "independent work\n")
	runner.failTransitionStart = true
	if _, err := reconcileManualRefresh(t, service); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation before transition, got %v", err)
	}
	refreshed := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD"))
	if refreshed == item.QACommit || runner.inner.project.status != "" {
		t.Fatal("did not stop between local refresh and remote QA transition")
	}
	restarted, err := New(runner.config, runner)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := reconcileManualRefresh(t, restarted); err != nil || !changed || runner.inner.project.status != "Agent QA" || runner.inner.project.qaFailures != 2 {
		t.Fatalf("completed local refresh lost its QA handoff: changed=%t status=%q count=%d err=%v", changed, runner.inner.project.status, runner.inner.project.qaFailures, err)
	}
	if head := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD")); head != refreshed {
		t.Fatal("recovery reconstructed a different candidate")
	}
	if remote := strings.TrimSpace(runGitTest(t, runner.remote, "rev-parse", "refs/heads/cortexium/task")); remote != item.QACommit || runner.reviews != 0 || runner.inner.mergeRequests != 0 {
		t.Fatal("interrupted refresh escaped fresh QA/manual merge policy")
	}
}

func TestManualPublishedRefreshRefusesUnsafeInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, *Engine, *manualRefreshRunner, github.WorkItem, workspace.Metadata)
		status string
		err    bool
	}{
		{"missing approval", func(t *testing.T, s *Engine, r *manualRefreshRunner, item github.WorkItem, m workspace.Metadata) {
			item.Approval = ""
			r.inner.project.itemsJSON = `{"items":[` + projectItemJSON(item) + `]}`
		}, "", true},
		{"invalid approval", func(t *testing.T, s *Engine, r *manualRefreshRunner, item github.WorkItem, m workspace.Metadata) {
			item.Approval += "invalid"
			r.inner.project.itemsJSON = `{"items":[` + projectItemJSON(item) + `]}`
		}, "", true},
		{"missing identity", func(t *testing.T, s *Engine, r *manualRefreshRunner, item github.WorkItem, m workspace.Metadata) {
			path := filepath.Join(s.implementationWorkspaceRoot(), ".runner-state", "assignment_"+safeRefComponent(item.ID)+".json")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, "Ready", false},
		{"dirty candidate", func(t *testing.T, s *Engine, r *manualRefreshRunner, item github.WorkItem, m workspace.Metadata) {
			if err := os.WriteFile(filepath.Join(m.WorktreePath, "feature.txt"), []byte("retained edit\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "Blocked", false},
		{"corrupt evidence", func(t *testing.T, s *Engine, r *manualRefreshRunner, item github.WorkItem, m workspace.Metadata) {
			if err := os.WriteFile(s.verificationEvidencePath(item.ID), []byte("invalid evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "Blocked", false},
		{"head changed on reinspection", func(t *testing.T, s *Engine, r *manualRefreshRunner, item github.WorkItem, m workspace.Metadata) {
			r.inner.onPRView = func(view int) {
				if view == 2 {
					r.inner.head = strings.Repeat("a", 40)
				}
			}
		}, "", true},
		{"authority changed on reinspection", func(t *testing.T, s *Engine, r *manualRefreshRunner, item github.WorkItem, m workspace.Metadata) {
			r.inner.onPRView = func(view int) {
				if view == 2 {
					item.Body += " unauthorized scope"
					r.inner.project.itemsJSON = `{"items":[` + projectItemJSON(item) + `]}`
				}
			}
		}, "Needs assessment", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, runner, item, metadata := manualRefreshService(t, config.MergeMethodMerge)
			advanceRemoteBase(t, service.cfg.ProjectDir, "new-base.txt", "independent work\n")
			tc.change(t, service, runner, item, metadata)
			_, err := reconcileManualRefresh(t, service)
			project := runner.inner.project
			if (err != nil) != tc.err || project.status != tc.status || project.qaFailures != 2 || runner.reviews != 0 || runner.inner.mergeRequests != 0 {
				t.Fatalf("unsafe refresh: status=%q count=%d reviews=%d merges=%d err=%v", project.status, project.qaFailures, runner.reviews, runner.inner.mergeRequests, err)
			}
			if head := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD")); head != item.QACommit {
				t.Fatal("refused refresh changed the retained candidate")
			}
			if remote := strings.TrimSpace(runGitTest(t, runner.remote, "rev-parse", "refs/heads/cortexium/task")); remote != item.QACommit {
				t.Fatal("refused refresh changed the published head")
			}
		})
	}
}

func TestManualPublishedRefreshRetainsConflictsAndCounts(t *testing.T) {
	service, runner, item, metadata := manualRefreshService(t, config.MergeMethodMerge)
	advanceRemoteBase(t, service.cfg.ProjectDir, "feature.txt", "competing feature\n")
	if changed, err := reconcileManualRefresh(t, service); err != nil || !changed || runner.inner.project.status != "Ready" || runner.inner.project.phase != "ready" || runner.inner.project.qaFailures != 2 {
		t.Fatalf("conflict lost the implementation route/history: changed=%t status=%q count=%d err=%v", changed, runner.inner.project.status, runner.inner.project.qaFailures, err)
	}
	if files := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "diff", "--name-only", "--diff-filter=U")); files != "feature.txt" {
		t.Fatalf("conflicts were not retained: %q", files)
	}
	for i := 0; i < 2; i++ {
		if changed, err := reconcileManualRefresh(t, service); err != nil || changed || runner.inner.project.qaFailures != 2 {
			t.Fatalf("conflict was mistaken for human rework: changed=%t err=%v", changed, err)
		}
	}
	if head := strings.TrimSpace(runGitTest(t, runner.remote, "rev-parse", "refs/heads/cortexium/task")); head != item.QACommit || runner.reviews != 0 || runner.inner.mergeRequests != 0 {
		t.Fatal("conflicted refresh was reviewed/published/merged")
	}
}

func TestUpToDateManualPullRequestDoesNotRequireWorkspaceIdentity(t *testing.T) {
	service, runner, project := autoMergeReconciliationService(t, "PR Ready", false)
	service.cfg.GitHubProject.AutoMerge = false
	path := filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", "assignment_"+safeRefComponent("PVTI_auto_merge_reconcile")+".json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if changed, err := reconcileManualRefresh(t, service); err != nil || changed || project.status != "" || runner.mergeRequests != 0 {
		t.Fatalf("current manual PR attempted refresh/merge: changed=%t status=%q err=%v", changed, project.status, err)
	}
}
