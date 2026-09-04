package github

import "strings"

type WorkEligibilityReason string

const (
	WorkEligibilityNotAgentLane                         WorkEligibilityReason = "not_agent_lane"
	WorkEligibilityTransitionLocked                     WorkEligibilityReason = "transition_locked"
	WorkEligibilityPlanningMetadataInvalid              WorkEligibilityReason = "planning_metadata_invalid"
	WorkEligibilityActionAuthorityInvalid               WorkEligibilityReason = "action_authority_invalid"
	WorkEligibilityDependenciesIncomplete               WorkEligibilityReason = "dependencies_incomplete"
	WorkEligibilityPlanningBatchIncomplete              WorkEligibilityReason = "planning_batch_incomplete"
	WorkEligibilityPlanningBatchSiblingAuthorityInvalid WorkEligibilityReason = "planning_batch_sibling_authority_invalid"
	WorkEligibilityPlanningBatchAuthorityInvalid        WorkEligibilityReason = "planning_batch_authority_invalid"
	WorkEligibilityUnavailable                          WorkEligibilityReason = "eligibility_unavailable"
)

type WorkEligibility struct {
	Item     WorkItem
	Eligible bool
	Reason   WorkEligibilityReason
	Summary  string
	action   *AuthorizedAction
}

// EvaluateWorkEligibility classifies every card in an agent lane without
// mutating Project state. ReadyItems and operator status consume this same
// result so visible queueing cannot drift from deterministic admission gates.
func (s *Project) EvaluateWorkEligibility(items []WorkItem) []WorkEligibility {
	index := newWorkItemIndex(items)
	result := make([]WorkEligibility, 0)
	for _, item := range items {
		if !s.agentStatus(item.Status) {
			continue
		}
		result = append(result, s.evaluateWorkEligibilityIn(item, index))
	}
	return result
}

func (s *Project) evaluateWorkEligibilityIn(item WorkItem, index *workItemIndex) WorkEligibility {
	if !s.agentStatus(item.Status) {
		return waitingWork(item, WorkEligibilityNotAgentLane, "The card is not in a configured agent lane.")
	}
	if strings.TrimSpace(item.Transition) != "" {
		return waitingWork(item, WorkEligibilityTransitionLocked, "Waiting for Runner to recover an interrupted Project transition.")
	}
	if item.PlanningMetadataInvalid {
		return waitingWork(item, WorkEligibilityPlanningMetadataInvalid, "Planning metadata is invalid; move the card to assessment and approve it again.")
	}
	action, err := s.validateAction(item)
	if err != nil {
		if s.canAdoptManualIntakeItem(item) {
			return WorkEligibility{Item: item, Eligible: true}
		}
		return waitingWork(item, WorkEligibilityActionAuthorityInvalid, "Runner approval is missing, invalid, or no longer matches the card; move it to assessment and approve it again.")
	}
	if reason, summary := s.planningBatchEligibilityIn(item, index); reason != "" {
		return waitingWork(item, reason, summary)
	}
	if !s.dependenciesSatisfiedIn(item, index) {
		return waitingWork(item, WorkEligibilityDependenciesIncomplete, "Waiting for dependencies to reach authenticated successful outcomes.")
	}
	return WorkEligibility{Item: item, Eligible: true, action: &action}
}

func waitingWork(item WorkItem, reason WorkEligibilityReason, summary string) WorkEligibility {
	return WorkEligibility{Item: item, Reason: reason, Summary: summary}
}

