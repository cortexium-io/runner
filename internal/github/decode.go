package github

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
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
	if item.ImplementationProfile != "" {
		fmt.Fprintf(&b, "\n\n## Execution profile\n\n%s — %s\n", item.ImplementationProfile, item.ProfileReason)
	}
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
		ImplementationProfile: item.ImplementationProfile,
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
	ImplementationProfile     string   `json:"implementation_profile,omitempty"`
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
	ImplementationProfile     string              `json:"implementation_profile,omitempty"`
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

func decodeManualDependencies(body string) ([]string, bool, error) {
	const heading = "## Dependencies"
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	found := false
	dependencies := []string{}
	seen := map[string]bool{}
	for index := 0; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) != heading {
			continue
		}
		if found {
			return nil, true, errors.New("ordinary work item contains more than one Dependencies section")
		}
		found = true
		for index++; index < len(lines); index++ {
			line := strings.TrimSpace(lines[index])
			if strings.HasPrefix(line, "## ") {
				index--
				break
			}
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "- ") {
				return nil, true, errors.New("ordinary work item Dependencies entries must be bullet-list references")
			}
			reference := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if !validManualDependencyReference(reference) {
				return nil, true, errors.New("ordinary work item Dependencies entries must be exact Project item IDs or issue URLs without labels")
			}
			if seen[reference] {
				return nil, true, fmt.Errorf("ordinary work item repeats dependency reference %q", reference)
			}
			seen[reference] = true
			dependencies = append(dependencies, reference)
		}
	}
	if found && len(dependencies) == 0 {
		return nil, true, errors.New("ordinary work item Dependencies section is empty")
	}
	return dependencies, found, nil
}

func validManualDependencyReference(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "PVTI_") && !strings.ContainsAny(value, " \t/:") {
		return true
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || !strings.EqualFold(parts[2], "issues") {
		return false
	}
	number, err := strconv.Atoi(parts[3])
	return err == nil && number > 0
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
		ImplementationProfile: wire.ImplementationProfile,
	}
	if strings.TrimSpace(payload[end+len(suffix):]) != "" {
		return metadata, true, errors.New("Runner planning metadata must be the final canonical section")
	}
	canonical, _ := json.Marshal(wire)
	if string(canonical) != payload[:end] || wire.Version != 1 || wire.PlanningSourceLane == "" || wire.PlanningSourceFingerprint == "" ||
		wire.PlanningDestination == "" || wire.PlanningBatchFingerprint == "" || wire.PlanningBatchSize < 1 || wire.PlanningItemIndex < 1 ||
		wire.ImplementationProfile != strings.TrimSpace(wire.ImplementationProfile) ||
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

type workItemIndex struct {
	all                 []WorkItem
	byReference         map[string][]WorkItem
	byID                map[string]WorkItem
	childrenBySource    map[string][]WorkItem
	directByFingerprint map[string][]WorkItem
	successful          map[string]bool
	successEvaluated    map[string]bool
}

func newWorkItemIndex(all []WorkItem) *workItemIndex {
	index := &workItemIndex{
		all: append([]WorkItem(nil), all...), byReference: map[string][]WorkItem{}, byID: map[string]WorkItem{},
		childrenBySource: map[string][]WorkItem{}, directByFingerprint: map[string][]WorkItem{},
		successful: map[string]bool{}, successEvaluated: map[string]bool{},
	}
	for _, item := range all {
		id := strings.TrimSpace(item.ID)
		if id != "" {
			index.byReference[id] = append(index.byReference[id], item)
			index.byID[id] = item
		}
		if itemURL := strings.TrimSpace(item.URL); itemURL != "" {
			index.byReference[itemURL] = append(index.byReference[itemURL], item)
		}
		if sourceID := strings.TrimSpace(item.PlanningSourceID); sourceID != "" {
			index.childrenBySource[sourceID] = append(index.childrenBySource[sourceID], item)
		} else if fingerprint := strings.TrimSpace(item.PlanningBatchFingerprint); fingerprint != "" {
			index.directByFingerprint[fingerprint] = append(index.directByFingerprint[fingerprint], item)
		}
	}
	return index
}

