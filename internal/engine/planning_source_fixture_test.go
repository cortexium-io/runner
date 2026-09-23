package engine

import (
	"context"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

// Planning staging tests keep their Project transport fake while exercising
// source revalidation through native Git against the disposable bare remote.
type planningGitFixtureRunner struct{ subprocess.Runner }

func (r planningGitFixtureRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" {
		return runEngineTestGit(ctx, args, dir, timeout)
	}
	return r.Runner.Run(ctx, command, args, dir, timeout)
}

func planningFixtureSource(t *testing.T, repo string) *workspace.PlanningSource {
	t.Helper()
	source, err := workspace.NewGitProvider(planningGitFixtureRunner{}).ObservePlanningSource(t.Context(), repo, "origin", "main", "owner/repo")
	if err != nil {
		t.Fatalf("observe disposable planning source: %v", err)
	}
	return &source
}

func sourcedDirectProjectPlanFixture(t *testing.T, repo string) ProjectPlan {
	t.Helper()
	plan := directProjectPlanFixture()
	plan.PlanningSource = planningFixtureSource(t, repo)
	return plan
}
