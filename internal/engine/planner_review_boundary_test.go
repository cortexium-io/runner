package engine

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestPlannerRejectsFutureDeliveryProofBeforeStaging(t *testing.T) {
	for _, field := range []string{"project_success_criteria", "acceptance_criteria", "verification"} {
		t.Run(field, func(t *testing.T) {
			cfg := completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
			service, err := New(cfg, &fakeGitHubProjectRunner{})
			if err != nil {
				t.Fatal(err)
			}
			// The admission boundary is enabled independently of gate execution;
			// this regression must not invoke GitHub, a model, or a complete gate.
			service.cfg.GitHubProject.PlanDelivery = true
			plan := ProjectPlan{GoalSummary: "Deliver pilot", SourceContext: "Original approved request", ProjectSuccessCriteria: []string{"Pilot behavior works"}, WorkItems: []github.PlannedItem{{Title: "Pilot", Summary: "Implement the pilot", AcceptanceCriteria: []string{"Behavior works"}, Verification: []string{"Focused behavior is demonstrated"}, Risks: []string{}, NonGoals: []string{}, ImplementationProfile: "implementer", ProfileReason: "Bounded behavior"}}}
			switch field {
			case "project_success_criteria":
				plan.ProjectSuccessCriteria = []string{"The pilot is actually merged to develop through normal Runner delivery; umbrella completion is checked against the merged PR and actual gate evidence."}
			case "acceptance_criteria":
				plan.WorkItems[0].AcceptanceCriteria = []string{"Whole-plan QA and complete verification have passed and the final PR is merged to develop."}
			case "verification":
				plan.WorkItems[0].Verification = []string{"Establish actual automatic merged delivery to develop and the final completion check for the parent from the merged PR and real gate evidence. Internal acceptance or PR Ready does not establish delivery."}
			}
			if _, err := service.ValidateProjectPlan(plan); err == nil || !strings.Contains(err.Error(), field) || !strings.Contains(err.Error(), "review boundary") {
				t.Fatalf("future proof accepted or wrong diagnostic: %v", err)
			}
			if _, err := service.prepareDirectProjectPlan(&plan); err == nil || !strings.Contains(err.Error(), "review boundary") {
				t.Fatalf("direct staging bypassed guard: %v", err)
			}
		})
	}
}

func TestGeneratedPlannerBoundaryIsConfiguredAndDoesNotRepeatModelWork(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	bad := "The pilot is actually merged to develop through normal Runner delivery."
	run := &canonicalizingPlannerRunner{result: func(value string) string {
		return strings.ReplaceAll(value, "The feature works.", bad)
	}}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo}), run)
	if err != nil {
		t.Fatal(err)
	}
	service.cfg.GitHubProject.PlanDelivery = true
	_, result, err := service.planProjectWithRole(t.Context(), "planner", "Deliver the requested pilot.")
	if err == nil || !strings.Contains(err.Error(), "review boundary") || run.calls != 2 || result.FailureClass != execution.FailureInvalidContract || result.RetryDisposition != execution.RetryNone {
		t.Fatalf("generated contradiction was accepted or replanned: calls=%d class=%s retry=%s error=%v", run.calls, result.FailureClass, result.RetryDisposition, err)
	}
	for _, prompt := range run.inputs {
		if strings.Count(prompt, "Configured plan-delivery order:") != 1 || !strings.Contains(prompt, "conflicting timing requires open_decisions") {
			t.Fatal("outline or details lost the configured delivery boundary")
		}
	}
	standalone := projectPlannerPrompt(nil, projectPlannerExecutionContext{}, "owner/repo", "Plan a standalone card")
	if strings.Contains(standalone, "Configured plan-delivery order:") {
		t.Fatal("invented plan-delivery policy for the standalone path")
	}
}

func TestRetainedPlannerBoundaryRefusesNewStagingWithoutDiscardingCheckpoint(t *testing.T) {
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: t.TempDir()}), &fakeGitHubProjectRunner{})
	if err != nil {
		t.Fatal(err)
	}
	service.cfg.GitHubProject.PlanDelivery = true
	item := github.WorkItem{ID: "retained", Body: "Deliver the pilot", Repository: "owner/repo"}
	content := github.DelegatedContentFor(item)
	plan := directProjectPlanFixture()
	plan.ProjectSuccessCriteria = []string{"The pilot is actually merged to develop through normal Runner delivery."}
	plan.PlanningSource = &workspace.PlanningSource{Repository: "owner/repo", DestinationBranch: "main", CommitOID: strings.Repeat("a", 40), TreeOID: strings.Repeat("b", 40)}
	// Simulate a protected checkpoint written before the new admission lint.
	if err := service.savePlannerCheckpoint(item, content, "context", "plan", "Ready", plan); err != nil {
		t.Fatal(err)
	}
	path := service.plannerCheckpointPath(item.ID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, found, err := service.loadPlannerCheckpoint(item, content, "context")
	if err != nil || !found {
		t.Fatalf("retained proposal could not be read: found=%t err=%v", found, err)
	}
	if _, err := service.applyPlannerBatch(t.Context(), github.AuthorizedAction{}, loaded, "plan"); err == nil || !strings.Contains(err.Error(), "review boundary") {
		t.Fatalf("retained proposal reached authority/staging calls: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("retained proposal was changed or discarded: %v", err)
	}
}
