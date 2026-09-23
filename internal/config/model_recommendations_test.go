package config

import (
	"slices"
	"testing"
)

func TestReasoningRecommendationsFollowSelectedModelAndHarness(t *testing.T) {
	for _, tc := range []struct{ harness, model, want string }{
		{HarnessCodexCLI, "gpt-6-sol", "high"},
		{HarnessCodexCLI, "gpt-6-luna", "high"},
		{HarnessCodexCLI, "gpt-6-astra", "medium"},
		{HarnessClaudeCLI, "claude-opus-5-5", "medium"},
		{HarnessPiCLI, "openai/gpt-6-sol", "high"},
		{HarnessPiCLI, "openai-codex/gpt-6-luna", "high"},
		{HarnessPiCLI, "anthropic/claude-opus-5-5", "medium"},
		{HarnessPiCLI, "local/unknown", "medium"},
	} {
		t.Run(tc.harness+"/"+tc.model, func(t *testing.T) {
			if got := RecommendedReasoning(tc.harness, tc.model); got != tc.want {
				t.Fatalf("reasoning = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestWholePlanReviewerSelectionIsExplicitAndDoesNotChangeCardRoles(t *testing.T) {
	cfg := explicitTestConfig()
	cfg.PlanDelivery = &PlanDeliveryConfig{Enabled: true, CompleteVerification: "complete", ReviewerRole: "plan_reviewer"}
	cfg.Verification = map[string]VerificationEntrypoint{"complete": {Command: "/bin/true", ToolchainCommands: []string{"/bin/true"}, InputPaths: []string{"src"}, TimeoutSeconds: 60}}
	cfg.Roles["plan_reviewer"] = RoleConfig{Extends: WorkRoleReviewer, Model: modelPointer("gpt-6-astra"), Reasoning: "medium"}
	runtime, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.ExecutionRoleIDs(), "plan_reviewer") || !slices.Contains(runtime.ExecutionRoleIDs(), "plan_reviewer") {
		t.Fatal("readiness omitted plan reviewer")
	}
	for _, tc := range []struct {
		role   string
		parent bool
		want   string
	}{
		{WorkRoleReviewer, false, WorkRoleReviewer},
		{WorkRoleReviewer, true, "plan_reviewer"},
		{WorkRoleImplementer, true, WorkRoleImplementer},
	} {
		if got := runtime.ReviewerRole(tc.role, tc.parent); got != tc.want {
			t.Fatalf("review role = %s, want %s", got, tc.want)
		}
	}
	for _, bad := range []string{"missing", WorkRoleImplementer, " plan_reviewer"} {
		cfg.PlanDelivery.ReviewerRole = bad
		if _, err := cfg.Resolve(); err == nil {
			t.Fatalf("invalid reviewer %q accepted", bad)
		}
	}
	cfg.PlanDelivery.ReviewerRole = ""
	runtime, err = cfg.Resolve()
	if err != nil || runtime.ReviewerRole(WorkRoleReviewer, true) != WorkRoleReviewer {
		t.Fatalf("existing configuration lost its reviewer: %v", err)
	}
}

func TestLadderCanStartBelowGeneralDefaultWithoutDowngradingReadyWork(t *testing.T) {
	cfg := explicitTestConfig()
	base := cfg.Roles[WorkRoleImplementer]
	base.Description = "General work"
	cfg.Roles[WorkRoleImplementer] = base
	cfg.Roles["bounded"] = RoleConfig{Extends: WorkRoleImplementer, Description: "Bounded work"}
	cfg.Roles["stronger"] = RoleConfig{Extends: WorkRoleImplementer, Description: "Difficult work"}
	cfg.ImplementerLadder = []string{"bounded", WorkRoleImplementer, "stronger"}
	cfg.PlannerImplementers = []string{"bounded", WorkRoleImplementer, "stronger"}
	runtime, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		selected string
		failures int
		want     string
	}{
		{"", 0, WorkRoleImplementer}, {"", 1, "stronger"}, {"", 9, "stronger"},
		{"bounded", 0, "bounded"}, {"bounded", 1, WorkRoleImplementer}, {"bounded", 2, "stronger"},
		{"stronger", 0, "stronger"},
	} {
		got, err := runtime.SelectedImplementer(WorkRoleImplementer, tc.selected, tc.failures)
		if err != nil || got != tc.want {
			t.Fatalf("selected=%s failures=%d: got=%s err=%v want=%s", tc.selected, tc.failures, got, err, tc.want)
		}
		if tc.selected == "" && cfg.AttemptRole(WorkRoleImplementer, tc.failures) != got {
			t.Fatal("config/runtime selection disagree")
		}
	}
	before := cfg.planImplementationProfileDigests()[WorkRoleImplementer]
	bounded := cfg.Roles["bounded"]
	bounded.Model = modelPointer("another-bounded-model")
	cfg.Roles["bounded"] = bounded
	if cfg.planImplementationProfileDigests()[WorkRoleImplementer] != before {
		t.Fatal("unreachable lower rung changed approval")
	}
	stronger := cfg.Roles["stronger"]
	stronger.Model = modelPointer("another-stronger-model")
	cfg.Roles["stronger"] = stronger
	if cfg.planImplementationProfileDigests()[WorkRoleImplementer] == before {
		t.Fatal("reachable escalation escaped approval binding")
	}
}
