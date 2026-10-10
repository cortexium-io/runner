package github

import (
	"context"
	"errors"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/workspace"
)

// WithPlanHistoryReplacement carries the exact protected predecessor through
// fresh parent QA. It grants no publication authority: both immutable records,
// current Project authority and the fresh complete guard are checked at push.
func (m PullRequestManager) WithPlanHistoryReplacement(prior workspace.PublicationRecord, mergeMethod string) PullRequestManager {
	m.planHistoryReplacement = &prior
	m.planHistoryMergeMethod = config.NormalizeMergeMethod(mergeMethod)
	return m
}

func (m PullRequestManager) replacePlanPublicationHistory(ctx context.Context, action AuthorizedAction, metadata workspace.Metadata, record workspace.PublicationRecord, details PullRequestDetails, baseBranch, remoteName string) (PullRequestDetails, error) {
	prior := m.planHistoryReplacement
	if prior == nil || m.planHistoryMergeMethod != config.MergeMethodRebase || m.publicationGuard == nil || action.Item.PlanRelease == "" {
		return PullRequestDetails{}, errors.New("plan history replacement requires rebase policy, protected predecessor and fresh complete proof")
	}
	if prior.CommitOID == record.CommitOID || prior.TreeOID != record.TreeOID ||
		prior.ItemID != record.ItemID || prior.DelegatedContentDigest != record.DelegatedContentDigest ||
		prior.Repository != record.Repository || prior.DestinationRef != record.DestinationRef ||
		prior.ApprovedBaseRef != record.ApprovedBaseRef || prior.ApprovedBaseOID != record.ApprovedBaseOID ||
		prior.PlanRevision == "" || prior.PlanRevision != record.PlanRevision {
		return PullRequestDetails{}, errors.New("plan history replacement changed accepted source tree or approved publication identity")
	}
	branch := strings.TrimPrefix(record.DestinationRef, "refs/heads/")
	validatePredecessor := func(current AuthorizedAction, observed PullRequestDetails) error {
		if strings.TrimSpace(current.Item.PullRequest) != observed.URL || current.Item.QACommit != prior.CommitOID {
			return errors.New("plan history replacement does not match the tracked pull request and accepted head")
		}
		return validatePublishedPullRequest(observed, record.Repository, branch, prior.CommitOID, baseBranch, prior.ApprovedBaseOID)
	}
	if err := validatePredecessor(action, details); err != nil {
		return PullRequestDetails{}, err
	}
	if err := m.verifyTerminalPlanAcceptance(ctx, action, metadata, *prior, baseBranch); err != nil {
		return PullRequestDetails{}, err
	}
	provider := workspace.NewGitProvider(m.run)
	needsNormalization, err := provider.CandidateNeedsRebaseNormalization(ctx, metadata, workspace.Candidate{CommitOID: record.CommitOID, TreeOID: record.TreeOID})
	if err != nil || needsNormalization {
		return PullRequestDetails{}, errors.Join(errors.New("replacement candidate must have exact rebase-compatible history"), err)
	}
	if err := provider.PublishAccepted(ctx, metadata, record, remoteName, baseBranch, workspace.PublicationPushPolicy{
		MergeMethod: config.MergeMethodRebase, ExpectedRemoteOID: prior.CommitOID,
	}, func() error {
		fresh, err := m.refreshAuthorizedAction(ctx, action)
		if err != nil {
			return err
		}
		if err := validatePublicationAuthority(fresh, record); err != nil {
			return err
		}
		observed, err := m.inspect(ctx, record.Repository, details.URL, false, false)
		if err != nil {
			return err
		}
		if err := validatePredecessor(fresh, observed); err != nil {
			return err
		}
		if err := m.publicationGuard(ctx, fresh); err != nil {
			return err
		}
		if err := m.validateRemoteRepository(ctx, metadata.RepoRoot, remoteName, record.Repository); err != nil {
			return err
		}
		return m.validateRemoteRepository(ctx, metadata.WorktreePath, remoteName, record.Repository)
	}); err != nil {
		return PullRequestDetails{}, publicationError(PublicationPushCandidate, err)
	}
	observed, err := m.inspect(ctx, record.Repository, details.URL, false, false)
	if err != nil {
		return PullRequestDetails{}, publicationError(PublicationInspectPR, err)
	}
	if err := validatePublishedPullRequest(observed, record.Repository, branch, record.CommitOID, baseBranch, record.ApprovedBaseOID); err != nil {
		return PullRequestDetails{}, err
	}
	return observed, nil
}