func (s *Project) dependenciesSatisfied(item WorkItem, all []WorkItem) bool {
	return s.dependenciesSatisfiedIn(item, newWorkItemIndex(all))
}

func (s *Project) dependenciesSatisfiedIn(item WorkItem, index *workItemIndex) bool {
	if len(item.Dependencies) == 0 {
		return true
	}
	seen := map[string]bool{}
	for _, dependency := range item.Dependencies {
		dependency = strings.TrimSpace(dependency)
		matches := index.byReference[dependency]
		if dependency == "" || seen[dependency] || len(matches) != 1 || matches[0].ID == item.ID || !s.hasSuccessfulOutcomeIn(matches[0], index) {
			return false
		}
		seen[dependency] = true
	}
	return true
}

// hasSuccessfulOutcome proves that the dependency reached the configured
// success lane through either an authenticated Runner transition or an
// authenticated planning-batch release. Status alone is insufficient because
// a human can move a Project card without minting Runner outcome authority.
func (s *Project) hasSuccessfulOutcome(item WorkItem, all []WorkItem) bool {
	return s.hasSuccessfulOutcomeIn(item, newWorkItemIndex(all))
}

func (s *Project) hasSuccessfulOutcomeIn(item WorkItem, index *workItemIndex) bool {
	itemID := strings.TrimSpace(item.ID)
	if index.successEvaluated[itemID] {
		return index.successful[itemID]
	}
	index.successEvaluated[itemID] = true
	if !strings.EqualFold(strings.TrimSpace(item.Status), s.doneStatus()) || strings.TrimSpace(item.Transition) != "" {
		return false
	}
	action, err := s.validateActionAssertion(item, false)
	if err == nil {
		successState := s.laneIDForStatus(s.doneStatus())
		index.successful[itemID] = successState != "" && action.state == successState
		return index.successful[itemID]
	}
	children := index.childrenBySource[itemID]
	_, err = s.validatePlanningBatch(item.Approval, item, children, batchReleasedState)
	index.successful[itemID] = err == nil
	return index.successful[itemID]
}

// ValidatePlanningDependencies verifies unique immutable item IDs and the
// acyclic portion of the graph contained in one staged batch. References to
// external item IDs are allowed and are resolved against the full Project when
// work is selected.
func ValidatePlanningDependencies(children []WorkItem) error {
	if len(children) == 0 {
		return errors.New("planning dependency graph has no children")
	}
	byReference := make(map[string]int, len(children)*2)
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
		if _, exists := byReference[id]; exists {
			return fmt.Errorf("planning dependency graph contains duplicate item ID %q", id)
		}
		byReference[id] = index
		if itemURL := strings.TrimSpace(child.URL); itemURL != "" {
			if _, exists := byReference[itemURL]; exists {
				return fmt.Errorf("planning dependency graph contains duplicate item URL %q", itemURL)
			}
			byReference[itemURL] = index
		}
	}
	edges := make([][]int, len(children))
	for index, child := range children {
		seen := map[string]bool{}
		for _, dependency := range child.Dependencies {
			id := strings.TrimSpace(dependency)
			if id == "" {
				return fmt.Errorf("planning child %s contains an empty dependency ID", child.ID)
			}
			if dependencyIndex, exists := byReference[id]; exists && dependencyIndex == index {
				return fmt.Errorf("planning child %s depends on itself", child.ID)
			}
			if seen[id] {
				return fmt.Errorf("planning child %s repeats dependency ID %q", child.ID, id)
			}
			seen[id] = true
			if dependencyIndex, exists := byReference[id]; exists {
				edges[index] = append(edges[index], dependencyIndex)
			}
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
