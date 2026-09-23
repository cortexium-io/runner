package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/verification"
	"github.com/cortexium-io/runner/internal/workspace"
)

func (s *Engine) planGate(ctx context.Context, action github.AuthorizedAction) (github.PlanDelivery, config.VerificationEntrypoint, error) {
	return s.planGateForReviewer(ctx, action, s.executionRole(action.Item))
}

// reviewerRole is the protected original QA profile when observing publication
// proof. It does not change the publication action or grant a model invocation.
func (s *Engine) planGateForReviewer(ctx context.Context, action github.AuthorizedAction, reviewerRole string) (github.PlanDelivery, config.VerificationEntrypoint, error) {
	delivery, present, err := s.source.DeliveryForItem(ctx, action.Item)
	if err != nil || !present || delivery.Parent.ID != action.Item.ID {
		return delivery, config.VerificationEntrypoint{}, errors.Join(errors.New("complete gate requires a currently authorized delivery parent"), err)
	}
	if !s.source.PlanMembersIntegrated(delivery) {
		return delivery, config.VerificationEntrypoint{}, errors.New("complete gate requires every exact member to be integrated")
	}
	entry, ok := s.cfg.Verification[delivery.Manifest.CompleteVerification]
	if !ok || entry.Digest() != delivery.Manifest.VerificationDigest {
		return delivery, entry, errors.New("approved complete verification entrypoint changed")
	}
	profile, ok := s.cfg.RoleProfile(reviewerRole)
	if !ok || s.cfg.RoleContract(reviewerRole) != config.WorkRoleReviewer || config.EffectiveRoleAccess(profile.Access) != config.RoleAccessHost {
		return delivery, entry, errors.New("complete verification is not supported in this role's containment; host execution is not authorized")
	}
	return delivery, entry, nil
}

func (s *Engine) runPlanVerification(ctx context.Context, action github.AuthorizedAction, progress *planVerificationProgress, attemptID string) (verification.Result, error) {
	assignment, metadata, accepted := progress.Assignment, progress.Metadata, progress.Candidate
	delivery, entry, err := s.planGate(ctx, action)
	if err != nil {
		return verification.Result{}, err
	}
	request := verification.Request{
		Entrypoint: delivery.Manifest.CompleteVerification, Entry: entry, Directory: metadata.WorktreePath, Repository: delivery.Manifest.Repository,
		AttemptID: attemptID, PlanID: delivery.Parent.ID, PlanRevision: delivery.Revision, Boundary: execution.VerificationComplete,
		Observe: func(ctx context.Context) (verification.Observation, error) {
			fresh, err := s.revalidatePlanProgress(ctx, action, progress)
			if err != nil {
				return verification.Observation{}, err
			}
			action = fresh
			if err := s.verifyProgressPlanHead(ctx, action, progress); err != nil {
				return verification.Observation{}, err
			}
			current, configured, err := s.planGate(ctx, action)
			if err != nil {
				return verification.Observation{}, err
			}
			if current.Revision != delivery.Revision || configured.Digest() != entry.Digest() {
				return verification.Observation{}, errors.New("plan or verification settings changed")
			}
			if err := s.revalidateDeliveryAssignment(ctx, action.Item, assignment); err != nil {
				return verification.Observation{}, err
			}
			snapshot, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
			if err != nil {
				return verification.Observation{}, err
			}
			if !snapshot.Clean || snapshot.Head != accepted.Head || snapshot.Tree != accepted.Tree || snapshot.Fingerprint != accepted.Fingerprint {
				return verification.Observation{}, errors.New("accepted combined candidate changed before or during complete verification")
			}
			return verification.ObserveCandidate(ctx, metadata.WorktreePath, metadata.BaseRevision, delivery.Parent.Body, entry)
		},
	}
	if progress.Gate != nil && progress.EnvelopeDigest != "" {
		if err := execution.AuthenticateVerificationEnvelope(progress.Gate.Evidence(), progress.EnvelopeDigest); err != nil {
			return verification.Result{}, err
		}
		if prior := progress.Gate.Receipt; prior != nil {
			request.PreviousReceipt = prior
			request.PreviousDigest, err = prior.Digest()
			if err != nil {
				return verification.Result{}, err
			}
		}
	}
	return verification.Run(ctx, request)
}

// A destination refresh can publish accepted R after integrated P. Both heads
// are exact, authenticated states; only an existing immutable acceptance can
// authorize the R exception. Unexpected remote substitutions still fail closed.
func (s *Engine) verifyProgressPlanHead(ctx context.Context, action github.AuthorizedAction, p *planVerificationProgress) error {
	if err := s.verifyPlanHead(ctx, action.Item, p.Metadata.RepoRoot); err == nil {
		return nil
	} else if p.Publication == nil {
		return err
	}
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	retained, found, err := provider.LoadPublicationAcceptance(ctx, p.Metadata, p.Candidate, workspace.PublicationEvidence{PlanRevision: p.Assignment.Spec.PlanContext.Revision})
	if err != nil || !found || retained != *p.Publication {
		return errors.Join(errors.New("remote plan head has no exact retained publication authority"), err)
	}
	return provider.VerifyPlanBranch(ctx, p.Metadata.RepoRoot, retained.Repository, s.remoteName(), p.Metadata.BranchName, retained.CommitOID, func() error {
		_, err := s.revalidatePlanProgress(ctx, action, p)
		return err
	})
}

