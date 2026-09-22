package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"time"

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/workspace"
)

type amendmentWorkspace struct {
	Identity           workspace.Identity
	Candidate          workspace.Snapshot
	Acceptance         *workspace.PublicationRecord
	VerificationDigest string
	CheckpointDigest   string
}

type planAmendmentRecord struct {
	State      github.PlanAmendmentState
	Workspaces []amendmentWorkspace
	Completed  bool
}

// DeliveryAmendment exposes only the operator preview. Signed recovery
// snapshots remain private and must never be printed in CLI JSON.
type DeliveryAmendment struct {
	ID                   string                      `json:"id"`
	PreviousRevision     string                      `json:"previous_revision"`
	Revision             string                      `json:"revision"`
	Digest               string                      `json:"preview_digest"`
	Request              github.PlanAmendmentRequest `json:"request"`
	PreviousManifest     github.PlanManifest         `json:"previous_manifest"`
	PreviousMemberBodies map[string]string           `json:"previous_member_bodies"`
	Affected             []string                    `json:"affected_members"`
	CarriedAcceptance    map[string]string           `json:"carried_original_acceptance"`
	record               planAmendmentRecord
}

func (s *Engine) PlanDeliveryAmendment(ctx context.Context, selector string, request github.PlanAmendmentRequest) (DeliveryAmendment, error) {
	state, err := s.source.PlanDeliveryAmendment(ctx, selector, request)
	if err != nil {
		return DeliveryAmendment{}, err
	}
	feedback, err := s.readReviewFeedbackRecord(state.Before[0])
	if err != nil {
		return DeliveryAmendment{}, err
	}
	if feedback != nil && feedback.PlanAmendment != nil && !feedback.PlanAmendment.Completed {
		return DeliveryAmendment{}, errors.New("an approved amendment is unfinished; resume that exact intent before another preview")
	}
	if state.Before[0].QACommit != "" {
		root, err := s.repositoryDir(ctx, state.Before[0].Repository)
		if err != nil {
			return DeliveryAmendment{}, err
		}
		if err := s.verifyPlanHead(ctx, state.Before[0], root); err != nil {
			return DeliveryAmendment{}, err
		}
	}
	record := planAmendmentRecord{State: state}
	for i, item := range state.Before {
		checkpoint, err := s.readImplementationCheckpoint(item.ID)
		if err != nil {
			return DeliveryAmendment{}, err
		}
		if checkpoint != nil && checkpoint.Incomplete {
			return DeliveryAmendment{}, fmt.Errorf("item %s has an unfinished spent implementation allowance; resolve that attempt explicitly before amending (no allowance reset)", item.ID)
		}
		retained, err := s.inspectAmendmentWorkspace(ctx, state, i)
		if err != nil {
			return DeliveryAmendment{}, fmt.Errorf("inspect amendment member %s: %w", item.ID, err)
		}
		record.Workspaces = append(record.Workspaces, retained)
	}
	return deliveryAmendmentPreview(record), nil
}

func deliveryAmendmentPreview(record planAmendmentRecord) DeliveryAmendment {
	state := record.State
	manifest, _, _ := github.ParsePlanManifest(state.Before[0].Body)
	b, _ := json.Marshal(record)
	p := DeliveryAmendment{ID: state.Before[0].ID, PreviousRevision: state.Request.ExpectedRevision, Revision: github.PlanRevision(state.After[0].Body),
		Digest: fmt.Sprintf("v1:%x", sha256.Sum256(b)), Request: state.Request, PreviousManifest: manifest, Affected: state.Affected,
		PreviousMemberBodies: map[string]string{}, CarriedAcceptance: map[string]string{}, record: record}
	for i, item := range state.Before {
		if _, changed := state.Request.MemberBodies[item.ID]; changed {
			p.PreviousMemberBodies[item.ID] = item.Body
		}
		if i > 0 && !slices.Contains(state.Affected, item.ID) && record.Workspaces[i].Acceptance != nil {
			p.CarriedAcceptance[item.ID] = workspace.PublicationAcceptanceDigest(*record.Workspaces[i].Acceptance)
		}
	}
	return p
}

func (s *Engine) amendmentWorkspaceRequest(ctx context.Context, state github.PlanAmendmentState, index int, digest string) (workspace.Request, error) {
	item := state.Before[index]
	repo, err := s.repositoryDir(ctx, item.Repository)
	if err != nil {
		return workspace.Request{}, err
	}
	request := s.workspaceRequestForItem(item, digest, repo, false)
	if index > 0 {
		request.BaseRef = s.remoteName() + "/" + state.Before[0].Branch
	}
	return request, nil
}

