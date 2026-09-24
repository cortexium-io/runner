package github

import (
	"errors"
	"reflect"
)

// validateCompletedPlanDelivery proves historical delivery, never execution
// authority. The signed terminal parent records the already-validated exact PR
// merge. Its original release and every retained member must still authenticate;
// changing current model/gate policy cannot undo that completed outcome.
func (s *Project) validateCompletedPlanDelivery(parent WorkItem, all []WorkItem) (PlanDelivery, error) {
	if parent.Status != s.doneStatus() || parent.Phase != "" || parent.Transition != "" || parent.PlanningMetadataInvalid ||
		parent.PullRequest == "" || !validGitObjectID(parent.QACommit) || parent.Branch != PlanBranch(parent.ID) {
		return PlanDelivery{}, errors.New("plan has no authenticated terminal delivery")
	}
	if _, err := s.validateAction(parent); err != nil {
		return PlanDelivery{}, err
	}
	if _, err := validatedPullRequestSelector(parent.Repository, parent.PullRequest); err != nil {
		return PlanDelivery{}, err
	}
	index := newWorkItemIndex(all)
	if len(index.byReference[parent.ID]) != 1 || !reflect.DeepEqual(index.byID[parent.ID], parent) {
		return PlanDelivery{}, errors.New("completed plan parent identity is missing or ambiguous")
	}
	children := index.childrenBySource[parent.ID]
	manifest, err := s.validatePlanMemberContract(parent, children)
	if err != nil {
		return PlanDelivery{}, err
	}
	if _, err := s.validateRecordedPlanningBatch(parent.PlanRelease, parent, children, batchReleasedState); err != nil {
		return PlanDelivery{}, err
	}
	delivery := PlanDelivery{Parent: parent, Manifest: manifest, Revision: PlanRevision(parent.Body)}
	for _, member := range manifest.Members {
		child := index.byID[member.ID]
		if len(index.byReference[child.ID]) != 1 || child.PlanningMetadataInvalid {
			return PlanDelivery{}, errors.New("completed plan member identity or metadata is invalid")
		}
		if member.Retired {
			if child.Status != s.backlogStatus() || child.Phase != PlanRetiredPhase || child.Transition != "" || child.PullRequest != "" {
				return PlanDelivery{}, errors.New("retired member lifecycle changed")
			}
			if _, err := s.validateAction(child); err != nil {
				return PlanDelivery{}, err
			}
			delivery.RetiredChildren = append(delivery.RetiredChildren, child)
			continue
		}
		if child.Status != s.backlogStatus() && child.Status != s.doneStatus() {
			return PlanDelivery{}, errors.New("completed plan member left its terminal or integrated lane")
		}
		// Closing the issue can move its Project row to Done. Validate a copy
		// against the original signed Backlog integration, allowing no other
		// drift. Do not rewrite/re-sign history or return an executable action.
		recorded := child
		recorded.Status = s.backlogStatus()
		if !s.integratedPlanMember(recorded) {
			return PlanDelivery{}, errors.New("completed plan member has no authenticated integration")
		}
		delivery.Children = append(delivery.Children, child)
	}
	return delivery, nil
}
