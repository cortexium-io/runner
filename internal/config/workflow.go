package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	WorkflowOutcomeSuccess    = "success"
	WorkflowOutcomeRejected   = "rejected"
	WorkflowOutcomeExhausted  = "exhausted"
	WorkflowOutcomeNeedsInput = "needs_input"
	WorkflowOutcomeError      = "error"

	WorkflowEventPRMerged    = "pull_request.merged"
	WorkflowEventPRClosed    = "pull_request.closed"
	WorkflowEventPROutOfDate = "pull_request.out_of_date"

	WorkflowActionPublishPR    = "publish_pull_request"
	WorkflowActionUpdateBranch = "update_branch"
)

type WorkflowConfig struct {
	IntakeLane   string                  `json:"intake_lane"`
	ApprovalLane string                  `json:"approval_lane"`
	ActiveLane   string                  `json:"active_lane"`
	Lanes        map[string]WorkflowLane `json:"lanes"`
	Events       []WorkflowEvent         `json:"events,omitempty"`
}

type WorkflowLane struct {
	Name        string            `json:"name"`
	Role        string            `json:"role,omitempty"`
	CreatesIn   string            `json:"creates_in,omitempty"`
	RejectLimit int               `json:"reject_limit,omitempty"`
	OnEnter     string            `json:"on_enter,omitempty"`
	Transitions map[string]string `json:"transitions,omitempty"`
}

type WorkflowEvent struct {
	On            string            `json:"on"`
	To            string            `json:"to,omitempty"`
	Action        string            `json:"action,omitempty"`
	RequireReview *bool             `json:"require_review,omitempty"`
	Transitions   map[string]string `json:"transitions,omitempty"`
}

// WorkflowTemplate returns the workflow persisted by init. Runtime loading
// requires an explicit workflow and never falls back to this template.
func WorkflowTemplate(requireReviewAfterBaseUpdate bool) WorkflowConfig {
	requireReview := requireReviewAfterBaseUpdate
	return WorkflowConfig{
		IntakeLane: "needs_assessment", ApprovalLane: "backlog", ActiveLane: "in_progress",
		Lanes: map[string]WorkflowLane{
			"needs_assessment": {Name: "Needs assessment"},
			"backlog":          {Name: "Backlog"},
			"plan": {
				Name: "Plan", Role: WorkRolePlanner, CreatesIn: "ready",
				Transitions: map[string]string{WorkflowOutcomeSuccess: "done", WorkflowOutcomeNeedsInput: "blocked", WorkflowOutcomeError: "blocked"},
			},
			"ready": {
				Name: "Ready", Role: WorkRoleImplementer,
				Transitions: map[string]string{WorkflowOutcomeSuccess: "agent_qa", WorkflowOutcomeNeedsInput: "blocked", WorkflowOutcomeError: "blocked"},
			},
			"in_progress": {Name: "In Progress"},
			"agent_qa": {
				Name: "Agent QA", Role: WorkRoleReviewer, RejectLimit: 3,
				Transitions: map[string]string{
					WorkflowOutcomeSuccess: "pr_ready", WorkflowOutcomeRejected: "ready", WorkflowOutcomeExhausted: "blocked",
					WorkflowOutcomeNeedsInput: "blocked", WorkflowOutcomeError: "blocked",
				},
			},
			"pr_ready": {Name: "PR Ready", OnEnter: WorkflowActionPublishPR},
			"blocked":  {Name: "Blocked"},
			"done":     {Name: "Done"},
		},
		Events: []WorkflowEvent{
			{On: WorkflowEventPRMerged, To: "done"},
			{On: WorkflowEventPRClosed, To: "done"},
			{On: WorkflowEventPROutOfDate, Action: WorkflowActionUpdateBranch, RequireReview: &requireReview, Transitions: map[string]string{"updated": "ready", "conflict": "ready", WorkflowOutcomeError: "blocked"}},
		},
	}
}

