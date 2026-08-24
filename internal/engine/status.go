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
	Blocked  []github.WorkItem `json:"blocked"`
	PRReady  []github.WorkItem `json:"pr_ready"`
}

func (s *Engine) WorkStatus(ctx context.Context) (WorkStatus, error) {
	items, err := s.source.LifecycleItems(ctx)
	if err != nil {
		return WorkStatus{}, err
	}
	status := WorkStatus{Items: items, ByStatus: map[string]int{}}
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
			status.Queued = append(status.Queued, item)
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
	return status, nil
}
