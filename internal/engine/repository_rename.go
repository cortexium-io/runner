package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/workspace"
)

const maxRenameJournalBytes = 64 * 1024 * 1024

type RenameWorkspace struct {
	ItemID   string             `json:"item_id"`
	Before   workspace.Identity `json:"before"`
	After    workspace.Identity `json:"after"`
	Snapshot workspace.Snapshot `json:"snapshot"`
}

type renamePrivateRecord struct {
	Path   string `json:"path"`
	Before []byte `json:"before"`
	After  []byte `json:"after"`
}

// RepositoryRenameRecovery is an exact operator preview and immutable recovery
// journal, including original authority and historical verification records.
type RepositoryRenameRecovery struct {
	Version    int                         `json:"version"`
	Batch      github.RepositoryRenamePlan `json:"batch"`
	Workspaces []RenameWorkspace           `json:"workspaces"`
	History    []renamePrivateRecord       `json:"history"`
	Journal    string                      `json:"journal"`
	Config     [32]byte                    `json:"config_digest"`
	seal       *renameRecoverySeal
}

type renameRecoverySeal struct {
	owner  *Engine
	digest [32]byte
}

func (s *Engine) renameJournalPath(sourceID, from string) string {
	key := sha256.Sum256([]byte(sourceID + "\x00" + from + "\x00" + s.cfg.GitHubProject.IntakeRepository))
	return filepath.Join(s.implementationWorkspaceRoot(), ".runner-state", "repository-renames", fmt.Sprintf("%x.json", key))
}

func (s *Engine) PlanRepositoryRename(ctx context.Context, selector, from string) (RepositoryRenameRecovery, error) {
	item, err := s.source.InspectRecoveryItem(ctx, selector)
	if err != nil {
		return RepositoryRenameRecovery{}, err
	}
	sourceID := item.PlanningSourceID
	if sourceID == "" {
		sourceID = item.ID
	}
	path := s.renameJournalPath(sourceID, from)
	encoded, err := readRenamePrivateFile(path, maxRenameJournalBytes)
	if err != nil {
		return RepositoryRenameRecovery{}, err
	}
	var plan RepositoryRenameRecovery
	if encoded != nil {
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&plan); err != nil {
			return plan, err
		}
		if err := ensureJSONEOF(decoder); err != nil {
			return plan, err
		}
		if plan.Version != 1 || plan.Journal != path || plan.Batch.Source.ID != sourceID || plan.Batch.From != from || plan.Config != qaPreviewDigest(s.cfg) {
			return plan, errors.New("saved rename recovery does not match this Runner configuration and exact batch")
		}
	} else {
		batch, err := s.source.PlanRepositoryRename(ctx, selector, from)
		if err != nil {
			return plan, err
		}
		plan = RepositoryRenameRecovery{Version: 1, Batch: batch, Journal: path, Config: qaPreviewDigest(s.cfg)}
		if err := s.captureRenameWorkspaces(ctx, &plan); err != nil {
			return plan, err
		}
	}
	if err := s.validateRenameRecovery(ctx, plan); err != nil {
		return RepositoryRenameRecovery{}, err
	}
	plan.seal = &renameRecoverySeal{owner: s, digest: qaPreviewDigest(plan)}
	return plan, nil
}

