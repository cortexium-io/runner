package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

// TestLivePlannerWorkItemContractAcrossHarnesses is an opt-in forward test for
// the complete Runner planner path. It makes paid model calls and is therefore
// excluded from ordinary test and release-readiness runs.
//
// Run one or more installed harnesses with:
//
//	CORTEXIUM_RUNNER_LIVE_HARNESSES=codex,claude,pi go test ./internal/engine -run '^TestLivePlannerWorkItemContractAcrossHarnesses$' -timeout 45m -v
func TestLivePlannerWorkItemContractAcrossHarnesses(t *testing.T) {
	requested := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_LIVE_HARNESSES"))
	if requested == "" {
		t.Skip("set CORTEXIUM_RUNNER_LIVE_HARNESSES to run paid live planner checks")
	}

	scenario := plannerEvalCorpus[1]
	for _, rawKind := range strings.Split(requested, ",") {
		kind := strings.TrimSpace(rawKind)
		t.Run(kind, func(t *testing.T) {
			if !config.ValidHarnessKind(kind) {
				t.Fatalf("unsupported live harness %q", kind)
			}
			repo, _ := createPublicationRepository(t)
			if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte(scenario.repositoryContext), 0o644); err != nil {
				t.Fatal(err)
			}
			runGitTest(t, repo, "add", "README.md")
			runGitTest(t, repo, "commit", "-m", "seed representative repository context")
			roles := config.RoleTemplate(config.HarnessCodexCLI)
			planner := roles[config.WorkRolePlanner]
			planner.Harness = kind
			roles[config.WorkRolePlanner] = planner
			reviewer := roles[config.WorkRoleReviewer]
			reviewer.Harness = kind
			roles[config.WorkRoleReviewer] = reviewer
			cfg := completeEngineTestConfig(config.Config{
				ConfigVersion: config.ConfigVersion,
				RunnerID:      "live_planner_contract_" + kind,
				ProjectDir:    repo,
				Roles:         roles,
				GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
			})
			service, err := New(cfg, nil)
			if err != nil {
				t.Fatalf("configure live planner: %v", err)
			}
			plan, err := service.PlanProject(t.Context(), scenario.idea)
			if err != nil {
				t.Fatalf("live planner contract: %v", err)
			}
			if len(plan.OpenDecisions) != 0 {
				t.Fatalf("reversible implementation details became open decisions: %#v", plan.OpenDecisions)
			}
			for index, item := range plan.WorkItems {
				if len(item.Verification) == 0 || item.Risks == nil || item.NonGoals == nil {
					t.Fatalf("work item %d omitted its task-local contract: %#v", index, item)
				}
			}
			encoded, _ := json.MarshalIndent(plan, "", "  ")
			t.Logf("validated live %s plan:\n%s", kind, encoded)
		})
	}
}
