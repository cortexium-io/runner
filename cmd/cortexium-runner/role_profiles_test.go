package main

import (
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestTrialProfileCLISelectionsPreserveModelEffortAndExecutionBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.json")
	initial := completeCLITestConfig("/project")
	if err := config.SaveConfig(path, initial); err != nil {
		t.Fatal(err)
	}
	command := func(args ...string) {
		t.Helper()
		if err := run(t.Context(), append(args, "--config", path), strings.NewReader(""), io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	profiles := []struct{ id, model, effort string }{
		{"trial_luna_max", "gpt-5.6-luna", "max"},
		{"trial_terra_medium", "gpt-5.6-terra", "medium"},
		{"trial_sol_medium", "gpt-5.6-sol", "medium"},
		{"trial_sol_high", "gpt-5.6-sol", "high"},
		{"trial_astra_medium", "gpt-6-astra", "medium"},
	}
	selection := []string{"role", "edit", "planner"}
	for _, profile := range profiles {
		command("role", "add", profile.id, "--extends", "implementer", "--model", profile.model,
			"--reasoning", profile.effort, "--description", "Explicit comparison candidate")
		selection = append(selection, "--implementer-profile", profile.id)
	}
	command("role", "edit", "implementer", "--clear-implementer-ladder")
	command(selection...)
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := loaded.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	base := runtime.Execution(config.WorkRoleImplementer, config.HarnessCodexCLI, "/workspace")
	for index, profile := range profiles {
		if loaded.PlannerImplementers[index] != profile.id {
			t.Fatal("operator preference order changed")
		}
		for _, failures := range []int{0, 1, 2} {
			role, err := runtime.SelectedImplementer(config.WorkRoleImplementer, profile.id, failures)
			if err != nil || role != profile.id {
				t.Fatalf("profile %s at %d rejections: %q, %v", profile.id, failures, role, err)
			}
		}
		execution := runtime.Execution(profile.id, config.HarnessCodexCLI, "/workspace")
		if execution.Harness.Model == nil || *execution.Harness.Model != profile.model || execution.Harness.ReasoningEffort != profile.effort {
			t.Fatalf("model/effort pair changed for %s: %#v", profile.id, execution.Harness)
		}
		// Selecting a model must not change inherited tools, permissions, skills,
		// timeouts, workspace configuration, or any other execution setting.
		execution.Harness.Model = base.Harness.Model
		execution.Harness.ReasoningEffort = base.Harness.ReasoningEffort
		if !reflect.DeepEqual(execution, base) {
			t.Fatalf("profile %s changed the execution boundary", profile.id)
		}
	}
	for id, role := range initial.Roles {
		if !reflect.DeepEqual(loaded.Roles[id], role) {
			t.Fatalf("profile setup changed existing role %s", id)
		}
	}
	if !reflect.DeepEqual(loaded.Workflow, initial.Workflow) {
		t.Fatal("profile setup changed workflow or rejection policy")
	}
}

func TestInitReasoningOffersMaxOnlyWhenAllSelectedHarnessesAreCodex(t *testing.T) {
	for _, tc := range []struct {
		harnesses []string
		wantMax   bool
	}{
		{[]string{"codex"}, true},
		{[]string{"codex", "codex", "codex"}, true},
		{[]string{"codex", "claude", "codex"}, false},
		{[]string{"pi"}, false},
		{nil, false},
	} {
		if got := slices.Contains(initReasoningOptions(tc.harnesses...), "max"); got != tc.wantMax {
			t.Fatalf("harnesses %v: max offered = %t", tc.harnesses, got)
		}
	}

	var harnesses, models, efforts [3]string
	if err := applyInitRoleDefaults("codex", "gpt-5.6-luna", "max",
		&harnesses[0], &harnesses[1], &harnesses[2],
		&models[0], &models[1], &models[2],
		&efforts[0], &efforts[1], &efforts[2]); err != nil {
		t.Fatal(err)
	}
	if efforts != [3]string{"max", "max", "max"} {
		t.Fatalf("init changed explicit reasoning defaults: %v", efforts)
	}
}
