package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type RetryPlan struct {
	Item             WorkItem `json:"item"`
	TargetLaneID     string   `json:"target_lane_id"`
	TargetStatus     string   `json:"target_status"`
	FeedbackOverride string   `json:"feedback_override,omitempty"`

	action AuthorizedAction
}

func (s *Project) PlanRetry(ctx context.Context, selector string) (RetryPlan, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return RetryPlan{}, errors.New("blocked project item id, URL, or exact title is required")
	}
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return RetryPlan{}, err
	}
	item, err := selectProjectItem(items, selector)
	if err != nil {
		return RetryPlan{}, err
	}
	action, err := s.validateAction(item)
	if err != nil {
		return RetryPlan{}, errors.New("blocked item has invalid Runner authority; move it to assessment and run approve again")
	}
	return s.retryPlanForAction(action)
}

func (s *Project) PlanRetryWithFeedback(ctx context.Context, selector, feedback string) (RetryPlan, error) {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return RetryPlan{}, errors.New("retry feedback override must not be empty")
	}
	if _, err := runnerProjectResult(feedback); err != nil {
		return RetryPlan{}, fmt.Errorf("validate retry feedback override: %w", err)
	}
	plan, err := s.PlanRetry(ctx, selector)
	if err != nil {
		return RetryPlan{}, err
	}
	plan.FeedbackOverride = feedback
	return plan, nil
}

func (s *Project) retryPlanForAction(action AuthorizedAction) (RetryPlan, error) {
	item := action.Item
	if !strings.EqualFold(strings.TrimSpace(item.Status), s.blockedStatus()) {
		return RetryPlan{}, fmt.Errorf("project item %s is in %q, not %q", item.ID, item.Status, s.blockedStatus())
	}
	targetLaneID := strings.TrimSpace(item.Phase)
	targetStatus := strings.TrimSpace(s.cfg.LaneStatuses[targetLaneID])
	if targetLaneID == "" {
		targetLaneID, targetStatus = s.uniqueAgentLaneForRole(action.Role)
	}
	if targetLaneID == "" || targetStatus == "" || !s.agentStatus(targetStatus) {
		return RetryPlan{}, errors.New("blocked item has no recorded Runner retry lane and its authenticated role does not identify one unique agent lane; move it to the intended agent lane manually")
	}
	return RetryPlan{Item: item, TargetLaneID: targetLaneID, TargetStatus: targetStatus, action: action}, nil
}

func (s *Project) uniqueAgentLaneForRole(role string) (string, string) {
	role = strings.TrimSpace(role)
	var matchedLane, matchedStatus string
	for laneID, laneRole := range s.cfg.LaneRoles {
		status := strings.TrimSpace(s.cfg.LaneStatuses[laneID])
		if strings.TrimSpace(laneRole) != role || status == "" || !s.agentStatus(status) {
			continue
		}
		if matchedLane != "" {
			return "", ""
		}
		matchedLane, matchedStatus = laneID, status
	}
	return matchedLane, matchedStatus
}

func (s *Project) ApplyRetry(ctx context.Context, plan RetryPlan) (WorkItem, error) {
	if strings.TrimSpace(plan.Item.ID) == "" || strings.TrimSpace(plan.TargetLaneID) == "" || strings.TrimSpace(plan.TargetStatus) == "" {
		return WorkItem{}, errors.New("retry plan is incomplete")
	}
	current, err := s.refreshAuthorizedAction(ctx, plan.action)
	if err != nil {
		return WorkItem{}, err
	}
	refreshed, err := s.retryPlanForAction(current)
	if err != nil {
		return WorkItem{}, err
	}
	if refreshed.TargetLaneID != plan.TargetLaneID || refreshed.TargetStatus != plan.TargetStatus {
		return WorkItem{}, errors.New("blocked item's retry destination changed after the preview; inspect it and try again")
	}
	detail := current.Item.Result
	resetFailures := false
	if strings.TrimSpace(plan.FeedbackOverride) != "" {
		detail = plan.FeedbackOverride
		resetFailures = true
	}
	if err := s.transition(ctx, current, plan.TargetStatus, detail, plan.TargetLaneID, false, func(next *WorkItem) {
		if resetFailures {
			next.QAFailures = 0
		}
	}, func(current WorkItem) error {
		if !resetFailures {
			return nil
		}
		return s.setNumberField(ctx, current.ID, s.qaFailuresFieldName(), 0)
	}); err != nil {
		return WorkItem{}, err
	}
	item := current.Item
	item.Status = plan.TargetStatus
	item.Phase = plan.TargetLaneID
	if resetFailures {
		published, err := runnerProjectResult(detail)
		if err != nil {
			return WorkItem{}, err
		}
		item.Result = canonicalProjectResult(published)
		item.QAFailures = 0
	}
	return item, nil
}
