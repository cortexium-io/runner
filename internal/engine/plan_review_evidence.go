package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/workspace"
)

type acceptedMemberEvidence struct {
	item     github.WorkItem
	metadata workspace.Metadata
	record   workspace.PublicationRecord
	snapshot workspace.EvidenceSnapshot
	found    bool
}

func (s *Engine) inspectMemberEvidence(ctx context.Context, delivery github.PlanDelivery, item github.WorkItem) (acceptedMemberEvidence, error) {
	result := acceptedMemberEvidence{item: item}
	if item.Phase != github.PlanIntegratedPhase || item.QACommit == "" || item.Transition != "" {
		return result, errors.New("whole-plan evidence requires an authenticated integrated member without pending transitions")
	}
	repo, err := s.repositoryDir(ctx, item.Repository)
	if err != nil {
		return result, err
	}
	content := github.DelegatedContentFor(item)
	request := s.workspaceRequestForItem(item, content.Digest, repo, false)
	request.BaseRef = s.remoteName() + "/" + delivery.Parent.Branch
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	result.metadata, err = provider.InspectRetainedReview(ctx, request)
	if err != nil {
		return result, err
	}
	if result.metadata.Identity.DelegatedContentDigest != content.Digest {
		return result, errors.New("member workspace does not match its approved content")
	}
	snapshot, err := s.checkoutSnapshotState(ctx, result.metadata.WorktreePath)
	if err != nil {
		return result, err
	}
	if !snapshot.Clean || snapshot.Head != item.QACommit {
		return result, errors.New("accepted member candidate changed; preserve retained work and reconcile before parent retry")
	}
	record, found, err := provider.LoadPublicationAcceptance(ctx, result.metadata, snapshot, workspace.PublicationEvidence{PlanRevision: delivery.Revision})
	if err != nil || !found || record.CommitOID != item.QACommit {
		return result, errors.Join(errors.New("member's exact protected acceptance is missing or changed"), err)
	}
	result.record = record
	result.snapshot, result.found, err = workspace.LoadAcceptedEvidence(ctx, result.metadata, record, delivery.Parent.ID, s.cfg.ReviewEvidencePaths, s.snapshotLimits())
	return result, err
}

func (s *Engine) preparePlanReviewEvidence(ctx context.Context, review *workspace.ReviewWorkspace, metadata workspace.Metadata, candidate workspace.Candidate, delivery github.PlanDelivery, present bool) (err error) {
	finish := metrics.StartStage(ctx, metrics.StageEvidenceCapture)
	defer func() { finish.FinishError(err) }()
	var provenance []workspace.EvidenceProvenance
	if present {
		provenance = append(provenance, workspace.MemberEvidenceProvenance(delivery.Parent.ID, delivery.Revision, metadata))
	}
	if err := review.PrepareEvidence(ctx, metadata, candidate, s.cfg.ReviewEvidencePaths, s.snapshotLimits(), provenance...); err != nil {
		return err
	}
	if !present || delivery.Parent.ID != metadata.Identity.ItemID || len(s.cfg.ReviewEvidencePaths) == 0 {
		return nil
	}
	var snapshots []workspace.EvidenceSnapshot
	for _, child := range delivery.Children {
		member, err := s.inspectMemberEvidence(ctx, delivery, child)
		if err != nil {
			return fmt.Errorf("accepted evidence for %s: %w", child.ID, err)
		}
		if !member.found {
			return fmt.Errorf("accepted member %s predates durable evidence capture; preview and apply a parent retry to recover retained historical evidence before QA", child.ID)
		}
		snapshots = append(snapshots, member.snapshot)
	}
	if err := review.AddAcceptedEvidence(ctx, snapshots, s.snapshotLimits()); err != nil {
		return err
	}
	return s.revalidateEvidenceDelivery(ctx, delivery)
}

