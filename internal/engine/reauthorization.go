package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

type ProjectItemReauthorization struct {
	Approval         github.ReauthorizationPlan `json:"approval"`
	Workspace        workspace.Identity         `json:"workspace"`
	PullRequest      *github.PullRequestDetails `json:"pull_request,omitempty"`
	RetainedSnapshot *workspace.Snapshot        `json:"retained_snapshot,omitempty"`
	EvidenceDigests  map[string]string          `json:"retained_evidence_digests,omitempty"`
}

func (s *Engine) PlanProjectItemReauthorization(ctx context.Context, selector string) (ProjectItemReauthorization, error) {
	plan, err := s.source.PlanReauthorization(ctx, selector)
	if err != nil {
		return ProjectItemReauthorization{}, err
	}
	if s.cfg.RoleContract(plan.Role) != config.WorkRoleImplementer {
		return ProjectItemReauthorization{}, errors.New("reauthorization requires the configured implementation role")
	}
	if plan.Item.PullRequest != "" {
		return s.inspectPublishedReauthorization(ctx, plan)
	}
	if plan.Unstarted {
		if err := s.checkAdoptionEvidenceAbsent(plan.Item.ID); err != nil {
			return ProjectItemReauthorization{}, err
		}
		repo, err := s.repositoryDir(ctx, plan.Item.Repository)
		if err != nil {
			return ProjectItemReauthorization{}, err
		}
		request := s.workspaceRequestForItem(plan.Item, github.DelegatedContentFor(plan.Item).Digest, repo, false)
		if err := workspace.NewGitProvider(s.run).VerifyWorkspaceAbsent(ctx, request); err != nil {
			return ProjectItemReauthorization{}, fmt.Errorf("unstarted plan member has retained work: %w", err)
		}
		return ProjectItemReauthorization{Approval: plan}, nil
	}
	identity, err := s.validateReauthorizationWorkspace(ctx, plan.Item)
	if err != nil {
		return ProjectItemReauthorization{}, err
	}
	result := ProjectItemReauthorization{Approval: plan, Workspace: identity}
	if strings.TrimSpace(plan.Item.Branch) == "" {
		repo, err := s.repositoryDir(ctx, plan.Item.Repository)
		if err != nil {
			return ProjectItemReauthorization{}, err
		}
		if err := github.NewPullRequestManager(s.run, s.source).RequireUnpublishedBranch(ctx, identity.Repository, identity.Branch); err != nil {
			return ProjectItemReauthorization{}, err
		}
		provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
		request := s.workspaceRequestForItem(plan.Item, identity.DelegatedContentDigest, repo, false)
		metadata, err := provider.InspectRetainedReview(ctx, request)
		if err != nil {
			return ProjectItemReauthorization{}, err
		}
		if metadata.Identity != identity {
			return ProjectItemReauthorization{}, errors.New("retained identity changed during reauthorization preview")
		}
		snapshot, err := workspace.CaptureSnapshotStateWithLimits(ctx, s.run, identity.WorktreePath, 30*time.Second, s.snapshotLimits())
		if err != nil {
			return ProjectItemReauthorization{}, err
		}
		if accepted, err := provider.HasPriorPublicationAcceptance(ctx, metadata, snapshot.Head); err != nil || accepted {
			return ProjectItemReauthorization{}, errors.Join(errors.New("branchless recovery cannot adopt publication acceptance"), err)
		}
		result.RetainedSnapshot = &snapshot
		result.EvidenceDigests = make(map[string]string)
		for kind, path := range map[string]string{
			"implementation": s.implementationCheckpointPath(plan.Item.ID),
			"qa_feedback":    s.reviewFeedbackPath(plan.Item.ID),
			"verification":   s.verificationEvidencePath(plan.Item.ID),
		} {
			digest, err := amendmentEvidenceDigest(path)
			if err != nil {
				return ProjectItemReauthorization{}, err
			}
			result.EvidenceDigests[kind] = digest
		}
	}
	return result, nil
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
	fresh, err := s.PlanProjectItemReauthorization(ctx, plan.Approval.Item.ID)
	if err != nil {
		return github.WorkItem{}, err
	}
	if !reflect.DeepEqual(fresh, plan) {
		return github.WorkItem{}, errors.New("card, PR, or private workspace changed after reauthorization preview")
	}
	return s.source.ApplyReauthorization(ctx, plan.Approval)
}

func (s *Engine) inspectPublishedReauthorization(ctx context.Context, plan github.ReauthorizationPlan) (ProjectItemReauthorization, error) {
	item := plan.Item
	repo, err := s.repositoryDir(ctx, item.Repository)
	if err != nil {
		return ProjectItemReauthorization{}, err
	}
	request := s.workspaceRequestForItem(item, github.DelegatedContentFor(item).Digest, repo, false)
	identity, err := workspace.NewGitProvider(s.run).ValidatePublishedIdentity(ctx, request)
	if err != nil {
		return ProjectItemReauthorization{}, err
	}
	head, err := s.git(ctx, []string{"rev-parse", "--verify", "refs/heads/" + identity.Branch + "^{commit}"}, repo, 30*time.Second)
	if err != nil {
		return ProjectItemReauthorization{}, err
	}
	if strings.TrimSpace(head.Stdout) != item.QACommit {
		return ProjectItemReauthorization{}, errors.New("retained branch differs from the published QA commit")
	}
	if _, err := s.git(ctx, []string{"merge-base", "--is-ancestor", identity.BaseRevision, item.QACommit}, repo, 30*time.Second); err != nil {
		return ProjectItemReauthorization{}, fmt.Errorf("published candidate does not descend from the retained base: %w", err)
	}
	if _, err := os.Lstat(identity.WorktreePath); err == nil {
		snapshot, err := s.checkoutSnapshotState(ctx, identity.WorktreePath)
		if err != nil {
			return ProjectItemReauthorization{}, err
		}
		if !snapshot.Clean || snapshot.Head != item.QACommit || snapshot.Branch != identity.Branch {
			return ProjectItemReauthorization{}, errors.New("published recovery requires the exact clean candidate or its cleaned checkout")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ProjectItemReauthorization{}, err
	}
	pr, err := github.NewPullRequestManager(s.run, s.source).InspectRecoveryWithFeedback(ctx, item.Repository, item.PullRequest)
	if err != nil {
		return ProjectItemReauthorization{}, err
	}
	if pr.State != "OPEN" || pr.AutoMergeEnabled || pr.Feedback == "" {
		return ProjectItemReauthorization{}, errors.New("published recovery requires an open PR with auto-merge disabled and trusted human feedback")
	}
	if err := github.ValidateTrackedPullRequest(pr, item.Repository, identity.Branch, item.QACommit, s.baseBranch(), ""); err != nil {
		return ProjectItemReauthorization{}, err
	}
	return ProjectItemReauthorization{Approval: plan, Workspace: identity, PullRequest: &pr}, nil
}