func (s *Project) planningBatchEligibilityIn(item WorkItem, index *workItemIndex) (WorkEligibilityReason, string) {
	if sourceID := strings.TrimSpace(item.PlanningSourceID); sourceID != "" {
		source := index.byID[sourceID]
		if source.ID == "" || !strings.EqualFold(strings.TrimSpace(source.Status), s.doneStatus()) {
			return WorkEligibilityPlanningBatchIncomplete, "Waiting for the planning batch source and complete batch release."
		}
		children := index.childrenBySource[sourceID]
		if len(children) != item.PlanningBatchSize {
			return WorkEligibilityPlanningBatchIncomplete, "Waiting for every card in the planning batch to be present and released."
		}
		for _, candidate := range children {
			if strings.EqualFold(strings.TrimSpace(candidate.Status), s.assessmentStatus()) || strings.TrimSpace(candidate.Transition) != "" {
				return WorkEligibilityPlanningBatchIncomplete, "Waiting for every card in the planning batch to finish assessment or transition recovery."
			}
			if strings.EqualFold(strings.TrimSpace(candidate.Status), s.doneStatus()) {
				if !s.hasSuccessfulOutcomeIn(candidate, index) {
					return WorkEligibilityPlanningBatchSiblingAuthorityInvalid, "A terminal planning-batch sibling has invalid lifecycle authority; maintainer reassessment is required."
				}
			} else if _, err := s.validateAction(candidate); err != nil {
				return WorkEligibilityPlanningBatchSiblingAuthorityInvalid, "A planning-batch sibling has invalid lifecycle authority; maintainer reassessment is required."
			}
		}
		if _, err := s.validatePlanningBatch(source.Approval, source, children, batchReleasedState); err != nil {
			return WorkEligibilityPlanningBatchAuthorityInvalid, "The planning batch release has invalid authority; maintainer reassessment is required."
		}
		return "", ""
	}

	fingerprint := strings.TrimSpace(item.PlanningBatchFingerprint)
	if fingerprint == "" {
		return "", ""
	}
	batchSize := item.PlanningBatchSize
	if batchSize < 1 || batchSize > MaxPlanningBatchChildren || item.PlanningItemIndex < 1 || item.PlanningItemIndex > batchSize {
		return WorkEligibilityPlanningBatchAuthorityInvalid, "Planning batch metadata is inconsistent; maintainer reassessment is required."
	}
	destination := strings.TrimSpace(item.PlanningDestination)
	if destination == "" || !s.agentStatus(destination) {
		return WorkEligibilityPlanningBatchAuthorityInvalid, "The planning batch destination is invalid; maintainer reassessment is required."
	}
	batchChildren := index.directByFingerprint[fingerprint]
	if len(batchChildren) != batchSize {
		return WorkEligibilityPlanningBatchIncomplete, "Waiting for every card in the planning batch to be present and released."
	}
	if err := ValidatePlanningDependencies(batchChildren); err != nil {
		return WorkEligibilityPlanningBatchAuthorityInvalid, "Planning batch metadata is inconsistent; maintainer reassessment is required."
	}
	seen := make([]bool, batchSize)
	for _, candidate := range batchChildren {
		if candidate.PlanningSourceLane != item.PlanningSourceLane || candidate.PlanningSourceFingerprint != item.PlanningSourceFingerprint ||
			candidate.PlanningDestination != item.PlanningDestination || candidate.PlanningBatchSize != batchSize ||
			candidate.PlanningItemIndex < 1 || candidate.PlanningItemIndex > batchSize || seen[candidate.PlanningItemIndex-1] {
			return WorkEligibilityPlanningBatchAuthorityInvalid, "Planning batch metadata is inconsistent; maintainer reassessment is required."
		}
		if strings.EqualFold(strings.TrimSpace(candidate.Status), s.assessmentStatus()) || strings.TrimSpace(candidate.Transition) != "" {
			return WorkEligibilityPlanningBatchIncomplete, "Waiting for every card in the planning batch to finish assessment or transition recovery."
		}
		if strings.EqualFold(strings.TrimSpace(candidate.Status), s.doneStatus()) {
			if !s.hasSuccessfulOutcomeIn(candidate, index) {
				return WorkEligibilityPlanningBatchSiblingAuthorityInvalid, "A terminal planning-batch sibling has invalid lifecycle authority; maintainer reassessment is required."
			}
		} else if _, err := s.validateAction(candidate); err != nil {
			return WorkEligibilityPlanningBatchSiblingAuthorityInvalid, "A planning-batch sibling has invalid lifecycle authority; maintainer reassessment is required."
		}
		seen[candidate.PlanningItemIndex-1] = true
	}
	return "", ""
}
