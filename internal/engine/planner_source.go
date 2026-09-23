package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cortexium-io/runner/internal/workspace"
)

func planningSourceDescription(source workspace.PlanningSource) string {
	return fmt.Sprintf("Planning source: repository %s, destination %s, commit %s, tree %s. This is the exact isolated source inspected for this proposal, not a claim about a later execution or delivery candidate.", source.Repository, source.DestinationBranch, source.CommitOID, source.TreeOID)
}

// The shared contract retains provenance through ordinary authenticated Project
// content; there is no new state field or source of execution authority.
func planningConstraints(plan ProjectPlan) []string {
	constraints := compactNonEmpty(plan.ProjectConstraints)
	if plan.PlanningSource != nil {
		constraints = append(constraints, planningSourceDescription(*plan.PlanningSource))
	}
	return constraints
}

func (s *Engine) validatePlanningSource(ctx context.Context, plan ProjectPlan) error {
	if plan.PlanningSource == nil {
		return errors.New("planning source is required before staging")
	}
	source := *plan.PlanningSource
	if err := source.Validate(); err != nil {
		return err
	}
	if !strings.EqualFold(source.Repository, s.cfg.GitHubProject.IntakeRepository) || source.DestinationBranch != s.baseBranch() {
		return errors.New("planning source does not match the configured repository and destination")
	}
	directory, err := s.repositoryDir(ctx, source.Repository)
	if err != nil {
		return err
	}
	current, err := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits()).ObservePlanningSource(ctx, directory, s.remoteName(), s.baseBranch(), source.Repository)
	if err != nil {
		return fmt.Errorf("revalidate planning source before staging: %w", err)
	}
	if current != source {
		return fmt.Errorf("destination changed since planning (inspected %s, current %s); further staging refused without a model call. Preserve the original proposal and its planning source. Revalidation against a different commit is not supported; ordinary retry retains and refuses this checkpoint again. A new planning run requires an explicit request and any existing partial batch must be resolved first", source.CommitOID, current.CommitOID)
	}
	return nil
}
