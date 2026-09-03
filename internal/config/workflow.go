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

	WorkflowEventLaneEntered    = "lane.entered"
	WorkflowEventPRMerged       = "pull_request.merged"
	WorkflowEventPRClosed       = "pull_request.closed"
	WorkflowEventPRChecksFailed = "pull_request.checks_failed"
	WorkflowEventPROutOfDate    = "pull_request.out_of_date"

	WorkflowActionRunRole      = "run_role"
	WorkflowActionTransition   = "transition"
	WorkflowActionPublishPR    = "publish_pull_request"
	WorkflowActionUpdateBranch = "update_branch"
)

// WorkflowConfig is the persisted workflow definition. Lanes name externally
// visible states; rules assign behavior to those states and external events.
type WorkflowConfig struct {
	IntakeLane   string                  `json:"intake_lane"`
	ApprovalLane string                  `json:"approval_lane"`
	PlanLane     string                  `json:"plan_lane"`
	ReadyLane    string                  `json:"ready_lane"`
	ActiveLane   string                  `json:"active_lane"`
	Lanes        map[string]WorkflowLane `json:"lanes"`
	Rules        []WorkflowRule          `json:"rules"`
}

type WorkflowLane struct {
	Name string `json:"name"`
}

// ResolvedWorkflow is the validated engine contract compiled from rules.
type ResolvedWorkflow struct {
	IntakeLane   string
	ApprovalLane string
	PlanLane     string
	ReadyLane    string
	ActiveLane   string
	Lanes        map[string]ResolvedWorkflowLane
	Rules        []WorkflowRule
	Events       []WorkflowEvent
}

type ResolvedWorkflowLane struct {
	Name            string
	Role            string
	CreatesIn       string
	MaxQARejections int
	OnEnter         string
	Transitions     map[string]string
}

// WorkflowRule binds one typed trigger to one typed action. Larger workflows
// are composed by routing an action outcome into another lane, which emits the
// next lane.entered event. One action per rule keeps ordering and retry
// semantics explicit.
type WorkflowRule struct {
	ID      string          `json:"id"`
	Trigger WorkflowTrigger `json:"trigger"`
	Action  WorkflowAction  `json:"action"`
}

type WorkflowTrigger struct {
	Event string `json:"event"`
	Lane  string `json:"lane,omitempty"`
}

type WorkflowAction struct {
	Type            string            `json:"type"`
	Role            string            `json:"role,omitempty"`
	To              string            `json:"to,omitempty"`
	CreatesIn       string            `json:"creates_in,omitempty"`
	MaxQARejections int               `json:"max_qa_rejections,omitempty"`
	RequireReview   *bool             `json:"require_review,omitempty"`
	Transitions     map[string]string `json:"transitions,omitempty"`
}

// WorkflowEvent is the engine-facing representation compiled from a rule.
type WorkflowEvent struct {
	On            string
	To            string
	Action        string
	RequireReview *bool
	Transitions   map[string]string
}