func (s *Engine) captureRenameWorkspaces(ctx context.Context, plan *RepositoryRenameRecovery) error {
	repo, err := s.repositoryDir(ctx, plan.Batch.To)
	if err != nil {
		return err
	}
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	for _, child := range plan.Batch.Children {
		old := child.Before
		old.Repository = plan.Batch.From
		request := s.workspaceRequestForItem(old, github.DelegatedContentFor(old).Digest, repo, false)
		identity, err := provider.InspectRepositoryRename(ctx, request)
		if err != nil {
			return fmt.Errorf("inspect original workspace for %s: %w", old.ID, err)
		}
		if identity != nil {
			snapshot, err := s.checkoutSnapshotState(ctx, identity.WorktreePath)
			if err != nil {
				return err
			}
			if snapshot.Branch != identity.Branch {
				return errors.New("retained candidate is not on its recorded branch")
			}
			after := *identity
			after.Repository, after.DelegatedContentDigest = plan.Batch.To, github.DelegatedContentFor(child.After).Digest
			plan.Workspaces = append(plan.Workspaces, RenameWorkspace{ItemID: old.ID, Before: *identity, After: after, Snapshot: snapshot})
		} else if err := s.checkAdoptionEvidenceAbsent(old.ID); err != nil {
			return err
		}
		for _, path := range []string{s.implementationCheckpointPath(old.ID), s.verificationEvidencePath(old.ID), s.reviewFeedbackPath(old.ID)} {
			data, err := readRenamePrivateFile(path, maxImplementationCheckpointBytes)
			if err != nil {
				return err
			}
			record := renamePrivateRecord{Path: path, Before: data, After: data}
			if path == s.implementationCheckpointPath(old.ID) && data != nil {
				checkpoint, err := s.readImplementationCheckpoint(old.ID)
				if err != nil {
					return err
				}
				if checkpoint.Incomplete || checkpoint.Specialist != nil || checkpoint.DelegatedContentDigest != github.DelegatedContentFor(old).Digest || checkpoint.Repository != old.Repository ||
					identity == nil || checkpoint.Branch != identity.Branch || checkpoint.BaseRevision != identity.BaseRevision || checkpoint.WorktreePath != identity.WorktreePath {
					return errors.New("rename recovery cannot discard an interrupted correction/specialist allowance or a mismatched implementation checkpoint; preserve it for explicit attempt recovery")
				}
				fullSnapshot, err := s.workspaceSnapshotState(ctx, identity.WorktreePath)
				if err != nil {
					return err
				}
				checkpoint.DelegatedContentDigest, checkpoint.Repository = github.DelegatedContentFor(child.After).Digest, plan.Batch.To
				checkpoint.SnapshotFingerprint = fullSnapshot.Fingerprint
				checkpoint.RenameRequiresProof = true
				record.After, err = json.Marshal(checkpoint)
				if err != nil {
					return err
				}
			}
			if path == s.reviewFeedbackPath(old.ID) && data != nil {
				feedback, err := s.readReviewFeedbackRecord(old)
				if err != nil {
					return err
				}
				if feedback.DelegatedContentDigest != github.DelegatedContentFor(old).Digest {
					return errors.New("historical feedback does not match the original approved requirements")
				}
				if feedback.PlanVerification != nil || feedback.PlanAmendment != nil || feedback.PlanRepair != nil {
					return errors.New("rename recovery cannot renew whole-plan verification, amendment or repair authority")
				}
				feedback.DelegatedContentDigest = github.DelegatedContentFor(child.After).Digest
				feedback.Baseline = nil // Renew actionable defects, never old acceptance or proof.
				record.After, err = json.Marshal(feedback)
				if err != nil {
					return err
				}
			}
			plan.History = append(plan.History, record)
		}
	}
	return nil
}

func (s *Engine) validateRenameRecovery(ctx context.Context, plan RepositoryRenameRecovery) error {
	if plan.Config != qaPreviewDigest(s.cfg) {
		return errors.New("Runner configuration changed after rename preview")
	}
	if err := s.processOwnership.CheckAdmission(); err != nil {
		return err
	}
	if _, active, err := github.InspectProcessState(s.cfg.GitHubProject.GitHubProjectConfig); err != nil || active {
		return errors.Join(errors.New("stop the Runner worker before previewing repository rename recovery"), err)
	}
	return s.validateRenameBindings(ctx, plan)
}

