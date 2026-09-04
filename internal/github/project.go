package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

const (
	MaxAssessmentIssues = 1000
	MaxProjectItems     = 10000
	// MaxPlanningBatchChildren is an emergency ceiling that bounds pathological
	// model output and GitHub staging loops. It is not planning or sizing advice.
	MaxPlanningBatchChildren = 1000
	PlanningApprovalPhase    = "planner_approval"
	transitionLockValue      = "v1"
)

type WorkItem struct {
	ID                        string   `json:"id"`
	DraftContentID            string   `json:"draft_content_id,omitempty"`
	Title                     string   `json:"title"`
	Body                      string   `json:"body,omitempty"`
	URL                       string   `json:"url,omitempty"`
	Repository                string   `json:"repository,omitempty"`
	IssueState                string   `json:"issue_state,omitempty"`
	Dependencies              []string `json:"dependencies,omitempty"`
	Status                    string   `json:"status"`
	Role                      string   `json:"role,omitempty"`
	Approval                  string   `json:"approval,omitempty"`
	Labels                    []string `json:"labels,omitempty"`
	Result                    string   `json:"result,omitempty"`
	Phase                     string   `json:"phase,omitempty"`
	Transition                string   `json:"transition,omitempty"`
	Activity                  string   `json:"activity,omitempty"`
	QAFailures                int      `json:"qa_failures,omitempty"`
	Branch                    string   `json:"branch,omitempty"`
	PullRequest               string   `json:"pull_request,omitempty"`
	QACommit                  string   `json:"qa_commit,omitempty"`
	PlanningSourceID          string   `json:"planning_source_id,omitempty"`
	PlanningSourceLane        string   `json:"planning_source_lane,omitempty"`
	PlanningSourceFingerprint string   `json:"planning_source_fingerprint,omitempty"`
	PlanningDestination       string   `json:"planning_destination,omitempty"`
	PlanningBatchFingerprint  string   `json:"planning_batch_fingerprint,omitempty"`
	PlanningBatchSize         int      `json:"planning_batch_size,omitempty"`
	PlanningItemIndex         int      `json:"planning_item_index,omitempty"`
	PlanningMetadataInvalid   bool     `json:"-"`
}

type PlannedDependency struct {
	ItemID string `json:"item_id"`
	Title  string `json:"title"`
}

type PlannedItem struct {
	Title                     string              `json:"title"`
	Repository                string              `json:"repository,omitempty"`
	Summary                   string              `json:"summary"`
	AcceptanceCriteria        []string            `json:"acceptance_criteria"`
	Verification              []string            `json:"verification"`
	Risks                     []string            `json:"risks"`
	NonGoals                  []string            `json:"non_goals"`
	Dependencies              []string            `json:"dependencies"`
	ResolvedDependencies      []PlannedDependency `json:"-"`
	DependencyIDsResolved     bool                `json:"-"`
	ProjectGoal               string              `json:"-"`
	ProjectSuccessCriteria    []string            `json:"-"`
	ProjectConstraints        []string            `json:"-"`
	ProjectSource             string              `json:"-"`
	PlanningSourceID          string              `json:"-"`
	PlanningSourceLane        string              `json:"-"`
	PlanningSourceFingerprint string              `json:"-"`
	PlanningDestination       string              `json:"-"`
	PlanningBatchFingerprint  string              `json:"-"`
	PlanningBatchSize         int                 `json:"-"`
	PlanningItemIndex         int                 `json:"-"`
}

type ProjectInspection struct {
	ProjectID            string `json:"project_id,omitempty"`
	BoardView            bool   `json:"board_view"`
	BoardLifecycleFields bool   `json:"board_lifecycle_fields"`
	StatusField          bool   `json:"status_field"`
	AssessmentStatus     bool   `json:"assessment_status"`
	BacklogStatus        bool   `json:"backlog_status"`
	ReadyStatus          bool   `json:"ready_status"`
	RunningStatus        bool   `json:"running_status"`
	QAStatus             bool   `json:"qa_status"`
	PRReadyStatus        bool   `json:"pr_ready_status"`
	BlockedStatus        bool   `json:"blocked_status"`
	DoneStatus           bool   `json:"done_status"`
	WorkflowStatuses     bool   `json:"workflow_statuses"`
	ResultField          bool   `json:"result_field"`
	ApprovalField        bool   `json:"approval_field"`
	PhaseField           bool   `json:"phase_field"`
	TransitionField      bool   `json:"transition_field"`
	ActivityField        bool   `json:"activity_field"`
	QAFailuresField      bool   `json:"qa_failures_field"`
	BranchField          bool   `json:"branch_field"`
	PullRequestField     bool   `json:"pull_request_field"`
	QACommitField        bool   `json:"qa_commit_field"`
	IntakeRepository     bool   `json:"intake_repository"`
	IntakeLabel          bool   `json:"intake_label"`
	SingleRunnerMVP      bool   `json:"single_runner_mvp"`
}

type AssessmentSyncResult struct {
	Discovered   int `json:"discovered"`
	Added        int `json:"added"`
	Reclassified int `json:"reclassified"`
	Routed       int `json:"routed"`
}

type Project struct {
	cfg       config.ProjectConfig
	run       subprocess.Runner
	authority *operatorAuthority

	mu     sync.Mutex
	schema githubProjectSchema
}

type githubProjectSchema struct {
	ProjectID string
	Fields    map[string]githubProjectField
}

type githubProjectField struct {
	ID       string
	Name     string
	Type     string
	DataType string
	Options  map[string]githubProjectOption
}

type githubProjectOption struct {
	ID   string
	Name string
}

type githubProjectView struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Layout        string `json:"layout"`
	Configuration struct {
		VisibleFields struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
			PageInfo projectPageInfo `json:"pageInfo"`
		} `json:"visibleFields"`
	} `json:"configuration"`
}

func NewProject(cfg config.ProjectConfig, run subprocess.Runner) *Project {
	if run == nil {
		run = subprocess.OSRunner{}
	}
	authority := newEphemeralOperatorAuthority()
	if len(cfg.ApprovalAuthorityKey) > 0 {
		authority = newOperatorAuthorityFromKey(cfg.ApprovalAuthorityKey)
	}
	return &Project{cfg: cfg, run: run, authority: authority}
}

func NewProjectWithRunnerAuthority(cfg config.ProjectConfig, run subprocess.Runner) (*Project, error) {
	if run == nil {
		run = subprocess.OSRunner{}
	}
	authority, err := newPersistentOperatorAuthority(cfg.RunnerID)
	if err != nil {
		return nil, err
	}
	return &Project{cfg: cfg, run: run, authority: authority}, nil
}

