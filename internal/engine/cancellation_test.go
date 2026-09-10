package engine

import (
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

type canceledReviewerRunner struct {
	project     *fakeGitHubProjectRunner
	cancel      context.CancelFunc
	mutate      bool
	unavailable bool
	stopped     bool
}

func (r *canceledReviewerRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *canceledReviewerRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "codex" {
		if r.mutate {
			if err := os.WriteFile(filepath.Join(profileReadRoot(args, dir), "unexpected.txt"), []byte("changed\n"), 0o600); err != nil {
				return subprocess.Result{}, err
			}
		}
		r.cancel()
		r.stopped = true
		return subprocess.Result{}, context.Canceled
	}
	if r.stopped && r.unavailable && command == "git" && strings.Contains(strings.Join(args, " "), "--no-optional-locks") {
		return subprocess.Result{}, errors.New("cannot read snapshot after cancellation")
	}
	if err := ctx.Err(); err != nil {
		return subprocess.Result{}, err
	}
	return (reviewerAcceptRunner{project: r.project}).Run(ctx, command, args, dir, timeout)
}

func TestCanceledQAStillChecksIntegrityAndNeverPublishes(t *testing.T) {
	for _, mode := range []string{"unchanged", "mutated", "unverified"} {
		t.Run(mode, func(t *testing.T) {
			repo, _ := createPublicationRepository(t)
			item := github.WorkItem{ID: "PVTI_cancel_qa", Title: "Review candidate", Body: "Criteria", Repository: "owner/repo", Status: "Agent QA", Phase: "agent_qa", Branch: "runner/retained", QAFailures: 2}
			item.Approval = testApproval(item)
			project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaFailures: 2}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			runner := &canceledReviewerRunner{project: project, cancel: cancel, mutate: mode == "mutated", unavailable: mode == "unverified"}
			service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo}), runner)
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := workspace.NewGitProvider(runner).Prepare(t.Context(), service.workspaceRequestForItem(item, github.DelegatedContentFor(item).Digest, repo, false))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "feature.txt"), []byte("ready\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			results, err := service.RunCycle(ctx)
			if (err != nil && !errors.Is(err, context.Canceled)) || len(results) != 1 {
				t.Fatalf("results=%#v error=%v", results, err)
			}
			wantClass, wantStatus := execution.FailureCanceled, "Agent QA"
			if mode == "mutated" {
				wantClass, wantStatus = execution.FailureIntegrityViolation, "Blocked"
			}
			if mode == "unverified" {
				wantClass, wantStatus = execution.FailureIntegrityUnverified, "Blocked"
				if project.phase != "agent_qa" || strings.Contains(project.result, "detected a workspace") {
					t.Fatalf("incomplete check alleged mutation or lost retry phase: %s / %s", project.result, project.phase)
				}
			}
			if results[0].FailureClass != string(wantClass) || project.status != wantStatus || project.qaFailures != 2 || project.pullRequest != "" {
				t.Fatalf("cancellation lost integrity/recovery boundary: %#v status=%s failures=%d", results, project.status, project.qaFailures)
			}
		})
	}
}

func TestClarificationPreservesRetryLaneWithoutPublishingQuestion(t *testing.T) {
	item := github.WorkItem{ID: "PVTI_question", Title: "Work", Body: "Criteria", Repository: "owner/repo", Status: "In Progress", Phase: "ready", Role: config.WorkRoleImplementer, QAFailures: 2}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaFailures: 2}
	service, err := New(completeEngineTestConfig(config.Config{}), project)
	if err != nil {
		t.Fatal(err)
	}
	action := mustAuthorizeTest(t, service.source, item)
	_, lane := service.laneForItem(item)
	question := "private diagnostic: secret-example"
	output := execution.Output{Outcome: execution.OutcomeNeedsInput, Summary: question, Blocker: &question, FailureClass: execution.FailureNeedsInput, RetryDisposition: execution.RetryManual}
	result := service.failExecution(t.Context(), action, lane, RunResult{Item: item}, "Implementation failed", errors.New(question), output)
	if result.Summary != "Awaiting human input." || result.FailureClass != "needs_input" || !strings.Contains(result.Error, question) || project.phase != "ready" || project.qaFailures != 2 || strings.Contains(project.result, question) {
		t.Fatalf("clarification lost private evidence or recovery state: %#v project=%q phase=%s", result, project.result, project.phase)
	}
	if plan, err := service.PlanProjectItemRetry(t.Context(), item.ID); err != nil || plan.TargetLaneID != "ready" {
		t.Fatalf("retry=%#v error=%v", plan, err)
	}
}

func TestCanceledAttemptReturnsCardToInterruptedLaneWithFreshContext(t *testing.T) {
	item := github.WorkItem{
		ID: "PVTI_cancel", Title: "Retain interrupted work", Body: "Acceptance criteria", URL: "https://github.com/owner/repo/issues/1",
		Repository: "owner/repo", Status: "In Progress", Phase: "ready", Role: config.WorkRoleImplementer,
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[]}`}
	project.itemsJSON = `{"items":[` + projectItemJSON(item) + `]}`
	service, err := New(completeEngineTestConfig(config.Config{}), project)
	if err != nil {
		t.Fatal(err)
	}
	action, err := service.source.Authorize(t.Context(), github.WorkItem{ID: item.ID})
	if err != nil {
		t.Fatalf("authorize item: %v", err)
	}
	_, lane := service.laneForItem(action.Item)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	blocker := "Confirm why Runner stopped."
	result := service.failExecution(canceled, action, lane, RunResult{Item: action.Item}, "Implementation failed", context.Canceled, execution.Output{
		Outcome: execution.OutcomeBlocked, Summary: "Harness execution was canceled.", WorkDone: []string{}, Blocker: &blocker,
		RemoteDetailSafe: true, DiscardDiagnostics: true, FailureClass: execution.FailureCanceled, RetryDisposition: execution.RetryNone,
	})
	if project.status != "Ready" || project.phase != "" {
		t.Fatalf("canceled card was stranded or blocked: status=%q phase=%q result=%#v", project.status, project.phase, result)
	}
	if project.qaFailures != 0 {
		t.Fatalf("cancellation incremented QA failures: %d", project.qaFailures)
	}
	if !strings.Contains(project.result, "Runner stopped") || result.FailureClass != string(execution.FailureCanceled) {
		t.Fatalf("cancellation recovery evidence is incomplete: project=%q result=%#v", project.result, result)
	}
	if result.Error != "" {
		t.Fatalf("unexpected cancellation error: %s", result.Error)
	}
}
