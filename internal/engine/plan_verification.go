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
	profile, ok := s.cfg.RoleProfile(action.Role)
	if !ok || s.cfg.RoleContract(action.Role) != config.WorkRoleReviewer || config.EffectiveRoleAccess(profile.Access) != config.RoleAccessHost {
		return delivery, entry, errors.New("complete verification is not supported in this role's containment; host execution is not authorized")
	}
	return delivery, entry, nil
}

func (s *Engine) completePlanVerification(ctx context.Context, action github.AuthorizedAction, assignment execution.Assignment, metadata workspace.Metadata, accepted workspace.Snapshot, attemptID string) (workspace.PublicationEvidence, error) {
	delivery, entry, err := s.planGate(ctx, action)
	if err != nil {
		return workspace.PublicationEvidence{}, err
	}
	result, err := verification.Run(ctx, verification.Request{
		Entrypoint: delivery.Manifest.CompleteVerification, Entry: entry, Directory: metadata.WorktreePath, Repository: delivery.Manifest.Repository,
		AttemptID: attemptID, PlanID: delivery.Parent.ID, PlanRevision: delivery.Revision, Boundary: execution.VerificationComplete,
		Observe: func(ctx context.Context) (verification.Observation, error) {
			if err := s.verifyPlanHead(ctx, action.Item, metadata.RepoRoot); err != nil {
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
	})
	if err != nil {
		return workspace.PublicationEvidence{}, err
	}
	if result.Receipt.Outcome != "passed" || !result.Receipt.CleanupResolved {
		return workspace.PublicationEvidence{}, errors.New("complete verification did not pass with resolved cleanup")
	}
	encoded, err := json.Marshal(result.Receipt)
	if err != nil {
		return workspace.PublicationEvidence{}, err
	}
	return workspace.PublicationEvidence{PlanRevision: delivery.Revision, VerificationDigest: result.Digest, VerificationReceipt: string(encoded)}, nil
}

func (s *Engine) validateCompletePlanEvidence(ctx context.Context, action github.AuthorizedAction, metadata workspace.Metadata, record workspace.PublicationRecord) error {
	delivery, entry, err := s.planGate(ctx, action)
	if err != nil {
		return err
	}
	if record.PlanRevision != delivery.Revision || record.VerificationDigest == "" || record.VerificationReceipt == "" {
		return errors.New("publication has no protected complete-verification provenance for this plan revision")
	}
	var receipt execution.VerificationReceipt
	if err := json.Unmarshal([]byte(record.VerificationReceipt), &receipt); err != nil {
		return err
	}
	if receipt.PlanRevision != delivery.Revision {
		return errors.New("complete verification belongs to another plan revision")
	}
	observed, err := verification.ObserveCandidate(ctx, metadata.WorktreePath, metadata.BaseRevision, delivery.Parent.Body, entry)
	if err != nil {
		return err
	}
	if observed.CommitOID != record.CommitOID || observed.TreeOID != record.TreeOID {
		return errors.New("complete-verification candidate changed")
	}
	applicable, err := execution.AssessVerificationReceipt(receipt, record.VerificationDigest, execution.VerificationTarget{
		Repository: record.Repository, PlanID: delivery.Parent.ID, CandidateOID: record.CommitOID, Entrypoint: delivery.Manifest.CompleteVerification,
		SettingsDigest: strings.TrimPrefix(entry.Digest(), "v1:"), Inputs: observed.Inputs, Boundary: execution.VerificationComplete, RequireCurrentCandidate: entry.RequireCurrentCandidate,
	})
	if err != nil {
		return err
	}
	if !applicable.Applicable {
		return errors.New("complete verification is not applicable: " + applicable.Reason)
	}
	return nil
}
