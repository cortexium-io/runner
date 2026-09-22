package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

func (s *Engine) terminalWorkspaceCleanupPending(itemID string) bool {
	root := strings.TrimSpace(s.implementationWorkspaceRoot())
	if root == "" {
		return false
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(filepath.Dir(s.cfg.ProjectDir), root)
	}
	// Successful cleanup removes the deterministic worktree path while
	// intentionally retaining its branch and private identity record.
	_, err := os.Lstat(filepath.Join(root, "assignment_"+safeRefComponent(itemID)))
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func (s *Engine) transitionProjectItem(ctx context.Context, action github.AuthorizedAction, targetStatus, detail, phase string) error {
	targetStatus, phase, detail, _ = s.planSafeTransition(action, targetStatus, phase, detail)
	return s.source.Transition(ctx, action, targetStatus, detail, phase)
}

func (s *Engine) cleanupAuthorizedItemWorkspace(ctx context.Context, action github.AuthorizedAction) (workspace.CleanupResult, error) {
	current, content, err := s.source.RefreshDelegatedContent(ctx, action)
	if err != nil {
		return workspace.CleanupResult{}, err
	}
	item := current.Item
	if item.PlanRelease != "" && strings.TrimSpace(item.PullRequest) != "" && s.cfg.LaneIDForStatus(item.Status) == s.cfg.PublicationLaneID() {
		// A pending final plan PR still owns its accepted checkout and prepared
		// dependencies. Removing them would destroy applicable complete proof
		// before the serialized merge observation. Terminal reconciliation first
		// transitions the signed item, then uses the ordinary cleanup below.
		return workspace.CleanupResult{}, nil
	}
	repoRoot, err := s.repositoryDir(ctx, item.Repository)
	if err != nil {
		return workspace.CleanupResult{}, err
	}
	if strings.TrimSpace(item.Branch) == "" {
		return workspace.CleanupResult{}, nil
	}
	if err := s.fetchBase(ctx, repoRoot); err != nil {
		return workspace.CleanupResult{}, err
	}
	repository := strings.TrimSpace(item.Repository)
	if repository == "" {
		repository = strings.TrimSpace(s.cfg.GitHubProject.IntakeRepository)
	}
	return workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits()).Cleanup(ctx, workspace.CleanupRequest{
		WorkingDir: repoRoot, WorktreeRoot: s.implementationWorkspaceRoot(),
		WorkID: "assignment_" + safeRefComponent(item.ID), ItemID: item.ID, DelegatedContentDigest: content.Digest,
		Repository: repository, BranchName: item.Branch, BaseRef: s.remoteName() + "/" + s.baseBranch(),
	})
}

func (s *Engine) transitionImplementation(ctx context.Context, action github.AuthorizedAction, targetStatus, targetPhase, summary, branch string) error {
	return s.source.TransitionImplementation(ctx, action, targetStatus, targetPhase, summary, branch)
}

func (s *Engine) transitionRejection(ctx context.Context, action github.AuthorizedAction, targetStatus, targetPhase, summary string, failures int) error {
	return s.source.TransitionRejection(ctx, action, targetStatus, targetPhase, summary, failures)
}

func (s *Engine) transitionPRReady(ctx context.Context, action github.AuthorizedAction, targetStatus, summary, branch, pullRequest, qaCommit string) error {
	return s.source.TransitionPRReady(ctx, action, targetStatus, summary, branch, pullRequest, qaCommit)
}

func (s *Engine) transitionAfterBranchUpdate(ctx context.Context, action github.AuthorizedAction, targetStatus, targetPhase, detail string) error {
	targetStatus, targetPhase, detail, _ = s.planSafeTransition(action, targetStatus, targetPhase, detail)
	return s.source.TransitionAfterBranchUpdate(ctx, action, targetStatus, targetPhase, detail)
}

func (s *Engine) transitionAutomaticRetry(ctx context.Context, action github.AuthorizedAction, targetStatus, targetPhase, detail, activity string) error {
	return s.source.TransitionAutomaticRetry(ctx, action, targetStatus, targetPhase, detail, activity)
}

func (s *Engine) transitionChecksFailed(ctx context.Context, action github.AuthorizedAction, targetStatus, targetPhase, detail string) error {
	targetStatus, targetPhase, detail, _ = s.planSafeTransition(action, targetStatus, targetPhase, detail)
	return s.source.TransitionChecksFailed(ctx, action, targetStatus, targetPhase, detail)
}

// Generic card recovery cannot assign implementation authority to a plan
// parent. Without reviewed owning-card targets, retain the candidate and stop
// explicitly instead of producing an ineligible Ready parent.
func (s *Engine) planSafeTransition(action github.AuthorizedAction, targetStatus, phase, detail string) (string, string, string, bool) {
	if action.Item.PlanRelease == "" {
		return targetStatus, phase, detail, false
	}
	target := s.cfg.Workflow.Lanes[s.cfg.LaneIDForStatus(targetStatus)]
	if s.cfg.RoleContract(target.Role) != config.WorkRoleImplementer && s.cfg.RoleContract(s.cfg.Workflow.Lanes[phase].Role) != config.WorkRoleImplementer {
		return targetStatus, phase, detail, false
	}
	return s.cfg.GitHubProject.BlockedStatus, s.cfg.LaneIDForStatus(s.cfg.GitHubProject.QAStatus),
		"Plan delivery requires operator recovery: this finding has no reviewed in-scope owning card. Candidate, accepted members and rejection counts are retained. Inspect the retained conflict or failed final-PR checks and authorize an owning-card amendment/recovery; retrying the parent cannot perform implementation.\n\nObserved condition:\n" + detail, true
}

func (s *Engine) updateActivity(ctx context.Context, action github.AuthorizedAction, activity string) error {
	return s.source.UpdateActivity(ctx, action, activity)
}

func (s *Engine) resetRejections(ctx context.Context, action github.AuthorizedAction, feedback, targetPhase string) error {
	return s.source.ResetRejections(ctx, action, feedback, targetPhase)
}