// WorkflowTemplate returns the workflow persisted by init. Runtime loading
// requires an explicit workflow and never falls back to this template.
func WorkflowTemplate(requireReviewAfterBaseUpdate bool) WorkflowConfig {
	requireReview := requireReviewAfterBaseUpdate
	workflow := WorkflowConfig{
		IntakeLane: "needs_assessment", ApprovalLane: "backlog", PlanLane: "plan", ReadyLane: "ready", ActiveLane: "in_progress",
		Lanes: map[string]WorkflowLane{
			"needs_assessment": {Name: "Needs assessment"},
			"backlog":          {Name: "Backlog"},
			"plan":             {Name: "Plan"},
			"ready":            {Name: "Ready"},
			"in_progress":      {Name: "In Progress"},
			"agent_qa":         {Name: "Agent QA"},
			"pr_ready":         {Name: "PR Ready"},
			"blocked":          {Name: "Blocked"},
			"done":             {Name: "Done"},
		},
		Rules: []WorkflowRule{
			{
				ID: "plan", Trigger: WorkflowTrigger{Event: WorkflowEventLaneEntered, Lane: "plan"},
				Action: WorkflowAction{Type: WorkflowActionRunRole, Role: WorkRolePlanner, CreatesIn: "ready", Transitions: map[string]string{
					WorkflowOutcomeSuccess: "done", WorkflowOutcomeNeedsInput: "blocked", WorkflowOutcomeError: "blocked",
				}},
			},
			{
				ID: "implement", Trigger: WorkflowTrigger{Event: WorkflowEventLaneEntered, Lane: "ready"},
				Action: WorkflowAction{Type: WorkflowActionRunRole, Role: WorkRoleImplementer, Transitions: map[string]string{
					WorkflowOutcomeSuccess: "agent_qa", WorkflowOutcomeNeedsInput: "blocked", WorkflowOutcomeError: "blocked",
				}},
			},
			{
				ID: "review", Trigger: WorkflowTrigger{Event: WorkflowEventLaneEntered, Lane: "agent_qa"},
				Action: WorkflowAction{Type: WorkflowActionRunRole, Role: WorkRoleReviewer, MaxQARejections: 3, Transitions: map[string]string{
					WorkflowOutcomeSuccess: "pr_ready", WorkflowOutcomeRejected: "ready", WorkflowOutcomeExhausted: "blocked",
					WorkflowOutcomeNeedsInput: "blocked", WorkflowOutcomeError: "blocked",
				}},
			},
			{ID: "publish", Trigger: WorkflowTrigger{Event: WorkflowEventLaneEntered, Lane: "pr_ready"}, Action: WorkflowAction{Type: WorkflowActionPublishPR}},
			{ID: "pull_request_merged", Trigger: WorkflowTrigger{Event: WorkflowEventPRMerged}, Action: WorkflowAction{Type: WorkflowActionTransition, To: "done"}},
			{ID: "pull_request_closed", Trigger: WorkflowTrigger{Event: WorkflowEventPRClosed}, Action: WorkflowAction{Type: WorkflowActionTransition, To: "blocked"}},
			{ID: "pull_request_checks_failed", Trigger: WorkflowTrigger{Event: WorkflowEventPRChecksFailed}, Action: WorkflowAction{Type: WorkflowActionTransition, To: "ready"}},
			{
				ID: "pull_request_out_of_date", Trigger: WorkflowTrigger{Event: WorkflowEventPROutOfDate},
				Action: WorkflowAction{Type: WorkflowActionUpdateBranch, RequireReview: &requireReview, Transitions: map[string]string{
					"updated": "ready", "conflict": "ready", WorkflowOutcomeError: "blocked",
				}},
			},
		},
	}
	return workflow
}

// SetMaxQARejections updates the reviewer action assigned to a lane.
func (w *WorkflowConfig) SetMaxQARejections(laneID string, maximum int) error {
	for index := range w.Rules {
		rule := &w.Rules[index]
		if rule.Trigger.Event == WorkflowEventLaneEntered && rule.Trigger.Lane == strings.TrimSpace(laneID) && rule.Action.Type == WorkflowActionRunRole {
			rule.Action.MaxQARejections = maximum
			return nil
		}
	}
	return fmt.Errorf("workflow lane %q has no run_role rule", laneID)
}

func (c Config) resolvedWorkflow() ResolvedWorkflow {
	if c.Workflow == nil {
		return ResolvedWorkflow{}
	}
	resolved, err := compileWorkflow(*c.Workflow)
	if err != nil {
		return ResolvedWorkflow{}
	}
	return resolved
}

func (c Config) EffectiveWorkflow() ResolvedWorkflow {
	return c.resolvedWorkflow()
}