func (s *Engine) inspectAmendmentWorkspace(ctx context.Context, state github.PlanAmendmentState, index int) (amendmentWorkspace, error) {
	item := state.Before[index]
	request, err := s.amendmentWorkspaceRequest(ctx, state, index, github.DelegatedContentFor(item).Digest)
	if err != nil {
		return amendmentWorkspace{}, err
	}
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	metadata, err := provider.InspectRetainedReview(ctx, request)
	// An integrated plan parent need not have entered its own review workspace yet.
	if errors.Is(err, os.ErrNotExist) && (index == 0 || item.Branch == "") {
		return amendmentWorkspace{}, nil
	}
	if err != nil {
		return amendmentWorkspace{}, err
	}
	if metadata.Identity.DelegatedContentDigest != request.DelegatedContentDigest {
		return amendmentWorkspace{}, errors.New("retained content differs from current approved contract")
	}
	candidate, err := workspace.CaptureSnapshotStateWithLimits(ctx, s.run, metadata.WorktreePath, 30*time.Second, s.snapshotLimits())
	if err != nil {
		return amendmentWorkspace{}, err
	}
	if !candidate.Clean {
		return amendmentWorkspace{}, errors.New("amendment requires a clean committed retained candidate")
	}
	retained := amendmentWorkspace{Identity: metadata.Identity, Candidate: candidate}
	retained.VerificationDigest, err = amendmentEvidenceDigest(s.verificationEvidencePath(item.ID))
	if err != nil {
		return amendmentWorkspace{}, err
	}
	retained.CheckpointDigest, err = amendmentEvidenceDigest(s.implementationCheckpointPath(item.ID))
	if err != nil {
		return amendmentWorkspace{}, err
	}
	checkout, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
	if err != nil {
		return amendmentWorkspace{}, err
	}
	accepted, found, err := provider.LoadPublicationAcceptance(ctx, metadata, checkout, workspace.PublicationEvidence{PlanRevision: state.Request.ExpectedRevision})
	if err != nil {
		return amendmentWorkspace{}, err
	}
	if found {
		retained.Acceptance = &accepted
	}
	if index > 0 && item.Phase == github.PlanIntegratedPhase && (!found || accepted.CommitOID != item.QACommit) {
		return amendmentWorkspace{}, errors.New("integrated member is missing its exact original protected acceptance")
	}
	return retained, nil
}

func (s *Engine) ApplyDeliveryAmendment(ctx context.Context, preview DeliveryAmendment) error {
	return s.withOfflineDeliveryOperator(amendmentIDs(preview.record.State), func() error {
		fresh, err := s.PlanDeliveryAmendment(ctx, preview.ID, preview.Request)
		if err != nil {
			return err
		}
		if fresh.Digest != preview.Digest || !reflect.DeepEqual(fresh.Request, preview.Request) {
			return errors.New("amendment authority, workspace or proof changed after preview; inspect a fresh preview")
		}
		parent := fresh.record.State.Before[0]
		feedback, err := s.readReviewFeedbackRecord(parent)
		if err != nil {
			return err
		}
		if err := s.archiveDeliveryAmendmentHistory(parent.ID); err != nil {
			return err
		}
		if feedback == nil {
			feedback = &reviewFeedbackRecord{Version: reviewFeedbackVersion, ItemID: parent.ID, DelegatedContentDigest: github.DelegatedContentFor(parent).Digest, Items: []string{"Approved plan amendment: " + preview.Request.Reason}}
		}
		feedback.PlanAmendment = &fresh.record
		// Intent is durable before the first Project write. A crash here cannot
		// authorize an unrecorded patch, and poll recovery runs before admission.
		if err := s.writeReviewFeedback(*feedback); err != nil {
			return err
		}
		return s.resumeDeliveryAmendment(ctx, feedback)
	})
}

func amendmentIDs(state github.PlanAmendmentState) []string {
	ids := make([]string, 0, len(state.Before))
	for _, item := range state.Before {
		ids = append(ids, item.ID)
	}
	return ids
}

