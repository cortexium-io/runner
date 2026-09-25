package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/workspace"
)

type RequirementAmendment struct {
	Approval            github.AmendmentPlan `json:"approval"`
	Workspace           workspace.Identity   `json:"workspace"`
	Candidate           workspace.Snapshot   `json:"candidate"`
	OldProof            []string             `json:"old_proof_obligations"`
	NewProof            []string             `json:"new_proof_obligations"`
	VerificationDigest  string               `json:"historical_verification_digest,omitempty"`
	HistoricalCandidate workspace.Candidate  `json:"historical_candidate"`
}

func (s *Engine) PlanRequirementAmendment(ctx context.Context, selector, body string) (RequirementAmendment, error) {
	approval, err := s.source.PlanAmendment(ctx, selector, body)
	if err != nil {
		return RequirementAmendment{}, err
	}
	lane, ok := s.cfg.Workflow.Lanes[approval.RetryLane]
	if !ok || (s.cfg.RoleContract(lane.Role) != config.WorkRoleImplementer && s.cfg.RoleContract(lane.Role) != config.WorkRoleReviewer) {
		return RequirementAmendment{}, errors.New("amendment supports only retained implementation or reviewer work")
	}
	if approval.Unstarted && s.cfg.RoleContract(lane.Role) != config.WorkRoleImplementer {
		return RequirementAmendment{}, errors.New("unstarted amendment requires an implementation member")
	}
	if s.cfg.RoleContract(lane.Role) == config.WorkRoleReviewer && strings.TrimSpace(approval.Item.Branch) == "" {
		return RequirementAmendment{}, errors.New("reviewer amendment requires the recorded Project branch; it cannot repair publication identity")
	}
	repo, err := s.repositoryDir(ctx, approval.Item.Repository)
	if err != nil {
		return RequirementAmendment{}, err
	}
	request := s.workspaceRequestForItem(approval.Item, github.DelegatedContentFor(approval.Item).Digest, repo, false)
	branch, err := workspace.RequestBranch(request)
	if err != nil {
		return RequirementAmendment{}, err
	}
	if err := github.NewPullRequestManager(s.run, s.source).RequireUnpublishedBranch(ctx, approval.Item.Repository, branch); err != nil {
		return RequirementAmendment{}, err
	}
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	plan := RequirementAmendment{Approval: approval, OldProof: approvedVerificationContract(approval.Item.Body), NewProof: approvedVerificationContract(approval.Body)}
	if approval.Unstarted {
		if err := provider.VerifyWorkspaceAbsent(ctx, request); err != nil {
			return RequirementAmendment{}, err
		}
		if err := s.checkAdoptionEvidenceAbsent(approval.Item.ID); err != nil {
			return RequirementAmendment{}, err
		}
		return plan, nil
	}
	// Retiring an implementation checkpoint could replenish a spent deadline or
	// correction allowance. This amendment does not authorize that recovery.
	if digest, err := amendmentEvidenceDigest(s.implementationCheckpointPath(approval.Item.ID)); err != nil || digest != "" {
		return RequirementAmendment{}, errors.Join(errors.New("amendment cannot retire an implementation checkpoint; inspect retained execution separately"), err)
	}
	identity, err := s.validateReauthorizationWorkspace(ctx, approval.Item)
	if err != nil {
		return RequirementAmendment{}, err
	}
	// Use the full retained-worktree snapshot, not a Git status summary.
	candidate, err := workspace.CaptureSnapshotStateWithLimits(ctx, s.run, identity.WorktreePath, 30*time.Second, s.snapshotLimits())
	if err != nil {
		return RequirementAmendment{}, err
	}
	if !candidate.Clean || candidate.Branch != identity.Branch || !reviewObjectID(candidate.Head) || !reviewObjectID(candidate.Tree) {
		return RequirementAmendment{}, errors.New("amendment requires a clean committed candidate on the retained branch")
	}
	metadata, err := provider.InspectRetainedReview(ctx, s.workspaceRequestForItem(approval.Item, identity.DelegatedContentDigest, repo, false))
	if err != nil {
		return RequirementAmendment{}, err
	}
	if metadata.Identity != identity {
		return RequirementAmendment{}, errors.New("workspace identity changed during amendment preview")
	}
	acceptanceSnapshot, err := s.checkoutSnapshotState(ctx, identity.WorktreePath)
	if err != nil {
		return RequirementAmendment{}, err
	}
	if acceptanceSnapshot.Head != candidate.Head || acceptanceSnapshot.Tree != candidate.Tree || !acceptanceSnapshot.Clean {
		return RequirementAmendment{}, errors.New("candidate changed while inspecting prior acceptance")
	}
	if _, accepted, err := provider.LoadPublicationAcceptance(ctx, metadata, acceptanceSnapshot); err != nil || accepted {
		return RequirementAmendment{}, errors.Join(errors.New("amendment cannot carry retained publication acceptance; reassess the accepted candidate separately"), err)
	}
	// Validate the original record's authority, not its applicability to the
	// correction. Only exact historical bytes are archived; normal execution
	// still demands verification bound to its current candidate.
	beforeDigest, err := amendmentEvidenceDigest(s.verificationEvidencePath(approval.Item.ID))
	if err != nil {
		return RequirementAmendment{}, err
	}
	record, err := s.readVerificationEvidence(approval.Item.ID)
	if err != nil {
		return RequirementAmendment{}, err
	}
	if record != nil {
		if err := verificationEvidenceBinding(*record, approval.Item, github.DelegatedContentFor(approval.Item), metadata, plan.OldProof); err != nil {
			return RequirementAmendment{}, err
		}
		plan.HistoricalCandidate = workspace.Candidate{CommitOID: record.CommitOID, TreeOID: record.TreeOID}
		if err := provider.ValidateAmendmentHistory(ctx, metadata, acceptanceSnapshot, plan.HistoricalCandidate); err != nil {
			return RequirementAmendment{}, err
		}
	}
	verificationDigest, err := amendmentEvidenceDigest(s.verificationEvidencePath(approval.Item.ID))
	if err != nil || verificationDigest != beforeDigest {
		return RequirementAmendment{}, errors.Join(errors.New("verification changed during amendment preview"), err)
	}
	plan.Workspace, plan.Candidate, plan.VerificationDigest = identity, candidate, verificationDigest
	return plan, nil
}

