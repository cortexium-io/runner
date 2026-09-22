package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

// A clean refresh needs fresh review, not another implementation invocation.
// Carry evidence only from the exact pre-refresh candidate and keep its source
// identity visible to QA. Neither the old tests nor old acceptance certify the
// refreshed candidate. Conflicts retain the normal implementation repair path.
func (s *Engine) refreshBranchForQA(ctx context.Context, action github.AuthorizedAction, metadata workspace.Metadata, baseBranch string, published bool) (github.BranchRefreshResult, error) {
	content, err := action.DelegatedContent()
	if err != nil {
		return github.BranchRefreshResult{}, err
	}
	before, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
	if err != nil {
		return github.BranchRefreshResult{}, err
	}
	if !before.Clean || before.Branch != metadata.BranchName {
		return github.BranchRefreshResult{}, errors.New("base refresh requires a clean candidate on the authorized branch")
	}
	entries, err := s.loadVerificationEvidence(action.Item, content, metadata,
		workspace.Candidate{CommitOID: before.Head, TreeOID: before.Tree}, approvedVerificationContract(content.BodySnapshot))
	if err != nil {
		return github.BranchRefreshResult{}, fmt.Errorf("verify evidence before base refresh: %w", err)
	}
	manager := github.NewPullRequestManager(s.run, s.source)
	var refreshed github.BranchRefreshResult
	delivery, isPlan, err := s.source.DeliveryForItem(ctx, action.Item)
	if err != nil {
		return refreshed, err
	}
	if isPlan && action.Item.ID != delivery.Parent.ID {
		if published {
			return refreshed, errors.New("plan member cannot refresh a child pull request")
		}
		unlock := s.lockPlan(delivery.Parent.ID)
		defer unlock()
		delivery, _, err = s.source.DeliveryForItem(ctx, action.Item)
		if err != nil {
			return refreshed, err
		}
		guard := func() error { return s.validateMemberPlanHead(ctx, action.Item, delivery) }
		if err := guard(); err != nil {
			return refreshed, err
		}
		provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
		if err := provider.VerifyPlanBranchAdvance(ctx, metadata.RepoRoot, delivery.Manifest.Repository, s.remoteName(), baseBranch, metadata.BaseRevision, delivery.Parent.QACommit, guard); err != nil {
			return refreshed, err
		}
		update, updateErr := provider.RefreshLocalPlanBaseForMergeMethod(ctx, metadata, s.remoteName(), baseBranch, s.cfg.GitHubProject.MergeMethod, delivery.Parent.QACommit, guard)
		refreshed = github.BranchRefreshResult{Updated: update.Updated, Conflicted: update.Conflicted, PreviousCommitSHA: update.PreviousCommitOID, CommitSHA: update.CommitOID, ConflictFiles: update.ConflictFiles, Summary: update.Summary}
		err = updateErr
	} else if published {
		refreshed, err = manager.RefreshBranchAuthorized(ctx, action, metadata, baseBranch, s.remoteName(), s.cfg.GitHubProject.MergeMethod)
	} else {
		refreshed, err = manager.RefreshUnpublishedBranchAuthorized(ctx, action, metadata, baseBranch, s.remoteName(), s.cfg.GitHubProject.MergeMethod)
	}
	if err != nil || refreshed.Conflicted {
		return refreshed, err
	}
	if refreshed.PreviousCommitSHA != before.Head {
		return refreshed, errors.New("candidate changed before Runner's base refresh; retained evidence was not rebound")
	}
	metadata, err = s.workspaceForItem(ctx, action.Item, content.Digest, metadata.RepoRoot)
	if err != nil {
		return refreshed, err
	}
	after, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
	if err != nil {
		return refreshed, err
	}
	if !after.Clean || after.Head != refreshed.CommitSHA || after.Branch != before.Branch {
		return refreshed, errors.New("refreshed candidate changed before retaining historical evidence")
	}
	candidate, err := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits()).ConstructCandidateForMergeMethod(ctx, metadata, action.Item.Title, s.cfg.GitHubProject.MergeMethod)
	if err != nil {
		return refreshed, err
	}
	if candidate.TreeOID != after.Tree {
		return refreshed, errors.New("refreshed tree changed during candidate preparation; retained evidence was not rebound")
	}
	if len(entries) > 0 {
		err = s.saveVerificationEntries(action.Item, content, metadata, candidate, entries)
		if err != nil {
			return refreshed, fmt.Errorf("retain pre-refresh evidence for fresh QA: %w", err)
		}
	}
	refreshed.CommitSHA = candidate.CommitOID
	return refreshed, nil
}