func (s *Engine) resumeDeliveryAmendment(ctx context.Context, feedback *reviewFeedbackRecord) error {
	r := feedback.PlanAmendment
	if r == nil || r.Completed {
		return nil
	}
	if len(r.Workspaces) != len(r.State.Before) {
		return errors.New("protected amendment workspace set is incomplete")
	}
	if err := s.source.ValidatePlanAmendmentState(r.State); err != nil {
		return err
	}
	if err := s.source.FencePlanAmendment(ctx, r.State); err != nil {
		return err
	}
	if r.State.Before[0].QACommit != "" {
		root, err := s.repositoryDir(ctx, r.State.Before[0].Repository)
		if err != nil {
			return err
		}
		parent := r.State.Before[0]
		if err := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits()).VerifyPlanBranch(ctx, root, parent.Repository, s.remoteName(), parent.Branch, parent.QACommit, func() error { return s.source.CheckPlanAmendment(ctx, r.State) }); err != nil {
			return err
		}
	}
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	for i, before := range r.State.Before {
		if err := s.source.CheckPlanAmendment(ctx, r.State); err != nil {
			return err
		}
		retained := r.Workspaces[i]
		oldDigest, newDigest := github.DelegatedContentFor(before).Digest, github.DelegatedContentFor(r.State.After[i]).Digest
		request, err := s.amendmentWorkspaceRequest(ctx, r.State, i, oldDigest)
		if err != nil {
			return err
		}
		metadata, err := provider.InspectRetainedReview(ctx, request)
		if retained.Identity.ItemID == "" {
			if !errors.Is(err, os.ErrNotExist) {
				return errors.Join(errors.New("previously absent amendment workspace appeared"), err)
			}
			continue
		}
		if err != nil {
			return err
		}
		currentIdentity := metadata.Identity
		if currentIdentity.DelegatedContentDigest != oldDigest && currentIdentity.DelegatedContentDigest != newDigest {
			return errors.New("amendment workspace content changed outside before/after states")
		}
		currentIdentity.DelegatedContentDigest = retained.Identity.DelegatedContentDigest
		if currentIdentity != retained.Identity {
			return errors.New("amendment workspace identity changed")
		}
		snapshot, err := workspace.CaptureSnapshotStateWithLimits(ctx, s.run, metadata.WorktreePath, 30*time.Second, s.snapshotLimits())
		if err != nil || snapshot.Fingerprint != retained.Candidate.Fingerprint || snapshot.Head != retained.Candidate.Head || !snapshot.Clean {
			return errors.Join(errors.New("amendment candidate changed; preserve work and inspect"), err)
		}
		if oldDigest != newDigest && metadata.Identity.DelegatedContentDigest == oldDigest {
			if err := provider.RebindRetainedContent(ctx, request, retained.Identity, retained.Candidate, newDigest); err != nil {
				return err
			}
		}
		if i > 0 && !slices.Contains(r.State.Affected, before.ID) && retained.Acceptance != nil {
			checkout, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
			if err != nil {
				return err
			}
			if _, err := provider.CarryPlanAcceptance(ctx, metadata, checkout, *retained.Acceptance, github.PlanRevision(r.State.After[0].Body), r.State.Digest()); err != nil {
				return err
			}
		} else {
			// An interrupted archival is idempotent only when the exact retained
			// bytes already exist in the immutable superseded file.
			for path, expected := range map[string]string{s.verificationEvidencePath(before.ID): retained.VerificationDigest, s.implementationCheckpointPath(before.ID): retained.CheckpointDigest} {
				if err := resumeAmendedEvidenceArchive(path, expected); err != nil {
					return err
				}
			}
		}
	}
	if err := s.source.WritePlanAmendmentContracts(ctx, r.State); err != nil {
		return err
	}
	if err := s.source.FinishPlanAmendment(ctx, r.State); err != nil {
		return err
	}
	r.Completed = true
	return s.writeReviewFeedback(*feedback)
}

func resumeAmendedEvidenceArchive(path, expected string) error {
	current, err := amendmentEvidenceDigest(path)
	if err != nil {
		return err
	}
	if current == "" && expected != "" {
		archived, err := amendmentEvidenceDigest(path + ".superseded-" + expected)
		if err != nil || archived != expected {
			return errors.Join(errors.New("historical amendment evidence disappeared"), err)
		}
		return nil
	}
	return archiveAmendedEvidence(path, expected)
}

// Existing protected QA feedback holds the pending intent, not a new journal.
// Keep its exact completed bytes before subsequent feedback replaces it.
func (s *Engine) archiveDeliveryAmendmentHistory(itemID string) error {
	record, err := s.readReviewFeedbackRecord(github.WorkItem{ID: itemID})
	if err != nil || record == nil || record.PlanAmendment == nil {
		return err
	}
	if !record.PlanAmendment.Completed {
		return errors.New("unfinished protected amendment cannot be discarded")
	}
	path := s.reviewFeedbackPath(itemID)
	data, err := readAmendmentEvidence(path)
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	archive := path + ".amended-" + digest
	if err := securefs.WriteFileExclusive(archive, data, 0o600); err != nil {
		stored, readErr := amendmentEvidenceDigest(archive)
		if readErr != nil || stored != digest {
			return errors.Join(err, readErr)
		}
	}
	return nil
}

func (s *Engine) recoverDeliveryAmendments(ctx context.Context, items []github.WorkItem) (bool, error) {
	changed := false
	for _, parent := range items {
		if parent.PlanRelease == "" {
			continue
		}
		record, err := s.readReviewFeedbackRecord(parent)
		if err != nil {
			return false, err
		}
		if record == nil || record.PlanAmendment == nil {
			if parent.Phase == github.PlanAmendingPhase {
				return false, errors.New("amendment fence has no protected intent; leave Runner stopped and inspect")
			}
			continue
		}
		if record.PlanAmendment.Completed {
			continue
		}
		// preparePoll already holds the worker and mutation guards and only calls
		// this with no in-flight assignments. Standalone operations still fence it.
		planning, lockErr := github.AcquirePlanningLock(s.cfg.GitHubProject.GitHubProjectConfig)
		if lockErr != nil {
			return false, lockErr
		}
		err = s.withDeliveryOperationGuards(amendmentIDs(record.PlanAmendment.State), func() error { return s.resumeDeliveryAmendment(ctx, record) })
		planning.Release()
		if err != nil {
			return false, fmt.Errorf("recover exact approved amendment (no model work admitted): %w", err)
		}
		changed = true
	}
	return changed, nil
}