func (s *Engine) validateCompletePlanEvidence(ctx context.Context, action github.AuthorizedAction, metadata workspace.Metadata, record workspace.PublicationRecord) error {
	content, err := action.DelegatedContent()
	if err != nil {
		return err
	}
	feedback, err := s.loadReviewFeedbackRecord(action.Item, content)
	if err != nil {
		return err
	}
	if feedback == nil || feedback.PlanVerification == nil {
		return errors.New("pending publication lost protected current verification progress")
	}
	p := feedback.PlanVerification
	if _, err := s.revalidatePlanProgress(ctx, action, p); err != nil {
		return err
	}
	delivery, entry, err := s.planGateForReviewer(ctx, action, p.ReviewerRole)
	if err != nil {
		return err
	}
	if record.PlanRevision != delivery.Revision || record.VerificationDigest == "" || record.VerificationReceipt == "" {
		return errors.New("publication has no protected complete-verification provenance for this plan revision")
	}
	var envelope execution.VerificationEnvelope
	if err := json.Unmarshal([]byte(record.VerificationReceipt), &envelope); err != nil {
		return err
	}
	if err := execution.AuthenticateVerificationEnvelope(envelope, record.VerificationDigest); err != nil {
		return err
	}
	// The immutable publication tuple retains its original complete proof. A
	// recovered current-candidate guard is a separately protected observation.
	if p.Publication == nil || *p.Publication != record || p.Candidate.Head != record.CommitOID || p.Candidate.Tree != record.TreeOID || p.Gate == nil || p.Gate.Invocation.Outcome != "passed" || !p.Gate.Invocation.CleanupResolved {
		return errors.New("pending publication has no current passing gate observation")
	}
	envelope, digest := p.Gate.Evidence(), p.EnvelopeDigest
	observed, err := verification.ObserveCandidate(ctx, metadata.WorktreePath, metadata.BaseRevision, delivery.Parent.Body, entry)
	if err != nil {
		return err
	}
	if observed.CommitOID != record.CommitOID || observed.TreeOID != record.TreeOID {
		return errors.New("complete-verification candidate changed")
	}
	applicable, err := execution.AssessVerificationEnvelope(envelope, digest, execution.VerificationEnvelopeTarget{VerificationTarget: execution.VerificationTarget{
		Repository: record.Repository, PlanID: delivery.Parent.ID, CandidateOID: record.CommitOID, Entrypoint: delivery.Manifest.CompleteVerification,
		SettingsDigest: strings.TrimPrefix(entry.Digest(), "v1:"), Inputs: observed.Inputs, Boundary: execution.VerificationComplete, RequireCurrentCandidate: entry.RequireCurrentCandidate,
	}, PlanRevision: delivery.Revision, TreeOID: observed.TreeOID, BaseOID: observed.BaseOID, CandidateIntegrity: observed.Integrity, CurrentCandidateCommand: planCurrentCandidateCommand(entry)})
	if err != nil {
		return err
	}
	if !applicable.Applicable {
		return errors.New("complete verification is not applicable: " + applicable.Reason)
	}
	_, err = s.revalidatePlanProgress(ctx, action, p)
	return err
}

// Called only for the serialized pending integration owner. Observe existing
// proof, never execute a verification command or a model in reconciliation.
// Inapplicability returns the parent to the ordinary admitted QA path.
func (s *Engine) validatePlanMergeProof(ctx context.Context, action github.AuthorizedAction, metadata workspace.Metadata, head string) error {
	if action.Item.PlanRelease == "" {
		return nil
	}
	content, err := action.DelegatedContent()
	if err != nil {
		return err
	}
	feedback, err := s.loadReviewFeedbackRecord(action.Item, content)
	if err != nil {
		return err
	}
	if feedback == nil || feedback.PlanVerification == nil || feedback.PlanVerification.Publication == nil {
		return errors.New("final plan merge has no protected acceptance and verification progress")
	}
	p := feedback.PlanVerification
	snapshot, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
	if err != nil {
		return err
	}
	if !snapshot.Clean || snapshot.Head != head || head != action.Item.QACommit || snapshot.Head != p.Candidate.Head || snapshot.Fingerprint != p.Candidate.Fingerprint || metadata.Identity != p.Metadata.Identity || metadata.BaseRevision != p.Metadata.BaseRevision {
		return errors.New("final plan merge candidate no longer matches accepted QA")
	}
	record, found, err := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits()).LoadPublicationAcceptance(ctx, metadata, snapshot, workspace.PublicationEvidence{PlanRevision: p.Assignment.Spec.PlanContext.Revision})
	if err != nil || !found || record != *p.Publication {
		return errors.Join(errors.New("final plan merge has no exact immutable publication acceptance"), err)
	}
	return s.validateCompletePlanEvidence(ctx, action, metadata, record)
}

func planCurrentCandidateCommand(entry config.VerificationEntrypoint) []string {
	if entry.CurrentCandidateCheck == nil {
		return nil
	}
	return append([]string{entry.CurrentCandidateCheck.Command}, entry.CurrentCandidateCheck.Args...)
}
