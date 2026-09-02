package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/securefs"
)

const (
	plannerCheckpointVersion  = 1
	maxPlannerCheckpointBytes = 2 * 1024 * 1024
)

type plannerCheckpointRecord struct {
	Version                int         `json:"version"`
	SourceID               string      `json:"source_id"`
	DelegatedContentDigest string      `json:"delegated_content_digest"`
	ContextDigest          string      `json:"context_digest"`
	SourceLane             string      `json:"source_lane"`
	Destination            string      `json:"destination"`
	BatchFingerprint       string      `json:"batch_fingerprint"`
	SourceContext          string      `json:"source_context"`
	Plan                   ProjectPlan `json:"plan"`
}

func (s *Engine) plannerCheckpointPath(itemID string) string {
	return filepath.Join(s.implementationWorkspaceRoot(), ".runner-state", "planning", "planning_"+safeRefComponent(itemID)+".json")
}

func plannerCheckpointContextDigest(item github.WorkItem, content github.DelegatedContent, role, sourceLane, destination, repository, sourceContext string) string {
	payload := struct {
		Version                int    `json:"version"`
		SourceID               string `json:"source_id"`
		DelegatedContentDigest string `json:"delegated_content_digest"`
		Role                   string `json:"role"`
		SourceLane             string `json:"source_lane"`
		Destination            string `json:"destination"`
		Repository             string `json:"repository"`
		SourceContext          string `json:"source_context"`
	}{
		Version: plannerCheckpointVersion, SourceID: strings.TrimSpace(item.ID), DelegatedContentDigest: strings.TrimSpace(content.Digest),
		Role: strings.TrimSpace(role), SourceLane: strings.TrimSpace(sourceLane), Destination: strings.TrimSpace(destination),
		Repository: strings.TrimSpace(repository), SourceContext: strings.TrimSpace(sourceContext),
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (s *Engine) savePlannerCheckpoint(item github.WorkItem, content github.DelegatedContent, contextDigest, sourceLane, destination string, plan ProjectPlan) error {
	fingerprint, err := planningBatchFingerprint(item.ID, sourceLane, destination, plan)
	if err != nil {
		return err
	}
	record := plannerCheckpointRecord{
		Version: plannerCheckpointVersion, SourceID: strings.TrimSpace(item.ID), DelegatedContentDigest: strings.TrimSpace(content.Digest),
		ContextDigest: strings.TrimSpace(contextDigest), SourceLane: strings.TrimSpace(sourceLane), Destination: strings.TrimSpace(destination),
		BatchFingerprint: fingerprint, SourceContext: strings.TrimSpace(plan.SourceContext), Plan: plan,
	}
	if _, err := validatePlannerCheckpointRecord(record); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode planner checkpoint: %w", err)
	}
	if len(encoded) > maxPlannerCheckpointBytes {
		return errors.New("encoded planner checkpoint exceeds the private storage limit")
	}
	path := s.plannerCheckpointPath(item.ID)
	if err := securefs.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("prepare private planner checkpoint directory: %w", err)
	}
	directory, err := securefs.OpenDir(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open private planner checkpoint directory: %w", err)
	}
	defer directory.Close()
	_, _, state, err := directory.ReadFile(filepath.Base(path), maxPlannerCheckpointBytes)
	if err != nil {
		return fmt.Errorf("inspect existing planner checkpoint: %w", err)
	}
	if err := directory.ReplaceFile(filepath.Base(path), append(encoded, '\n'), 0o600, state); err != nil {
		return fmt.Errorf("write private planner checkpoint: %w", err)
	}
	return nil
}

func (s *Engine) loadPlannerCheckpoint(item github.WorkItem, content github.DelegatedContent, contextDigest string) (ProjectPlan, bool, error) {
	path := s.plannerCheckpointPath(item.ID)
	encoded, mode, state, err := securefs.ReadFile(path, maxPlannerCheckpointBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProjectPlan{}, false, nil
		}
		return ProjectPlan{}, false, fmt.Errorf("read private planner checkpoint: %w", err)
	}
	if !state.Exists {
		return ProjectPlan{}, false, nil
	}
	if mode.Perm() != 0o600 {
		return ProjectPlan{}, false, fmt.Errorf("private planner checkpoint mode is %04o, want 0600", mode.Perm())
	}
	if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return ProjectPlan{}, false, fmt.Errorf("validate private planner checkpoint: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var record plannerCheckpointRecord
	if err := decoder.Decode(&record); err != nil {
		return ProjectPlan{}, false, fmt.Errorf("decode private planner checkpoint: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ProjectPlan{}, false, fmt.Errorf("decode private planner checkpoint: %w", err)
	}
	plan, err := validatePlannerCheckpointRecord(record)
	if err != nil {
		return ProjectPlan{}, false, fmt.Errorf("validate private planner checkpoint: %w", err)
	}
	stale := record.SourceID != strings.TrimSpace(item.ID) || record.DelegatedContentDigest != strings.TrimSpace(content.Digest) ||
		record.ContextDigest != strings.TrimSpace(contextDigest)
	if stale {
		if err := s.clearPlannerCheckpoint(item.ID); err != nil {
			return ProjectPlan{}, false, fmt.Errorf("remove stale planner checkpoint: %w", err)
		}
		return ProjectPlan{}, false, nil
	}
	return plan, true, nil
}

func validatePlannerCheckpointRecord(record plannerCheckpointRecord) (ProjectPlan, error) {
	if record.Version != plannerCheckpointVersion || strings.TrimSpace(record.SourceID) == "" || strings.TrimSpace(record.DelegatedContentDigest) == "" ||
		strings.TrimSpace(record.ContextDigest) == "" || strings.TrimSpace(record.SourceLane) == "" || strings.TrimSpace(record.Destination) == "" ||
		strings.TrimSpace(record.BatchFingerprint) == "" || strings.TrimSpace(record.SourceContext) == "" {
		return ProjectPlan{}, errors.New("private planner checkpoint has an invalid identity")
	}
	plan := record.Plan
	plan.SourceContext = strings.TrimSpace(record.SourceContext)
	normalized, err := normalizeProjectPlan(plan)
	if err != nil {
		return ProjectPlan{}, fmt.Errorf("private planner checkpoint contains an invalid plan: %w", err)
	}
	if len(normalized.OpenDecisions) > 0 || !reflect.DeepEqual(normalized, plan) {
		return ProjectPlan{}, errors.New("private planner checkpoint plan is not a canonical executable plan")
	}
	fingerprint, err := planningBatchFingerprint(record.SourceID, record.SourceLane, record.Destination, normalized)
	if err != nil {
		return ProjectPlan{}, err
	}
	if fingerprint != strings.TrimSpace(record.BatchFingerprint) {
		return ProjectPlan{}, errors.New("private planner checkpoint batch fingerprint does not match its exact plan")
	}
	return normalized, nil
}

func (s *Engine) clearPlannerCheckpoint(itemID string) error {
	err := securefs.RemoveFile(s.plannerCheckpointPath(itemID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
