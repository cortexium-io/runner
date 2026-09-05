package config

import (
	"slices"
	"testing"
)

func TestPlannerProfilesValidateAndSelectAnApprovedStartingPoint(t *testing.T) {
	cfg := explicitTestConfig()
	cfg.Roles["mechanical"] = RoleConfig{Extends: WorkRoleImplementer, Description: "Bounded mechanical tasks", Reasoning: "medium", Model: modelPointer("small-model")}
	cfg.Roles["stronger"] = RoleConfig{Extends: WorkRoleImplementer, Description: "Complex tasks", Reasoning: "xhigh", Model: modelPointer("large-model")}
	cfg.PlannerImplementers = []string{"mechanical", "stronger"}
	runtime, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(runtime.ExecutionRoleIDs(), "mechanical") {
		t.Fatal("planner-only profile omitted from runtime admission/probes")
	}
	for _, failures := range []int{0, 1, 20} {
		got, err := runtime.SelectedImplementer(WorkRoleImplementer, "mechanical", failures)
		if err != nil || got != "mechanical" {
			t.Fatalf("no ladder: %q %v", got, err)
		}
	}
	profile, _ := runtime.RoleProfile("mechanical")
	if profile.Reasoning != "medium" || profile.Model == nil || *profile.Model != "small-model" {
		t.Fatalf("lost bundled model/reasoning: %#v", profile)
	}
	cfg.ImplementerLadder = []string{WorkRoleImplementer, "mechanical", "stronger"}
	runtime, err = cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		selected string
		failures int
		want     string
	}{
		{"", 0, WorkRoleImplementer}, {"mechanical", 0, "mechanical"}, {"mechanical", 1, "stronger"}, {"stronger", 20, "stronger"},
	} {
		got, err := runtime.SelectedImplementer(WorkRoleImplementer, tc.selected, tc.failures)
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %q %v", tc, got, err)
		}
	}
	if _, err := runtime.SelectedImplementer(WorkRoleImplementer, "invented", 0); err == nil {
		t.Fatal("unapproved selection accepted")
	}
	runtime.PlannerImplementers = nil
	if _, err := runtime.SelectedImplementer(WorkRoleImplementer, "mechanical", 0); err == nil {
		t.Fatal("removed selection silently fell back")
	}
	if got, err := runtime.SelectedImplementer(WorkRoleReviewer, "mechanical", 2); err != nil || got != WorkRoleReviewer {
		t.Fatal("selection affected reviewer")
	}
}

func modelPointer(value string) *string { return &value }

func TestPlannerProfilesRejectInvalidOperatorChoices(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ids         []string
		description string
		ladder      []string
	}{
		{"missing", []string{"missing"}, "", nil},
		{"wrong contract", []string{WorkRoleReviewer}, "", nil},
		{"missing guidance", []string{"mechanical"}, "", nil},
		{"duplicate", []string{"mechanical", "mechanical"}, "Mechanical", nil},
		{"noncanonical", []string{" mechanical"}, "Mechanical", nil},
		{"outside ladder", []string{"mechanical"}, "Mechanical", []string{WorkRoleImplementer, "stronger"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := explicitTestConfig()
			cfg.Roles["mechanical"] = RoleConfig{Extends: WorkRoleImplementer, Description: tc.description}
			cfg.Roles["stronger"] = RoleConfig{Extends: WorkRoleImplementer}
			cfg.PlannerImplementers, cfg.ImplementerLadder = tc.ids, tc.ladder
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid planner profiles accepted")
			}
		})
	}
}