func (c Config) resolvedWorkflow() WorkflowConfig {
	if c.Workflow == nil {
		return WorkflowConfig{}
	}
	return cloneWorkflow(*c.Workflow)
}

func (c Config) EffectiveWorkflow() WorkflowConfig {
	return c.resolvedWorkflow()
}

func (c Config) Lane(id string) (WorkflowLane, bool) {
	lane, ok := c.resolvedWorkflow().Lanes[strings.TrimSpace(id)]
	return lane, ok
}

func (c Config) LaneIDForStatus(status string) string {
	for id, lane := range c.resolvedWorkflow().Lanes {
		if strings.EqualFold(strings.TrimSpace(lane.Name), strings.TrimSpace(status)) {
			return id
		}
	}
	return ""
}

func (c Config) LaneStatus(id string) string {
	if lane, ok := c.Lane(id); ok {
		return strings.TrimSpace(lane.Name)
	}
	return ""
}

func (c Config) WorkflowEventFor(name string) (WorkflowEvent, bool) {
	for _, event := range c.resolvedWorkflow().Events {
		if event.On == name {
			return event, true
		}
	}
	return WorkflowEvent{}, false
}

func (c Config) PublicationLaneID() string {
	for id, lane := range c.resolvedWorkflow().Lanes {
		if lane.OnEnter == WorkflowActionPublishPR {
			return id
		}
	}
	return ""
}

