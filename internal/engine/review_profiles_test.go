package engine

import (
	"slices"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestWholePlanReviewerUsesSeparateProfileThroughPublicationAndRestart(t *testing.T) {
	f := newProductionDelivery(t)
	sol, astra := "gpt-6-sol", "gpt-6-astra"
	card := f.cfg.Roles[config.WorkRoleReviewer]
	card.Model, card.Reasoning = &sol, "high"
	f.cfg.Roles[config.WorkRoleReviewer] = card
	f.cfg.Roles["plan_reviewer"] = config.RoleConfig{Extends: config.WorkRoleReviewer, Model: &astra, Reasoning: "medium"}
	f.cfg.PlanDelivery.ReviewerRole = "plan_reviewer"
	var err error
	f.service, err = New(f.cfg, f.runner)
	if err != nil {
		t.Fatal(err)
	}
	f.integrateMembers(t)
	if !slices.Equal(f.runner.reviewModels, []string{sol, sol}) {
		t.Fatalf("card reviews: %v", f.runner.reviewModels)
	}
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	event := f.service.newItemAttempt(action.Item)
	if event.Role != "plan_reviewer" || event.Model != astra || event.Reasoning != "medium" {
		t.Fatalf("attempt did not expose effective profile: %+v", event)
	}
	result := f.service.executeQA(t.Context(), action, "plan-profile-attempt")
	if result.Error != "" || f.parent(t).Status != "PR Ready" {
		t.Fatalf("parent QA: %s %s", result.Summary, result.Error)
	}
	if !slices.Equal(f.runner.reviewModels, []string{sol, sol, astra}) {
		t.Fatalf("plan reviews: %v", f.runner.reviewModels)
	}
	progress := retainedPlanProgress(t, f)
	if progress.ReviewerRole != "plan_reviewer" {
		t.Fatal("retained proof lost actual reviewer")
	}
	// Retained acceptance must not survive a switch back to the card profile.
	f.cfg.PlanDelivery.ReviewerRole = ""
	f.service, err = New(f.cfg, f.runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.revalidatePlanProgress(t.Context(), action, progress); err == nil {
		t.Fatal("changed reviewer reused acceptance")
	}
	f.cfg.PlanDelivery.ReviewerRole = "plan_reviewer"
	f.service, err = New(f.cfg, f.runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.revalidatePlanProgress(t.Context(), action, progress); err != nil {
		t.Fatal(err)
	}
	parent := f.parent(t)
	runGitTest(t, "", "--git-dir", f.remote, "update-ref", "refs/heads/main", parent.QACommit)
	f.runner.merged = true
	for i := 0; i < 2; i++ {
		if _, err := f.service.RunCycle(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if f.parent(t).Status != "Done" || f.runner.reviews != 3 || f.runner.creates != 1 {
		t.Fatal("recovery duplicated work or lost confirmed delivery")
	}
}
