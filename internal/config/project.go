package config

import (
	"errors"
	"fmt"
	"strings"

	bundledskills "github.com/cortexium-io/runner/skills"
)

const ConfigVersion = 2

const MaxSupportedParallelism = 16

const GitHubProjectCapabilityID = "github_project"

const RunnerActivityFieldName = "Runner Activity"

const RunnerTransitionFieldName = "Runner Transition"

const (
	MergeMethodMerge  = "merge"
	MergeMethodRebase = "rebase"
	MergeMethodSquash = "squash"
)

func RunnerActivityForRoleContract(contract string) string {
	switch strings.TrimSpace(contract) {
	case WorkRolePlanner:
		return "Planning"
	case WorkRoleImplementer:
		return "Implementing"
	case WorkRoleReviewer:
		return "Reviewing"
	default:
		return "Running"
	}
}

type GitHubProjectConfig struct {
	Owner            string `json:"owner"`
	Number           int    `json:"number"`
	IntakeRepository string `json:"intake_repository,omitempty"`
	IntakeLabel      string `json:"intake_label,omitempty"`
	ResultField      string `json:"result_field,omitempty"`
	ApprovalField    string `json:"approval_field,omitempty"`
	PhaseField       string `json:"phase_field,omitempty"`
	TransitionField  string `json:"transition_field,omitempty"`
	QAFailuresField  string `json:"qa_failures_field,omitempty"`
	BranchField      string `json:"branch_field,omitempty"`
	PullRequestField string `json:"pull_request_field,omitempty"`
	QACommitField    string `json:"qa_commit_field,omitempty"`
	BaseBranch       string `json:"base_branch,omitempty"`
	RemoteName       string `json:"remote_name,omitempty"`
	AutoMerge        bool   `json:"auto_merge"`
	MergeMethod      string `json:"merge_method,omitempty"`
}

// ProjectConfig is the runtime Project contract derived from the persisted
// GitHubProjectConfig and the resolved workflow.
type ProjectConfig struct {
	GitHubProjectConfig
	ActivityField        string
	RunnerID             string
	ApprovalAuthorityKey []byte
	AssessmentStatus     string
	BacklogStatus        string
	ReadyStatus          string
	RunningStatus        string
	QAStatus             string
	PRReadyStatus        string
	BlockedStatus        string
	DoneStatus           string
	RequiredStatuses     []string
	AgentStatuses        []string
	LaneStatuses         map[string]string
	LaneRoles            map[string]string
	PlanningDestinations map[string]string
	InitialLaneID        string
	InitialRole          string
	ApprovalLaneID       string
	ActiveLaneID         string
}

func (c GitHubProjectConfig) ApprovalFieldName() string {
	return strings.TrimSpace(c.ApprovalField)
}

func (c GitHubProjectConfig) TransitionFieldName() string {
	if name := strings.TrimSpace(c.TransitionField); name != "" {
		return name
	}
	return RunnerTransitionFieldName
}

func (c Config) HasProject() bool {
	return c.GitHubProject != nil
}

