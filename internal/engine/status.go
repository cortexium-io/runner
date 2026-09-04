package engine

import (
	"context"
	"sort"
	"strings"

	"github.com/cortexium-io/runner/internal/github"
)

type WorkStatus struct {
	Items    []github.WorkItem `json:"items"`
	ByStatus map[string]int    `json:"by_status"`
	Active   []github.WorkItem `json:"active"`
	Queued   []github.WorkItem `json:"queued"`
	Waiting  []WaitingWork     `json:"waiting"`
	Blocked  []github.WorkItem `json:"blocked"`
	PRReady  []github.WorkItem `json:"pr_ready"`
}

type WaitingWork struct {
	Item    github.WorkItem              `json:"item"`
	Reason  github.WorkEligibilityReason `json:"reason"`
	Summary string                       `json:"summary"`
}

func (s *Engine) WorkStatus(ctx context.Context) (WorkStatus, error) {
	items, err := s.source.LifecycleItems(ctx)
	if err != nil {
		return WorkStatus{}, err
	}
	status := WorkStatus{Items: items, ByStatus: map[string]int{}}
	eligibilityByID := map[string]github.WorkEligibility{}
	for _, eligibility := range s.source.EvaluateWorkEligibility(items) {
		eligibilityByID[eligibility.Item.ID] = eligibility
	}
	agentLanes := map[string]bool{}
	for _, id := range s.cfg.AgentLaneIDs() {
		agentLanes[id] = true
	}
	publication := s.cfg.PublicationLaneID()
	for index, item := range items {
		status.ByStatus[item.Status]++
		laneID := s.cfg.LaneIDForStatus(item.Status)
		roleLaneID := laneID
		if (laneID == s.cfg.Workflow.ActiveLane || strings.EqualFold(strings.TrimSpace(item.Status), strings.TrimSpace(s.cfg.GitHubProject.BlockedStatus))) && strings.TrimSpace(item.Phase) != "" {
			roleLaneID = strings.TrimSpace(item.Phase)
		}
		if lane, ok := s.cfg.Lane(roleLaneID); ok {
			item.Role = lane.Role
			status.Items[index].Role = lane.Role
		}
		switch {
		case laneID == s.cfg.Workflow.ActiveLane:
			status.Active = append(status.Active, item)
		case strings.EqualFold(strings.TrimSpace(item.Status), strings.TrimSpace(s.cfg.GitHubProject.BlockedStatus)):
			status.Blocked = append(status.Blocked, item)
		case laneID == publication:
			status.PRReady = append(status.PRReady, item)
		case agentLanes[laneID]:
			eligibility, ok := eligibilityByID[item.ID]
			if ok && eligibility.Eligible {
				status.Queued = append(status.Queued, item)
			} else if ok {
				status.Waiting = append(status.Waiting, WaitingWork{Item: item, Reason: eligibility.Reason, Summary: eligibility.Summary})
			} else {
				status.Waiting = append(status.Waiting, WaitingWork{
					Item: item, Reason: github.WorkEligibilityUnavailable,
					Summary: "Runner could not classify this card's eligibility; inspect Project configuration and authority.",
				})
			}
		}
	}
	for _, list := range [][]github.WorkItem{status.Items, status.Active, status.Queued, status.Blocked, status.PRReady} {
		sort.Slice(list, func(i, j int) bool {
			if strings.EqualFold(list[i].Status, list[j].Status) {
				return strings.ToLower(list[i].Title) < strings.ToLower(list[j].Title)
			}
			return strings.ToLower(list[i].Status) < strings.ToLower(list[j].Status)
		})
	}
	sort.Slice(status.Waiting, func(i, j int) bool {
		if strings.EqualFold(status.Waiting[i].Item.Status, status.Waiting[j].Item.Status) {
			return strings.ToLower(status.Waiting[i].Item.Title) < strings.ToLower(status.Waiting[j].Item.Title)
		}
		return strings.ToLower(status.Waiting[i].Item.Status) < strings.ToLower(status.Waiting[j].Item.Status)
	})
	return status, nil
}
