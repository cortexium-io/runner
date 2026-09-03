package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

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
	return s.source.Transition(ctx, action, targetStatus, detail, phase)
}

func (s *Engine) cleanupAuthorizedItemWorkspace(ctx context.Context, action github.AuthorizedAction) (workspace.CleanupResult, error) {
	current, content, err := s.source.RefreshDelegatedContent(ctx, action)
	if err != nil {
		return workspace.CleanupResult{}, err
	}
	item := current.Item
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
	return s.source.TransitionAfterBranchUpdate(ctx, action, targetStatus, targetPhase, detail)
}

func (s *Engine) transitionChecksFailed(ctx context.Context, action github.AuthorizedAction, targetStatus, targetPhase, detail string) error {
	return s.source.TransitionChecksFailed(ctx, action, targetStatus, targetPhase, detail)
}

func (s *Engine) updateActivity(ctx context.Context, action github.AuthorizedAction, activity string) error {
	return s.source.UpdateActivity(ctx, action, activity)
}

func (s *Engine) resetRejections(ctx context.Context, action github.AuthorizedAction, feedback, targetPhase string) error {
	return s.source.ResetRejections(ctx, action, feedback, targetPhase)
}