func (c Config) Lane(id string) (ResolvedWorkflowLane, bool) {
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
	project.TransitionField = project.TransitionFieldName()
	project.MergeMethod = NormalizeMergeMethod(project.MergeMethod)
	workflow := c.resolvedWorkflow()
	project.AssessmentStatus = workflow.Lanes[workflow.IntakeLane].Name
	project.BacklogStatus = workflow.Lanes[workflow.ApprovalLane].Name
	project.ReadyStatus = workflow.Lanes[workflow.ReadyLane].Name
	project.RunningStatus = workflow.Lanes[workflow.ActiveLane].Name
	project.LaneStatuses = map[string]string{}
	project.LaneRoles = map[string]string{}
	project.PlanningDestinations = map[string]string{}
	project.InitialLaneID = workflow.PlanLane
	project.InitialRole = strings.TrimSpace(workflow.Lanes[workflow.PlanLane].Role)
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
		if c.RoleContract(lane.Role) == WorkRoleReviewer && project.QAStatus == "" {
			project.QAStatus = lane.Name
		}
		if lane.OnEnter == WorkflowActionPublishPR {
			project.PRReadyStatus = lane.Name
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
	if c.Workflow == nil {
		return errors.New("workflow is required")
	}
	workflow, err := compileWorkflow(*c.Workflow)
	if err != nil {
		return err
	}
	roles := c.resolvedRoles()
	if len(workflow.Lanes) == 0 {
		return errors.New("workflow.lanes must define at least one lane")
	}
	specialLanes := []struct {
		field    string
		id       string
		contract string
	}{
		{field: "intake_lane", id: workflow.IntakeLane},
		{field: "approval_lane", id: workflow.ApprovalLane},
		{field: "plan_lane", id: workflow.PlanLane, contract: WorkRolePlanner},
		{field: "ready_lane", id: workflow.ReadyLane, contract: WorkRoleImplementer},
		{field: "active_lane", id: workflow.ActiveLane},
	}
	seenSpecial := map[string]string{}
	for _, special := range specialLanes {
		id := strings.TrimSpace(special.id)
		if !validWorkflowID(id) {
			return fmt.Errorf("workflow.%s references invalid lane id %q", special.field, special.id)
		}
		lane, exists := workflow.Lanes[id]
		if !exists {
			return fmt.Errorf("workflow.%s references undefined lane %q", special.field, id)
		}
		if previous, duplicate := seenSpecial[id]; duplicate {
			return fmt.Errorf("workflow.%s and workflow.%s must reference different lanes", previous, special.field)
		}
		seenSpecial[id] = special.field
		if special.contract == "" && strings.TrimSpace(lane.Role) != "" {
			return fmt.Errorf("workflow.%s must reference a human or Runner lane without a run_role action", special.field)
		}
		if special.contract != "" && c.RoleContract(lane.Role) != special.contract {
			return fmt.Errorf("workflow.%s must reference a lane that runs a %s role", special.field, special.contract)
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
				return fmt.Errorf("workflow rule for lane %s references undefined role %q", id, lane.Role)
			}
			contract := c.RoleContract(lane.Role)
			if contract == "" {
				return fmt.Errorf("workflow rule for lane %s role %q must be planner, implementer, reviewer, or extend one of them", id, lane.Role)
			}
			for _, outcome := range []string{WorkflowOutcomeSuccess, WorkflowOutcomeNeedsInput, WorkflowOutcomeError} {
				if strings.TrimSpace(lane.Transitions[outcome]) == "" {
					return fmt.Errorf("workflow run_role action for lane %s transitions.%s is required", id, outcome)
				}
			}
			if contract == WorkRolePlanner && strings.TrimSpace(lane.CreatesIn) == "" {
				return fmt.Errorf("workflow run_role action for lane %s creates_in is required for planner roles", id)
			}
			if contract != WorkRolePlanner && strings.TrimSpace(lane.CreatesIn) != "" {
				return fmt.Errorf("workflow run_role action for lane %s creates_in is supported only for planner roles", id)
			}
			if contract == WorkRoleReviewer {
				if lane.MaxQARejections <= 0 {
					return fmt.Errorf("workflow run_role action for lane %s max_qa_rejections must be positive for reviewer roles", id)
				}
				for _, outcome := range []string{WorkflowOutcomeRejected, WorkflowOutcomeExhausted} {
					if strings.TrimSpace(lane.Transitions[outcome]) == "" {
						return fmt.Errorf("workflow run_role action for lane %s transitions.%s is required for reviewer roles", id, outcome)
					}
				}
			} else if lane.MaxQARejections != 0 {
				return fmt.Errorf("workflow run_role action for lane %s max_qa_rejections is supported only for reviewer roles", id)
			}
			if contract != WorkRoleReviewer {
				for _, outcome := range []string{WorkflowOutcomeRejected, WorkflowOutcomeExhausted} {
					if _, exists := lane.Transitions[outcome]; exists {
						return fmt.Errorf("workflow run_role action for lane %s transitions.%s is supported only for reviewer roles", id, outcome)
					}
				}
			}
			for _, outcome := range []string{WorkflowOutcomeNeedsInput, WorkflowOutcomeError} {
				target := workflow.Lanes[lane.Transitions[outcome]]
				if target.Role != "" || target.OnEnter != "" {
					return fmt.Errorf("workflow run_role action for lane %s transitions.%s must target a recovery lane without an automatic action", id, outcome)
				}
			}
			if contract == WorkRoleReviewer {
				retryLane := workflow.Lanes[lane.Transitions[WorkflowOutcomeRejected]]
				if c.RoleContract(retryLane.Role) != WorkRoleImplementer {
					return fmt.Errorf("workflow run_role action for lane %s transitions.%s must target an implementer lane", id, WorkflowOutcomeRejected)
				}
				exhaustedLane := workflow.Lanes[lane.Transitions[WorkflowOutcomeExhausted]]
				if exhaustedLane.Role != "" || exhaustedLane.OnEnter != "" {
					return fmt.Errorf("workflow run_role action for lane %s transitions.%s must target a recovery lane without an automatic action", id, WorkflowOutcomeExhausted)
				}
			}
		}
		for outcome, target := range lane.Transitions {
			if !validWorkflowOutcome(outcome) {
				return fmt.Errorf("workflow run_role action for lane %s transitions contains unsupported outcome %q", id, outcome)
			}
			if _, exists := workflow.Lanes[target]; !exists {
				return fmt.Errorf("workflow run_role action for lane %s transitions.%s references undefined lane %q", id, outcome, target)
			}
			if target == workflow.ActiveLane {
				return fmt.Errorf("workflow run_role action for lane %s transitions.%s cannot target active_lane; Runner enters it only while claiming work", id, outcome)
			}
		}
		if target := strings.TrimSpace(lane.CreatesIn); target != "" {
			if _, exists := workflow.Lanes[target]; !exists {
				return fmt.Errorf("workflow run_role action for lane %s creates_in references undefined lane %q", id, target)
			}
			if target == workflow.ActiveLane {
				return fmt.Errorf("workflow run_role action for lane %s creates_in cannot target active_lane", id)
			}
			if c.RoleContract(workflow.Lanes[target].Role) != WorkRoleImplementer {
				return fmt.Errorf("workflow run_role action for lane %s creates_in must target an implementer lane", id)
			}
		}
	}
	if err := validateAgentSuccessGraph(workflow); err != nil {
		return err
	}
	publicationLane := ""
	for id, lane := range workflow.Lanes {
		if lane.OnEnter != WorkflowActionPublishPR {
			continue
		}
		if publicationLane != "" {
			return fmt.Errorf("workflow rules for lanes %q and %q both publish pull requests; exactly one publication lane is supported", publicationLane, id)
		}
		publicationLane = id
		if strings.TrimSpace(lane.Role) != "" {
			return fmt.Errorf("workflow lane %s cannot run a role and publish a pull request on the same lane.entered event", id)
		}
		reviewerPredecessor := false
		for _, candidate := range workflow.Lanes {
			if c.RoleContract(candidate.Role) == WorkRoleReviewer && candidate.Transitions[WorkflowOutcomeSuccess] == id {
				reviewerPredecessor = true
				break
			}
		}
		if !reviewerPredecessor {
			return fmt.Errorf("workflow publish_pull_request action for lane %s must be the success target of a reviewer role", id)
		}
	}
	if c.GitHubProject != nil && c.GitHubProject.AutoMerge && publicationLane == "" {
		return errors.New("github_project.auto_merge requires a workflow publish_pull_request rule")
	}
	if err := validateImplementerLadder(c); err != nil {
		return err
	}
	if err := validateRoleConfigs(c, roles); err != nil {
		return err
	}
	if err := validateWorkflowEvents(c, workflow); err != nil {
		return err
	}
	return validatePublicationWorkflow(workflow, publicationLane)
}

