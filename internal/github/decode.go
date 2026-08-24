package github

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cortexium-io/runner/internal/subprocess"
)

// PlanningSourceFingerprint binds a staged batch to the immutable planning
// request without depending on mutable lifecycle fields, presentation text,
// or provenance URLs used during recovery.
func PlanningSourceFingerprint(item WorkItem) string {
	payload := struct {
		ID                     string `json:"id"`
		DelegatedContentDigest string `json:"delegated_content_digest"`
	}{
		ID: strings.TrimSpace(item.ID), DelegatedContentDigest: DelegatedContentFor(item).Digest,
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("v1:%x", digest[:])
}

func normalizeProjectKey(value string) string {
	var b strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			b.WriteRune(character)
		}
	}
	return b.String()
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func commandFailure(err error, result subprocess.Result) error {
	detail := firstString(result.Stderr, result.Stdout)
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, truncate(detail, 1000))
}

func FormatPlannedItemBody(item PlannedItem) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(item.Summary))
	if strings.TrimSpace(item.ProjectGoal) != "" {
		b.WriteString("\n\n## Project outcome\n\n")
		b.WriteString(strings.TrimSpace(item.ProjectGoal))
	}
	if len(item.ProjectSuccessCriteria) > 0 {
		b.WriteString("\n\n## Project success criteria\n")
		for _, criterion := range item.ProjectSuccessCriteria {
			if strings.TrimSpace(criterion) != "" {
				b.WriteString("- ")
				b.WriteString(strings.TrimSpace(criterion))
				b.WriteByte('\n')
			}
		}
	}
	if len(item.ProjectConstraints) > 0 {
		b.WriteString("\n## Project constraints and non-goals\n")
		for _, constraint := range item.ProjectConstraints {
			if strings.TrimSpace(constraint) != "" {
				b.WriteString("- ")
				b.WriteString(strings.TrimSpace(constraint))
				b.WriteByte('\n')
			}
		}
	}
	if strings.TrimSpace(item.ProjectSource) != "" {
		b.WriteString("\n## Original project request\n\n--- BEGIN ORIGINAL REQUEST ---\n")
		b.WriteString(strings.TrimSpace(item.ProjectSource))
		b.WriteString("\n--- END ORIGINAL REQUEST ---")
	}
	if strings.TrimSpace(item.Repository) != "" {
		b.WriteString("\n\nRepository: ")
		b.WriteString(strings.TrimSpace(item.Repository))
	}
	if len(item.AcceptanceCriteria) > 0 {
		b.WriteString("\n\n## Acceptance criteria\n")
		for _, criterion := range item.AcceptanceCriteria {
			if strings.TrimSpace(criterion) != "" {
				b.WriteString("- [ ] ")
				b.WriteString(strings.TrimSpace(criterion))
				b.WriteByte('\n')
			}
		}
	}
	if len(item.Verification) > 0 {
		b.WriteString("\n## Proof obligations\n")
		for _, check := range item.Verification {
			if strings.TrimSpace(check) != "" {
				b.WriteString("- ")
				b.WriteString(strings.TrimSpace(check))
				b.WriteByte('\n')
			}
		}
	}
	if len(item.Risks) > 0 {
		b.WriteString("\n## Assumptions and risks\n")
		for _, risk := range item.Risks {
			if strings.TrimSpace(risk) != "" {
				b.WriteString("- ")
				b.WriteString(strings.TrimSpace(risk))
				b.WriteByte('\n')
			}
		}
	}
	if len(item.NonGoals) > 0 {
		b.WriteString("\n## Task non-goals\n")
		for _, nonGoal := range item.NonGoals {
			if strings.TrimSpace(nonGoal) != "" {
				b.WriteString("- ")
				b.WriteString(strings.TrimSpace(nonGoal))
				b.WriteByte('\n')
			}
		}
	}
	if len(item.ResolvedDependencies) > 0 {
		b.WriteString("\n## Dependencies\n")
		for _, dependency := range item.ResolvedDependencies {
			if strings.TrimSpace(dependency.ItemID) != "" && strings.TrimSpace(dependency.Title) != "" {
				b.WriteString("- ")
				b.WriteString(strings.TrimSpace(dependency.ItemID))
				b.WriteString(" — ")
				b.WriteString(strings.TrimSpace(dependency.Title))
				b.WriteByte('\n')
			}
		}
	}
	if strings.TrimSpace(item.PlanningBatchFingerprint) == "" {
		return strings.TrimSpace(b.String())
	}
	metadata, _ := json.Marshal(plannedItemMetadataWire{
		Version: 1, Repository: strings.TrimSpace(item.Repository), Dependencies: canonicalDependencies(item.ResolvedDependencies),
		DependencyIDsResolved: item.DependencyIDsResolved,
		PlanningSourceID:      strings.TrimSpace(item.PlanningSourceID), PlanningSourceLane: strings.TrimSpace(item.PlanningSourceLane),
		PlanningSourceFingerprint: strings.TrimSpace(item.PlanningSourceFingerprint), PlanningDestination: strings.TrimSpace(item.PlanningDestination),
		PlanningBatchFingerprint: strings.TrimSpace(item.PlanningBatchFingerprint),
		PlanningBatchSize:        item.PlanningBatchSize, PlanningItemIndex: item.PlanningItemIndex,
	})
	b.WriteString("\n\n## Runner planning metadata\n\n")
	b.WriteString("This operator-visible metadata is authenticated with the staged batch and is not editable scheduling input.\n\n```json\n")
	b.Write(metadata)
	b.WriteString("\n```")
	return strings.TrimSpace(b.String())
}