func (s *Project) Inspect(ctx context.Context) (ProjectInspection, error) {
	schema, err := s.loadSchema(ctx)
	if err != nil {
		return ProjectInspection{}, err
	}
	views, err := s.loadViews(ctx, schema.ProjectID)
	if err != nil {
		return ProjectInspection{}, err
	}
	status, statusOK := schema.field(s.statusFieldName())
	result, resultOK := schema.field(s.resultFieldName())
	resultOK = resultOK && projectFieldHasDataType(result, "TEXT")
	approval, approvalOK := schema.field(s.approvalFieldName())
	approvalOK = approvalOK && projectFieldHasDataType(approval, "TEXT")
	phase, phaseOK := schema.field(s.phaseFieldName())
	phaseOK = phaseOK && projectFieldHasDataType(phase, "TEXT")
	transition, transitionOK := schema.field(s.transitionFieldName())
	transitionOK = transitionOK && projectFieldHasDataType(transition, "TEXT")
	activity, activityOK := schema.field(s.activityFieldName())
	activityOK = activityOK && projectFieldHasDataType(activity, "TEXT")
	qaFailures, qaFailuresOK := schema.field(s.qaFailuresFieldName())
	qaFailuresOK = qaFailuresOK && projectFieldHasDataType(qaFailures, "NUMBER")
	branch, branchOK := schema.field(s.branchFieldName())
	branchOK = branchOK && projectFieldHasDataType(branch, "TEXT")
	pullRequest, pullRequestOK := schema.field(s.pullRequestFieldName())
	pullRequestOK = pullRequestOK && projectFieldHasDataType(pullRequest, "TEXT")
	qaCommit, qaCommitOK := schema.field(s.qaCommitFieldName())
	qaCommitOK = qaCommitOK && projectFieldHasDataType(qaCommit, "TEXT")
	assessmentOK := s.assessmentStatus() == "" || status.hasOption(s.assessmentStatus())
	intakeRepositoryOK, intakeLabelOK, err := s.inspectIntake(ctx)
	if err != nil {
		return ProjectInspection{}, err
	}
	return ProjectInspection{
		ProjectID: schema.ProjectID, BoardView: hasBoardView(views), BoardLifecycleFields: boardViewHasLifecycleFields(views, []string{phase.ID, transition.ID}, activity.ID, qaFailures.ID), StatusField: statusOK,
		AssessmentStatus: assessmentOK, BacklogStatus: status.hasOption(s.backlogStatus()), ReadyStatus: status.hasOption(s.readyStatus()), RunningStatus: status.hasOption(s.runningStatus()),
		QAStatus: status.hasOption(s.qaStatus()), PRReadyStatus: status.hasOption(s.prReadyStatus()), BlockedStatus: status.hasOption(s.blockedStatus()), DoneStatus: status.hasOption(s.doneStatus()), WorkflowStatuses: statusOK && !missingOptions(status, s.requiredStatuses()),
		ResultField: resultOK, ApprovalField: approvalOK, PhaseField: phaseOK, TransitionField: transitionOK, ActivityField: activityOK, QAFailuresField: qaFailuresOK,
		BranchField: branchOK, PullRequestField: pullRequestOK, QACommitField: qaCommitOK, IntakeRepository: intakeRepositoryOK, IntakeLabel: intakeLabelOK, SingleRunnerMVP: true,
	}, nil
}

