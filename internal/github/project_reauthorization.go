package github

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ReauthorizationPlan is a new operator approval of one exact released action,
// not an automatic repair of an invalid signature or an initial batch release.
type ReauthorizationPlan struct {
	Item              WorkItem `json:"item"`
	TargetLaneID      string   `json:"target_lane_id"`
	TargetStatus      string   `json:"target_status"`
	Role              string   `json:"role"`
	Result            string   `json:"result"`
	Unstarted         bool     `json:"unstarted,omitempty"`
	PlanRevision      string   `json:"plan_revision,omitempty"`
	RemoveIntakeLabel bool     `json:"remove_intake_label,omitempty"`

	item WorkItem
	next AuthorizedAction
}

// InspectRecoveryItem is read-only and deliberately does not construct an
// AuthorizedAction. Operator-only review must not mint workflow authority.
func (s *Project) InspectRecoveryItem(ctx context.Context, selector string) (WorkItem, error) {
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return WorkItem{}, err
	}
	return selectProjectItem(items, selector)
}

func (s *Project) PlanReauthorization(ctx context.Context, selector string) (ReauthorizationPlan, error) {
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return ReauthorizationPlan{}, err
	}
	item, err := selectProjectItem(items, selector)
	if err != nil {
		return ReauthorizationPlan{}, err
	}
	plan, err := s.planReauthorization(item, items)
	if err != nil || !plan.Unstarted {
		return plan, err
	}
	// Project lifecycle reads do not include issue labels. Inspect the issue
	// explicitly, as ordinary approval does, and bind that observation into
	// the preview that ApplyReauthorization rechecks before any write.
	plan.RemoveIntakeLabel, err = s.hasIntakeLabel(ctx, item)
	if err != nil {
		return ReauthorizationPlan{}, err
	}
	return plan, nil
}

func (s *Project) planReauthorization(item WorkItem, items []WorkItem) (ReauthorizationPlan, error) {
	if !strings.EqualFold(strings.TrimSpace(item.Status), s.assessmentStatus()) || strings.TrimSpace(item.Approval) != "" {
		return ReauthorizationPlan{}, errors.New("reauthorization requires a card in assessment with missing Runner approval")
	}
	lane := s.laneIDForStatus(s.readyStatus())
	role := s.cfg.LaneRoles[lane]
	published := strings.TrimSpace(item.PullRequest) != ""
	unstarted := strings.TrimSpace(item.Branch) == "" && item.PlanningSourceID != ""
	if lane == "" || role == "" || (strings.TrimSpace(item.Branch) == "" && !unstarted) || item.PlanRelease != "" {
		return ReauthorizationPlan{}, errors.New("reauthorization requires a retained implementation branch and configured Ready lane; plan delivery parents cannot return to implementation")
	}
	if unstarted {
		if published || item.Phase != lane || item.QACommit != "" || item.QAFailures != 0 || !s.isIntakeIssueURL(item.URL) || item.IssueState != "OPEN" {
			return ReauthorizationPlan{}, errors.New("unstarted plan recovery cannot adopt implementation, review or publication history")
		}
	} else if published {
		if item.Phase != "" || !validGitObjectID(item.QACommit) {
			return ReauthorizationPlan{}, errors.New("published reauthorization requires completed publication with an exact QA commit and no active phase; paused reviewer work requires --qa-only")
		}
	} else if strings.TrimSpace(item.Phase) != lane || strings.TrimSpace(item.QACommit) != "" {
		return ReauthorizationPlan{}, errors.New("unpublished reauthorization requires retained implementation work in the configured Ready phase")
	}
	if item.PlanningMetadataInvalid || strings.TrimSpace(item.Transition) != "" || (!unstarted && strings.TrimSpace(item.Activity) != "") ||
		strings.TrimSpace(item.Body) == "" || item.QAFailures < 0 || strings.TrimSpace(item.DraftContentID) != "" {
		return ReauthorizationPlan{}, errors.New("card has incomplete content, an active transition, or invalid recovery state")
	}
	next := item
	next.Status = s.readyStatus()
	next.Result = "Operator reauthorized the retained implementation and requested a retry."
	removeLabel := unstarted && containsNormalized(item.Labels, s.intakeLabel())
	if unstarted {
		next.Result = "Operator reauthorized the exact approved, unstarted plan member for implementation."
		if removeLabel {
			next.Labels = withoutNormalizedValue(next.Labels, s.intakeLabel())
		}
	}
	if published {
		feedback, err := pullRequestFeedbackProjectResult(item.Repository, item.PullRequest)
		if err != nil {
			return ReauthorizationPlan{}, err
		}
		next.Phase = lane
		next.Result += "\n\n" + feedback
	}
	action, err := s.signAction(next, role, lane)
	if err != nil {
		return ReauthorizationPlan{}, err
	}
	// Replace only the selected card in the validation snapshot. Every other
	// child and any source release must retain their existing signed authority.
	released := append([]WorkItem(nil), items...)
	for index := range released {
		if released[index].ID == item.ID {
			released[index] = action.Item
		}
	}
	if reason, summary := s.planningBatchEligibilityIn(action.Item, newWorkItemIndex(released)); reason != "" {
		return ReauthorizationPlan{}, fmt.Errorf("cannot recover this card independently: %s", summary)
	}
	var revision string
	if unstarted {
		parent, found := newWorkItemIndex(released).byID[item.PlanningSourceID]
		if !found {
			return ReauthorizationPlan{}, errors.New("unstarted recovery requires an exact released delivery parent")
		}
		delivery, err := s.ValidatePlanDelivery(parent, released)
		if err != nil {
			return ReauthorizationPlan{}, fmt.Errorf("unstarted recovery requires current signed plan/member authority: %w", err)
		}
		revision = delivery.Revision
	}
	return ReauthorizationPlan{Item: item, TargetLaneID: lane, TargetStatus: next.Status, Role: role, Result: next.Result,
		Unstarted: unstarted, PlanRevision: revision, RemoveIntakeLabel: removeLabel, item: item, next: action}, nil
}

