package engine

import (
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
)

func TestWorkStatusSeparatesEligibleAndIneligibleAgentWork(t *testing.T) {
	cfg := completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4}})
	batch := "v1:" + strings.Repeat("a", 64)
	sourceFingerprint := "v1:" + strings.Repeat("b", 64)
	plannedBody := func(summary string, index int) string {
		return github.FormatPlannedItemBody(github.PlannedItem{
			Title: summary, Repository: "owner/repo", Summary: summary,
			AcceptanceCriteria: []string{"Works"}, Verification: []string{"Test it"}, Risks: []string{}, NonGoals: []string{}, Dependencies: []string{},
			DependencyIDsResolved: true, PlanningSourceLane: "plan", PlanningSourceFingerprint: sourceFingerprint,
			PlanningDestination: "Ready", PlanningBatchFingerprint: batch, PlanningBatchSize: 2, PlanningItemIndex: index,
		})
	}
	done := github.WorkItem{
		ID: "PVTI_done", Title: "Completed sibling", Body: plannedBody("Completed work", 1), Repository: "owner/repo",
		Status: "Done", Result: "Merged successfully.", PlanningSourceLane: "plan", PlanningSourceFingerprint: sourceFingerprint,
		PlanningDestination: "Ready", PlanningBatchFingerprint: batch, PlanningBatchSize: 2, PlanningItemIndex: 1,
	}
	done.Approval = testApproval(done)
	done.Result = "Manually repaired result."
	pending := github.WorkItem{
		ID: "PVTI_pending", Title: "Pending sibling", Body: plannedBody("Pending work", 2), Repository: "owner/repo",
		Status: "Ready", PlanningSourceLane: "plan", PlanningSourceFingerprint: sourceFingerprint,
		PlanningDestination: "Ready", PlanningBatchFingerprint: batch, PlanningBatchSize: 2, PlanningItemIndex: 2,
	}
	pending.Approval = testApproval(pending)
	ordinary := github.WorkItem{ID: "PVTI_ready", Title: "Independent work", Body: "Implement it", Repository: "owner/repo", Status: "Ready"}
	ordinary.Approval = testApproval(ordinary)
	run := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(done) + `,` + projectItemJSON(pending) + `,` + projectItemJSON(ordinary) + `]}`}
	service, err := New(cfg, run)
	if err != nil {
		t.Fatal(err)
	}

	status, err := service.WorkStatus(t.Context())
	if err != nil {
		t.Fatalf("work status: %v", err)
	}
	if len(status.Queued) != 1 || status.Queued[0].ID != ordinary.ID {
		t.Fatalf("queued work = %#v, want only executable item", status.Queued)
	}
	if len(status.Waiting) != 1 || status.Waiting[0].Item.ID != pending.ID || status.Waiting[0].Reason != github.WorkEligibilityPlanningBatchSiblingAuthorityInvalid {
		t.Fatalf("waiting work = %#v", status.Waiting)
	}
}