func (s *Engine) validateRenameBindings(ctx context.Context, plan RepositoryRenameRecovery) error {
	if err := s.source.ValidateRepositoryRename(ctx, plan.Batch); err != nil {
		return err
	}
	repo, err := s.repositoryDir(ctx, plan.Batch.To)
	if err != nil {
		return err
	}
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	byID := map[string]RenameWorkspace{}
	for _, retained := range plan.Workspaces {
		if _, duplicate := byID[retained.ItemID]; duplicate {
			return errors.New("duplicate retained workspace in recovery journal")
		}
		byID[retained.ItemID] = retained
	}
	for _, child := range plan.Batch.Children {
		old := child.Before
		old.Repository = plan.Batch.From
		request := s.workspaceRequestForItem(old, github.DelegatedContentFor(old).Digest, repo, false)
		identity, inspectErr := provider.InspectRepositoryRename(ctx, request)
		retained, exists := byID[old.ID]
		if exists && errors.Is(inspectErr, workspace.ErrIdentityMismatch) {
			request.Repository, request.DelegatedContentDigest = retained.After.Repository, retained.After.DelegatedContentDigest
			identity, inspectErr = provider.InspectRepositoryRename(ctx, request)
		}
		if inspectErr != nil {
			return inspectErr
		}
		if !exists {
			if identity != nil {
				return errors.New("previously unstarted card gained a retained workspace after preview")
			}
			continue
		}
		if identity == nil || *identity != retained.Before && *identity != retained.After {
			return errors.New("retained workspace binding changed after rename preview")
		}
		if retained.Before.ItemID != old.ID || retained.Before.Repository != old.Repository || retained.Before.DelegatedContentDigest != github.DelegatedContentFor(old).Digest ||
			retained.After.Repository != plan.Batch.To || retained.After.DelegatedContentDigest != github.DelegatedContentFor(child.After).Digest {
			return errors.New("retained workspace journal does not match the exact old and renewed cards")
		}
		snapshot, err := s.checkoutSnapshotState(ctx, identity.WorktreePath)
		if err != nil {
			return err
		}
		if qaPreviewDigest(snapshot) != qaPreviewDigest(retained.Snapshot) {
			return errors.New("retained candidate or worktree bytes changed after rename preview")
		}
		metadata, err := provider.InspectRetainedReview(ctx, request)
		if err != nil {
			return err
		}
		accepted, err := provider.HasPriorPublicationAcceptance(ctx, metadata, snapshot.Head)
		if err != nil || accepted {
			return errors.Join(errors.New("rename recovery requires an unpublished candidate without QA acceptance"), err)
		}
		remote, err := s.git(ctx, []string{"ls-remote", "--heads", s.remoteName(), "refs/heads/" + identity.Branch}, repo, 30*time.Second)
		if err != nil || strings.TrimSpace(remote.Stdout) != "" {
			return errors.Join(errors.New("retained branch must be unpublished and its remote state verifiable"), err)
		}
		if err := github.NewPullRequestManager(s.run, s.source).RequireUnpublishedBranch(ctx, plan.Batch.To, identity.Branch); err != nil {
			return err
		}
	}
	for _, record := range plan.History {
		data, err := readRenamePrivateFile(record.Path, maxImplementationCheckpointBytes)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, record.Before) && !bytes.Equal(data, record.After) {
			return errors.New("private execution history changed after rename preview")
		}
	}
	return nil
}

func (s *Engine) ApplyRepositoryRename(ctx context.Context, plan RepositoryRenameRecovery) error {
	if plan.seal == nil || plan.seal.owner != s || plan.seal.digest != qaPreviewDigest(plan) || plan.Config != qaPreviewDigest(s.cfg) {
		return errors.New("repository rename preview is missing or modified; preview and confirm again")
	}
	ids := []string{plan.Batch.Source.ID}
	for _, child := range plan.Batch.Children {
		ids = append(ids, child.Before.ID)
	}
	return s.withOfflineDeliveryOperator(ids, func() error {
		return s.applyRepositoryRename(ctx, plan)
	})
}

