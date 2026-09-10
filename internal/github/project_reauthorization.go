package github

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ReauthorizationPlan is a new operator approval of one exact retained action,
// not an automatic repair of an invalid signature or an initial batch release.
type ReauthorizationPlan struct {
	Item         WorkItem `json:"item"`
	TargetLaneID string   `json:"target_lane_id"`
	TargetStatus string   `json:"target_status"`
	Role         string   `json:"role"`

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
	return s.planReauthorization(item, items)
}

func (s *Project) planReauthorization(item WorkItem, items []WorkItem) (ReauthorizationPlan, error) {
	if item.Approval == "" && item.Branch != "" && item.PullRequest != "" {
		return ReauthorizationPlan{}, errors.New("retained PR work needs an explicitly scoped review; preview `retry --item ITEM_ID --reauthorize --qa-only --dry-run` while preserving its runtime history")
	}
	if !strings.EqualFold(strings.TrimSpace(item.Status), s.assessmentStatus()) || strings.TrimSpace(item.Approval) != "" {
		return ReauthorizationPlan{}, errors.New("reauthorization requires a card in assessment with missing Runner approval")
	}
	lane := s.laneIDForStatus(s.readyStatus())
	role := s.cfg.LaneRoles[lane]
	if lane == "" || role == "" || strings.TrimSpace(item.Phase) != lane || strings.TrimSpace(item.Branch) == "" ||
		strings.TrimSpace(item.PullRequest) != "" || strings.TrimSpace(item.QACommit) != "" {
		return ReauthorizationPlan{}, errors.New("reauthorization supports only retained, unpublished implementation work in the configured Ready phase")
	}
	if item.PlanningMetadataInvalid || strings.TrimSpace(item.Transition) != "" || strings.TrimSpace(item.Activity) != "" ||
		strings.TrimSpace(item.Body) == "" || item.QAFailures < 0 || strings.TrimSpace(item.DraftContentID) != "" {
		return ReauthorizationPlan{}, errors.New("card has incomplete content, an active transition, or invalid recovery state")
	}
	next := item
	next.Status = s.readyStatus()
	next.Result = "Operator reauthorized the retained implementation and requested a retry."
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
	return ReauthorizationPlan{Item: item, TargetLaneID: lane, TargetStatus: next.Status, Role: role, item: item, next: action}, nil
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
	if err := s.applyFieldUpdates(ctx, next.ID,
		textProjectField(s.resultFieldName(), next.Result),
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