func (s *Engine) revalidateEvidenceDelivery(ctx context.Context, expected github.PlanDelivery) error {
	fresh, present, err := s.source.DeliveryForItem(ctx, expected.Parent)
	if err != nil || !present || !reflect.DeepEqual(fresh, expected) {
		return errors.Join(errors.New("plan authority or membership changed during evidence capture; inspect a fresh retry preview"), err)
	}
	return nil
}

// RetryPlan extends the existing operator preview, not Project authority. Its
// private seal prevents altered caller-provided recovery fields being applied.
type RetryPlan struct {
	github.RetryPlan
	EvidenceRecovery *EvidenceRecovery `json:"evidence_recovery,omitempty"`
	recoverySeal     [32]byte
}

type EvidenceRecovery struct {
	ParentID       string                     `json:"parent_id"`
	PlanRevision   string                     `json:"plan_revision"`
	Candidate      workspace.Candidate        `json:"parent_candidate"`
	ParentEvidence workspace.EvidenceSnapshot `json:"parent_evidence"`
	Members        []MemberEvidenceRecovery   `json:"members"`
}

type MemberEvidenceRecovery struct {
	ID               string                     `json:"member_id"`
	Workspace        string                     `json:"source_workspace"`
	AcceptanceDigest string                     `json:"acceptance_digest"`
	RecoveryRequired bool                       `json:"recovery_required"`
	Evidence         workspace.EvidenceSnapshot `json:"evidence"`
}

// inspectRetryEvidence does not preserve a capture during preview. Apply
// recaptures, checks the exact preview and then seals it before changing state.
func (s *Engine) inspectRetryEvidence(ctx context.Context, plan github.RetryPlan, expected *EvidenceRecovery) (*EvidenceRecovery, error) {
	if plan.Item.PlanRelease == "" || len(s.cfg.ReviewEvidencePaths) == 0 {
		return nil, nil
	}
	delivery, present, err := s.source.DeliveryForItem(ctx, plan.Item)
	if err != nil || !present || delivery.Parent.ID != plan.Item.ID {
		return nil, errors.Join(errors.New("parent retry requires current whole-plan authority"), err)
	}
	repo, err := s.repositoryDir(ctx, plan.Item.Repository)
	if err != nil {
		return nil, err
	}
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	metadata, err := provider.InspectRetainedReview(ctx, s.workspaceRequestForItem(plan.Item, github.DelegatedContentFor(plan.Item).Digest, repo, false))
	if err != nil {
		return nil, err
	}
	snapshot, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
	if err != nil {
		return nil, err
	}
	if !snapshot.Clean || metadata.Identity.DelegatedContentDigest != github.DelegatedContentFor(plan.Item).Digest {
		return nil, errors.New("parent retry requires the unchanged clean approved workspace")
	}
	recovery := &EvidenceRecovery{ParentID: plan.Item.ID, PlanRevision: delivery.Revision, Candidate: workspace.Candidate{CommitOID: snapshot.Head, TreeOID: snapshot.Tree}}
	parentEvidence, cleanup, err := workspace.CaptureSelectedEvidence(ctx, metadata, recovery.Candidate, s.cfg.ReviewEvidencePaths, s.snapshotLimits(), workspace.MemberEvidenceProvenance(delivery.Parent.ID, delivery.Revision, metadata))
	if err != nil {
		return nil, err
	}
	defer cleanup()
	recovery.ParentEvidence = parentEvidence
	collection := []workspace.EvidenceSnapshot{parentEvidence}
	var captures []acceptedMemberEvidence
	for _, child := range delivery.Children {
		member, err := s.inspectMemberEvidence(ctx, delivery, child)
		if err != nil {
			return nil, fmt.Errorf("cannot recover evidence for %s: %w", child.ID, err)
		}
		if !member.found {
			capture, cleanup, err := workspace.CaptureRecoveryEvidence(ctx, member.metadata, member.record, delivery.Parent.ID, s.cfg.ReviewEvidencePaths, s.snapshotLimits())
			if err != nil {
				return nil, err
			}
			defer cleanup()
			member.snapshot = capture
			captures = append(captures, member)
		}
		recovery.Members = append(recovery.Members, MemberEvidenceRecovery{ID: child.ID, Workspace: member.metadata.WorktreePath, AcceptanceDigest: workspace.PublicationAcceptanceDigest(member.record), RecoveryRequired: !member.found, Evidence: member.snapshot})
		collection = append(collection, member.snapshot)
	}
	if err := workspace.CheckEvidenceCollection(ctx, collection, s.snapshotLimits()); err != nil {
		return nil, err
	}
	if err := s.revalidateEvidenceDelivery(ctx, delivery); err != nil {
		return nil, err
	}
	if expected != nil {
		if qaPreviewDigest(recovery) != qaPreviewDigest(expected) {
			return nil, errors.New("retry evidence or candidate changed after preview; inspect a fresh preview")
		}
		for _, member := range captures {
			if len(member.snapshot.Manifest.MissingPaths) > 0 {
				return nil, fmt.Errorf("member %s is missing selected evidence %v; restore retained evidence or obtain a new scope decision before retry", member.record.ItemID, member.snapshot.Manifest.MissingPaths)
			}
		}
		for _, member := range captures {
			current, err := s.inspectMemberEvidence(ctx, delivery, member.item)
			if err != nil || current.record != member.record || current.metadata.Identity != member.metadata.Identity {
				return nil, errors.Join(errors.New("member identity changed before evidence recovery"), err)
			}
			if err := member.snapshot.PreserveRecovered(ctx, member.metadata, member.record, s.snapshotLimits()); err != nil {
				return nil, err
			}
		}
		if err := s.revalidateEvidenceDelivery(ctx, delivery); err != nil {
			return nil, err
		}
	}
	return recovery, nil
}

