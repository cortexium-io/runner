package runner

const (
	MaxGeneralTaskAttempts = 32768
	TaskWaitingChildren    = "waiting_children"
	TaskWaitingInput       = "waiting_input"
)

// TaskGraphWaiting distinguishes automatic child progress from a question or
// explicit pause that needs an operator. Execution observation continues in both.
func TaskGraphWaiting(r Run) bool {
	return r.TaskGraph != nil && r.TaskGraph.Phase == TaskWaitingChildren && !r.TaskGraph.Paused && !r.Terminal()
}

// TaskPolicy permits role-assigned child work within one retained request. It
// selects existing role definitions; model output never defines a new executor.
type TaskPolicy struct {
	Roles         []string `json:"roles"`
	MaxTasks      int      `json:"max_tasks"`
	MaxDepth      int      `json:"max_depth"`
	MaxRevisions  int      `json:"max_revisions"`
	MaxConcurrent int      `json:"max_concurrent"`
}

type RoleTask struct {
	ID         string           `json:"id"`
	Role       string           `json:"role"`
	Brief      string           `json:"brief"`
	Inputs     map[string]Value `json:"inputs,omitempty"`
	Acceptance []string         `json:"acceptance,omitempty"`
	DependsOn  []string         `json:"depends_on,omitempty"`
	WritePaths []string         `json:"write_paths,omitempty"`
}

// TaskAction is an explicit domain result, not a command or permission grant.
// A revision names affected settled children in Replace; unrelated children and
// every previous execution remain retained.
type TaskAction struct {
	Kind     string     `json:"kind"`
	Reason   string     `json:"reason,omitempty"`
	Tasks    []RoleTask `json:"tasks,omitempty"`
	Replace  []string   `json:"replace,omitempty"`
	Question string     `json:"question,omitempty"`
}

type RoleTaskLink struct {
	Spec       RoleTask        `json:"spec"`
	SpecSource *TaskSpecSource `json:"spec_source,omitempty"`
	RunID      string          `json:"run_id"`
	Retired    bool            `json:"retired,omitempty"`
}

type RoleTaskResult struct {
	ID      string           `json:"id"`
	RunID   string           `json:"run_id"`
	Role    string           `json:"role"`
	Status  string           `json:"status"`
	Reason  string           `json:"reason,omitempty"`
	Outputs map[string]Value `json:"outputs,omitempty"`
}

// TaskResultRef retains identity only. The child run owns its accepted outputs
// and status; task contexts resolve those canonical records when dispatched.
type TaskResultRef struct {
	ID    string `json:"id"`
	RunID string `json:"run_id"`
}

type TaskGraphState struct {
	AllowanceGrants []TaskAllowanceGrant     `json:"allowance_grants,omitempty"`
	Recoveries      []TaskRecoveryAudit      `json:"recoveries,omitempty"`
	Lineage         *TaskContinuationLineage `json:"lineage,omitempty"`
	RootRunID       string                   `json:"root_run_id"`
	ParentRunID     string                   `json:"parent_run_id,omitempty"`
	TaskID          string                   `json:"task_id"`
	Role            string                   `json:"role,omitempty"`
	Brief           string                   `json:"brief"`
	Acceptance      []string                 `json:"acceptance,omitempty"`
	DependsOn       []string                 `json:"depends_on,omitempty"`
	WritePaths      []string                 `json:"write_paths,omitempty"`
	Children        []RoleTaskLink           `json:"children,omitempty"`
	Results         []TaskResultRef          `json:"results,omitempty"`
	Phase           string                   `json:"phase,omitempty"`
	Question        string                   `json:"question,omitempty"`
	Answer          string                   `json:"answer,omitempty"`
	Directive       string                   `json:"directive,omitempty"`
	Paused          bool                     `json:"paused,omitempty"`
	Revisions       int                      `json:"revisions"`
}

type TaskRoleContract struct {
	ID        string               `json:"id"`
	Purpose   string               `json:"purpose"`
	Inputs    map[string]string    `json:"inputs"`
	Outputs   map[string]string    `json:"outputs"`
	Profile   string               `json:"profile"`
	Harness   string               `json:"harness"`
	Mode      string               `json:"mode,omitempty"`
	Model     string               `json:"model,omitempty"`
	Reasoning string               `json:"reasoning,omitempty"`
	Context   string               `json:"context,omitempty"`
	Access    string               `json:"access,omitempty"`
	Tools     *CapabilitySelection `json:"tools,omitempty"`
	Skills    *CapabilitySelection `json:"skills,omitempty"`
	Network   bool                 `json:"network,omitempty"`
}

type TaskContext struct {
	AttemptBudget     *TaskAttemptBudget       `json:"attempt_budget,omitempty"`
	AllowanceGrant    *TaskAllowanceGrant      `json:"allowance_grant,omitempty"`
	Maintenance       *TaskRecoveryAudit       `json:"maintenance,omitempty"`
	Lineage           *TaskContinuationLineage `json:"lineage,omitempty"`
	RootRunID         string                   `json:"root_run_id"`
	ParentRunID       string                   `json:"parent_run_id,omitempty"`
	TaskID            string                   `json:"task_id"`
	Goal              string                   `json:"goal"`
	Brief             string                   `json:"brief"`
	Acceptance        []string                 `json:"acceptance,omitempty"`
	WritePaths        []string                 `json:"write_paths,omitempty"`
	Results           []RoleTaskResult         `json:"results,omitempty"`
	Directive         string                   `json:"directive,omitempty"`
	Answer            string                   `json:"answer,omitempty"`
	PreviousFailure   string                   `json:"previous_failure,omitempty"`
	Revision          int                      `json:"revision"`
	AllowedRoles      []TaskRoleContract       `json:"allowed_roles"`
	Policy            TaskPolicy               `json:"policy"`
	RemainingAttempts int                      `json:"remaining_attempts"`
	RemainingTasks    int                      `json:"remaining_tasks"`
	AvailableRunSlots int                      `json:"available_run_slots"`
}

type TaskInterventionRequest struct {
	RequestID string `json:"request_id"`
	RunID     string `json:"run_id"`
	Revision  int64  `json:"revision"`
	Action    string `json:"action"`
	Text      string `json:"text,omitempty"`
	Profile   string `json:"profile,omitempty"`
}
