package github

import "strings"

// PlanningProgress is a read-only projection of the existing authenticated
// lifecycle. It is not a second delivery ledger and cannot admit work.
type PlanningProgress struct {
	IntegratedUndelivered []WorkItem
	PlanningCompleted     []WorkItem
	CancelledPlans        []WorkItem
}

func (s *Project) PlanningProgress(items []WorkItem) PlanningProgress {
	var result PlanningProgress
	index := newWorkItemIndex(items)
	for _, parent := range items {
		if parent.PlanRelease != "" {
			delivery, err := s.validatePlanDeliveryState(parent, items, true)
			if err != nil || s.planningSourceWorkCompletedIn(parent, index) {
				continue
			}
			if parent.Phase == PlanCancelledPhase {
				result.CancelledPlans = append(result.CancelledPlans, parent)
			}
			for _, child := range delivery.Children {
				if s.integratedPlanMember(child) {
					result.IntegratedUndelivered = append(result.IntegratedUndelivered, child)
				}
			}
			continue
		}
		// Historical parents used Done to mean that planning had finished.
		// Preserve that record and explicitly label its narrower meaning.
		if !strings.EqualFold(parent.Status, s.doneStatus()) || parent.Transition != "" || parent.PullRequest != "" {
			continue
		}
		if _, present, _ := ParsePlanManifest(parent.Body); present {
			continue
		}
		children := index.childrenBySource[parent.ID]
		if len(children) == 0 {
			continue
		}
		if _, err := s.validatePlanningBatch(parent.Approval, parent, children, batchReleasedState); err == nil {
			result.PlanningCompleted = append(result.PlanningCompleted, parent)
		}
	}
	return result
}