func (s *Engine) currentPlanEvidenceDigest(ctx context.Context, delivery github.PlanDelivery, metadata workspace.Metadata, candidate workspace.Candidate) (string, error) {
	if len(s.cfg.ReviewEvidencePaths) == 0 {
		return "", nil
	}
	parent, cleanup, err := workspace.CaptureSelectedEvidence(ctx, metadata, candidate, s.cfg.ReviewEvidencePaths, s.snapshotLimits(), workspace.MemberEvidenceProvenance(delivery.Parent.ID, delivery.Revision, metadata))
	if err != nil {
		return "", err
	}
	defer cleanup()
	collection := []workspace.EvidenceSnapshot{parent}
	for _, child := range delivery.Children {
		member, err := s.inspectMemberEvidence(ctx, delivery, child)
		if err != nil {
			return "", err
		}
		if !member.found {
			return "", fmt.Errorf("member %s has no durable evidence; preview parent retry recovery before any further review or validation", child.ID)
		}
		collection = append(collection, member.snapshot)
	}
	if err := workspace.CheckEvidenceCollection(ctx, collection, s.snapshotLimits()); err != nil {
		return "", err
	}
	if err := s.revalidateEvidenceDelivery(ctx, delivery); err != nil {
		return "", err
	}
	return workspace.EvidenceCollectionDigest(collection), nil
}

func (s *Engine) planRetryEvidence(ctx context.Context, plan github.RetryPlan) (RetryPlan, error) {
	recovery, err := s.inspectRetryEvidence(ctx, plan, nil)
	result := RetryPlan{RetryPlan: plan, EvidenceRecovery: recovery}
	result.recoverySeal = qaPreviewDigest(recovery)
	return result, err
}

func (s *Engine) applyRetryEvidence(ctx context.Context, plan RetryPlan) error {
	if qaPreviewDigest(plan.EvidenceRecovery) != plan.recoverySeal {
		return errors.New("retry evidence preview was modified")
	}
	if plan.EvidenceRecovery == nil {
		return nil
	}
	_, err := s.inspectRetryEvidence(ctx, plan.RetryPlan, plan.EvidenceRecovery)
	return err
}