type PlannedItemMetadata struct {
	Repository                string   `json:"repository,omitempty"`
	Dependencies              []string `json:"dependencies,omitempty"`
	PlanningSourceID          string   `json:"planning_source_id,omitempty"`
	PlanningSourceLane        string   `json:"planning_source_lane,omitempty"`
	PlanningSourceFingerprint string   `json:"planning_source_fingerprint,omitempty"`
	PlanningDestination       string   `json:"planning_destination,omitempty"`
	PlanningBatchFingerprint  string   `json:"planning_batch_fingerprint,omitempty"`
	PlanningBatchSize         int      `json:"planning_batch_size,omitempty"`
	PlanningItemIndex         int      `json:"planning_item_index,omitempty"`
}

type plannedItemMetadataWire struct {
	Version                   int                 `json:"version"`
	Repository                string              `json:"repository"`
	Dependencies              []PlannedDependency `json:"dependencies"`
	DependencyIDsResolved     bool                `json:"dependency_ids_resolved"`
	PlanningSourceID          string              `json:"planning_source_id,omitempty"`
	PlanningSourceLane        string              `json:"planning_source_lane"`
	PlanningSourceFingerprint string              `json:"planning_source_fingerprint"`
	PlanningDestination       string              `json:"planning_destination"`
	PlanningBatchFingerprint  string              `json:"planning_batch_fingerprint"`
	PlanningBatchSize         int                 `json:"planning_batch_size"`
	PlanningItemIndex         int                 `json:"planning_item_index"`
}

var errPlanningDependencyIDsPending = errors.New("Runner planning dependency IDs are not finalized")

func canonicalDependencies(dependencies []PlannedDependency) []PlannedDependency {
	result := make([]PlannedDependency, 0, len(dependencies))
	for _, dependency := range dependencies {
		id, title := strings.TrimSpace(dependency.ItemID), strings.TrimSpace(dependency.Title)
		if id != "" && title != "" {
			result = append(result, PlannedDependency{ItemID: id, Title: title})
		}
	}
	return result
}

func DecodePlannedItemMetadata(body string) PlannedItemMetadata {
	metadata, _, _ := decodePlannedItemMetadata(body)
	return metadata
}