func (c Config) AgentLaneIDs() []string {
	result := []string{}
	for id, lane := range c.resolvedWorkflow().Lanes {
		if strings.TrimSpace(lane.Role) != "" {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}

func (c Config) ResolveProject() ProjectConfig {
	project := ProjectConfig{
		GitHubProjectConfig: *c.GitHubProject,
		ActivityField:       RunnerActivityFieldName,
		RunnerID:            strings.TrimSpace(c.RunnerID),
	}
	workflow := c.resolvedWorkflow()
	project.AssessmentStatus = workflow.Lanes[workflow.IntakeLane].Name
	project.BacklogStatus = workflow.Lanes[workflow.ApprovalLane].Name
	project.RunningStatus = workflow.Lanes[workflow.ActiveLane].Name
	project.LaneStatuses = map[string]string{}
	project.LaneRoles = map[string]string{}
	project.PlanningDestinations = map[string]string{}
	project.ApprovalLaneID = workflow.ApprovalLane
	project.ActiveLaneID = workflow.ActiveLane
	project.RequiredStatuses = make([]string, 0, len(workflow.Lanes))
	project.AgentStatuses = make([]string, 0)
	ids := make([]string, 0, len(workflow.Lanes))
	for id := range workflow.Lanes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		lane := workflow.Lanes[id]
		project.LaneStatuses[id] = lane.Name
		project.LaneRoles[id] = strings.TrimSpace(lane.Role)
		if c.RoleContract(lane.Role) == WorkRolePlanner {
			project.PlanningDestinations[id] = strings.TrimSpace(workflow.Lanes[lane.CreatesIn].Name)
		}
		project.RequiredStatuses = append(project.RequiredStatuses, lane.Name)
		if lane.Role != "" {
			project.AgentStatuses = append(project.AgentStatuses, lane.Name)
		}
		switch c.RoleContract(lane.Role) {
		case WorkRoleImplementer:
			if project.ReadyStatus == "" || project.ReadyStatus == "Ready" {
				project.ReadyStatus = lane.Name
			}
		case WorkRoleReviewer:
			if project.QAStatus == "" || project.QAStatus == "Agent QA" {
				project.QAStatus = lane.Name
			}
		}
		if lane.OnEnter == WorkflowActionPublishPR && (project.PRReadyStatus == "" || project.PRReadyStatus == "PR Ready") {
			project.PRReadyStatus = lane.Name
		}
	}
	for _, id := range ids {
		if c.RoleContract(workflow.Lanes[id].Role) == WorkRolePlanner {
			project.InitialLaneID = id
			project.InitialRole = strings.TrimSpace(workflow.Lanes[id].Role)
			break
		}
	}
	if event, ok := c.WorkflowEventFor(WorkflowEventPRMerged); ok {
		project.DoneStatus = workflow.Lanes[event.To].Name
	}
	if blocked, ok := workflow.Lanes["blocked"]; ok {
		project.BlockedStatus = blocked.Name
	} else {
		for _, id := range ids {
			if target := workflow.Lanes[id].Transitions[WorkflowOutcomeError]; target != "" {
				project.BlockedStatus = workflow.Lanes[target].Name
				break
			}
		}
	}
	return project
}

func validateWorkflowConfig(c Config) error {
	workflow := c.resolvedWorkflow()
	roles := c.resolvedRoles()
	if len(workflow.Lanes) == 0 {
		return errors.New("workflow.lanes must define at least one lane")
	}
	specialLanes := []struct {
		field string
		id    string
	}{
		{field: "intake_lane", id: workflow.IntakeLane},
		{field: "approval_lane", id: workflow.ApprovalLane},
		{field: "active_lane", id: workflow.ActiveLane},
	}
	seenSpecial := map[string]string{}
	for _, special := range specialLanes {
		id := special.id
		if !validWorkflowID(id) {
			return fmt.Errorf("workflow references invalid lane id %q", id)
		}
		lane, exists := workflow.Lanes[id]
		if !exists {
			return fmt.Errorf("workflow references undefined lane %q", id)
		}
		if previous, duplicate := seenSpecial[id]; duplicate {
			return fmt.Errorf("workflow.%s and workflow.%s must reference different lanes", previous, special.field)
		}
		seenSpecial[id] = special.field
		if strings.TrimSpace(lane.Role) != "" {
			return fmt.Errorf("workflow.%s must reference a human or Runner lane without role", special.field)
		}
	}
	seenNames := map[string]string{}
	for id, lane := range workflow.Lanes {
		if !validWorkflowID(id) {
			return fmt.Errorf("workflow lane id %q must use lowercase letters, numbers, and underscores", id)
		}
		name := strings.TrimSpace(lane.Name)
		if name == "" {
			return fmt.Errorf("workflow.lanes.%s.name is required", id)
		}
		key := normalizeProjectKey(name)
		if previous, exists := seenNames[key]; exists {
			return fmt.Errorf("workflow lanes %q and %q use the same Project status name", previous, id)
		}
		seenNames[key] = id
		if lane.Role != "" {
			if _, exists := roles[lane.Role]; !exists {
				return fmt.Errorf("workflow.lanes.%s references undefined role %q", id, lane.Role)
			}
			contract := c.RoleContract(lane.Role)
			if contract == "" {
				return fmt.Errorf("workflow.lanes.%s role %q must be planner, implementer, reviewer, or extend one of them", id, lane.Role)
			}
			for _, outcome := range []string{WorkflowOutcomeSuccess, WorkflowOutcomeNeedsInput, WorkflowOutcomeError} {
				if strings.TrimSpace(lane.Transitions[outcome]) == "" {
					return fmt.Errorf("workflow.lanes.%s.transitions.%s is required", id, outcome)
				}
			}
			if contract == WorkRolePlanner && strings.TrimSpace(lane.CreatesIn) == "" {
				return fmt.Errorf("workflow.lanes.%s.creates_in is required for planner roles", id)
			}
			if contract == WorkRoleReviewer {
				if lane.RejectLimit <= 0 {
					return fmt.Errorf("workflow.lanes.%s.reject_limit must be positive for reviewer roles", id)
				}
				for _, outcome := range []string{WorkflowOutcomeRejected, WorkflowOutcomeExhausted} {
					if strings.TrimSpace(lane.Transitions[outcome]) == "" {
						return fmt.Errorf("workflow.lanes.%s.transitions.%s is required for reviewer roles", id, outcome)
					}
				}
			}
		}
		if lane.OnEnter != "" && lane.OnEnter != WorkflowActionPublishPR {
			return fmt.Errorf("workflow.lanes.%s.on_enter has unsupported action %q", id, lane.OnEnter)
		}
		for outcome, target := range lane.Transitions {
			if !validWorkflowOutcome(outcome) {
				return fmt.Errorf("workflow.lanes.%s.transitions contains unsupported outcome %q", id, outcome)
			}
			if _, exists := workflow.Lanes[target]; !exists {
				return fmt.Errorf("workflow.lanes.%s.transitions.%s references undefined lane %q", id, outcome, target)
			}
			if target == workflow.ActiveLane {
				return fmt.Errorf("workflow.lanes.%s.transitions.%s cannot target active_lane; Runner enters it only while claiming work", id, outcome)
			}
		}
		if target := strings.TrimSpace(lane.CreatesIn); target != "" {
			if _, exists := workflow.Lanes[target]; !exists {
				return fmt.Errorf("workflow.lanes.%s.creates_in references undefined lane %q", id, target)
			}
			if target == workflow.ActiveLane {
				return fmt.Errorf("workflow.lanes.%s.creates_in cannot target active_lane", id)
			}
		}
	}
	publicationLane := ""
	for id, lane := range workflow.Lanes {
		if lane.OnEnter != WorkflowActionPublishPR {
			continue
		}
		if publicationLane != "" {
			return fmt.Errorf("workflow lanes %q and %q both publish pull requests; exactly one publication lane is supported", publicationLane, id)
		}
		publicationLane = id
		if strings.TrimSpace(lane.Role) != "" {
			return fmt.Errorf("workflow.lanes.%s.on_enter publish_pull_request must be a human lane without role", id)
		}
		reviewerPredecessor := false
		for _, candidate := range workflow.Lanes {
			if c.RoleContract(candidate.Role) == WorkRoleReviewer && candidate.Transitions[WorkflowOutcomeSuccess] == id {
				reviewerPredecessor = true
				break
			}
		}
		if !reviewerPredecessor {
			return fmt.Errorf("workflow.lanes.%s.on_enter publish_pull_request must be the success target of a reviewer lane", id)
		}
	}
	if c.GitHubProject != nil && c.GitHubProject.AutoMerge && publicationLane == "" {
		return errors.New("github_project.auto_merge requires a workflow publication lane")
	}
	if err := validateImplementerLadder(c); err != nil {
		return err
	}
	if err := validateRoleConfigs(c, roles); err != nil {
		return err
	}
	if err := validateWorkflowEvents(workflow); err != nil {
		return err
	}
	return validatePublicationWorkflow(workflow, publicationLane)
}

func validateImplementerLadder(c Config) error {
	if len(c.ImplementerLadder) == 0 {
		return nil
	}
	if len(c.ImplementerLadder) < 2 {
		return errors.New("implementer_ladder must contain at least two roles or be omitted")
	}
	primary := c.RoleIDForContract(WorkRoleImplementer)
	if primary == "" {
		return errors.New("implementer_ladder requires an implementer workflow role")
	}
	seen := map[string]struct{}{}
	for index, rawRole := range c.ImplementerLadder {
		role := strings.TrimSpace(rawRole)
		if role == "" {
			return fmt.Errorf("implementer_ladder[%d] cannot be blank", index)
		}
		if index == 0 && role != primary {
			return fmt.Errorf("implementer_ladder[0] must be the workflow implementer role %q", primary)
		}
		if _, exists := seen[role]; exists {
			return fmt.Errorf("implementer_ladder contains duplicate role %q", role)
		}
		seen[role] = struct{}{}
		if _, exists := c.Roles[role]; !exists {
			return fmt.Errorf("implementer_ladder[%d] references undefined role %q", index, role)
		}
		if c.RoleContract(role) != WorkRoleImplementer {
			return fmt.Errorf("implementer_ladder[%d] role %q must use the implementer contract", index, role)
		}
	}
	maxAttempts := 0
	workflow := c.resolvedWorkflow()
	for _, lane := range workflow.Lanes {
		if c.RoleContract(lane.Role) != WorkRoleReviewer {
			continue
		}
		retryLane, exists := workflow.Lanes[lane.Transitions[WorkflowOutcomeRejected]]
		if !exists || strings.TrimSpace(retryLane.Role) != primary {
			continue
		}
		if lane.RejectLimit > maxAttempts {
			maxAttempts = lane.RejectLimit
		}
	}
	if maxAttempts == 0 {
		return fmt.Errorf("implementer_ladder requires a reviewer rejection transition back to implementer role %q", primary)
	}
	if len(c.ImplementerLadder) > maxAttempts {
		return fmt.Errorf("implementer_ladder has %d roles but the QA rejection limit permits at most %d implementation attempts", len(c.ImplementerLadder), maxAttempts)
	}
	return nil
}

func validateWorkflowEvents(workflow WorkflowConfig) error {
	seen := map[string]struct{}{}
	for index, event := range workflow.Events {
		if _, exists := seen[event.On]; exists {
			return fmt.Errorf("workflow.events[%d] duplicates %q", index, event.On)
		}
		seen[event.On] = struct{}{}
		switch event.On {
		case WorkflowEventPRMerged, WorkflowEventPRClosed:
			if _, exists := workflow.Lanes[event.To]; !exists {
				return fmt.Errorf("workflow.events[%d].to references undefined lane %q", index, event.To)
			}
			if event.To == workflow.ActiveLane {
				return fmt.Errorf("workflow.events[%d].to cannot target active_lane", index)
			}
		case WorkflowEventPROutOfDate:
			if event.Action != WorkflowActionUpdateBranch {
				return fmt.Errorf("workflow.events[%d].action must be %q", index, WorkflowActionUpdateBranch)
			}
			if event.RequireReview == nil {
				return fmt.Errorf("workflow.events[%d].require_review is required for %q", index, WorkflowEventPROutOfDate)
			}
			if !*event.RequireReview {
				return fmt.Errorf("workflow.events[%d].require_review must be true so base refreshes complete QA before publication", index)
			}
			for _, outcome := range []string{"updated", "conflict", WorkflowOutcomeError} {
				target := event.Transitions[outcome]
				if _, exists := workflow.Lanes[target]; !exists {
					return fmt.Errorf("workflow.events[%d].transitions.%s references undefined lane %q", index, outcome, target)
				}
				if target == workflow.ActiveLane {
					return fmt.Errorf("workflow.events[%d].transitions.%s cannot target active_lane", index, outcome)
				}
			}
		default:
			return fmt.Errorf("workflow.events[%d].on has unsupported event %q", index, event.On)
		}
	}
	return nil
}

func validatePublicationWorkflow(workflow WorkflowConfig, publicationLane string) error {
	if publicationLane == "" {
		return nil
	}
	required := []string{WorkflowEventPRMerged, WorkflowEventPRClosed, WorkflowEventPROutOfDate}
	events := make(map[string]WorkflowEvent, len(workflow.Events))
	for _, event := range workflow.Events {
		events[event.On] = event
	}
	for _, name := range required {
		if _, exists := events[name]; !exists {
			return fmt.Errorf("workflow with publish_pull_request requires event %q", name)
		}
	}
	return nil
}

func validWorkflowOutcome(value string) bool {
	switch value {
	case WorkflowOutcomeSuccess, WorkflowOutcomeRejected, WorkflowOutcomeExhausted, WorkflowOutcomeNeedsInput, WorkflowOutcomeError:
		return true
	default:
		return false
	}
}

func validWorkflowID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "_") || strings.HasSuffix(value, "_") {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func cloneWorkflow(input WorkflowConfig) WorkflowConfig {
	result := input
	result.Lanes = make(map[string]WorkflowLane, len(input.Lanes))
	for id, lane := range input.Lanes {
		lane.Transitions = cloneStringMap(lane.Transitions)
		result.Lanes[id] = lane
	}
	result.Events = make([]WorkflowEvent, len(input.Events))
	for index, event := range input.Events {
		event.Transitions = cloneStringMap(event.Transitions)
		result.Events[index] = event
	}
	return result
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