// ApplyReauthorization must follow the operator's exact preview and the
// engine's matching private-workspace check, under the local mutation guard.
func (s *Project) ApplyReauthorization(ctx context.Context, plan ReauthorizationPlan) (WorkItem, error) {
	if plan.item.ID == "" || !reflect.DeepEqual(plan.Item, plan.item) {
		return WorkItem{}, errors.New("reauthorization preview is incomplete or modified")
	}
	fresh, err := s.PlanReauthorization(ctx, plan.item.ID)
	if err != nil {
		return WorkItem{}, err
	}
	if !reflect.DeepEqual(plan, fresh) {
		return WorkItem{}, errors.New("card or recovery destination changed after the preview; review it again")
	}
	next := fresh.next.Item
	if err := s.beginTransition(ctx, next.ID); err != nil {
		return WorkItem{}, err
	}
	if fresh.RemoveIntakeLabel {
		result, err := s.gh(ctx, "issue", "edit", next.URL, "--remove-label", s.intakeLabel())
		if err != nil {
			return WorkItem{}, fmt.Errorf("remove reassessment label; retain reauthorization transition lock: %w", commandFailure(err, result))
		}
	}
	if err := s.applyFieldUpdates(ctx, next.ID,
		textProjectField(s.resultFieldName(), next.Result),
		textProjectField(s.phaseFieldName(), next.Phase),
		textProjectField(s.approvalFieldName(), next.Approval),
		statusProjectField(s.statusFieldName(), next.Status),
	); err != nil {
		return WorkItem{}, fmt.Errorf("reauthorize retained implementation; item remains transition-locked: %w", err)
	}
	if err := s.finishTransition(ctx, next.ID); err != nil {
		return WorkItem{}, fmt.Errorf("reauthorization committed but its transition lock could not be cleared: %w", err)
	}
	return next, nil
}
