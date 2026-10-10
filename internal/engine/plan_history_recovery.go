package engine

import (
	"context"
	"errors"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

// History recovery is derived from an exact immutable acceptance, not operator
// supplied Git identities. Preview/apply never change the candidate or the PR.
func (s *Engine) inspectRetryHistory(ctx context.Context, plan github.RetryPlan) (*workspace.PublicationRecord, *github.PlanDelivery, error) {
	if plan.Item.PlanRelease == "" || plan.FeedbackOverride != "" || config.NormalizeMergeMethod(s.cfg.GitHubProject.MergeMethod) != config.MergeMethodRebase {
		return nil, nil, nil
	}
	content := github.DelegatedContentFor(plan.Item)
	feedback, err := s.loadReviewFeedbackRecord(plan.Item, content)
	if err != nil || feedback == nil || feedback.PlanVerification == nil || feedback.PlanVerification.Publication == nil {
		return nil, nil, err
	}
	p := feedback.PlanVerification
	root, err := s.repositoryDir(ctx, plan.Item.Repository)
	if err != nil {
		return nil, nil, err
	}
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	metadata, err := provider.InspectRetainedReview(ctx, s.workspaceRequestForItem(plan.Item, content.Digest, root, false))
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
	if err != nil {
		return nil, nil, err
	}
	// A previously normalized candidate needs fresh QA, not another rewrite.
	// Its exact prior publication remains in protected progress for publication.
	if snapshot.Head != p.Candidate.Head {
		if p.HistoryRecovery != nil {
			if preserved, err := s.preserveNormalizedPlanHistory(ctx, metadata, plan.Item, plan.Item.QACommit); err != nil || !preserved {
				return nil, nil, errors.Join(errors.New("pending normalized parent no longer matches its retained history recovery"), err)
			}
		}
		return nil, nil, nil
	}
	needed, err := provider.CandidateNeedsRebaseNormalization(ctx, metadata, workspace.Candidate{CommitOID: snapshot.Head, TreeOID: snapshot.Tree})
	if err != nil || !needed {
		return nil, nil, err
	}
	action, err := s.source.Authorize(ctx, plan.Item)
	if err != nil {
		return nil, nil, err
	}
	if err := p.validate(); err != nil {
		return nil, nil, err
	}
	if p.Failure != nil || p.Classification != nil || p.Gate == nil || p.Gate.Invocation.Outcome != "passed" || !p.Gate.Invocation.CleanupResolved || action.Item.QAFailures != p.QAFailures {
		return nil, nil, errors.New("history recovery requires the exact accepted parent and resolved passing complete gate without resetting QA history")
	}
	if metadata.Identity != p.Metadata.Identity || metadata.SourceSnapshot != p.Metadata.SourceSnapshot || metadata.BaseRevision != p.Metadata.BaseRevision || snapshot.Fingerprint != p.publicationCandidate().Fingerprint {
		return nil, nil, errors.New("history recovery workspace or accepted candidate changed")
	}
	p.Metadata = metadata // restore current privileged administration, never trust serialized bindings
	if err := s.verifyPlanProgressCandidate(ctx, action, p); err != nil {
		return nil, nil, err
	}
	prior := *p.Publication
	if prior.CommitOID != snapshot.Head || prior.TreeOID != snapshot.Tree || prior.ApprovedBaseOID != metadata.BaseRevision || prior.PlanRevision != github.PlanRevision(action.Item.Body) || action.Item.QACommit != prior.CommitOID || action.Item.PullRequest == "" {
		return nil, nil, errors.New("history recovery requires the exact recorded parent publication and unchanged approved base")
	}
	if err := provider.VerifyTerminalPlanAcceptance(metadata, prior); err != nil {
		return nil, nil, err
	}
	details, err := github.NewPullRequestManager(s.run, s.source).InspectAuthorized(ctx, action)
	if err != nil {
		return nil, nil, err
	}
	if err := github.ValidateTrackedPullRequest(details, prior.Repository, metadata.BranchName, prior.CommitOID, s.baseBranch(), prior.ApprovedBaseOID); err != nil {
		return nil, nil, err
	}
	if details.State != "OPEN" || details.AutoMergeEnabled {
		return nil, nil, errors.New("history recovery requires the exact open pull request without armed automatic merge")
	}
	delivery, _, err := s.planGate(ctx, action)
	if err != nil {
		return nil, nil, err
	}
	if err := s.revalidateEvidenceDelivery(ctx, delivery); err != nil {
		return nil, nil, err
	}
	return &prior, &delivery, nil
}

func historyRecoveryDigest(prior *workspace.PublicationRecord, delivery *github.PlanDelivery) [32]byte {
	return qaPreviewDigest(struct {
		Publication *workspace.PublicationRecord
		Delivery    *github.PlanDelivery
	}{prior, delivery})
}

func (s *Engine) retainRetryHistory(item github.WorkItem, prior workspace.PublicationRecord) error {
	feedback, err := s.loadReviewFeedbackRecord(item, github.DelegatedContentFor(item))
	if err != nil || feedback == nil || feedback.PlanVerification == nil || feedback.PlanVerification.Publication == nil || *feedback.PlanVerification.Publication != prior {
		return errors.Join(errors.New("accepted parent changed before retaining history recovery"), err)
	}
	p := feedback.PlanVerification
	if p.HistoryRecovery != nil {
		if *p.HistoryRecovery != prior {
			return errors.New("parent has a different retained history recovery")
		}
		return nil
	}
	if err := s.archivePlanVerification(item.ID, feedback); err != nil {
		return err
	}
	p.HistoryRecovery = &prior
	return s.savePlanVerification(item, github.DelegatedContentFor(item), p)
}

// A crash can leave the normalized commit ahead of protected fresh QA progress.
// The old remote head remains the lease anchor, not a parent to merge back into
// the locally normalized candidate. Only the explicitly retained same-tree
// recovery can authorize this divergence; ordinary plan repairs still sync.
func (s *Engine) preserveNormalizedPlanHistory(ctx context.Context, metadata workspace.Metadata, item github.WorkItem, expectedHead string) (bool, error) {
	if config.NormalizeMergeMethod(s.cfg.GitHubProject.MergeMethod) != config.MergeMethodRebase {
		return false, nil
	}
	feedback, err := s.loadReviewFeedbackRecord(item, github.DelegatedContentFor(item))
	if err != nil || feedback == nil || feedback.PlanVerification == nil || feedback.PlanVerification.HistoryRecovery == nil {
		return false, err
	}
	p := feedback.PlanVerification
	prior := p.HistoryRecovery
	if err := p.validate(); err != nil {
		return false, err
	}
	if p.classificationPending() || prior.CommitOID != expectedHead || item.QACommit != expectedHead || metadata.Identity != p.Metadata.Identity || metadata.SourceSnapshot != p.Metadata.SourceSnapshot || metadata.BaseRevision != prior.ApprovedBaseOID {
		return false, errors.New("retained history recovery no longer matches the authenticated plan head or base")
	}
	snapshot, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
	if err != nil {
		return false, err
	}
	if !snapshot.Clean || snapshot.Tree != prior.TreeOID || snapshot.Branch != metadata.BranchName {
		return false, errors.New("normalized plan candidate changed its accepted tree or branch")
	}
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	if err := provider.VerifyTerminalPlanAcceptance(metadata, *prior); err != nil {
		return false, err
	}
	needed, err := provider.CandidateNeedsRebaseNormalization(ctx, metadata, workspace.Candidate{CommitOID: snapshot.Head, TreeOID: snapshot.Tree})
	if err != nil {
		return false, err
	}
	if needed {
		return false, errors.New("retained history recovery has an unexpected nonlinear local candidate")
	}
	action, err := s.source.Authorize(ctx, item)
	if err != nil {
		return false, err
	}
	p.Metadata = metadata
	if _, err := s.revalidatePlanProgress(ctx, action, p); err != nil {
		return false, err
	}
	return true, nil
}

// The old publication is an active lease anchor only until the exact new head
// is both remotely published and recorded in signed Project state. Archive the
// recovery progress before retiring that anchor so later base advances use the
// normal review/refresh path without inheriting obsolete history constraints.
func (s *Engine) retirePlanHistoryRecovery(ctx context.Context, action github.AuthorizedAction, published github.PublishedPullRequest) error {
	if action.Item.PlanRelease == "" {
		return nil
	}
	content, err := action.DelegatedContent()
	if err != nil {
		return err
	}
	feedback, err := s.loadReviewFeedbackRecord(action.Item, content)
	if err != nil || feedback == nil || feedback.PlanVerification == nil || feedback.PlanVerification.HistoryRecovery == nil {
		return err
	}
	p := feedback.PlanVerification
	publication := p.Publication
	if publication == nil || publication.CommitOID == p.HistoryRecovery.CommitOID || action.Item.QACommit != publication.CommitOID || published.CommitSHA != publication.CommitOID || published.Branch != p.Metadata.BranchName || action.Item.Branch != published.Branch || published.URL == "" || action.Item.PullRequest != published.URL {
		return errors.New("history recovery cannot retire before the exact new signed publication")
	}
	// The caller has already validated this exact remote publication. Retiring
	// its old lease anchor grants no new publication authority, so do not add
	// another remote read between the signed transition and protected cleanup.
	if _, err := s.revalidatePlanProgress(ctx, action, p); err != nil {
		return err
	}
	if err := s.archivePlanVerification(action.Item.ID, feedback); err != nil {
		return err
	}
	p.HistoryRecovery = nil
	return s.savePlanVerification(action.Item, content, p)
}
