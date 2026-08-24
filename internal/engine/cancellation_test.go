package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
)

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