func compileWorkflow(input WorkflowConfig) (ResolvedWorkflow, error) {
	workflow := ResolvedWorkflow{
		IntakeLane: strings.TrimSpace(input.IntakeLane), ApprovalLane: strings.TrimSpace(input.ApprovalLane),
		PlanLane: strings.TrimSpace(input.PlanLane), ReadyLane: strings.TrimSpace(input.ReadyLane), ActiveLane: strings.TrimSpace(input.ActiveLane),
		Lanes: make(map[string]ResolvedWorkflowLane, len(input.Lanes)), Rules: cloneWorkflowRules(input.Rules),
	}
	for id, lane := range input.Lanes {
		workflow.Lanes[id] = ResolvedWorkflowLane{Name: lane.Name}
	}
	seenIDs := map[string]struct{}{}
	seenTriggers := map[string]string{}
	for index, rule := range workflow.Rules {
		path := fmt.Sprintf("workflow.rules[%d]", index)
		rule.ID = strings.TrimSpace(rule.ID)
		rule.Trigger.Event = strings.TrimSpace(rule.Trigger.Event)
		rule.Trigger.Lane = strings.TrimSpace(rule.Trigger.Lane)
		rule.Action.Type = strings.TrimSpace(rule.Action.Type)
		rule.Action.Role = strings.TrimSpace(rule.Action.Role)
		rule.Action.To = strings.TrimSpace(rule.Action.To)
		rule.Action.CreatesIn = strings.TrimSpace(rule.Action.CreatesIn)
		workflow.Rules[index] = rule
		if !validWorkflowID(rule.ID) {
			return ResolvedWorkflow{}, fmt.Errorf("%s.id %q must use lowercase letters, numbers, and underscores", path, rule.ID)
		}
		if _, exists := seenIDs[rule.ID]; exists {
			return ResolvedWorkflow{}, fmt.Errorf("%s duplicates rule id %q", path, rule.ID)
		}
		seenIDs[rule.ID] = struct{}{}
		event := rule.Trigger.Event
		laneID := rule.Trigger.Lane
		actionType := rule.Action.Type
		triggerKey := event + ":" + laneID
		if previous, exists := seenTriggers[triggerKey]; exists {
			return ResolvedWorkflow{}, fmt.Errorf("%s and rule %q use the same trigger; compose their behavior through explicit lane transitions", path, previous)
		}
		seenTriggers[triggerKey] = rule.ID
		switch event {
		case WorkflowEventLaneEntered:
			lane, exists := workflow.Lanes[laneID]
			if !exists {
				return ResolvedWorkflow{}, fmt.Errorf("%s.trigger.lane references undefined lane %q", path, laneID)
			}
			switch actionType {
			case WorkflowActionRunRole:
				if err := rejectActionFields(path, rule.Action, "role", "creates_in", "max_qa_rejections", "transitions"); err != nil {
					return ResolvedWorkflow{}, err
				}
				lane.Role = strings.TrimSpace(rule.Action.Role)
				if lane.Role == "" {
					return ResolvedWorkflow{}, fmt.Errorf("%s.action.role is required for %q", path, WorkflowActionRunRole)
				}
				lane.CreatesIn = strings.TrimSpace(rule.Action.CreatesIn)
				lane.MaxQARejections = rule.Action.MaxQARejections
				lane.Transitions = cloneStringMap(rule.Action.Transitions)
			case WorkflowActionPublishPR:
				if err := rejectActionFields(path, rule.Action); err != nil {
					return ResolvedWorkflow{}, err
				}
				lane.OnEnter = WorkflowActionPublishPR
			default:
				return ResolvedWorkflow{}, fmt.Errorf("%s.action.type %q is not supported for %s", path, actionType, WorkflowEventLaneEntered)
			}
			workflow.Lanes[laneID] = lane
		case WorkflowEventPRMerged, WorkflowEventPRClosed, WorkflowEventPRChecksFailed:
			if laneID != "" {
				return ResolvedWorkflow{}, fmt.Errorf("%s.trigger.lane is supported only for %s", path, WorkflowEventLaneEntered)
			}
			if actionType != WorkflowActionTransition {
				return ResolvedWorkflow{}, fmt.Errorf("%s.action.type must be %q for %q", path, WorkflowActionTransition, event)
			}
			if err := rejectActionFields(path, rule.Action, "to"); err != nil {
				return ResolvedWorkflow{}, err
			}
			workflow.Events = append(workflow.Events, WorkflowEvent{On: event, To: strings.TrimSpace(rule.Action.To)})
		case WorkflowEventPROutOfDate:
			if laneID != "" {
				return ResolvedWorkflow{}, fmt.Errorf("%s.trigger.lane is supported only for %s", path, WorkflowEventLaneEntered)
			}
			if actionType != WorkflowActionUpdateBranch {
				return ResolvedWorkflow{}, fmt.Errorf("%s.action.type must be %q for %q", path, WorkflowActionUpdateBranch, event)
			}
			if err := rejectActionFields(path, rule.Action, "require_review", "transitions"); err != nil {
				return ResolvedWorkflow{}, err
			}
			workflow.Events = append(workflow.Events, WorkflowEvent{
				On: event, Action: WorkflowActionUpdateBranch, RequireReview: rule.Action.RequireReview,
				Transitions: cloneStringMap(rule.Action.Transitions),
			})
		default:
			return ResolvedWorkflow{}, fmt.Errorf("%s.trigger.event has unsupported event %q", path, event)
		}
	}
	if len(workflow.Rules) == 0 {
		return ResolvedWorkflow{}, errors.New("workflow.rules must define at least one trigger and action")
	}
	return workflow, nil
}

