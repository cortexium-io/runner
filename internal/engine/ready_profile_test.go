package engine

import (
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
)

func TestManualProfileUnavailableFailsBeforeWorkspaceOrHarness(t *testing.T) {
	body, err := github.WithManualImplementationProfile("Fix only the header.", "removed_profile")
	if err != nil {
		t.Fatal(err)
	}
	item := github.WorkItem{ID: "PVTI_manual", Title: "Header", Body: body, Repository: "owner/repo", Status: "In Progress", Phase: "ready", Role: "implementer", ImplementationProfile: "removed_profile"}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	runner := &successfulImplementationRunner{project: project}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: t.TempDir()}), runner)
	if err != nil {
		t.Fatal(err)
	}
	result := service.executeImplementation(t.Context(), mustAuthorizeTest(t, service.source, item))
	if result.Outcome != execution.OutcomeBlocked || result.Summary != "Approved execution profile is unavailable" || len(runner.args) != 0 || result.WorktreePath != "" {
		t.Fatalf("unavailable selection was executed or substituted: %#v", result)
	}
}

func TestRequirementAmendmentPreservesManualExecutionProfile(t *testing.T) {
	body, err := github.WithManualImplementationProfile("Old requirement.", "mechanical")
	if err != nil {
		t.Fatal(err)
	}
	item := github.WorkItem{ID: "PVTI_manual", Title: "Header", Body: body, Repository: "owner/repo", URL: "https://github.com/owner/repo/issues/8",
		Status: "Blocked", Phase: "ready", Branch: "runner/manual", ImplementationProfile: "mechanical"}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: t.TempDir()}), project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.source.PlanAmendment(t.Context(), item.ID, strings.Replace(body, "Old requirement.", "Revised requirement.", 1)); err != nil {
		t.Fatalf("requirement-only amendment refused: %v", err)
	}
	for _, changed := range []string{strings.Replace(body, "mechanical", "implementer", 1), "Revised requirement without the existing selection."} {
		if _, err := service.source.PlanAmendment(t.Context(), item.ID, changed); err == nil || !strings.Contains(err.Error(), "execution profile") {
			t.Fatalf("requirement amendment changed execution policy: %v", err)
		}
	}
}
