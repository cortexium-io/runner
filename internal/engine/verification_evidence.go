package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/workspace"
)

const (
	verificationEvidenceVersion   = 1
	maxVerificationEvidenceBytes  = 1024 * 1024
	maxVerificationEvidenceItems  = 1000
	maxVerificationEvidenceLength = 8 * 1024
)

type verificationEvidenceRecord struct {
	Version                int                              `json:"version"`
	ItemID                 string                           `json:"item_id"`
	DelegatedContentDigest string                           `json:"delegated_content_digest"`
	Repository             string                           `json:"repository"`
	Branch                 string                           `json:"branch"`
	CommitOID              string                           `json:"commit_oid"`
	TreeOID                string                           `json:"tree_oid"`
	Entries                []execution.VerificationEvidence `json:"entries"`
}

func (s *Engine) verificationEvidencePath(itemID string) string {
	return filepath.Join(s.implementationWorkspaceRoot(), ".runner-state", "verification", "verification_"+safeRefComponent(itemID)+".json")
}

func (s *Engine) saveVerificationEvidence(item github.WorkItem, content github.DelegatedContent, metadata workspace.Metadata, candidate workspace.Candidate, criteria, evidence []string) error {
	entries, err := verificationEvidenceEntries(criteria, evidence)
	if err != nil {
		return err
	}
	record := verificationEvidenceRecord{
		Version: verificationEvidenceVersion, ItemID: strings.TrimSpace(item.ID), DelegatedContentDigest: strings.TrimSpace(content.Digest),
		Repository: strings.TrimSpace(metadata.Identity.Repository), Branch: strings.TrimSpace(metadata.BranchName),
		CommitOID: strings.TrimSpace(candidate.CommitOID), TreeOID: strings.TrimSpace(candidate.TreeOID), Entries: entries,
	}
	if err := validateVerificationEvidenceRecord(record); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode verification evidence: %w", err)
	}
	if len(encoded) > maxVerificationEvidenceBytes {
		return errors.New("encoded verification evidence exceeds the private storage limit")
	}
	path := s.verificationEvidencePath(item.ID)
	if err := securefs.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("prepare private verification evidence directory: %w", err)
	}
	directory, err := securefs.OpenDir(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open private verification evidence directory: %w", err)
	}
	defer directory.Close()
	_, _, state, err := directory.ReadFile(filepath.Base(path), maxVerificationEvidenceBytes)
	if err != nil {
		return fmt.Errorf("inspect existing verification evidence: %w", err)
	}
	if err := directory.ReplaceFile(filepath.Base(path), append(encoded, '\n'), 0o600, state); err != nil {
		return fmt.Errorf("write private verification evidence: %w", err)
	}
	return nil
}

func (s *Engine) loadVerificationEvidence(item github.WorkItem, content github.DelegatedContent, metadata workspace.Metadata, candidate workspace.Candidate, criteria []string) ([]execution.VerificationEvidence, error) {
	path := s.verificationEvidencePath(item.ID)
	encoded, mode, state, err := securefs.ReadFile(path, maxVerificationEvidenceBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read private verification evidence: %w", err)
	}
	if !state.Exists {
		return nil, nil
	}
	if mode.Perm() != 0o600 {
		return nil, fmt.Errorf("private verification evidence mode is %04o, want 0600", mode.Perm())
	}
	if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return nil, fmt.Errorf("validate private verification evidence: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var record verificationEvidenceRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf("decode private verification evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode private verification evidence: trailing data")
	}
	if err := validateVerificationEvidenceRecord(record); err != nil {
		return nil, err
	}
	if record.ItemID != strings.TrimSpace(item.ID) || record.DelegatedContentDigest != strings.TrimSpace(content.Digest) ||
		record.Repository != strings.TrimSpace(metadata.Identity.Repository) || record.Branch != strings.TrimSpace(metadata.BranchName) ||
		record.CommitOID != strings.TrimSpace(candidate.CommitOID) || record.TreeOID != strings.TrimSpace(candidate.TreeOID) ||
		len(record.Entries) != len(criteria) {
		return nil, errors.New("private verification evidence does not match the approved item, content, workspace, candidate, or criteria")
	}
	for index := range criteria {
		if record.Entries[index].Criterion != strings.TrimSpace(criteria[index]) {
			return nil, errors.New("private verification evidence does not match the approved item, content, workspace, candidate, or criteria")
		}
	}
	return append([]execution.VerificationEvidence(nil), record.Entries...), nil
}

func verificationEvidenceEntries(criteria, evidence []string) ([]execution.VerificationEvidence, error) {
	if len(criteria) == 0 || len(criteria) != len(evidence) || len(criteria) > maxVerificationEvidenceItems {
		return nil, fmt.Errorf("successful implementation must return exactly one verification evidence entry for each of %d approved checks", len(criteria))
	}
	entries := make([]execution.VerificationEvidence, len(criteria))
	for index := range criteria {
		criterion := strings.TrimSpace(criteria[index])
		value := strings.TrimSpace(evidence[index])
		if criterion == "" || value == "" || len(criterion) > maxVerificationEvidenceLength || len(value) > maxVerificationEvidenceLength {
			return nil, fmt.Errorf("verification evidence entry %d is empty or too large", index)
		}
		entries[index] = execution.VerificationEvidence{Criterion: criterion, Evidence: value}
	}
	return entries, nil
}

func validateVerificationEvidenceRecord(record verificationEvidenceRecord) error {
	if record.Version != verificationEvidenceVersion || record.ItemID == "" || record.DelegatedContentDigest == "" || record.Repository == "" || record.Branch == "" || record.CommitOID == "" || record.TreeOID == "" || len(record.Entries) == 0 || len(record.Entries) > maxVerificationEvidenceItems {
		return errors.New("private verification evidence has an invalid identity or entry count")
	}
	for index, entry := range record.Entries {
		if strings.TrimSpace(entry.Criterion) == "" || strings.TrimSpace(entry.Evidence) == "" || len(entry.Criterion) > maxVerificationEvidenceLength || len(entry.Evidence) > maxVerificationEvidenceLength {
			return fmt.Errorf("private verification evidence entry %d is invalid", index)
		}
	}
	return nil
}