func (s *Project) loadViews(ctx context.Context, projectID string) ([]githubProjectView, error) {
	query := `query($project_id:ID!){node(id:$project_id){... on ProjectV2{views(first:100){nodes{` + projectViewSelection + `}}}}}`
	result, err := s.gh(ctx, "api", "graphql", "-f", "query="+query, "-F", "project_id="+strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("inspect GitHub Project views: %w", commandFailure(err, result))
	}
	var payload struct {
		Data struct {
			Node struct {
				Views struct {
					Nodes []githubProjectView `json:"nodes"`
				} `json:"views"`
			} `json:"node"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return nil, fmt.Errorf("decode GitHub Project views: %w", err)
	}
	for _, view := range payload.Data.Node.Views.Nodes {
		if view.Configuration.VisibleFields.PageInfo.HasNextPage {
			return nil, fmt.Errorf("GitHub Project view %q exceeds the supported limit of 100 visible fields", strings.TrimSpace(view.Name))
		}
	}
	return payload.Data.Node.Views.Nodes, nil
}

const projectViewSelection = `id name layout configuration{visibleFields(first:100){nodes{... on ProjectV2Field{id} ... on ProjectV2IterationField{id} ... on ProjectV2MultiSelectField{id} ... on ProjectV2SingleSelectField{id}}pageInfo{hasNextPage endCursor}}}`

func hasBoardView(views []githubProjectView) bool {
	for _, view := range views {
		if strings.EqualFold(strings.TrimSpace(view.Layout), "BOARD_LAYOUT") {
			return true
		}
	}
	return false
}

func boardViewHasLifecycleFields(views []githubProjectView, hiddenFieldIDs []string, fieldIDs ...string) bool {
	for _, view := range views {
		if !strings.EqualFold(strings.TrimSpace(view.Layout), "BOARD_LAYOUT") {
			continue
		}
		visible := map[string]bool{}
		for _, field := range view.Configuration.VisibleFields.Nodes {
			visible[strings.TrimSpace(field.ID)] = true
		}
		ready := true
		for _, fieldID := range hiddenFieldIDs {
			fieldID = strings.TrimSpace(fieldID)
			if fieldID == "" || visible[fieldID] {
				ready = false
				break
			}
		}
		for _, fieldID := range fieldIDs {
			fieldID = strings.TrimSpace(fieldID)
			if fieldID == "" || !visible[fieldID] {
				ready = false
				break
			}
		}
		if ready {
			return true
		}
	}
	return false
}

func projectFieldHasDataType(field githubProjectField, dataType string) bool {
	return field.Type == "ProjectV2Field" && strings.EqualFold(strings.TrimSpace(field.DataType), dataType)
}

func (s *Project) Poll(ctx context.Context, limit int) ([]AuthorizedAction, error) {
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return nil, err
	}
	return s.ReadyItems(ctx, items, limit)
}

func (s *Project) ReadyItems(ctx context.Context, items []WorkItem, limit int) ([]AuthorizedAction, error) {
	if limit <= 0 {
		limit = 1
	}
	ready := make([]AuthorizedAction, 0, limit)
	index := newWorkItemIndex(items)
	for _, item := range items {
		if !s.agentStatus(item.Status) {
			continue
		}
		if strings.TrimSpace(item.Transition) != "" {
			continue
		}
		if item.PlanningMetadataInvalid {
			detail := "Runner dependency or planning metadata is hidden, malformed, or not canonical; maintainer reassessment is required."
			if err := s.reclassifyForApproval(ctx, item, detail); err != nil {
				return nil, err
			}
			continue
		}
		action, err := s.validateAction(item)
		if err != nil {
			adopted, adoptErr := s.adoptManualIntakeItem(ctx, item)
			if adoptErr != nil {
				return nil, adoptErr
			}
			if adopted != nil {
				action = *adopted
			} else {
				detail := "Runner approval is missing, invalid, or no longer matches the work item; maintainer reassessment is required before running approve again."
				if err := s.reclassifyForApproval(ctx, item, detail); err != nil {
					return nil, err
				}
				continue
			}
		}
		if !s.dependenciesSatisfiedIn(item, index) {
			continue
		}
		if !s.planningBatchReleasedIn(item, index, s.doneStatus()) {
			continue
		}
		ready = append(ready, action)
		if len(ready) == limit {
			break
		}
	}
	return ready, nil
}

// ReconcileDependencyActivities makes dependency backpressure visible without
// changing eligibility or adopting unsigned human intake. It writes only when
// an authenticated card enters or leaves the dependency-wait condition.
func (s *Project) ReconcileDependencyActivities(ctx context.Context, items []WorkItem) (int, error) {
	index := newWorkItemIndex(items)
	changed := 0
	for _, item := range items {
		if !s.agentStatus(item.Status) || strings.TrimSpace(item.Transition) != "" || len(item.Dependencies) == 0 {
			continue
		}
		action, err := s.validateAction(item)
		if err != nil {
			continue
		}
		desired := ""
		if !s.dependenciesSatisfiedIn(item, index) {
			desired = config.RunnerActivityWaitingForDependencies
		} else if strings.TrimSpace(item.Activity) != config.RunnerActivityWaitingForDependencies {
			continue
		}
		if strings.TrimSpace(item.Activity) == desired {
			continue
		}
		if err := s.UpdateActivity(ctx, action, desired); err != nil {
			return changed, fmt.Errorf("update dependency activity for item %s: %w", item.ID, err)
		}
		changed++
	}
	return changed, nil
}

// adoptManualIntakeItem treats a maintainer placing an ordinary card in Plan or
// the initial Ready lane as the authorization decision. This deliberately
// includes a status-only move from Blocked to Ready, which means retry through
// implementation while preserving the recorded result and QA failure count.
// Runner immediately binds that exact snapshot to its local authority; every
// later operation still validates the signed snapshot. Staged planning children
// cannot bypass their complete-batch release because their creation assertion
// is deliberately non-executable.
func (s *Project) adoptManualIntakeItem(ctx context.Context, item WorkItem) (*AuthorizedAction, error) {
	statusLane := s.laneIDForStatus(item.Status)
	if statusLane == "" || !s.manualIntakeLane(statusLane, item.Status) {
		return nil, nil
	}
	if strings.TrimSpace(item.Approval) == "" && item.PlanningBatchFingerprint != "" {
		return nil, nil
	}
	if strings.TrimSpace(item.Approval) != "" {
		if err := s.validateRecordedAction(item); err != nil {
			return nil, nil
		}
	}
	role := strings.TrimSpace(s.cfg.LaneRoles[statusLane])
	if role == "" {
		return nil, nil
	}
	issueBacked, err := s.ensureIssueBacked(ctx, []WorkItem{item})
	if err != nil {
		return nil, fmt.Errorf("prepare manually readied item %s for discussion: %w", item.ID, err)
	}
	item = issueBacked[0]
	action, err := s.signAction(item, role, statusLane)
	if err != nil {
		return nil, fmt.Errorf("authorize manually readied item %s: %w", item.ID, err)
	}
	if err := s.setApproval(ctx, item.ID, action.assertion); err != nil {
		return nil, fmt.Errorf("record authorization for manually readied item %s: %w", item.ID, err)
	}
	return &action, nil
}

func (s *Project) manualIntakeLane(laneID, status string) bool {
	if strings.EqualFold(strings.TrimSpace(status), s.readyStatus()) {
		return true
	}
	return laneID == strings.TrimSpace(s.cfg.InitialLaneID) && strings.TrimSpace(s.cfg.InitialRole) != ""
}

// Claim is intentionally a compare-then-update operation. The CLI prevents two
// local processes from polling one Project, but GitHub Projects do not expose an
// atomic claim across machines.
func (s *Project) Claim(ctx context.Context, expected AuthorizedAction, requestedPhase ...string) (AuthorizedAction, error) {
	items, err := s.ListItems(ctx)
	if err != nil {
		return AuthorizedAction{}, err
	}
	return s.ClaimFromSnapshot(ctx, expected, items, requestedPhase...)
}

// ClaimFromSnapshot performs claim validation against a fresh board snapshot.
// It lets a coordinator revalidate several selected cards with one Project
// read while preserving the same dependency and approval checks as Claim.
func (s *Project) ClaimFromSnapshot(ctx context.Context, expected AuthorizedAction, items []WorkItem, requestedPhase ...string) (AuthorizedAction, error) {
	expectedItem, err := expected.authorizedItem()
	if err != nil {
		return AuthorizedAction{}, err
	}
	index := newWorkItemIndex(items)
	for _, current := range items {
		if current.ID != expectedItem.ID {
			continue
		}
		action, validateErr := s.validateAction(current)
		if validateErr != nil {
			if s.agentStatus(current.Status) {
				_ = s.reclassifyForApproval(ctx, current, "Runner approval is invalid or stale; review the item and run approve again.")
			}
			return AuthorizedAction{}, validateErr
		}
		if !sameAuthorizedAction(expected, action) {
			return AuthorizedAction{}, fmt.Errorf("project item %s changed after polling; retry on a later poll", expectedItem.ID)
		}
		expectedStatus := strings.TrimSpace(expectedItem.Status)
		if !strings.EqualFold(strings.TrimSpace(current.Status), expectedStatus) {
			return AuthorizedAction{}, fmt.Errorf("project item %s is no longer in expected status %q", expectedItem.ID, expectedStatus)
		}
		if !s.agentStatus(expectedStatus) {
			return AuthorizedAction{}, fmt.Errorf("project item %s cannot be claimed from status %q", expectedItem.ID, expectedStatus)
		}
		if !s.dependenciesSatisfiedIn(current, index) {
			return AuthorizedAction{}, fmt.Errorf("project item %s dependencies no longer all have authenticated successful outcomes", expectedItem.ID)
		}
		if !s.planningBatchReleasedIn(current, index, s.doneStatus()) {
			return AuthorizedAction{}, fmt.Errorf("project item %s belongs to a planning batch that has not completed", expectedItem.ID)
		}
		phase := ""
		activity := ""
		if len(requestedPhase) > 0 {
			phase = strings.TrimSpace(requestedPhase[0])
		}
		if len(requestedPhase) > 1 {
			activity = strings.TrimSpace(requestedPhase[1])
		}
		if phase == "" {
			phase = strings.TrimSpace(expectedStatus)
		}
		if activity == "" {
			activity = config.RunnerActivityForRoleContract(action.Role)
		}
		next := current
		next.Status = s.runningStatus()
		next.Phase = phase
		next.Activity = activity
		next.Role = action.Role
		state, err := s.stateForStatus(next.Status)
		if err != nil {
			return AuthorizedAction{}, err
		}
		nextAction, err := s.signAction(next, action.Role, state)
		if err != nil {
			return AuthorizedAction{}, err
		}
		if err := s.beginTransition(ctx, current.ID); err != nil {
			return AuthorizedAction{}, fmt.Errorf("lock item before claim; retry on a later poll: %w", err)
		}
		if err := s.applyFieldUpdates(ctx, current.ID,
			textProjectField(s.phaseFieldName(), phase),
			textProjectField(s.activityFieldName(), activity),
			textProjectField(s.approvalFieldName(), nextAction.assertion),
			statusProjectField(s.statusFieldName(), s.runningStatus()),
		); err != nil {
			return AuthorizedAction{}, fmt.Errorf("commit authenticated claim; the item remains safely transition-locked: %w", err)
		}
		if err := s.finishTransition(ctx, current.ID); err != nil {
			return AuthorizedAction{}, fmt.Errorf("claim committed but its transition lock could not be cleared; a later poll will recover it: %w", err)
		}
		return nextAction, nil
	}
	return AuthorizedAction{}, fmt.Errorf("project item %s is no longer present", expectedItem.ID)
}

// Authorize refreshes one item and returns the centralized action object used
// by all privileged Project and repository operations.
func (s *Project) Authorize(ctx context.Context, item WorkItem) (AuthorizedAction, error) {
	current, err := s.itemByID(ctx, item.ID)
	if err != nil {
		return AuthorizedAction{}, fmt.Errorf("refresh Project state before privileged action: %w", err)
	}
	action, err := s.validateAction(current)
	if err != nil {
		return AuthorizedAction{}, err
	}
	if strings.TrimSpace(item.Approval) != "" && item.Approval != current.Approval {
		return AuthorizedAction{}, errors.New("Project action changed after it was loaded; reload it and try again")
	}
	return action, nil
}

// RefreshAction re-fetches Project state and proves that an already validated
// action still represents the exact same Runner-authorized operation.
func (s *Project) RefreshAction(ctx context.Context, expected AuthorizedAction) (AuthorizedAction, error) {
	return s.refreshAuthorizedAction(ctx, expected)
}

// RefreshDelegatedContent re-fetches the Project-backed issue or draft body
// immediately before assignment construction. The harness receives only the
// approval-bound snapshot, and a mutable source mismatch is parked for fresh
// operator approval before any harness can run.
func (s *Project) RefreshDelegatedContent(ctx context.Context, expected AuthorizedAction) (AuthorizedAction, DelegatedContent, error) {
	expectedItem, err := expected.authorizedItem()
	if err != nil {
		return AuthorizedAction{}, DelegatedContent{}, err
	}
	expectedContent := DelegatedContentFor(expectedItem)
	current, err := s.itemByID(ctx, expectedItem.ID)
	if err != nil {
		return AuthorizedAction{}, DelegatedContent{}, fmt.Errorf("refresh approved delegated content: %w", err)
	}
	currentContent := DelegatedContentFor(current)
	if currentContent.Digest != expectedContent.Digest {
		detail := "Approved delegated content changed before assignment construction; review the exact body and planning metadata, then run approve again."
		if reclassifyErr := s.reclassifyForApproval(ctx, current, detail); reclassifyErr != nil {
			return AuthorizedAction{}, DelegatedContent{}, errors.Join(errors.New("approved delegated content digest changed"), reclassifyErr)
		}
		return AuthorizedAction{}, DelegatedContent{}, errors.New("approved delegated content digest changed; item was returned to assessment before harness invocation")
	}
	authorized, err := s.validateAction(current)
	if err != nil {
		detail := "Approved source metadata changed before assignment construction; review the item and run approve again."
		if reclassifyErr := s.reclassifyForApproval(ctx, current, detail); reclassifyErr != nil {
			return AuthorizedAction{}, DelegatedContent{}, errors.Join(err, reclassifyErr)
		}
		return AuthorizedAction{}, DelegatedContent{}, err
	}
	if !sameAuthorizedAction(expected, authorized) {
		return AuthorizedAction{}, DelegatedContent{}, errors.New("Project action changed before assignment construction; reload the item and try again")
	}
	return authorized, DelegatedContent{Digest: expectedContent.Digest, BodySnapshot: expectedContent.BodySnapshot}, nil
}

func (s *Project) Transition(ctx context.Context, action AuthorizedAction, targetStatus, detail, phase string) error {
	return s.transition(ctx, action, targetStatus, detail, phase, false, nil, nil)
}

func (s *Project) TransitionImplementation(ctx context.Context, action AuthorizedAction, targetStatus, targetPhase, summary, branch string) error {
	return s.transition(ctx, action, targetStatus, summary, targetPhase, false, func(next *WorkItem) {
		next.Branch = strings.TrimSpace(branch)
	}, []projectFieldUpdate{textProjectField(s.branchFieldName(), branch)})
}

func (s *Project) TransitionRejection(ctx context.Context, action AuthorizedAction, targetStatus, targetPhase, summary string, failures int) error {
	return s.transition(ctx, action, targetStatus, summary, targetPhase, false, func(next *WorkItem) {
		next.QAFailures = failures
	}, []projectFieldUpdate{numberProjectField(s.qaFailuresFieldName(), failures)})
}

func (s *Project) TransitionPRReady(ctx context.Context, action AuthorizedAction, targetStatus, summary, branch, pullRequest, qaCommit string) error {
	if _, err := validatedPullRequestSelector(action.Item.Repository, pullRequest); err != nil {
		return fmt.Errorf("persist pull request: %w", err)
	}
	if !validGitObjectID(qaCommit) {
		return errors.New("persist QA snapshot: commit must be a full Git object id")
	}
	return s.transition(ctx, action, targetStatus, summary, "", false, func(next *WorkItem) {
		next.Activity = config.RunnerActivityAwaitingHumanReview
		if s.cfg.AutoMerge {
			next.Activity = config.RunnerActivityWaitingForCI
		}
		next.Branch = strings.TrimSpace(branch)
		next.PullRequest = strings.TrimSpace(pullRequest)
		next.QACommit = strings.TrimSpace(qaCommit)
	}, []projectFieldUpdate{
		textProjectField(s.branchFieldName(), branch),
		textProjectField(s.pullRequestFieldName(), pullRequest),
		textProjectField(s.qaCommitFieldName(), qaCommit),
	})
}

func (s *Project) ResetRejections(ctx context.Context, action AuthorizedAction, feedback, targetPhase string) error {
	if strings.TrimSpace(action.Item.PullRequest) != "" && strings.TrimSpace(feedback) == "" {
		feedback = "Human requested pull request revisions."
	}
	return s.transition(ctx, action, action.Item.Status, feedback, targetPhase, strings.TrimSpace(action.Item.PullRequest) != "", func(next *WorkItem) {
		next.QAFailures = 0
	}, []projectFieldUpdate{numberProjectField(s.qaFailuresFieldName(), 0)})
}

func (s *Project) TransitionAfterBranchUpdate(ctx context.Context, action AuthorizedAction, targetStatus, targetPhase, detail string) error {
	return s.transition(ctx, action, targetStatus, detail, targetPhase, false, func(next *WorkItem) {
		next.QAFailures = 0
	}, []projectFieldUpdate{numberProjectField(s.qaFailuresFieldName(), 0)})
}

func (s *Project) TransitionAutomaticRetry(ctx context.Context, action AuthorizedAction, targetStatus, targetPhase, detail string) error {
	return s.transition(ctx, action, targetStatus, detail, targetPhase, false, func(next *WorkItem) {
		next.Activity = config.RunnerActivityWaitingForHarness
	}, nil)
}

// TransitionChecksFailed returns an integration candidate to implementation
// without treating repository CI as an Agent QA rejection. The existing pull
// request, branch, reviewed commit, and rejection count remain available to the
// implementer and the next publication attempt. Runner recreates the
// deterministic worktree if publication cleanup already removed it.
func (s *Project) TransitionChecksFailed(ctx context.Context, action AuthorizedAction, targetStatus, targetPhase, detail string) error {
	return s.transition(ctx, action, targetStatus, detail, targetPhase, false, func(next *WorkItem) {
		next.Activity = config.RunnerActivityCIFailed
	}, nil)
}

func (s *Project) UpdateActivity(ctx context.Context, action AuthorizedAction, activity string) error {
	activity = strings.TrimSpace(activity)
	if strings.TrimSpace(action.Item.Activity) == activity {
		return nil
	}
	current, err := s.refreshAuthorizedAction(ctx, action)
	if err != nil {
		return err
	}
	next := current.Item
	next.Activity = activity
	state, err := s.stateForStatus(next.Status)
	if err != nil {
		return err
	}
	role, err := s.roleForNextState(current, next, state)
	if err != nil {
		return err
	}
	nextAction, err := s.signAction(next, role, state)
	if err != nil {
		return err
	}
	if err := s.beginTransition(ctx, current.Item.ID); err != nil {
		return fmt.Errorf("lock item before activity update; reload it and retry: %w", err)
	}
	if err := s.applyFieldUpdates(ctx, current.Item.ID,
		textProjectField(s.activityFieldName(), activity),
		textProjectField(s.approvalFieldName(), nextAction.assertion),
	); err != nil {
		return fmt.Errorf("update authenticated Project activity; the item remains safely transition-locked: %w", err)
	}
	if err := s.finishTransition(ctx, current.Item.ID); err != nil {
		return fmt.Errorf("Project activity committed but its transition lock could not be cleared; a later poll will recover it: %w", err)
	}
	return nil
}

func (s *Project) transition(ctx context.Context, expected AuthorizedAction, targetStatus, detail, phase string, pullRequestFeedback bool, mutate func(*WorkItem), extraUpdates []projectFieldUpdate) error {
	current, err := s.refreshAuthorizedAction(ctx, expected)
	if err != nil {
		return err
	}
	next := current.Item
	next.Status = strings.TrimSpace(targetStatus)
	next.Phase = strings.TrimSpace(phase)
	next.Activity = ""
	if strings.TrimSpace(detail) != "" {
		published := ""
		if pullRequestFeedback {
			published, err = pullRequestFeedbackProjectResult(current.Item.Repository, current.Item.PullRequest)
		} else {
			published, err = runnerProjectResult(detail)
		}
		if err != nil {
			return err
		}
		next.Result = canonicalProjectResult(published)
	}
	if mutate != nil {
		mutate(&next)
	}
	state, err := s.stateForStatus(next.Status)
	if err != nil {
		return err
	}
	role, err := s.roleForNextState(current, next, state)
	if err != nil {
		return err
	}
	nextAction, err := s.signAction(next, role, state)
	if err != nil {
		return err
	}
	if err := s.beginTransition(ctx, current.Item.ID); err != nil {
		return fmt.Errorf("lock item before Project transition; reload it and retry: %w", err)
	}
	updates := append([]projectFieldUpdate(nil), extraUpdates...)
	if strings.TrimSpace(detail) != "" {
		updates = append(updates, textProjectField(s.resultFieldName(), next.Result))
	}
	updates = append(updates, textProjectField(s.phaseFieldName(), next.Phase))
	currentActivity := strings.TrimSpace(current.Item.Activity)
	nextActivity := strings.TrimSpace(next.Activity)
	if currentActivity != nextActivity {
		updates = append(updates, textProjectField(s.activityFieldName(), nextActivity))
	}
	updates = append(updates,
		textProjectField(s.approvalFieldName(), nextAction.assertion),
		statusProjectField(s.statusFieldName(), next.Status),
	)
	if err := s.applyFieldUpdates(ctx, current.Item.ID, updates...); err != nil {
		return fmt.Errorf("update authenticated Project action; the item remains safely transition-locked: %w", err)
	}
	if err := s.finishTransition(ctx, current.Item.ID); err != nil {
		return fmt.Errorf("Project transition committed but its lock could not be cleared; a later poll will recover it: %w", err)
	}
	return nil
}

func (s *Project) LifecycleItems(ctx context.Context) ([]WorkItem, error) {
	if _, err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	return s.ListItems(ctx)
}

func (s *Project) RecoverInterrupted(ctx context.Context) (int, error) {
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return 0, err
	}
	return s.RecoverInterruptedFrom(ctx, items)
}

func (s *Project) RecoverInterruptedFrom(ctx context.Context, items []WorkItem) (int, error) {
	recovered := 0
	recoveredDirectBatches := map[string]bool{}
	for _, item := range items {
		if strings.TrimSpace(item.Transition) != "" {
			unlocked := item
			unlocked.Transition = ""
			if _, actionErr := s.validateActionAssertion(unlocked, true); actionErr != nil {
				if err := s.reclassifyForApproval(ctx, item, "Interrupted Project transition has incomplete Runner authority; review it and run approve again."); err != nil {
					return recovered, err
				}
				if err := s.finishTransition(ctx, item.ID); err != nil {
					return recovered, fmt.Errorf("clear recovered Project transition lock: %w", err)
				}
				recovered++
				continue
			}
			if err := s.finishTransition(ctx, item.ID); err != nil {
				return recovered, fmt.Errorf("clear completed Project transition lock: %w", err)
			}
			item = unlocked
			recovered++
		}
		if staged, _, _, parseErr := parsePlanningBatchAssertion(item.Approval); parseErr == nil && staged.State == batchStagedState {
			if staged.SourceID == "direct:"+staged.BatchFingerprint {
				if recoveredDirectBatches[item.Approval] {
					continue
				}
				children := make([]WorkItem, 0, staged.BatchSize)
				for _, candidate := range items {
					if strings.TrimSpace(candidate.PlanningSourceID) != "" || strings.TrimSpace(candidate.PlanningBatchFingerprint) != staged.BatchFingerprint {
						continue
					}
					if !strings.EqualFold(strings.TrimSpace(candidate.Status), s.assessmentStatus()) || HasRuntimeActionState(candidate) {
						return recovered, fmt.Errorf("recover direct staged planning batch %s: child %s changed or contains runtime action state", staged.BatchFingerprint, candidate.ID)
					}
					children = append(children, candidate)
				}
				if _, err := s.validateDirectStaging(children, false); err != nil {
					return recovered, fmt.Errorf("recover direct staged planning batch %s: %w", staged.BatchFingerprint, err)
				}
				recoveredDirectBatches[item.Approval] = true
				continue
			}
			resumed, err := s.recoverStagedPlanningApproval(ctx, item, items)
			if err != nil {
				return recovered, fmt.Errorf("recover staged planning source %s: %w", item.ID, err)
			}
			if resumed {
				recovered++
			}
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(item.Status), s.runningStatus()) || strings.TrimSpace(item.Phase) == "" {
			continue
		}
		action, authorizeErr := s.Authorize(ctx, item)
		if authorizeErr != nil {
			if err := s.reclassifyForApproval(ctx, item, "Interrupted item has invalid Runner authority; review it and run approve again."); err != nil {
				return recovered, err
			}
			continue
		}
		target := s.cfg.LaneStatuses[strings.TrimSpace(item.Phase)]
		if target == "" {
			target = s.readyStatus()
		}
		detail := fmt.Sprintf("Recovered an interrupted Runner %s phase; the work has been returned to %s.", item.Phase, target)
		if err := s.Transition(ctx, action, target, detail, ""); err != nil {
			return recovered, err
		}
		recovered++
	}
	return recovered, nil
}

func (s *Project) recoverStagedPlanningApproval(ctx context.Context, source WorkItem, items []WorkItem) (bool, error) {
	children := make([]WorkItem, 0)
	for _, item := range items {
		if strings.TrimSpace(item.PlanningSourceID) != strings.TrimSpace(source.ID) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(item.Status), s.assessmentStatus()) || HasRuntimeActionState(item) {
			return false, fmt.Errorf("planning child %s changed or contains partial authority", item.ID)
		}
		children = append(children, item)
	}
	provenance, err := s.validatePlanningBatch(source.Approval, source, children, batchStagedState)
	if err != nil {
		return false, err
	}
	status := strings.TrimSpace(source.Status)
	if !strings.EqualFold(status, s.runningStatus()) && !strings.EqualFold(status, s.assessmentStatus()) {
		return false, fmt.Errorf("authenticated planning source moved to unexpected status %q", source.Status)
	}
	phase := strings.TrimSpace(source.Phase)
	if phase != "" && phase != provenance.SourceLane && phase != PlanningApprovalPhase {
		return false, fmt.Errorf("authenticated planning source moved to unexpected phase %q", source.Phase)
	}
	for index := range children {
		if strings.TrimSpace(children[index].Approval) == "" {
			continue
		}
		if err := s.validateStagedChild(children[index]); err != nil {
			return false, fmt.Errorf("planning child %s has unexpected partial authority", children[index].ID)
		}
		if err := s.clearApproval(ctx, children[index].ID); err != nil {
			return false, fmt.Errorf("clear recovered planning child creation provenance: %w", err)
		}
		children[index].Approval = ""
	}
	if strings.EqualFold(status, s.assessmentStatus()) && phase == PlanningApprovalPhase {
		return false, nil
	}
	if !strings.EqualFold(status, s.assessmentStatus()) {
		if err := s.setStatus(ctx, source.ID, s.assessmentStatus()); err != nil {
			return false, fmt.Errorf("park authenticated planning source: %w", err)
		}
	}
	if strings.TrimSpace(source.Activity) != "" {
		if err := s.clearField(ctx, source.ID, s.activityFieldName()); err != nil {
			return false, fmt.Errorf("clear recovered planning activity: %w", err)
		}
	}
	detail := fmt.Sprintf("Planning completed and staged %d unapproved work items. Preview and explicitly approve the complete batch before release.", len(children))
	publishedDetail, err := runnerProjectResult(detail)
	if err != nil {
		return false, err
	}
	if err := s.setResult(ctx, source.ID, publishedDetail); err != nil {
		return false, fmt.Errorf("record recovered planning result: %w", err)
	}
	if phase != PlanningApprovalPhase {
		if err := s.setTextField(ctx, source.ID, s.phaseFieldName(), PlanningApprovalPhase); err != nil {
			return false, fmt.Errorf("mark recovered planning source for complete-batch approval: %w", err)
		}
	}
	return true, nil
}

// StagePlanningApproval replaces the claimed source authority with an
// authenticated assertion for the exact staged batch before changing any
// lifecycle field. A retry can therefore resume every later partial state
// without rerunning the planner or trusting public child metadata.
func (s *Project) StagePlanningApproval(ctx context.Context, expected AuthorizedAction, children []WorkItem, detail string) error {
	expectedSource, err := expected.authorizedItem()
	if err != nil {
		return fmt.Errorf("validate planning source before staging approval: %w", err)
	}
	items, err := s.ListItems(ctx)
	if err != nil {
		return fmt.Errorf("revalidate planning source and exact children before staging approval: %w", err)
	}
	currentSource, err := selectProjectItem(items, expectedSource.ID)
	if err != nil {
		return fmt.Errorf("revalidate planning source before staging approval: %w", err)
	}
	refreshedChildren := make([]WorkItem, 0, len(children))
	for index, expectedChild := range children {
		child, selectErr := selectProjectItem(items, expectedChild.ID)
		if selectErr != nil || !reflect.DeepEqual(child, expectedChild) || !strings.EqualFold(strings.TrimSpace(child.Status), s.assessmentStatus()) {
			return fmt.Errorf("planning child %d changed before authenticated staging; explicitly restage the batch", index+1)
		}
		refreshedChildren = append(refreshedChildren, child)
	}
	stagingAssertion := strings.TrimSpace(currentSource.Approval)
	currentAction, actionErr := s.validateAction(currentSource)
	if actionErr == nil && sameAuthorizedAction(expected, currentAction) {
		stagingAssertion, err = s.signPlanningBatch(currentSource, refreshedChildren, batchStagedState, "")
		if err != nil {
			return fmt.Errorf("authenticate exact staged planning batch: %w", err)
		}
	} else {
		if PlanningSourceFingerprint(currentSource) != PlanningSourceFingerprint(expectedSource) {
			return errors.New("planning source changed before interrupted staging could be resumed")
		}
		if _, err := s.validatePlanningBatch(stagingAssertion, currentSource, refreshedChildren, batchStagedState); err != nil {
			return fmt.Errorf("planning source is neither the authorized action nor the exact interrupted staged batch: %w", err)
		}
	}
	if currentSource.Approval != stagingAssertion {
		if err := s.setApproval(ctx, currentSource.ID, stagingAssertion); err != nil {
			return fmt.Errorf("record authenticated staged planning batch before lifecycle writes: %w", err)
		}
		currentSource.Approval = stagingAssertion
	}
	for index := range refreshedChildren {
		if strings.TrimSpace(refreshedChildren[index].Approval) == "" {
			continue
		}
		if err := s.validateStagedChild(refreshedChildren[index]); err != nil {
			return fmt.Errorf("planning child %d has unexpected authority before staging: %w", index+1, err)
		}
		if err := s.clearApproval(ctx, refreshedChildren[index].ID); err != nil {
			return fmt.Errorf("clear planning child %d creation provenance after authenticated batch staging: %w", index+1, err)
		}
		refreshedChildren[index].Approval = ""
	}
	if !strings.EqualFold(strings.TrimSpace(currentSource.Status), s.assessmentStatus()) {
		if err := s.setStatus(ctx, currentSource.ID, s.assessmentStatus()); err != nil {
			return fmt.Errorf("park planning source for complete-batch approval: %w", err)
		}
		currentSource.Status = s.assessmentStatus()
	}
	if strings.TrimSpace(currentSource.Activity) != "" {
		if err := s.clearField(ctx, currentSource.ID, s.activityFieldName()); err != nil {
			return fmt.Errorf("clear completed planning activity; the source remains safely in assessment: %w", err)
		}
		currentSource.Activity = ""
	}
	publishedDetail, err := runnerProjectResult(detail)
	if err != nil {
		return err
	}
	canonicalDetail := canonicalProjectResult(publishedDetail)
	if currentSource.Result != canonicalDetail {
		if err := s.setResult(ctx, currentSource.ID, canonicalDetail); err != nil {
			return fmt.Errorf("record staged planning result; the source remains safely in assessment: %w", err)
		}
		currentSource.Result = canonicalDetail
	}
	if strings.TrimSpace(currentSource.Phase) != PlanningApprovalPhase {
		if err := s.setTextField(ctx, currentSource.ID, s.phaseFieldName(), PlanningApprovalPhase); err != nil {
			return fmt.Errorf("mark planning source for complete-batch approval; the source remains safely in assessment: %w", err)
		}
	}
	return nil
}

// CreateStagedFrom creates a non-executable planning child only while the
// claimed parent action remains current. Parent authority is checked before
// creation and before the child is parked in assessment.
func (s *Project) CreateStagedFrom(ctx context.Context, parent AuthorizedAction, planned PlannedItem) (WorkItem, error) {
	if _, err := s.refreshAuthorizedAction(ctx, parent); err != nil {
		return WorkItem{}, fmt.Errorf("planning source authority changed before child creation; review the source and run approve again if needed: %w", err)
	}
	revalidateParent := func() error {
		if _, err := s.refreshAuthorizedAction(ctx, parent); err != nil {
			return fmt.Errorf("planning source authority changed while staging its child; the child remains safely in assessment; review the source and run approve again if needed: %w", err)
		}
		return nil
	}
	return s.createStaged(ctx, planned, revalidateParent)
}

func (s *Project) CreateStaged(ctx context.Context, planned PlannedItem) (WorkItem, error) {
	return s.createStaged(ctx, planned, nil)
}

// EnsureStaged repairs the one safe interrupted-creation state: the exact
// child exists without authority but its assessment status write did not
// complete. Any content, authority, or non-empty status change is rejected.
func (s *Project) EnsureStaged(ctx context.Context, expected WorkItem) (WorkItem, error) {
	items, err := s.ListItems(ctx)
	if err != nil {
		return WorkItem{}, err
	}
	current, err := selectProjectItem(items, expected.ID)
	if err != nil {
		return WorkItem{}, err
	}
	if !reflect.DeepEqual(current, expected) {
		return WorkItem{}, errors.New("staged planning child changed before interrupted creation could be resumed")
	}
	if err := s.validateStagedChild(current); err != nil {
		return WorkItem{}, err
	}
	if strings.EqualFold(strings.TrimSpace(current.Status), s.assessmentStatus()) {
		return current, nil
	}
	if strings.TrimSpace(current.Status) != "" {
		return WorkItem{}, fmt.Errorf("staged planning child moved to status %q and cannot be resumed", current.Status)
	}
	if err := s.setStatus(ctx, current.ID, s.assessmentStatus()); err != nil {
		return WorkItem{}, fmt.Errorf("resume staged planning child in assessment: %w", err)
	}
	current.Status = s.assessmentStatus()
	return current, nil
}

// ValidateStagedPlanningChild authenticates the exact, non-executable
// creation provenance written immediately after Runner creates a child.
func (s *Project) ValidateStagedPlanningChild(item WorkItem) error {
	return s.validateStagedChild(item)
}

// ValidateStagedPlanningChildBodies permits only the exact provisional or
// canonical body supplied by the caller while recovering the two writes that
// finalize one child.
func (s *Project) ValidateStagedPlanningChildBodies(item WorkItem, bodies ...string) error {
	for _, body := range bodies {
		candidate := item
		candidate.Body = body
		metadata := DecodePlannedItemMetadata(body)
		candidate.Dependencies = metadata.Dependencies
		if candidate.Repository == "" {
			candidate.Repository = metadata.Repository
		}
		candidate.PlanningSourceID = metadata.PlanningSourceID
		candidate.PlanningSourceLane = metadata.PlanningSourceLane
		candidate.PlanningSourceFingerprint = metadata.PlanningSourceFingerprint
		candidate.PlanningDestination = metadata.PlanningDestination
		candidate.PlanningBatchFingerprint = metadata.PlanningBatchFingerprint
		candidate.PlanningBatchSize = metadata.PlanningBatchSize
		candidate.PlanningItemIndex = metadata.PlanningItemIndex
		candidate.PlanningMetadataInvalid = false
		if err := s.validateStagedChild(candidate); err == nil {
			return nil
		}
	}
	return errors.New("staged planning child has no valid Runner provenance for its recoverable body state")
}

func (s *Project) validateDirectStaging(children []WorkItem, requireCompleteBatch bool) (string, error) {
	var batchAssertion string
	for _, child := range children {
		assertion := strings.TrimSpace(child.Approval)
		if signed, _, _, err := parsePlanningBatchAssertion(assertion); err == nil && signed.State == batchStagedState {
			if batchAssertion != "" && batchAssertion != assertion {
				return "", errors.New("direct planning children contain mixed staging generations")
			}
			batchAssertion = assertion
			continue
		}
		if requireCompleteBatch {
			return "", errors.New("direct planning child has no authenticated complete-batch staging provenance")
		}
		if err := s.validateStagedChild(child); err != nil {
			return "", err
		}
	}
	if batchAssertion == "" {
		if requireCompleteBatch {
			return "", errors.New("direct planning batch has no authenticated Runner staging provenance")
		}
		return "", nil
	}
	if _, err := s.validateDirectPlanningBatch(batchAssertion, children, batchStagedState); err != nil {
		return "", err
	}
	return batchAssertion, nil
}

// ValidateDirectPlanningBatchStaging rejects canonical-looking public
// metadata unless this Runner authenticated the exact complete ID-bound batch.
func (s *Project) ValidateDirectPlanningBatchStaging(children []WorkItem) error {
	_, err := s.validateDirectStaging(children, true)
	return err
}

// ValidateRecoverableDirectPlanningChildren accepts only Runner-authenticated
// per-child creation proofs or an exact interrupted complete-batch write.
func (s *Project) ValidateRecoverableDirectPlanningChildren(children []WorkItem) error {
	_, err := s.validateDirectStaging(children, false)
	return err
}

// AuthenticateDirectPlanningBatch replaces per-child creation proofs with one
// complete-batch assertion after every child ID and canonical body are known.
// Mixed partial writes of that exact assertion are safe to resume.
func (s *Project) AuthenticateDirectPlanningBatch(ctx context.Context, expected []WorkItem) ([]WorkItem, error) {
	if err := ValidatePlanningDependencies(expected); err != nil {
		return nil, err
	}
	itemIDs := make([]string, len(expected))
	for index := range expected {
		itemIDs[index] = expected[index].ID
	}
	current, err := s.LifecycleItemsByID(ctx, itemIDs)
	if err != nil {
		return nil, err
	}
	for index, child := range expected {
		if !reflect.DeepEqual(current[index], child) {
			return nil, fmt.Errorf("direct planning child %d changed before authenticated staging", index+1)
		}
	}
	assertion, err := s.validateDirectStaging(current, false)
	if err != nil {
		return nil, err
	}
	if assertion == "" {
		source, sourceErr := directPlanningBatchSource(current)
		if sourceErr != nil {
			return nil, sourceErr
		}
		assertion, err = s.signPlanningBatch(source, current, batchStagedState, "")
		if err != nil {
			return nil, fmt.Errorf("authenticate direct staged planning batch: %w", err)
		}
	}
	for index := range current {
		if current[index].Approval == assertion {
			continue
		}
		if err := s.setApproval(ctx, current[index].ID, assertion); err != nil {
			return current[:index], fmt.Errorf("record direct batch staging provenance for child %d of %d: %w", index+1, len(current), err)
		}
		current[index].Approval = assertion
	}
	return current, nil
}

// FinalizeStaged replaces the exact provisional body with its canonical,
// ID-bound representation while the child remains non-executable in assessment.
// It is idempotent so an interrupted batch finalization can safely resume.
func (s *Project) FinalizeStaged(ctx context.Context, expected WorkItem, planned PlannedItem) (WorkItem, error) {
	current, err := s.itemByID(ctx, expected.ID)
	if err != nil {
		return WorkItem{}, err
	}
	finalBody := FormatPlannedItemBody(planned)
	if !strings.EqualFold(strings.TrimSpace(current.Status), s.assessmentStatus()) || HasRuntimeActionState(current) {
		return WorkItem{}, errors.New("staged planning child is no longer safely in assessment and cannot be finalized")
	}
	if strings.TrimSpace(current.DraftContentID) == "" {
		return WorkItem{}, errors.New("staged planning child has no draft content identity")
	}
	if current.Body == finalBody {
		if !reflect.DeepEqual(current, expected) {
			return WorkItem{}, errors.New("finalized planning child changed before the batch could resume")
		}
		if err := s.validateStagedChild(current); err != nil {
			provisional := planned
			provisional.ResolvedDependencies = nil
			provisional.DependencyIDsResolved = false
			prior := current
			prior.Body = FormatPlannedItemBody(provisional)
			if priorErr := s.validateStagedChild(prior); priorErr != nil {
				return WorkItem{}, err
			}
			assertion, signErr := s.signAction(current, stagedChildRole, stagedChildState)
			if signErr != nil {
				return WorkItem{}, signErr
			}
			if err := s.setApproval(ctx, current.ID, assertion.assertion); err != nil {
				return WorkItem{}, fmt.Errorf("refresh finalized planning child provenance: %w", err)
			}
			current.Approval = assertion.assertion
		}
		return current, nil
	}
	if !reflect.DeepEqual(current, expected) {
		return WorkItem{}, errors.New("staged planning child changed before dependency IDs could be finalized")
	}
	if err := s.validateStagedChild(current); err != nil {
		finalized := current
		finalized.Body = finalBody
		finalized.PlanningMetadataInvalid = false
		finalized.Dependencies = make([]string, 0, len(planned.ResolvedDependencies))
		for _, dependency := range planned.ResolvedDependencies {
			finalized.Dependencies = append(finalized.Dependencies, strings.TrimSpace(dependency.ItemID))
		}
		if finalErr := s.validateStagedChild(finalized); finalErr != nil {
			return WorkItem{}, err
		}
	}
	next := current
	next.Body = finalBody
	next.PlanningMetadataInvalid = false
	next.Dependencies = make([]string, 0, len(planned.ResolvedDependencies))
	for _, dependency := range planned.ResolvedDependencies {
		next.Dependencies = append(next.Dependencies, strings.TrimSpace(dependency.ItemID))
	}
	assertion, err := s.signAction(next, stagedChildRole, stagedChildState)
	if err != nil {
		return WorkItem{}, err
	}
	if current.Approval != assertion.assertion {
		if err := s.setApproval(ctx, current.ID, assertion.assertion); err != nil {
			return WorkItem{}, fmt.Errorf("authenticate finalized planning child: %w", err)
		}
	}
	result, err := s.gh(ctx, "project", "item-edit", "--id", current.DraftContentID, "--body", finalBody)
	if err != nil {
		return WorkItem{}, fmt.Errorf("finalize staged GitHub Project item metadata: %w", commandFailure(err, result))
	}
	next.Approval = assertion.assertion
	if err := s.validateStagedChild(next); err != nil {
		return WorkItem{}, fmt.Errorf("authenticate finalized planning child: %w", err)
	}
	return next, nil
}

// CreateHumanWorkItem adds one ordinary, unsigned card to Plan or Ready. The
// status change is the human authorization event; ReadyItems binds the exact
// observed snapshot to Runner authority before any agent can claim it.
func (s *Project) CreateHumanWorkItem(ctx context.Context, title, body, targetStatus string) (WorkItem, error) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	targetStatus = strings.TrimSpace(targetStatus)
	if title == "" {
		return WorkItem{}, errors.New("work item requires a title")
	}
	if body == "" {
		return WorkItem{}, errors.New("work item requires a body")
	}
	if _, err := s.ensureSchema(ctx); err != nil {
		return WorkItem{}, err
	}
	laneID := s.laneIDForStatus(targetStatus)
	if laneID == "" || !s.manualIntakeLane(laneID, targetStatus) {
		return WorkItem{}, fmt.Errorf("GitHub Project status %q is not the configured Plan or Ready intake lane", targetStatus)
	}
	item, err := s.createDraftItem(ctx, title, body)
	if err != nil {
		return WorkItem{}, err
	}
	if err := s.setStatus(ctx, item.ID, targetStatus); err != nil {
		return item, fmt.Errorf("created GitHub Project item %s but could not move it to %q; move or remove the unscheduled item manually: %w", item.ID, targetStatus, err)
	}
	item.Status = targetStatus
	return item, nil
}

func (s *Project) createStaged(ctx context.Context, planned PlannedItem, revalidateSource func() error) (WorkItem, error) {
	title := strings.TrimSpace(planned.Title)
	if title == "" {
		return WorkItem{}, errors.New("planned item requires a title")
	}
	if revalidateSource != nil {
		if err := revalidateSource(); err != nil {
			return WorkItem{}, err
		}
	}
	body := FormatPlannedItemBody(planned)
	if _, present, metadataErr := decodePlannedItemMetadata(body); !present || (metadataErr != nil && !errors.Is(metadataErr, errPlanningDependencyIDsPending)) {
		return WorkItem{}, errors.New("planned item requires complete canonical Runner planning metadata")
	}
	created, err := s.createDraftItem(ctx, title, body)
	if err != nil {
		return WorkItem{}, fmt.Errorf("create staged GitHub Project item: %w", err)
	}
	item := WorkItem{
		ID: created.ID, Title: title, Body: body, Repository: strings.TrimSpace(planned.Repository),
		Dependencies: []string{}, Status: s.assessmentStatus(),
		PlanningSourceID: strings.TrimSpace(planned.PlanningSourceID), PlanningSourceLane: strings.TrimSpace(planned.PlanningSourceLane),
		PlanningSourceFingerprint: strings.TrimSpace(planned.PlanningSourceFingerprint), PlanningDestination: strings.TrimSpace(planned.PlanningDestination),
		PlanningBatchFingerprint: strings.TrimSpace(planned.PlanningBatchFingerprint),
		PlanningBatchSize:        planned.PlanningBatchSize, PlanningItemIndex: planned.PlanningItemIndex,
		PlanningMetadataInvalid: !planned.DependencyIDsResolved,
	}
	for _, dependency := range planned.ResolvedDependencies {
		item.Dependencies = append(item.Dependencies, strings.TrimSpace(dependency.ItemID))
	}
	assertion, err := s.signAction(item, stagedChildRole, stagedChildState)
	if err != nil {
		return item, fmt.Errorf("authenticate new staged planning child: %w", err)
	}
	if err := s.setApproval(ctx, created.ID, assertion.assertion); err != nil {
		return item, fmt.Errorf("authenticate new staged planning child; the unsigned item cannot be recovered as Runner-created: %w", err)
	}
	item.Approval = assertion.assertion
	if revalidateSource != nil {
		if err := revalidateSource(); err != nil {
			_ = s.setStatus(ctx, created.ID, s.assessmentStatus())
			return item, err
		}
	}
	if err := s.setStatus(ctx, created.ID, s.assessmentStatus()); err != nil {
		return item, fmt.Errorf("park new staged item in assessment: %w", err)
	}
	current, err := s.itemByID(ctx, created.ID)
	if err != nil {
		return item, fmt.Errorf("reload new staged item identity: %w", err)
	}
	if strings.TrimSpace(current.DraftContentID) == "" {
		return item, errors.New("new staged planning child has no draft content identity")
	}
	expected := item
	expected.DraftContentID = current.DraftContentID
	if !reflect.DeepEqual(current, expected) {
		return item, errors.New("new staged planning child changed before its draft content identity was loaded")
	}
	return current, nil
}

func (s *Project) createDraftItem(ctx context.Context, title, body string) (WorkItem, error) {
	result, err := s.gh(ctx, "project", "item-create", strconv.Itoa(s.cfg.Number), "--owner", strings.TrimSpace(s.cfg.Owner), "--title", title, "--body", body, "--format", "json")
	if err != nil {
		return WorkItem{}, commandFailure(err, result)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &created); err != nil || strings.TrimSpace(created.ID) == "" {
		return WorkItem{}, errors.New("GitHub Project item creation did not return an item id")
	}
	return WorkItem{ID: strings.TrimSpace(created.ID), Title: title, Body: body}, nil
}
