package engine

import (
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
)

func TestActionResourcesAllowImplementerAndReviewerOnDistinctBranches(t *testing.T) {
	service := &Engine{cfg: config.RuntimeConfig{
		RoleContracts: map[string]string{"builder": config.WorkRoleImplementer, "checker": config.WorkRoleReviewer, "planner": config.WorkRolePlanner},
		GitHubProject: config.ProjectConfig{GitHubProjectConfig: config.GitHubProjectConfig{IntakeRepository: "owner/repo"}},
	}}
	implementation, err := service.actionResourceKeys(github.AuthorizedAction{
		Item: github.WorkItem{ID: "PVTI_build", Repository: "owner/repo", Branch: "runner/build"}, Role: "builder",
	})
	if err != nil {
		t.Fatalf("derive implementation resources: %v", err)
	}
	review, err := service.actionResourceKeys(github.AuthorizedAction{
		Item: github.WorkItem{ID: "PVTI_review", Repository: "owner/repo", Branch: "runner/review"}, Role: "checker",
	})
	if err != nil {
		t.Fatalf("derive review resources: %v", err)
	}
	planning, err := service.actionResourceKeys(github.AuthorizedAction{
		Item: github.WorkItem{ID: "PVTI_plan", Repository: "owner/repo"}, Role: "planner",
	})
	if err != nil {
		t.Fatalf("derive planning resources: %v", err)
	}
	if want := []string{"item:PVTI_build", "workspace:owner/repo/runner/build"}; !reflect.DeepEqual(implementation, want) {
		t.Fatalf("implementation resources = %#v, want %#v", implementation, want)
	}
	if want := []string{"item:PVTI_review", "workspace:owner/repo/runner/review"}; !reflect.DeepEqual(review, want) {
		t.Fatalf("review resources = %#v, want %#v", review, want)
	}
	if want := []string{"item:PVTI_plan"}; !reflect.DeepEqual(planning, want) {
		t.Fatalf("planning resources = %#v, want %#v", planning, want)
	}
	occupied := occupiedResourceKeys(map[string][]string{"PVTI_build": implementation})
	if !resourcesAvailable(review, occupied) || !resourcesAvailable(planning, occupied) {
		t.Fatal("distinct review or planning work was blocked by unrelated implementation resources")
	}
}

func TestActionResourcesRejectSharedWorkspaceBranch(t *testing.T) {
	service := &Engine{cfg: config.RuntimeConfig{
		RoleContracts: map[string]string{"builder": config.WorkRoleImplementer, "checker": config.WorkRoleReviewer},
		GitHubProject: config.ProjectConfig{GitHubProjectConfig: config.GitHubProjectConfig{IntakeRepository: "owner/repo"}},
	}}
	implementation, err := service.actionResourceKeys(github.AuthorizedAction{
		Item: github.WorkItem{ID: "PVTI_build", Branch: "runner/shared"}, Role: "builder",
	})
	if err != nil {
		t.Fatal(err)
	}
	review, err := service.actionResourceKeys(github.AuthorizedAction{
		Item: github.WorkItem{ID: "PVTI_review", Branch: "runner/shared"}, Role: "checker",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resourcesAvailable(review, occupiedResourceKeys(map[string][]string{"PVTI_build": implementation})) {
		t.Fatal("two actions could reserve the same repository branch")
	}
	items := []github.WorkItem{
		{ID: "PVTI_build", Branch: "runner/shared", PullRequest: "https://github.com/owner/repo/pull/1"},
		{ID: "PVTI_other_shared", Branch: "runner/shared", PullRequest: "https://github.com/owner/repo/pull/2"},
		{ID: "PVTI_independent", Branch: "runner/independent", PullRequest: "https://github.com/owner/repo/pull/3"},
	}
	filtered, err := service.itemsWithoutResourceConflicts(items, map[string][]string{"PVTI_build": implementation})
	if err != nil {
		t.Fatalf("filter reconciliation resources: %v", err)
	}
	if want := []github.WorkItem{items[2]}; !reflect.DeepEqual(filtered, want) {
		t.Fatalf("reconcilable items = %#v, want only the independent branch", filtered)
	}
}

func TestIntegrationResourceIsScopedToRepositoryAndBase(t *testing.T) {
	service := &Engine{cfg: config.RuntimeConfig{
		GitHubProject: config.ProjectConfig{GitHubProjectConfig: config.GitHubProjectConfig{
			IntakeRepository: "owner/repo", BaseBranch: "main",
		}},
	}}
	key, err := service.integrationResourceKey(github.WorkItem{Repository: "Owner/Repo"}, "release/v2")
	if err != nil {
		t.Fatalf("derive integration resource: %v", err)
	}
	if key != "integration:owner/repo/release/v2" {
		t.Fatalf("integration resource = %q", key)
	}
	fallback, err := service.integrationResourceKey(github.WorkItem{}, "")
	if err != nil {
		t.Fatalf("derive fallback integration resource: %v", err)
	}
	if fallback != "integration:owner/repo/main" {
		t.Fatalf("fallback integration resource = %q", fallback)
	}
}