func (s *Engine) applyRepositoryRename(ctx context.Context, plan RepositoryRenameRecovery) error {
	release, err := s.acquireRenameAdmission()
	if err != nil {
		return err
	}
	defer release()
	if err := s.validateRenameBindings(ctx, plan); err != nil {
		return err
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if len(encoded) > maxRenameJournalBytes {
		return errors.New("repository rename history exceeds the private journal size limit")
	}
	if err := securefs.EnsurePrivateDir(filepath.Dir(plan.Journal)); err != nil {
		return err
	}
	previous, err := readRenamePrivateFile(plan.Journal, maxRenameJournalBytes)
	if err != nil {
		return err
	}
	if previous == nil {
		if err := securefs.WriteFileExclusive(plan.Journal, encoded, 0o600); err != nil {
			return err
		}
	} else if !bytes.Equal(previous, encoded) {
		return errors.New("a different rename recovery journal already exists; preview it again")
	}
	// The immutable journal is written before the first external mutation. It
	// preserves original records even when the normal implementation overwrites
	// stale checkpoints and verification evidence on its next attempt.
	return s.source.ApplyRepositoryRename(ctx, plan.Batch, func() error {
		repo, err := s.repositoryDir(ctx, plan.Batch.To)
		if err != nil {
			return err
		}
		provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
		for _, retained := range plan.Workspaces {
			item := github.WorkItem{ID: retained.ItemID, Repository: retained.Before.Repository, Branch: retained.Before.Branch}
			request := s.workspaceRequestForItem(item, retained.Before.DelegatedContentDigest, repo, false)
			if err := provider.RebindRepositoryRename(ctx, request, retained.Before, retained.After); err != nil {
				return err
			}
		}
		for _, record := range plan.History {
			if bytes.Equal(record.Before, record.After) {
				continue
			}
			directory, err := securefs.OpenDir(filepath.Dir(record.Path))
			if err != nil {
				return err
			}
			data, _, state, err := directory.ReadFile(filepath.Base(record.Path), maxReviewFeedbackBytes)
			if err == nil && !bytes.Equal(data, record.Before) && !bytes.Equal(data, record.After) {
				err = errors.New("private execution record changed during repository rename recovery")
			}
			if err == nil && !bytes.Equal(data, record.After) {
				err = directory.ReplaceFile(filepath.Base(record.Path), record.After, 0o600, state)
			}
			closeErr := directory.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
		}
		return s.validateRenameBindings(ctx, plan)
	})
}

// The existing offline operator guard owns worker, planning, mutation,
// verification/review and process-ownership exclusion. Hold admission and every
// configured slot as well, including standalone work outside this batch.
func (s *Engine) acquireRenameAdmission() (func(), error) {
	project := s.cfg.GitHubProject.GitHubProjectConfig
	var locks []*github.ProcessLock
	release := func() {
		for i := len(locks) - 1; i >= 0; i-- {
			_ = locks[i].Release()
		}
	}
	admission, err := github.AcquireAdmissionLock(project)
	if err != nil {
		return nil, err
	}
	locks = append(locks, admission)
	for range s.maxParallelism() {
		lock, err := github.AcquireExecutionSlot(project, s.maxParallelism())
		if err != nil {
			release()
			return nil, fmt.Errorf("repository rename recovery requires stopped, idle Runner operations: %w", err)
		}
		locks = append(locks, lock)
	}
	return release, nil
}

func readRenamePrivateFile(path string, maximum int64) ([]byte, error) {
	data, mode, state, err := securefs.ReadFile(path, maximum)
	if errors.Is(err, os.ErrNotExist) || err == nil && !state.Exists {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if mode.Perm() != 0o600 {
		return nil, fmt.Errorf("private rename recovery input must have mode 0600: %s", path)
	}
	if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return nil, err
	}
	return data, nil
}
