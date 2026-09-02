package engine

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type eventWhileHarnessRunner struct {
	project *fakeGitHubProjectRunner

	gitMu sync.Mutex
	mu    sync.Mutex

	mergeRequested   bool
	pullRequestViews int

	implementationStarted chan struct{}
	releaseImplementation chan struct{}
	pullRequestReconciled chan struct{}
	startedOnce           sync.Once
	reconciledOnce        sync.Once
}

func (r *eventWhileHarnessRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *eventWhileHarnessRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	joined := strings.Join(args, " ")
	switch {
	case command == "git":
		r.gitMu.Lock()
		defer r.gitMu.Unlock()
		return runEngineTestGit(ctx, args, dir, timeout)
	case command == "codex":
		r.startedOnce.Do(func() { close(r.implementationStarted) })
		select {
		case <-r.releaseImplementation:
		case <-ctx.Done():
			return subprocess.Result{}, ctx.Err()
		}
		encoded, err := json.Marshal(map[string]any{
			"outcome": execution.OutcomeSucceeded, "summary": "Implemented after the independent event was reconciled.",
			"work_done": []string{"Completed the approved card."}, "verification": []string{"Verified the completed card."}, "blockers": []string{},
		})
		if err != nil {
			return subprocess.Result{}, err
		}
		if err := os.WriteFile(argumentValue(args, "--output-last-message"), encoded, 0o600); err != nil {
			return subprocess.Result{}, err
		}
		return subprocess.Result{}, nil
	case command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view":
		r.mu.Lock()
		r.pullRequestViews++
		merged := r.mergeRequested
		r.mu.Unlock()
		state := "OPEN"
		mergeState := "CLEAN"
		if merged {
			state = "MERGED"
			mergeState = "UNKNOWN"
		}
		return subprocess.Result{Stdout: `{"url":"https://github.com/owner/repo/pull/12","number":12,"state":"` + state + `","headRepository":{"nameWithOwner":"owner/repo"},"headRefName":"cortexium/event-test","headRefOid":"qa-head","baseRefName":"main","baseRefOid":"","mergeStateStatus":"` + mergeState + `","comments":[],"reviews":[]}`}, nil
	default:
		result, err := r.project.Run(ctx, command, args, dir, timeout)
		if err == nil && command == "gh" && strings.HasPrefix(joined, "project item-edit ") && argumentValue(args, "--id") == "PVTI_event_pr" && strings.Contains(joined, "--single-select-option-id O_done") {
			r.reconciledOnce.Do(func() { close(r.pullRequestReconciled) })
		}
		return result, err
	}
}

func (r *eventWhileHarnessRunner) requestMerge() {
	r.mu.Lock()
	r.mergeRequested = true
	r.mu.Unlock()
}

func (r *eventWhileHarnessRunner) viewCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pullRequestViews
}

func TestRunLoopReconcilesIndependentPullRequestWhileHarnessActionIsRunning(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	implementation := github.WorkItem{
		ID: "PVTI_event_implementation", Title: "Run a deliberately slow implementation", Body: "Complete the independent implementation.",
		Repository: "owner/repo", Status: "Ready",
	}
	implementation.Approval = testApproval(implementation)
	pullRequest := github.WorkItem{
		ID: "PVTI_event_pr", Title: "Observe an independently merged pull request", Body: "Reconcile the pull request event.",
		Repository: "owner/repo", Status: "Agent QA", PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/event-test", QACommit: "qa-head",
	}
	pullRequest.Approval = testApproval(pullRequest)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(implementation) + `,` + projectItemJSON(pullRequest) + `]}`}
	runner := &eventWhileHarnessRunner{
		project: project, implementationStarted: make(chan struct{}), releaseImplementation: make(chan struct{}), pullRequestReconciled: make(chan struct{}),
	}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, MaxParallelism: 1,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), runner)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	implementationCompleted := make(chan struct{})
	var completedOnce sync.Once
	loopDone := make(chan error, 1)
	go func() {
		loopDone <- service.RunLoop(ctx, 5*time.Millisecond, 5*time.Millisecond, func(result RunResult) {
			if result.Item.ID == implementation.ID {
				completedOnce.Do(func() { close(implementationCompleted) })
			}
		}, nil, nil)
	}()

	select {
	case <-runner.implementationStarted:
	case <-ctx.Done():
		t.Fatal("implementation did not start")
	}
	runner.requestMerge()
	select {
	case <-runner.pullRequestReconciled:
	case <-ctx.Done():
		t.Fatal("merged pull request was not reconciled while implementation was running")
	}
	select {
	case <-implementationCompleted:
		t.Fatal("implementation completed before its harness was released")
	default:
	}
	close(runner.releaseImplementation)
	select {
	case <-implementationCompleted:
	case <-ctx.Done():
		t.Fatal("implementation did not complete after release")
	}
	cancel()
	if err := <-loopDone; err != nil {
		t.Fatalf("run loop: %v", err)
	}
	if views := runner.viewCount(); views < 2 {
		t.Fatalf("pull request was inspected %d times, want at least two observations", views)
	}

	project.mu.Lock()
	defer project.mu.Unlock()
	statuses := map[string]string{}
	for _, item := range project.remoteItems {
		statuses[item.ID] = item.Status
	}
	if statuses[pullRequest.ID] != "Done" {
		t.Fatalf("merged pull request status = %q, want Done", statuses[pullRequest.ID])
	}
}
