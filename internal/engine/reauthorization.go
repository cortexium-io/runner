package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

type ProjectItemReauthorization struct {
	Approval  github.ReauthorizationPlan `json:"approval"`
	Workspace workspace.Identity         `json:"workspace"`
}

func (s *Engine) PlanProjectItemReauthorization(ctx context.Context, selector string) (ProjectItemReauthorization, error) {
	plan, err := s.source.PlanReauthorization(ctx, selector)
	if err != nil {
		return ProjectItemReauthorization{}, err
	}
	identity, err := s.validateReauthorizationWorkspace(ctx, plan.Item)
	if err != nil {
		return ProjectItemReauthorization{}, err
	}
	return ProjectItemReauthorization{Approval: plan, Workspace: identity}, nil
}

func (s *Engine) validateReauthorizationWorkspace(ctx context.Context, item github.WorkItem) (workspace.Identity, error) {
	repoRoot, err := s.repositoryDir(ctx, item.Repository)
	if err != nil {
		return workspace.Identity{}, err
	}
	request := s.workspaceRequestForItem(item, github.DelegatedContentFor(item).Digest, repoRoot, false)
	identity, err := workspace.NewGitProvider(s.run).ValidateRetainedIdentity(ctx, request)
	if err != nil {
		return workspace.Identity{}, fmt.Errorf("reauthorization requires unchanged, previously executed content and its private worktree binding: %w", err)
	}
	return identity, nil
}

func (s *Engine) ApplyProjectItemReauthorization(ctx context.Context, plan ProjectItemReauthorization) (github.WorkItem, error) {
	guard, err := s.acquireLocalGate(ctx, true, github.AcquirePlanningMutationLock)
	if err != nil {
		return github.WorkItem{}, err
	}
	defer guard.Release()
	identity, err := s.validateReauthorizationWorkspace(ctx, plan.Approval.Item)
	if err != nil {
		return github.WorkItem{}, err
	}
	if identity != plan.Workspace {
		return github.WorkItem{}, errors.New("private workspace identity changed after reauthorization preview")
	}
	return s.source.ApplyReauthorization(ctx, plan.Approval)
}
