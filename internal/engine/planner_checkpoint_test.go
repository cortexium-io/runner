package engine

import (
	"os"
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
)

func TestPlannerCheckpointRoundTripAndContextInvalidation(t *testing.T) {
	projectDir := t.TempDir()
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: projectDir, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), &fakeGitHubProjectRunner{})
	if err != nil {
		t.Fatal(err)
	}
	item := github.WorkItem{ID: "PVTI_planner_checkpoint", Body: "Plan the exact request.", Repository: "owner/repo"}
	content := github.DelegatedContentFor(item)
	plan := directProjectPlanFixture()
	if err := service.normalizeProjectPlan(&plan); err != nil {
		t.Fatal(err)
	}
	contextDigest := plannerCheckpointContextDigest(item, content, config.WorkRolePlanner, "plan", "Ready", "owner/repo", plan.SourceContext)
	if err := service.savePlannerCheckpoint(item, content, contextDigest, "plan", "Ready", plan); err != nil {
		t.Fatalf("save planner checkpoint: %v", err)
	}
	if info, err := os.Stat(service.plannerCheckpointPath(item.ID)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("planner checkpoint is not private: info=%v error=%v", info, err)
	}
	loaded, found, err := service.loadPlannerCheckpoint(item, content, contextDigest)
	if err != nil || !found || !reflect.DeepEqual(loaded, plan) {
		t.Fatalf("load exact planner checkpoint: plan=%#v found=%v error=%v", loaded, found, err)
	}

	if _, found, err := service.loadPlannerCheckpoint(item, content, "sha256:changed-context"); err != nil || found {
		t.Fatalf("context-mismatched planner checkpoint was retained: found=%v error=%v", found, err)
	}
	if _, err := os.Stat(service.plannerCheckpointPath(item.ID)); !os.IsNotExist(err) {
		t.Fatalf("stale planner checkpoint was not removed: %v", err)
	}
}
