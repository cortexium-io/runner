package engine

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type blockedImplementationRunner struct {
	project *fakeGitHubProjectRunner
	outcome string
	calls   int
}

func (r *blockedImplementationRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *blockedImplementationRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "codex" {
		r.calls++
		if err := os.WriteFile(filepath.Join(dir, "partial.txt"), []byte("retained work\n"), 0o644); err != nil {
			return subprocess.Result{}, err
		}
		content, err := json.Marshal(map[string]any{
			"outcome": r.outcome, "summary": "private-summary: fixture checks passed, persistence not run",
			"work_done": []string{"Prepared the bounded proof."}, "verification": []string{"Fixture checks passed; persistence not run."},
			"blockers": []string{"private-question: nominate a disposable record and supply a local session"},
		})
		if err != nil {
			return subprocess.Result{}, err
		}
		return subprocess.Result{}, os.WriteFile(argumentValue(args, "--output-last-message"), content, 0o600)
	}
	return (permissionDeniedImplementationRunner{project: r.project}).Run(ctx, command, args, dir, timeout)
}

func TestAgentStopsRetainWorkspaceAndRetryWithoutPublishingDiagnostics(t *testing.T) {
	for _, outcome := range []string{execution.OutcomeBlocked, execution.OutcomeNeedsInput} {
		t.Run(outcome, func(t *testing.T) {
			repo, _ := createPublicationRepository(t)
			item := github.WorkItem{ID: "PVTI_stop", Title: "Integrated proof", Body: "Acceptance criteria", Repository: "owner/repo", Status: "Ready", Role: config.WorkRoleImplementer, QAFailures: 2}
			item.Approval = testApproval(item)
			project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaFailures: 2}
			runner := &blockedImplementationRunner{project: project, outcome: outcome}
			service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo}), runner)
			if err != nil {
				t.Fatal(err)
			}
			historyStore := metrics.NewStore(filepath.Join(t.TempDir(), "metrics.jsonl"))
			service.SetMetricsObserver(historyStore.Append)
			results, err := service.RunCycle(t.Context())
			if err != nil || len(results) != 1 {
				t.Fatalf("results=%#v error=%v", results, err)
			}
			result := results[0]
			wantClass, wantSummary := "agent_blocked", "Work blocked."
			if outcome == execution.OutcomeNeedsInput {
				wantClass, wantSummary = "needs_input", "Awaiting human input."
			}
			if result.Outcome != outcome || result.Summary != wantSummary || result.FailureClass != wantClass || result.RetryDisposition != "manual" ||
				project.status != "Blocked" || project.phase != "ready" || project.qaFailures != 2 || runner.calls != 1 {
				t.Fatalf("agent stop lost its recovery contract: result=%#v status=%s phase=%s QA=%d calls=%d", result, project.status, project.phase, project.qaFailures, runner.calls)
			}
			if !strings.Contains(result.Error, "private-question:") || !strings.Contains(result.Error, "private-summary:") ||
				strings.Contains(project.result, "private-") || strings.Contains(project.result, "Implementation failed") || !strings.Contains(project.result, "cortexium-runner retry") {
				t.Fatalf("agent stop lost local diagnostics or published them: result=%#v project=%s", result, project.result)
			}
			if project.pullRequest != "" || project.qaCommit != "" || result.WorktreeCleaned {
				t.Fatalf("blocked work reached publication or cleanup: %#v", result)
			}
			if content, err := os.ReadFile(filepath.Join(result.WorktreePath, "partial.txt")); err != nil || string(content) != "retained work\n" {
				t.Fatalf("partial work was discarded: %s %v", content, err)
			}
			if head := runGitTest(t, result.WorktreePath, "rev-parse", "HEAD"); head != runGitTest(t, repo, "rev-parse", "HEAD") {
				t.Fatal("incomplete work was committed as a candidate")
			}
			if plan, err := service.PlanProjectItemRetry(t.Context(), item.ID); err != nil || plan.TargetLaneID != "ready" {
				t.Fatalf("retry=%#v error=%v", plan, err)
			}
			if again, err := service.RunCycle(t.Context()); err != nil || len(again) != 0 || runner.calls != 1 {
				t.Fatalf("blocked work was automatically retried: results=%#v error=%v calls=%d", again, err, runner.calls)
			}
			history, err := historyStore.Read()
			if err != nil || history.MalformedRecords != 0 || len(history.Attempts) != 1 || !history.Attempts[0].Completed ||
				history.Attempts[0].FailureClass != wantClass || history.Attempts[0].RetryDisposition != "manual" || len(history.Attempts[0].Verification) != 1 {
				t.Fatalf("agent stop lost durable outcome/evidence: %#v %v", history, err)
			}
		})
	}
}
