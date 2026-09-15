package engine

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
)

func TestRunLoopGracefulStopFinishesActiveAssignmentsWithoutStartingNext(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	first := github.WorkItem{ID: "PVTI_drain_first", Title: "Finish this assignment", Body: "Implement this change.", Repository: "owner/repo", Status: "Ready"}
	second := github.WorkItem{ID: "PVTI_drain_second", Title: "Finish another assignment", Body: "Implement another change.", Repository: "owner/repo", Status: "Ready"}
	third := github.WorkItem{ID: "PVTI_drain_third", Title: "Leave this assignment queued", Body: "Do not start during shutdown.", Repository: "owner/repo", Status: "Ready"}
	first.Approval, second.Approval, third.Approval = testApproval(first), testApproval(second), testApproval(third)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(first) + `,` + projectItemJSON(second) + `,` + projectItemJSON(third) + `]}`}
	runner := &eventWhileHarnessRunner{project: project, implementationStarted: make(chan struct{}), releaseImplementation: make(chan struct{}), pullRequestReconciled: make(chan struct{})}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo, MaxParallelism: 2, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}}), runner)
	if err != nil {
		t.Fatal(err)
	}
	var request atomic.Bool
	service.SetStopCheck(func() (bool, error) { return request.Load(), nil })
	acknowledged := make(chan struct{})
	var ackOnce sync.Once
	var results []RunResult
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- service.RunLoop(ctx, time.Hour, time.Hour, func(result RunResult) { results = append(results, result) }, nil, func(poll PollState) {
			if poll.Stopping {
				ackOnce.Do(func() { close(acknowledged) })
			}
		})
	}()
	select {
	case <-runner.implementationStarted:
	case <-ctx.Done():
		t.Fatal("assignment did not start")
	}
	request.Store(true)
	select {
	case <-acknowledged:
	case <-ctx.Done():
		t.Fatal("stop not acknowledged independently of GitHub poll timer")
	}
	select {
	case err := <-done:
		t.Fatalf("stop canceled active assignment: %v", err)
	default:
	}
	close(runner.releaseImplementation)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("drained loop did not exit")
	}
	if ctx.Err() != nil {
		t.Fatal("drain canceled the work context")
	}
	if len(results) != 2 {
		t.Fatalf("unexpected results after drain: %+v", results)
	}
	for _, result := range results {
		if result.Item.ID == third.ID || result.Outcome != "succeeded" {
			t.Fatalf("unexpected result after drain: %+v", result)
		}
	}
}

func TestRunLoopControlReadFailureStopsAdmissionAndReportsDiagnostic(t *testing.T) {
	service := &Engine{}
	service.SetStopCheck(func() (bool, error) { return false, errors.New("control record is unsafe") })
	var diagnostic error
	if err := service.RunLoop(t.Context(), time.Hour, time.Hour, nil, func(err error) { diagnostic = err }, nil); err != nil {
		t.Fatal(err)
	}
	if diagnostic == nil {
		t.Fatal("unsafe control stopped the worker without a diagnostic")
	}
}

func TestRunLoopGracefulStopBeforeFirstPollDoesNotObserveOrClaim(t *testing.T) {
	service := &Engine{}
	service.SetStopCheck(func() (bool, error) { return true, nil })
	acknowledged := false
	if err := service.RunLoop(t.Context(), time.Hour, time.Hour, nil, nil, func(poll PollState) { acknowledged = poll.Stopping && poll.Active == 0 }); err != nil {
		t.Fatal(err)
	}
	if !acknowledged {
		t.Fatal("idle stop was not acknowledged")
	}
}