func decodePlannedItemMetadata(body string) (PlannedItemMetadata, bool, error) {
	const prefix = "## Runner planning metadata\n\nThis operator-visible metadata is authenticated with the staged batch and is not editable scheduling input.\n\n```json\n"
	const suffix = "\n```"
	reserved := strings.Contains(body, "<!-- runner-metadata") || strings.Contains(body, "## Runner planning metadata")
	start := strings.LastIndex(body, prefix)
	if start < 0 {
		if reserved {
			return PlannedItemMetadata{}, true, errors.New("reserved Runner planning metadata is hidden or malformed")
		}
		return PlannedItemMetadata{}, false, nil
	}
	payload := body[start+len(prefix):]
	end := strings.Index(payload, suffix)
	if end < 0 {
		return PlannedItemMetadata{}, true, errors.New("Runner planning metadata must be the final canonical section")
	}
	var wire plannedItemMetadataWire
	if err := json.Unmarshal([]byte(payload[:end]), &wire); err != nil {
		return PlannedItemMetadata{}, true, errors.New("Runner planning metadata JSON is malformed")
	}
	metadata := PlannedItemMetadata{
		Repository: wire.Repository, PlanningSourceID: wire.PlanningSourceID, PlanningSourceLane: wire.PlanningSourceLane,
		PlanningSourceFingerprint: wire.PlanningSourceFingerprint, PlanningDestination: wire.PlanningDestination,
		PlanningBatchFingerprint: wire.PlanningBatchFingerprint, PlanningBatchSize: wire.PlanningBatchSize, PlanningItemIndex: wire.PlanningItemIndex,
	}
	if strings.TrimSpace(payload[end+len(suffix):]) != "" {
		return metadata, true, errors.New("Runner planning metadata must be the final canonical section")
	}
	canonical, _ := json.Marshal(wire)
	if string(canonical) != payload[:end] || wire.Version != 1 || wire.PlanningSourceLane == "" || wire.PlanningSourceFingerprint == "" ||
		wire.PlanningDestination == "" || wire.PlanningBatchFingerprint == "" || wire.PlanningBatchSize < 1 || wire.PlanningItemIndex < 1 ||
		wire.Repository != strings.TrimSpace(wire.Repository) || wire.PlanningSourceID != strings.TrimSpace(wire.PlanningSourceID) ||
		wire.PlanningSourceLane != strings.TrimSpace(wire.PlanningSourceLane) || wire.PlanningSourceFingerprint != strings.TrimSpace(wire.PlanningSourceFingerprint) ||
		wire.PlanningDestination != strings.TrimSpace(wire.PlanningDestination) || wire.PlanningBatchFingerprint != strings.TrimSpace(wire.PlanningBatchFingerprint) {
		return PlannedItemMetadata{}, true, errors.New("Runner planning metadata is not canonical or complete")
	}
	seen := map[string]bool{}
	for _, dependency := range wire.Dependencies {
		id := strings.TrimSpace(dependency.ItemID)
		if id == "" || id != dependency.ItemID || strings.TrimSpace(dependency.Title) == "" || strings.TrimSpace(dependency.Title) != dependency.Title || seen[id] {
			return PlannedItemMetadata{}, true, errors.New("Runner planning dependencies are incomplete or duplicated")
		}
		seen[id] = true
		metadata.Dependencies = append(metadata.Dependencies, id)
	}
	if !wire.DependencyIDsResolved {
		if len(wire.Dependencies) != 0 {
			return PlannedItemMetadata{}, true, errors.New("unresolved Runner planning metadata cannot contain dependency IDs")
		}
		return metadata, true, errPlanningDependencyIDsPending
	}
	metadata.Repository = strings.TrimSpace(metadata.Repository)
	metadata.Dependencies = compactNonEmpty(metadata.Dependencies)
	metadata.PlanningSourceID = strings.TrimSpace(metadata.PlanningSourceID)
	metadata.PlanningSourceLane = strings.TrimSpace(metadata.PlanningSourceLane)
	metadata.PlanningSourceFingerprint = strings.TrimSpace(metadata.PlanningSourceFingerprint)
	metadata.PlanningDestination = strings.TrimSpace(metadata.PlanningDestination)
	metadata.PlanningBatchFingerprint = strings.TrimSpace(metadata.PlanningBatchFingerprint)
	return metadata, true, nil
}

func dependenciesSatisfied(item WorkItem, all []WorkItem, doneStatus string) bool {
	if len(item.Dependencies) == 0 {
		return true
	}
	if item.PlanningMetadataInvalid || strings.TrimSpace(item.PlanningBatchFingerprint) == "" {
		return false
	}
	itemsByID := map[string][]WorkItem{}
	for _, candidate := range all {
		id := strings.TrimSpace(candidate.ID)
		if id != "" {
			itemsByID[id] = append(itemsByID[id], candidate)
		}
	}
	seen := map[string]bool{}
	for _, dependency := range item.Dependencies {
		dependency = strings.TrimSpace(dependency)
		matches := itemsByID[dependency]
		if dependency == "" || dependency == item.ID || seen[dependency] || len(matches) != 1 ||
			matches[0].PlanningMetadataInvalid || matches[0].PlanningBatchFingerprint != item.PlanningBatchFingerprint ||
			matches[0].PlanningSourceID != item.PlanningSourceID || !strings.EqualFold(strings.TrimSpace(matches[0].Status), doneStatus) {
			return false
		}
		seen[dependency] = true
	}
	return true
}

