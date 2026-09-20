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
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/securefs"
)

const (
	reviewFeedbackVersion   = 1
	maxReviewFeedbackBytes  = 1024 * 1024
	reviewFeedbackDelimiter = "---"
)

var errReviewFeedbackLimit = errors.New("Agent QA feedback exceeds the 1 MiB safety limit; no feedback was truncated or replaced")

type reviewFeedbackRecord struct {
	Baseline               *execution.ReviewBaseline `json:"baseline,omitempty"`
	Version                int                       `json:"version"`
	ItemID                 string                    `json:"item_id"`
	DelegatedContentDigest string                    `json:"delegated_content_digest"`
	Items                  []string                  `json:"items"`
}

func (record *reviewFeedbackRecord) UnmarshalJSON(data []byte) error {
	var stored struct {
		Baseline               json.RawMessage `json:"baseline"`
		Version                int             `json:"version"`
		ItemID                 string          `json:"item_id"`
		DelegatedContentDigest string          `json:"delegated_content_digest"`
		Items                  []string        `json:"items"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	*record = reviewFeedbackRecord{
		Version: stored.Version, ItemID: stored.ItemID,
		DelegatedContentDigest: stored.DelegatedContentDigest, Items: stored.Items,
	}
	if len(stored.Baseline) == 0 || bytes.Equal(bytes.TrimSpace(stored.Baseline), []byte("null")) {
		return nil
	}
	baselineDecoder := json.NewDecoder(bytes.NewReader(stored.Baseline))
	baselineDecoder.DisallowUnknownFields()
	var baseline execution.ReviewBaseline
	if err := baselineDecoder.Decode(&baseline); err != nil {
		// Invalid baseline evidence cannot prevent use of the separately bounded
		// actionable feedback. Omitting it forces a renewed cumulative review.
		return nil
	}
	if err := ensureJSONEOF(baselineDecoder); err != nil {
		return nil
	}
	record.Baseline = &baseline
	return nil
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
	items, err := actionableReviewFeedback(assessment)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return errors.New("Agent QA requested changes without actionable feedback")
	}
	record := reviewFeedbackRecord{
		Version: reviewFeedbackVersion, ItemID: strings.TrimSpace(item.ID),
		DelegatedContentDigest: strings.TrimSpace(content.Digest), Items: items,
	}
	if baseline != nil {
		copy := *baseline
		record.Baseline = &copy
		record.Baseline.Assessment = assessment
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode Agent QA feedback: %w", err)
	}
	if len(encoded)+1 > maxReviewFeedbackBytes {
		return errReviewFeedbackLimit
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
	record, err := s.readReviewFeedbackRecord(item)
	if err != nil || record == nil {
		return nil, err
	}
	if record.DelegatedContentDigest != strings.TrimSpace(content.Digest) {
		// An operator amendment renews the contract, not the historical verdict.
		// Keep the old record intact without presenting it as current proof.
		return nil, nil
	}
	return record, nil
}

// readReviewFeedbackRecord never discards history during an operator preview.
func (s *Engine) readReviewFeedbackRecord(item github.WorkItem) (*reviewFeedbackRecord, error) {
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
	if !utf8.Valid(encoded) {
		return nil, errors.New("private Agent QA feedback is not valid UTF-8")
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
	if err := validateReviewFeedbackItems(record.Items); err != nil {
		return nil, err
	}
	if record.Baseline != nil {
		// The complete assessment is the source for actionable feedback. This
		// also recovers historical byte-clipped items without rewriting their
		// record. Validate its shape here, not its applicability to a new task
		// or candidate: content binding and review reuse have separate gates.
		var spec execution.Spec
		for _, criterion := range record.Baseline.Assessment.Criteria {
			spec.RequiredVerification = append(spec.RequiredVerification, criterion.Criterion)
		}
		if execution.ValidateReviewBaseline(spec, record.Baseline) == nil {
			items, err := actionableReviewFeedback(record.Baseline.Assessment)
			if err != nil {
				return nil, err
			}
			record.Items = items
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

func actionableReviewFeedback(assessment execution.ReviewAssessment) ([]string, error) {
	var items []string
	add := func(label, summary string, evidence []string) {
		value := strings.TrimSpace(label) + ": " + strings.TrimSpace(summary)
		if evidence = compactNonEmpty(evidence); len(evidence) > 0 {
			value += "\nEvidence:\n- " + strings.Join(evidence, "\n- ")
		}
		value = strings.ReplaceAll(value, reviewFeedbackDelimiter, "—")
		if strings.TrimSpace(value) != "" {
			items = append(items, value)
		}
	}
	for _, criterion := range assessment.Criteria {
		if criterion.Status == "failed" {
			add("Failed criterion "+strings.TrimSpace(criterion.Criterion), criterion.Summary, criterion.Evidence)
		} else if criterion.Status == "blocked" {
			add("Blocked criterion "+strings.TrimSpace(criterion.Criterion), criterion.Summary, criterion.Evidence)
		}
	}
	for _, rule := range assessment.Rules {
		if rule.Status == "blocked" {
			add("Blocked repository-rule check", rule.Summary, nil)
		}
		for _, finding := range rule.Findings {
			if finding.Severity == "blocking" {
				add("Blocking repository-rule finding", finding.Summary, finding.Evidence)
			}
		}
	}
	if assessment.Maintainability.Status == "failed" {
		add("Failed maintainability check", assessment.Maintainability.Summary, assessment.Maintainability.Evidence)
	} else if assessment.Maintainability.Status == "blocked" {
		add("Blocked maintainability check", assessment.Maintainability.Summary, assessment.Maintainability.Evidence)
	}
	if len(items) == 0 && strings.TrimSpace(assessment.Summary) != "" {
		add("Agent QA summary", assessment.Summary, nil)
	}
	if len(items) == 0 {
		return nil, nil
	}
	return items, validateReviewFeedbackItems(items)
}

func validateReviewFeedbackItems(items []string) error {
	if len(items) == 0 {
		return errors.New("private Agent QA feedback contains no actionable items")
	}
	totalBytes := 0
	for index, item := range items {
		if strings.TrimSpace(item) == "" || !utf8.ValidString(item) || strings.Contains(item, reviewFeedbackDelimiter) {
			return fmt.Errorf("private Agent QA feedback item %d is invalid", index)
		}
		// Include the separators used when passing findings to an assignment.
		if len(item)+3 > maxReviewFeedbackBytes-totalBytes {
			return errReviewFeedbackLimit
		}
		totalBytes += len(item) + 3
	}
	return nil
}

func reviewFeedbackFailureOutput(summary string, err error, reviewed ...execution.Output) execution.Output {
	output := integrityViolationOutput(summary, err, reviewed...)
	if errors.Is(err, errReviewFeedbackLimit) {
		output.FailureClass = execution.FailureInvalidContract
		output.Summary = errReviewFeedbackLimit.Error()
		output.Blocker = stringPtr(errReviewFeedbackLimit.Error())
	}
	return output
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

// The baseline binding covers immutable review inputs. Comment context is kept
// separately on the baseline so the reviewer can compare changes without
// treating every coordination update as a new task contract.
func reviewBaselineBindingDigest(spec execution.Spec) string {
	data, _ := json.Marshal(struct {
		Repository, Content string
		Proof               []string
	}{spec.Repository, spec.DelegatedContentDigest, spec.RequiredVerification})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func matchingReviewBaseline(record *reviewFeedbackRecord, spec execution.Spec, base, bindingDigest string) *execution.ReviewBaseline {
	if record == nil || record.Baseline == nil {
		return nil
	}
	baseline := record.Baseline
	if baseline.BaseOID != base || baseline.BindingDigest != bindingDigest || !reviewObjectID(baseline.CommitOID) || execution.ValidateReviewBaseline(spec, baseline) != nil {
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