func (s *Engine) ApplyRequirementAmendment(ctx context.Context, plan RequirementAmendment) (item github.WorkItem, err error) {
	// This rare authority change is intentionally offline. Normal CLI intake,
	// planning, and retries remain usable while the coordinator is running.
	err = s.withOfflineDeliveryOperator([]string{plan.Approval.Item.ID}, func() error {
		item, err = s.applyRequirementAmendment(ctx, plan)
		return err
	})
	return item, err
}

func (s *Engine) applyRequirementAmendment(ctx context.Context, plan RequirementAmendment) (github.WorkItem, error) {
	fresh, err := s.PlanRequirementAmendment(ctx, plan.Approval.Item.ID, plan.Approval.Body)
	if err != nil {
		return github.WorkItem{}, err
	}
	// Snapshot's maps are private workspace implementation details. Its full
	// fingerprint is public and participates in the exact preview comparison.
	if !reflect.DeepEqual(plan.Approval, fresh.Approval) || plan.Workspace != fresh.Workspace ||
		plan.Candidate.Fingerprint != fresh.Candidate.Fingerprint || plan.Candidate.Head != fresh.Candidate.Head ||
		plan.Candidate.Tree != fresh.Candidate.Tree || plan.VerificationDigest != fresh.VerificationDigest ||
		!reflect.DeepEqual(plan.HistoricalCandidate, fresh.HistoricalCandidate) || !reflect.DeepEqual(plan.OldProof, fresh.OldProof) || !reflect.DeepEqual(plan.NewProof, fresh.NewProof) {
		return github.WorkItem{}, errors.New("requirements, card, or candidate changed after amendment preview")
	}
	if fresh.Approval.Unstarted {
		return s.source.ApplyAmendment(ctx, fresh.Approval)
	}
	repo, err := s.repositoryDir(ctx, fresh.Approval.Item.Repository)
	if err != nil {
		return github.WorkItem{}, err
	}
	request := s.workspaceRequestForItem(fresh.Approval.Item, fresh.Workspace.DelegatedContentDigest, repo, false)
	next := fresh.Approval.Item
	next.Body = fresh.Approval.Body
	newDigest := github.DelegatedContentFor(next).Digest
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	if err := provider.RebindRetainedContent(ctx, request, fresh.Workspace, fresh.Candidate, newDigest); err != nil {
		return github.WorkItem{}, err
	}
	item, applyErr := s.source.ApplyAmendment(ctx, fresh.Approval)
	if applyErr == nil || item.ID != "" {
		// The new card is paused. Retire the old proof record from the active
		// lookup without deleting its bytes or weakening mismatch checks for
		// normal execution. An archival failure leaves the card paused.
		archiveErr := s.archiveAmendedVerification(item.ID, fresh.VerificationDigest)
		return item, errors.Join(applyErr, archiveErr)
	}
	// Restore the local binding only after proving the remote rollback restored
	// the exact original authority. Never conceal an uncertain remote outcome.
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	current, inspectErr := s.source.InspectRecoveryItem(rollbackCtx, next.ID)
	if inspectErr != nil || current.Approval != fresh.Approval.Item.Approval || current.Body != fresh.Approval.Item.Body || current.Transition != "" {
		return github.WorkItem{}, errors.Join(applyErr, inspectErr, errors.New("leave Runner stopped: amendment outcome requires operator inspection"))
	}
	rebound := fresh.Workspace
	rebound.DelegatedContentDigest, request.DelegatedContentDigest = newDigest, newDigest
	rollbackErr := provider.RebindRetainedContent(rollbackCtx, request, rebound, fresh.Candidate, fresh.Workspace.DelegatedContentDigest)
	if rollbackErr != nil {
		return github.WorkItem{}, errors.Join(applyErr, fmt.Errorf("restore workspace binding; leave Runner stopped: %w", rollbackErr))
	}
	return github.WorkItem{}, fmt.Errorf("amendment failed; original card and workspace restored: %w", applyErr)
}