// ValidatePlanningDependencies verifies that every scheduling edge stays
// within one exact staged batch and uses unique immutable item IDs.
func ValidatePlanningDependencies(children []WorkItem) error {
	if len(children) == 0 {
		return errors.New("planning dependency graph has no children")
	}
	byID := make(map[string]int, len(children))
	batch := strings.TrimSpace(children[0].PlanningBatchFingerprint)
	source := strings.TrimSpace(children[0].PlanningSourceID)
	if batch == "" {
		return errors.New("planning dependency graph has no canonical batch identity")
	}
	for index, child := range children {
		id := strings.TrimSpace(child.ID)
		if id == "" || child.PlanningMetadataInvalid || child.PlanningBatchFingerprint != batch || strings.TrimSpace(child.PlanningSourceID) != source {
			return errors.New("planning dependency graph is incomplete or contains mixed-batch children")
		}
		if _, exists := byID[id]; exists {
			return fmt.Errorf("planning dependency graph contains duplicate item ID %q", id)
		}
		byID[id] = index
	}
	edges := make([][]int, len(children))
	for index, child := range children {
		seen := map[string]bool{}
		for _, dependency := range child.Dependencies {
			id := strings.TrimSpace(dependency)
			dependencyIndex, exists := byID[id]
			if id == "" || !exists {
				return fmt.Errorf("planning child %s references unknown or cross-batch dependency ID %q", child.ID, id)
			}
			if dependencyIndex == index {
				return fmt.Errorf("planning child %s depends on itself", child.ID)
			}
			if seen[id] {
				return fmt.Errorf("planning child %s repeats dependency ID %q", child.ID, id)
			}
			seen[id] = true
			edges[index] = append(edges[index], dependencyIndex)
		}
	}
	states := make([]uint8, len(children))
	var visit func(int) error
	visit = func(index int) error {
		if states[index] == 1 {
			return fmt.Errorf("planning dependency graph contains a cycle involving %s", children[index].ID)
		}
		if states[index] == 2 {
			return nil
		}
		states[index] = 1
		for _, dependency := range edges[index] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		states[index] = 2
		return nil
	}
	for index := range children {
		if err := visit(index); err != nil {
			return err
		}
	}
	return nil
}

func (s *Project) planningBatchReleased(item WorkItem, all []WorkItem, doneStatus string) bool {
	if sourceID := strings.TrimSpace(item.PlanningSourceID); sourceID != "" {
		var source WorkItem
		for _, candidate := range all {
			if candidate.ID == sourceID {
				source = candidate
				break
			}
		}
		if source.ID == "" || !strings.EqualFold(strings.TrimSpace(source.Status), strings.TrimSpace(doneStatus)) {
			return false
		}
		children := make([]WorkItem, 0, item.PlanningBatchSize)
		for _, candidate := range all {
			if strings.TrimSpace(candidate.PlanningSourceID) != sourceID {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(candidate.Status), s.assessmentStatus()) {
				return false
			}
			if strings.EqualFold(strings.TrimSpace(candidate.Status), strings.TrimSpace(doneStatus)) {
				if err := s.validateRecordedAction(candidate); err != nil {
					return false
				}
			} else if _, err := s.validateAction(candidate); err != nil {
				return false
			}
			children = append(children, candidate)
		}
		_, err := s.validatePlanningBatch(source.Approval, source, children, batchReleasedState)
		return err == nil
	}

	fingerprint := strings.TrimSpace(item.PlanningBatchFingerprint)
	if fingerprint == "" {
		return true
	}
	// Direct batches have no parent source to serve as a release commit marker.
	// Treat the batch as released only when every exact indexed sibling has a
	// valid post-staging Runner action. This remains true as accepted children
	// progress, while a partial release always retains an invalid staged sibling.
	batchSize := item.PlanningBatchSize
	if batchSize < 1 || batchSize > MaxPlanningBatchChildren || item.PlanningItemIndex < 1 || item.PlanningItemIndex > batchSize {
		return false
	}
	destination := strings.TrimSpace(item.PlanningDestination)
	if destination == "" || !s.agentStatus(destination) {
		return false
	}
	batchChildren := make([]WorkItem, 0, batchSize)
	for _, candidate := range all {
		if strings.TrimSpace(candidate.PlanningSourceID) == "" && strings.TrimSpace(candidate.PlanningBatchFingerprint) == fingerprint {
			batchChildren = append(batchChildren, candidate)
		}
	}
	if err := ValidatePlanningDependencies(batchChildren); err != nil {
		return false
	}
	seen := make([]bool, batchSize)
	count := 0
	for _, candidate := range all {
		if strings.TrimSpace(candidate.PlanningSourceID) != "" || strings.TrimSpace(candidate.PlanningBatchFingerprint) != fingerprint {
			continue
		}
		if candidate.PlanningSourceLane != item.PlanningSourceLane || candidate.PlanningSourceFingerprint != item.PlanningSourceFingerprint ||
			candidate.PlanningDestination != item.PlanningDestination || candidate.PlanningBatchSize != batchSize ||
			candidate.PlanningItemIndex < 1 || candidate.PlanningItemIndex > batchSize || seen[candidate.PlanningItemIndex-1] ||
			strings.EqualFold(strings.TrimSpace(candidate.Status), s.assessmentStatus()) {
			return false
		}
		if strings.EqualFold(strings.TrimSpace(candidate.Status), strings.TrimSpace(doneStatus)) {
			if err := s.validateRecordedAction(candidate); err != nil {
				return false
			}
		} else if _, err := s.validateAction(candidate); err != nil {
			return false
		}
		seen[candidate.PlanningItemIndex-1] = true
		count++
	}
	return count == batchSize
}