func (c Config) Validate() error {
	if c.ConfigVersion != ConfigVersion {
		return fmt.Errorf("config_version must be %d", ConfigVersion)
	}
	if strings.TrimSpace(c.RunnerID) == "" {
		return errors.New("runner_id is required")
	}
	if c.GitHubProject == nil {
		return errors.New("github_project is required")
	}
	project := c.GitHubProject
	if strings.TrimSpace(project.Owner) == "" || project.Number <= 0 {
		return errors.New("github_project requires owner and a positive number")
	}
	if !ValidRepositoryName(project.IntakeRepository) {
		return errors.New("github_project.intake_repository must use owner/repository format")
	}
	if strings.TrimSpace(project.IntakeLabel) == "" {
		return errors.New("github_project.intake_label is required")
	}
	if strings.TrimSpace(c.ProjectDir) == "" {
		return errors.New("project_dir is required")
	}
	if value := strings.TrimSpace(project.RemoteName); value == "" {
		return errors.New("github_project.remote_name is required")
	} else if strings.HasPrefix(value, "-") || strings.ContainsAny(value, " \t\r\n") {
		return errors.New("github_project.remote_name must be a Git remote name without whitespace or a leading dash")
	}
	if value := strings.TrimSpace(project.BaseBranch); value == "" {
		return errors.New("github_project.base_branch is required")
	} else if strings.HasPrefix(value, "-") || strings.ContainsAny(value, " \t\r\n~^:?*[\\") {
		return errors.New("github_project.base_branch must be a safe Git branch name")
	}
	if !ValidMergeMethod(project.MergeMethod) {
		return errors.New("github_project.merge_method must be merge, rebase, or squash")
	}
	if c.MaxParallelism <= 0 || c.MaxParallelism > MaxSupportedParallelism {
		return fmt.Errorf("max_parallelism must be between 1 and %d", MaxSupportedParallelism)
	}
	if err := validateAdmissionBudget(c.AdmissionBudget); err != nil {
		return err
	}
	if err := validateResourceLimits(c.ResourceLimits); err != nil {
		return err
	}
	if len(c.Harnesses) == 0 {
		return errors.New("harnesses must define at least one explicit harness")
	}
	if len(c.Roles) == 0 {
		return errors.New("roles must define the workflow roles explicitly")
	}
	if c.Workflow == nil {
		return errors.New("workflow is required")
	}
	if err := validateHarnessConfigs(c.Harnesses); err != nil {
		return err
	}
	if err := validateWorkflowConfig(c); err != nil {
		return err
	}
	fields := []string{
		project.ResultField,
		project.ApprovalField,
		project.PhaseField,
		project.TransitionFieldName(),
		project.QAFailuresField,
		project.BranchField,
		project.PullRequestField,
		project.QACommitField,
	}
	seenFields := map[string]struct{}{}
	for _, field := range fields {
		key := normalizeProjectKey(field)
		if key == "" || key == normalizeProjectKey("Status") {
			return errors.New("github_project lifecycle field names are required and cannot use the reserved Status name")
		}
		if _, exists := seenFields[key]; exists {
			return errors.New("github_project field names must be distinct")
		}
		seenFields[key] = struct{}{}
	}
	seenRequirements := map[string]struct{}{}
	for index, requirement := range c.DoctorRequirements {
		if err := validateCapabilityRequirement(requirement); err != nil {
			return fmt.Errorf("doctor_requirements[%d]: %w", index, err)
		}
		key := strings.TrimSpace(requirement.Type) + "/" + strings.TrimSpace(requirement.ID)
		if _, exists := seenRequirements[key]; exists {
			return fmt.Errorf("doctor_requirements contains duplicate %q", key)
		}
		seenRequirements[key] = struct{}{}
	}
	return nil
}

// EffectiveMergeMethod preserves the original merge-commit behavior for
// configs created before merge_method became explicit.
func EffectiveMergeMethod(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return MergeMethodMerge
	}
	return value
}

func ValidMergeMethod(value string) bool {
	switch EffectiveMergeMethod(value) {
	case MergeMethodMerge, MergeMethodRebase, MergeMethodSquash:
		return true
	default:
		return false
	}
}

func validateCapabilityRequirement(requirement CapabilityRequirement) error {
	id := strings.TrimSpace(requirement.ID)
	typeName := strings.TrimSpace(requirement.Type)
	if id == "" || typeName == "" {
		return errors.New("id and type are required")
	}
	switch typeName {
	case CapabilityTypeLocalTool:
		if strings.ContainsAny(id, "/\\ \t\r\n") {
			return errors.New("local tool id must be a command name without path separators")
		}
	case CapabilityTypeSkill:
		harness, skill, ok := strings.Cut(id, "/")
		if !ok || !ValidHarnessKind(harness) || !bundledskills.ValidID(skill) {
			return errors.New("skill id must use <harness_kind>/<skill_name>")
		}
	case CapabilityTypeMCPServer:
		harness, server, ok := strings.Cut(id, "/")
		if !ok || !ValidHarnessKind(harness) || strings.TrimSpace(server) == "" || strings.ContainsAny(server, "/\\ \t\r\n") {
			return errors.New("MCP server id must use <harness_kind>/<server_name>")
		}
	case CapabilityTypeProfile:
	default:
		return fmt.Errorf("unsupported capability type %q", typeName)
	}
	return nil
}

func ValidRepositoryName(value string) bool {
	owner, repository, ok := strings.Cut(strings.TrimSpace(value), "/")
	return ok && owner != "" && repository != "" && !strings.Contains(repository, "/") &&
		!strings.ContainsAny(value, " \\\t\r\n")
}

func validateHarnessConfigs(harnesses []HarnessConfig) error {
	seen := map[string]struct{}{}
	for i, harness := range harnesses {
		kind := strings.TrimSpace(harness.Kind)
		if !ValidHarnessKind(kind) {
			return fmt.Errorf("harnesses[%d].kind must be codex, claude, or pi", i)
		}
		if _, exists := seen[kind]; exists {
			return fmt.Errorf("harnesses contains duplicate kind %q", kind)
		}
		seen[kind] = struct{}{}
		if harness.Enabled == nil {
			return fmt.Errorf("harnesses[%d].enabled is required", i)
		}
		if command := strings.TrimSpace(harness.Command); command == "" {
			return fmt.Errorf("harnesses[%d].command is required", i)
		} else if strings.HasPrefix(command, "-") || strings.ContainsAny(command, "\x00\r\n") {
			return fmt.Errorf("harnesses[%d].command must be one executable name or path without arguments", i)
		}
	}
	return nil
}
