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
	"github.com/cortexium-io/runner/internal/workspace"
)

type decisionReviewerRunner struct {
	project *fakeGitHubProjectRunner
	calls   int
}

func (r *decisionReviewerRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *decisionReviewerRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command != "codex" {
		return (reviewerAcceptRunner{project: r.project}).Run(ctx, command, args, dir, timeout)
	}
	r.calls++
	encoded, err := reviewerContentForSchema(args, true, "Independent defect: unrelated state was deleted.")
	if err != nil {
		return subprocess.Result{}, err
	}
	var content map[string]any
	if err := json.Unmarshal(encoded, &content); err != nil {
		return subprocess.Result{}, err
	}
	content["requirements"] = execution.ReviewCheckResult{Status: "needs_input", Summary: "Should names preserve case or be lowercased?", Evidence: []string{"Both requirements are approved without a priority."}}
	content["summary"] = "A human decision is required; an independent defect is retained."
	encoded, err = json.Marshal(content)
	if err != nil {
		return subprocess.Result{}, err
	}
	return subprocess.Result{}, os.WriteFile(argumentValue(args, "--output-last-message"), encoded, 0o600)
}

func TestReviewDecisionPausesWithoutRejectionAndRetainsQuestions(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_review_decision", Title: "Normalize names", Body: "Preserve case and lowercase names.", Repository: "owner/repo",
		URL: "https://github.com/owner/repo/issues/1", Status: "Agent QA", Phase: "agent_qa", Branch: "cortexium/decision", QAFailures: 2,
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaFailures: item.QAFailures}
	runner := &decisionReviewerRunner{project: project}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo}), runner)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), service.workspaceRequestForItem(item, github.DelegatedContentFor(item).Digest, repo, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "feature.txt"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := metrics.NewStore(filepath.Join(t.TempDir(), "metrics", "metrics.jsonl"))
	service.SetMetricsObserver(store.Append)
	results, err := service.RunCycle(t.Context())
	if err != nil || len(results) != 1 {
		t.Fatalf("QA cycle: %#v %v", results, err)
	}
	result := results[0]
	if result.MetricsError != "" {
		t.Fatalf("decision metrics: %s", result.MetricsError)
	}
	if result.Outcome != execution.OutcomeNeedsInput || result.ReviewVerdict != "needs_input" || result.FailureClass != "needs_input" || result.RetryDisposition != "manual" || result.Summary != "Awaiting human input." {
		t.Fatalf("decision did not use human recovery: %#v", result)
	}
	if runner.calls != 1 || project.qaFailures != 2 || project.status != "Blocked" || project.phase != "agent_qa" || project.pullRequest != "" || project.qaCommit != "" || result.WorktreeCleaned {
		t.Fatalf("decision triggered verification, rejection, or publication: %#v; project=%#v", result, project)
	}
	feedback, err := service.loadReviewFeedbackRecord(item, github.DelegatedContentFor(item))
	if err != nil || feedback == nil {
		t.Fatalf("decision feedback was not retained: %#v %v", feedback, err)
	}
	text := strings.Join(feedback.Items, "\n")
	if !strings.Contains(text, "Should names preserve case") || !strings.Contains(text, "Independent defect") || feedback.Baseline != nil {
		t.Fatalf("decision lost context or became a reusable rejected baseline: %#v", feedback)
	}
	if len(project.postedComments) != 1 || !strings.Contains(project.postedComments[0], "Human decision needed") || !strings.Contains(project.postedComments[0], "Should names preserve case") || strings.Contains(project.result, "Should names preserve case") {
		t.Fatalf("question was not delivered through the intended issue surface: %#v, %s", project.postedComments, project.result)
	}
	if again, err := service.RunCycle(t.Context()); err != nil || len(again) != 0 || runner.calls != 1 {
		t.Fatalf("decision was automatically retried: %#v %v", again, err)
	}
	plan, err := service.PlanProjectItemRetry(t.Context(), item.ID)
	if err != nil || plan.TargetLaneID != "agent_qa" {
		t.Fatalf("manual retry lost review destination: %#v %v", plan, err)
	}
	history, err := store.Read()
	if err != nil || history.MalformedRecords != 0 || len(history.Attempts) != 1 || history.Attempts[0].ReviewVerdict != "needs_input" || metrics.Summarize(history.Attempts).ReviewBlockedAttempts != 1 {
		t.Fatalf("decision was not recorded distinctly: %#v %v", history, err)
	}
}