func amendmentEvidenceDigest(path string) (string, error) {
	data, err := readAmendmentEvidence(path)
	if err != nil || data == nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func readAmendmentEvidence(path string) ([]byte, error) {
	data, mode, state, err := securefs.ReadFile(path, maxVerificationEvidenceBytes)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !state.Exists) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if mode.Perm() != 0o600 {
		return nil, errors.New("amendment evidence must be private mode-0600")
	}
	if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *Engine) archiveAmendedVerification(itemID, expectedDigest string) error {
	return archiveAmendedEvidence(s.verificationEvidencePath(itemID), expectedDigest)
}

func archiveAmendedEvidence(path, expectedDigest string) error {
	data, err := readAmendmentEvidence(path)
	digest := ""
	if data != nil {
		digest = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	if err != nil || digest != expectedDigest {
		return errors.Join(errors.New("requirements were amended but verification changed; leave the card paused and inspect its evidence"), err)
	}
	if digest == "" {
		return nil
	}
	archive := path + ".superseded-" + digest
	// Keep independent private bytes: hard-linking would invalidate the
	// single-link invariant enforced when reading Runner control files.
	if err := securefs.WriteFileExclusive(archive, data, 0o600); err != nil {
		if existing, readErr := amendmentEvidenceDigest(archive); readErr != nil || existing != digest {
			return errors.Join(errors.New("requirements were amended but historical verification could not be retained; leave the card paused"), err, readErr)
		}
	}
	if current, err := amendmentEvidenceDigest(path); err != nil || current != digest {
		return errors.Join(errors.New("requirements were amended but verification changed before retirement; leave the card paused"), err)
	}
	if err := securefs.RemoveFile(path); err != nil {
		return fmt.Errorf("requirements amended and verification archived, but active proof could not be retired; leave the card paused: %w", err)
	}
	return nil
}