func rejectActionFields(path string, action WorkflowAction, allowed ...string) error {
	permit := map[string]bool{}
	for _, field := range allowed {
		permit[field] = true
	}
	present := []struct {
		name string
		set  bool
	}{
		{name: "role", set: strings.TrimSpace(action.Role) != ""},
		{name: "to", set: strings.TrimSpace(action.To) != ""},
		{name: "creates_in", set: strings.TrimSpace(action.CreatesIn) != ""},
		{name: "max_qa_rejections", set: action.MaxQARejections != 0},
		{name: "require_review", set: action.RequireReview != nil},
		{name: "transitions", set: len(action.Transitions) != 0},
	}
	for _, field := range present {
		if field.set && !permit[field.name] {
			return fmt.Errorf("%s.action.%s is not supported by action type %q", path, field.name, action.Type)
		}
	}
	return nil
}

func validateImplementerLadder(c Config) error {
	if len(c.ImplementerLadder) == 0 {
		return nil
	}
	if len(c.ImplementerLadder) < 2 {
		return errors.New("implementer_ladder must contain at least two roles or be omitted")
	}
	workflow := c.resolvedWorkflow()
	primary := strings.TrimSpace(workflow.Lanes[workflow.ReadyLane].Role)
	if c.RoleContract(primary) != WorkRoleImplementer {
		return errors.New("implementer_ladder requires ready_lane to run an implementer role")
	}
	seen := map[string]struct{}{}
	for index, rawRole := range c.ImplementerLadder {
		role := strings.TrimSpace(rawRole)
		if role == "" {
			return fmt.Errorf("implementer_ladder[%d] cannot be blank", index)
		}
		if index == 0 && role != primary {
			return fmt.Errorf("implementer_ladder[0] must be the ready_lane implementer role %q", primary)
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
	for _, lane := range workflow.Lanes {
		if c.RoleContract(lane.Role) != WorkRoleReviewer {
			continue
		}
		retryLane, exists := workflow.Lanes[lane.Transitions[WorkflowOutcomeRejected]]
		if !exists || strings.TrimSpace(retryLane.Role) != primary {
			continue
		}
		if lane.MaxQARejections > maxAttempts {
			maxAttempts = lane.MaxQARejections
		}
	}
	if maxAttempts == 0 {
		return fmt.Errorf("implementer_ladder requires a reviewer rejection transition back to implementer role %q", primary)
	}
	if len(c.ImplementerLadder) > maxAttempts {
		return fmt.Errorf("implementer_ladder has %d roles but max_qa_rejections permits at most %d implementation attempts", len(c.ImplementerLadder), maxAttempts)
	}
	return nil
}

func validateWorkflowEvents(c Config, workflow ResolvedWorkflow) error {
	seen := map[string]struct{}{}
	for index, event := range workflow.Events {
		if _, exists := seen[event.On]; exists {
			return fmt.Errorf("compiled workflow event %d duplicates %q", index, event.On)
		}
		seen[event.On] = struct{}{}
		switch event.On {
		case WorkflowEventPRMerged, WorkflowEventPRClosed:
			lane, exists := workflow.Lanes[event.To]
			if !exists {
				return fmt.Errorf("workflow %s transition references undefined lane %q", event.On, event.To)
			}
			if event.To == workflow.ActiveLane {
				return fmt.Errorf("workflow %s transition cannot target active_lane", event.On)
			}
			if lane.Role != "" || lane.OnEnter != "" {
				return fmt.Errorf("workflow %s transition must target a lane without an automatic action", event.On)
			}
		case WorkflowEventPRChecksFailed:
			lane, exists := workflow.Lanes[event.To]
			if !exists {
				return fmt.Errorf("workflow %s transition references undefined lane %q", event.On, event.To)
			}
			if event.To == workflow.ActiveLane || c.RoleContract(lane.Role) != WorkRoleImplementer {
				return fmt.Errorf("workflow %s transition must target an implementer lane", event.On)
			}
		case WorkflowEventPROutOfDate:
			if event.Action != WorkflowActionUpdateBranch {
				return fmt.Errorf("workflow %s action must be %q", event.On, WorkflowActionUpdateBranch)
			}
			if event.RequireReview == nil {
				return fmt.Errorf("workflow %s action require_review is required", event.On)
			}
			if !*event.RequireReview {
				return fmt.Errorf("workflow %s action require_review must be true so base refreshes complete QA before publication", event.On)
			}
			for _, outcome := range []string{"updated", "conflict", WorkflowOutcomeError} {
				target := event.Transitions[outcome]
				if _, exists := workflow.Lanes[target]; !exists {
					return fmt.Errorf("workflow %s action transitions.%s references undefined lane %q", event.On, outcome, target)
				}
				if target == workflow.ActiveLane {
					return fmt.Errorf("workflow %s action transitions.%s cannot target active_lane", event.On, outcome)
				}
				if outcome != WorkflowOutcomeError && c.RoleContract(workflow.Lanes[target].Role) != WorkRoleImplementer {
					return fmt.Errorf("workflow %s action transitions.%s must target an implementer lane", event.On, outcome)
				}
			}
		default:
			return fmt.Errorf("compiled workflow has unsupported event %q", event.On)
		}
	}
	return nil
}

func validateAgentSuccessGraph(workflow ResolvedWorkflow) error {
	const (
		unvisited = iota
		visiting
		visited
	)
	state := map[string]int{}
	var visit func(string) error
	visit = func(laneID string) error {
		if state[laneID] == visiting {
			return fmt.Errorf("workflow run_role success transitions contain an automatic cycle through lane %q", laneID)
		}
		if state[laneID] == visited {
			return nil
		}
		state[laneID] = visiting
		lane := workflow.Lanes[laneID]
		targetID := lane.Transitions[WorkflowOutcomeSuccess]
		if target, exists := workflow.Lanes[targetID]; exists && target.Role != "" {
			if err := visit(targetID); err != nil {
				return err
			}
		}
		state[laneID] = visited
		return nil
	}
	for laneID, lane := range workflow.Lanes {
		if lane.Role != "" {
			if err := visit(laneID); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePublicationWorkflow(workflow ResolvedWorkflow, publicationLane string) error {
	if publicationLane == "" {
		return nil
	}
	required := []string{WorkflowEventPRMerged, WorkflowEventPRClosed, WorkflowEventPRChecksFailed, WorkflowEventPROutOfDate}
	events := make(map[string]WorkflowEvent, len(workflow.Events))
	for _, event := range workflow.Events {
		events[event.On] = event
	}
	for _, name := range required {
		if _, exists := events[name]; !exists {
			return fmt.Errorf("workflow with publish_pull_request requires trigger event %q", name)
		}
	}
	if events[WorkflowEventPRMerged].To == events[WorkflowEventPRClosed].To {
		return errors.New("pull_request.merged and pull_request.closed must target different lanes so a closed pull request cannot satisfy dependencies")
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
		result.Lanes[id] = lane
	}
	result.Rules = cloneWorkflowRules(input.Rules)
	return result
}

func cloneWorkflowRules(input []WorkflowRule) []WorkflowRule {
	result := make([]WorkflowRule, len(input))
	for index, rule := range input {
		rule.Action.Transitions = cloneStringMap(rule.Action.Transitions)
		result[index] = rule
	}
	return result
}

func cloneResolvedWorkflow(input ResolvedWorkflow) ResolvedWorkflow {
	result := input
	result.Lanes = make(map[string]ResolvedWorkflowLane, len(input.Lanes))
	for id, lane := range input.Lanes {
		lane.Transitions = cloneStringMap(lane.Transitions)
		result.Lanes[id] = lane
	}
	result.Rules = cloneWorkflowRules(input.Rules)
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
