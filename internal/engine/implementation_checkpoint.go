package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/workspace"
)

const (
	implementationCheckpointVersion  = 1
	maxImplementationCheckpointBytes = 1024 * 1024
)

var errImplementationCheckpointChanged = errors.New("implementation checkpoint changed; inspect retained work before an explicit retry")

type implementationCheckpointRecord struct {
	Version                int       `json:"version"`
	ItemID                 string    `json:"item_id"`
	DelegatedContentDigest string    `json:"delegated_content_digest"`
	ContextDigest          string    `json:"context_digest"`
	Repository             string    `json:"repository"`
	Branch                 string    `json:"branch"`
	BaseRevision           string    `json:"base_revision"`
	WorktreePath           string    `json:"worktree_path"`
	SnapshotFingerprint    string    `json:"snapshot_fingerprint"`
	CandidateCommitOID     string    `json:"candidate_commit_oid,omitempty"`
	CandidateTreeOID       string    `json:"candidate_tree_oid,omitempty"`
	Summary                string    `json:"summary"`
	WorkDone               []string  `json:"work_done"`
	Verification           []string  `json:"verification"`
	ExecutionDeadline      time.Time `json:"execution_deadline,omitzero"`
	CorrectionUsed         bool      `json:"correction_used,omitempty"`
	Incomplete             bool      `json:"incomplete,omitempty"`
	Blocker                string    `json:"blocker,omitempty"`
}

type implementationCheckpoint struct {
	Output            execution.Output
	Candidate         workspace.Candidate
	ExecutionDeadline time.Time
	CorrectionUsed    bool
}

func (s *Engine) implementationCheckpointPath(itemID string) string {
	return filepath.Join(s.implementationWorkspaceRoot(), ".runner-state", "implementation", "implementation_"+safeRefComponent(itemID)+".json")
}

func implementationContextDigest(content github.DelegatedContent, item github.WorkItem, reviewFeedback, comments, criteria []string, delivery ...execution.Spec) string {
	payload := struct {
		Version                int                            `json:"version"`
		DelegatedContentDigest string                         `json:"delegated_content_digest"`
		PullRequest            string                         `json:"pull_request,omitempty"`
		ReviewFeedback         []string                       `json:"review_feedback"`
		HumanComments          []string                       `json:"human_comments"`
		Criteria               []string                       `json:"criteria"`
		Plan                   *execution.PlanContext         `json:"plan,omitempty"`
		ReviewScope            execution.ReviewScope          `json:"review_scope,omitempty"`
		VerificationBoundary   execution.VerificationBoundary `json:"verification_boundary,omitempty"`
	}{
		Version: implementationCheckpointVersion, DelegatedContentDigest: strings.TrimSpace(content.Digest),
		PullRequest: strings.TrimSpace(item.PullRequest), ReviewFeedback: compactNonEmpty(reviewFeedback),
		HumanComments: compactNonEmpty(comments), Criteria: compactNonEmpty(criteria),
	}
	if len(delivery) > 0 {
		payload.Plan, payload.ReviewScope, payload.VerificationBoundary = delivery[0].PlanContext, delivery[0].ReviewScope, delivery[0].VerificationBoundary
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// Retain both the original deadline and the spent allowance through successful
// post-processing: restarting cannot renew either before the QA handoff.
func (s *Engine) saveImplementationCheckpoint(item github.WorkItem, content github.DelegatedContent, contextDigest string, metadata workspace.Metadata, snapshot workspace.Snapshot, candidate workspace.Candidate, output execution.Output, deadline time.Time, correctionUsed bool) error {
	record := implementationCheckpointRecord{
		Version: implementationCheckpointVersion, ItemID: strings.TrimSpace(item.ID), DelegatedContentDigest: strings.TrimSpace(content.Digest),
		ContextDigest: strings.TrimSpace(contextDigest), Repository: strings.TrimSpace(metadata.Identity.Repository),
		Branch: strings.TrimSpace(metadata.BranchName), BaseRevision: strings.TrimSpace(metadata.BaseRevision),
		WorktreePath: strings.TrimSpace(metadata.WorktreePath), SnapshotFingerprint: strings.TrimSpace(snapshot.Fingerprint),
		CandidateCommitOID: strings.TrimSpace(candidate.CommitOID), CandidateTreeOID: strings.TrimSpace(candidate.TreeOID),
		Summary: strings.TrimSpace(output.Summary), WorkDone: append([]string(nil), output.WorkDone...), Verification: append([]string(nil), output.Verification...),
		ExecutionDeadline: deadline, CorrectionUsed: correctionUsed, Incomplete: output.Outcome != execution.OutcomeSucceeded,
	}
	if record.Incomplete && output.Blocker != nil {
		record.Blocker = *output.Blocker
	}
	if err := validateImplementationCheckpointRecord(record); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode implementation checkpoint: %w", err)
	}
	if len(encoded)+1 > maxImplementationCheckpointBytes {
		return errors.New("encoded implementation checkpoint exceeds the private storage limit")
	}
	path := s.implementationCheckpointPath(item.ID)
	if err := securefs.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("prepare private implementation checkpoint directory: %w", err)
	}
	directory, err := securefs.OpenDir(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open private implementation checkpoint directory: %w", err)
	}
	defer directory.Close()
	_, _, state, err := directory.ReadFile(filepath.Base(path), maxImplementationCheckpointBytes)
	if err != nil {
		return fmt.Errorf("inspect existing implementation checkpoint: %w", err)
	}
	if err := directory.ReplaceFile(filepath.Base(path), append(encoded, '\n'), 0o600, state); err != nil {
		return fmt.Errorf("write private implementation checkpoint: %w", err)
	}
	return nil
}

