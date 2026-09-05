package engine

import (
	"bytes"
	"crypto/sha256"
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
)

const (
	reviewFeedbackVersion   = 1
	maxReviewFeedbackBytes  = 1024 * 1024
	maxReviewFeedbackItems  = 20
	maxReviewFeedbackLength = 2_000
	reviewFeedbackDelimiter = "---"
)

type reviewFeedbackRecord struct {
	Baseline               *execution.ReviewBaseline `json:"baseline,omitempty"`
	Version                int                       `json:"version"`
	ItemID                 string                    `json:"item_id"`
	DelegatedContentDigest string                    `json:"delegated_content_digest"`
	Items                  []string                  `json:"items"`
}

func (s *Engine) reviewFeedbackPath(itemID string) string {
	return filepath.Join(
		s.implementationWorkspaceRoot(),
		".runner-state",
		"qa-feedback",
		"review_"+safeRefComponent(itemID)+".json",
	)
}

func (s *Engine) saveReviewFeedback(item github.WorkItem, content github.DelegatedContent, assessment execution.ReviewAssessment, baseline *execution.ReviewBaseline) error {
	items := actionableReviewFeedback(assessment)
	if len(items) == 0 {
		return errors.New("Agent QA requested changes without actionable feedback")
	}
	record := reviewFeedbackRecord{
		Version: reviewFeedbackVersion, ItemID: strings.TrimSpace(item.ID),
		DelegatedContentDigest: strings.TrimSpace(content.Digest), Items: items,
	}
	if baseline != nil {
		record.Baseline = baseline
		record.Baseline.Assessment = assessment
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode Agent QA feedback: %w", err)
	}
	if len(encoded) > maxReviewFeedbackBytes {
		return errors.New("encoded Agent QA feedback exceeds the private storage limit")
	}
	path := s.reviewFeedbackPath(item.ID)
	if err := securefs.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("prepare private Agent QA feedback directory: %w", err)
	}
	directory, err := securefs.OpenDir(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open private Agent QA feedback directory: %w", err)
	}
	defer directory.Close()
	_, _, state, err := directory.ReadFile(filepath.Base(path), maxReviewFeedbackBytes)
	if err != nil {
		return fmt.Errorf("inspect existing Agent QA feedback: %w", err)
	}
	if err := directory.ReplaceFile(filepath.Base(path), append(encoded, '\n'), 0o600, state); err != nil {
		return fmt.Errorf("write private Agent QA feedback: %w", err)
	}
	return nil
}

func (s *Engine) loadReviewFeedback(item github.WorkItem, content github.DelegatedContent) ([]string, error) {
	record, err := s.loadReviewFeedbackRecord(item, content)
	if err != nil || record == nil {
		return nil, err
	}
	return record.Items, nil
}

func (s *Engine) loadReviewFeedbackRecord(item github.WorkItem, content github.DelegatedContent) (*reviewFeedbackRecord, error) {
	path := s.reviewFeedbackPath(item.ID)
	encoded, mode, state, err := securefs.ReadFile(path, maxReviewFeedbackBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read private Agent QA feedback: %w", err)
	}
	if !state.Exists {
		return nil, nil
	}
	if mode.Perm() != 0o600 {
		return nil, fmt.Errorf("private Agent QA feedback mode is %04o, want 0600", mode.Perm())
	}
	if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return nil, fmt.Errorf("validate private Agent QA feedback: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var record reviewFeedbackRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf("decode private Agent QA feedback: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode private Agent QA feedback: %w", err)
	}
	if record.Version != reviewFeedbackVersion || strings.TrimSpace(record.ItemID) != strings.TrimSpace(item.ID) {
		return nil, errors.New("private Agent QA feedback identity does not match this item")
	}
	if record.DelegatedContentDigest != strings.TrimSpace(content.Digest) {
		if err := s.clearReviewFeedback(item.ID); err != nil {
			return nil, fmt.Errorf("remove stale Agent QA feedback: %w", err)
		}
		return nil, nil
	}
	if len(record.Items) == 0 || len(record.Items) > maxReviewFeedbackItems {
		return nil, errors.New("private Agent QA feedback contains an invalid item count")
	}
	for index := range record.Items {
		record.Items[index] = strings.TrimSpace(record.Items[index])
		if record.Items[index] == "" || len(record.Items[index]) > maxReviewFeedbackLength || strings.Contains(record.Items[index], reviewFeedbackDelimiter) {
			return nil, fmt.Errorf("private Agent QA feedback item %d is invalid", index)
		}
	}
	return &record, nil
}

func (s *Engine) clearReviewFeedback(itemID string) error {
	err := securefs.RemoveFile(s.reviewFeedbackPath(itemID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func actionableReviewFeedback(assessment execution.ReviewAssessment) []string {
	items := make([]string, 0, maxReviewFeedbackItems)
	add := func(label, summary string, evidence []string) {
		if len(items) >= maxReviewFeedbackItems {
			return
		}
		value := strings.TrimSpace(label) + ": " + strings.TrimSpace(summary)
		if evidence = compactNonEmpty(evidence); len(evidence) > 0 {
			value += " Evidence: " + strings.Join(evidence, "; ")
		}
		value = strings.Join(strings.Fields(value), " ")
		value = strings.ReplaceAll(value, reviewFeedbackDelimiter, "—")
		if len(value) > maxReviewFeedbackLength {
			value = value[:maxReviewFeedbackLength]
		}
		if strings.TrimSpace(value) != "" {
			items = append(items, value)
		}
	}
	for _, criterion := range assessment.Criteria {
		if criterion.Status == "failed" {
			add("Failed criterion "+strings.TrimSpace(criterion.Criterion), criterion.Summary, criterion.Evidence)
		}
	}
	for _, rule := range assessment.Rules {
		for _, finding := range rule.Findings {
			if finding.Severity == "blocking" {
				add("Blocking repository-rule finding", finding.Summary, finding.Evidence)
			}
		}
	}
	if assessment.Maintainability.Status == "failed" {
		add("Failed maintainability check", assessment.Maintainability.Summary, assessment.Maintainability.Evidence)
	}
	if len(items) == 0 && strings.TrimSpace(assessment.Summary) != "" {
		add("Agent QA summary", assessment.Summary, nil)
	}
	return items
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

// Comments and proof obligations can change without changing the card body.
func reviewContextDigest(spec execution.Spec, comments []string) string {
	data, _ := json.Marshal(struct {
		Repository, Content string
		Proof, Comments     []string
	}{spec.Repository, spec.DelegatedContentDigest, spec.RequiredVerification, compactNonEmpty(comments)})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func matchingReviewBaseline(record *reviewFeedbackRecord, base, contextDigest string) *execution.ReviewBaseline {
	if record == nil || record.Baseline == nil {
		return nil
	}
	baseline := record.Baseline
	if baseline.BaseOID != base || baseline.ContextDigest != contextDigest || !reviewObjectID(baseline.CommitOID) {
		return nil
	}
	return baseline
}

func reviewObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