func (s *Engine) loadImplementationCheckpoint(item github.WorkItem, content github.DelegatedContent, contextDigest string, metadata workspace.Metadata, snapshot workspace.Snapshot, criteria []string) (implementationCheckpoint, bool, error) {
	record, err := s.readImplementationCheckpoint(item.ID)
	if err != nil || record == nil {
		return implementationCheckpoint{}, false, err
	}
	output := execution.Output{
		Outcome: execution.OutcomeSucceeded, Summary: record.Summary,
		WorkDone: append([]string(nil), record.WorkDone...), Verification: append([]string(nil), record.Verification...),
	}
	if record.Incomplete {
		// A crash may have left arbitrary partial edits from the correction.
		// This record only stops another call; it never authorizes resumption or
		// attests that its historical evidence covers the current workspace.
		output.Outcome = execution.OutcomeRepairNeeded
		output.Blocker = &record.Blocker
		return implementationCheckpoint{Output: output, ExecutionDeadline: record.ExecutionDeadline, CorrectionUsed: record.CorrectionUsed}, true, nil
	}
	stale := record.ItemID != strings.TrimSpace(item.ID) || record.DelegatedContentDigest != strings.TrimSpace(content.Digest) ||
		record.ContextDigest != strings.TrimSpace(contextDigest) || record.Repository != strings.TrimSpace(metadata.Identity.Repository) ||
		record.Branch != strings.TrimSpace(metadata.BranchName) || record.BaseRevision != strings.TrimSpace(metadata.BaseRevision) ||
		record.WorktreePath != strings.TrimSpace(metadata.WorktreePath) || record.SnapshotFingerprint != strings.TrimSpace(snapshot.Fingerprint) ||
		record.Branch != strings.TrimSpace(snapshot.Branch) ||
		len(record.Verification) != len(criteria)
	if stale {
		if !record.ExecutionDeadline.IsZero() {
			return implementationCheckpoint{}, false, errImplementationCheckpointChanged
		}
		if err := s.clearImplementationCheckpoint(item.ID); err != nil {
			return implementationCheckpoint{}, false, fmt.Errorf("remove stale implementation checkpoint: %w", err)
		}
		return implementationCheckpoint{}, false, nil
	}
	if _, err := verificationEvidenceEntries(criteria, record.Verification); err != nil {
		return implementationCheckpoint{}, false, fmt.Errorf("validate implementation checkpoint evidence: %w", err)
	}
	if record.CandidateCommitOID != "" && (!snapshot.Clean || snapshot.Head != record.CandidateCommitOID || snapshot.Tree != record.CandidateTreeOID) {
		return implementationCheckpoint{}, false, errors.New("private implementation checkpoint candidate does not match the current clean workspace")
	}
	return implementationCheckpoint{Output: output, Candidate: workspace.Candidate{CommitOID: record.CandidateCommitOID, TreeOID: record.CandidateTreeOID}, ExecutionDeadline: record.ExecutionDeadline, CorrectionUsed: record.CorrectionUsed}, true, nil
}

func (s *Engine) readImplementationCheckpoint(itemID string) (*implementationCheckpointRecord, error) {
	path := s.implementationCheckpointPath(itemID)
	encoded, mode, state, err := securefs.ReadFile(path, maxImplementationCheckpointBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read private implementation checkpoint: %w", err)
	}
	if !state.Exists {
		return nil, nil
	}
	if mode.Perm() != 0o600 {
		return nil, fmt.Errorf("private implementation checkpoint mode is %04o, want 0600", mode.Perm())
	}
	if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return nil, fmt.Errorf("validate private implementation checkpoint: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var record implementationCheckpointRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf("decode private implementation checkpoint: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode private implementation checkpoint: trailing data")
	}
	if err := validateImplementationCheckpointRecord(record); err != nil {
		return nil, err
	}
	if record.ItemID != strings.TrimSpace(itemID) {
		return nil, errors.New("private implementation checkpoint belongs to a different item")
	}
	return &record, nil
}

func validateImplementationCheckpointRecord(record implementationCheckpointRecord) error {
	if record.Version != implementationCheckpointVersion || record.ItemID == "" || record.DelegatedContentDigest == "" || record.ContextDigest == "" ||
		record.Repository == "" || record.Branch == "" || record.BaseRevision == "" || record.WorktreePath == "" || record.SnapshotFingerprint == "" ||
		record.Summary == "" || len(record.WorkDone) == 0 || len(record.Verification) == 0 ||
		(record.CandidateCommitOID == "") != (record.CandidateTreeOID == "") {
		return errors.New("private implementation checkpoint has an invalid identity or result")
	}
	if record.CorrectionUsed && record.ExecutionDeadline.IsZero() ||
		record.Incomplete && (!record.CorrectionUsed || record.Blocker == "" || record.CandidateCommitOID != "") ||
		!record.Incomplete && record.Blocker != "" {
		return errors.New("private implementation checkpoint has an invalid correction state")
	}
	for _, objectID := range []string{record.CandidateCommitOID, record.CandidateTreeOID} {
		if objectID == "" {
			continue
		}
		decoded, err := hex.DecodeString(objectID)
		if err != nil || len(decoded) != 20 && len(decoded) != 32 || strings.ToLower(objectID) != objectID {
			return errors.New("private implementation checkpoint contains an invalid candidate object ID")
		}
	}
	for _, value := range append(append([]string(nil), record.WorkDone...), record.Verification...) {
		if strings.TrimSpace(value) == "" {
			return errors.New("private implementation checkpoint contains an empty result entry")
		}
	}
	return nil
}

func (s *Engine) clearImplementationCheckpoint(itemID string) error {
	err := securefs.RemoveFile(s.implementationCheckpointPath(itemID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
