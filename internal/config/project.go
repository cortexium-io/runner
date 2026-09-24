package config

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"

	bundledskills "github.com/cortexium-io/runner/skills"
)

const ConfigVersion = 5

const MaxSupportedParallelism = 16

const GitHubProjectCapabilityID = "github_project"

const RunnerActivityFieldName = "Runner Activity"

const RunnerTransitionFieldName = "Runner Transition"

const RunnerPlanReleaseFieldName = "Runner Plan Release"

const (
	RunnerActivityAwaitingHumanReview    = "Awaiting human review"
	RunnerActivityWaitingForCI           = "Waiting for CI"
	RunnerActivityWaitingForIntegration  = "Waiting for integration slot"
	RunnerActivityWaitingForMerge        = "Waiting for merge"
	RunnerActivityCIFailed               = "CI failed — rework queued"
	RunnerActivityWaitingForDependencies = "Waiting for dependencies"
	RunnerActivityWaitingForHarness      = "Waiting for harness provider"
	RunnerActivityWaitingForCapacity     = "Waiting for model capacity"
)

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
	Owner                 string                       `json:"owner"`
	Number                int                          `json:"number"`
	IntakeRepository      string                       `json:"intake_repository,omitempty"`
	IntakeLabel           string                       `json:"intake_label,omitempty"`
	AutonomousIssueIntake *AutonomousIssueIntakeConfig `json:"autonomous_issue_intake,omitempty"`
	ResultField           string                       `json:"result_field,omitempty"`
	ApprovalField         string                       `json:"approval_field,omitempty"`
	PhaseField            string                       `json:"phase_field,omitempty"`
	TransitionField       string                       `json:"transition_field,omitempty"`
	QAFailuresField       string                       `json:"qa_failures_field,omitempty"`
	BranchField           string                       `json:"branch_field,omitempty"`
	PullRequestField      string                       `json:"pull_request_field,omitempty"`
	QACommitField         string                       `json:"qa_commit_field,omitempty"`
	BaseBranch            string                       `json:"base_branch,omitempty"`
	RemoteName            string                       `json:"remote_name,omitempty"`
	AutoMerge             bool                         `json:"auto_merge"`
	MergeMethod           string                       `json:"merge_method,omitempty"`
}

// AutonomousIssueIntakeConfig is deliberately presence-enabled. An empty
// object trusts issues only when the configured intake repository is private;
// public repositories additionally require an exact author allowlist match.
type AutonomousIssueIntakeConfig struct {
	TrustedAuthors []string `json:"trusted_authors,omitempty"`
}

// ProjectConfig is the runtime Project contract derived from the persisted
// GitHubProjectConfig and the resolved workflow.
type ProjectConfig struct {
	GitHubProjectConfig
	PlanDelivery           bool
	PlanVerificationID     string
	PlanVerificationDigest string
	PlanProfileDigests     map[string]string
	ActivityField          string
	RunnerID               string
	ApprovalAuthorityKey   []byte
	AssessmentStatus       string
	BacklogStatus          string
	ReadyStatus            string
	RunningStatus          string
	QAStatus               string
	PRReadyStatus          string
	BlockedStatus          string
	DoneStatus             string
	RequiredStatuses       []string
	AgentStatuses          []string
	LaneStatuses           map[string]string
	LaneRoles              map[string]string
	PlanningDestinations   map[string]string
	InitialLaneID          string
	InitialRole            string
	ApprovalLaneID         string
	ActiveLaneID           string
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
	if project.AutonomousIssueIntake != nil {
		seenAuthors := map[string]struct{}{}
		for index, author := range project.AutonomousIssueIntake.TrustedAuthors {
			author = strings.TrimSpace(author)
			if author == "" {
				return fmt.Errorf("github_project.autonomous_issue_intake.trusted_authors[%d] cannot be blank", index)
			}
			key := strings.ToLower(author)
			if _, exists := seenAuthors[key]; exists {
				return fmt.Errorf("github_project.autonomous_issue_intake.trusted_authors contains duplicate %q", author)
			}
			seenAuthors[key] = struct{}{}
		}
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
	if c.EffectiveGuidanceMinOccurrences() < 2 {
		return errors.New("guidance_min_occurrences must be at least 2 (or omitted for 2)")
	}
	if err := validateAdmissionBudget(c.AdmissionBudget); err != nil {
		return err
	}
	if err := validateResourceLimits(c.ResourceLimits); err != nil {
		return err
	}
	if c.PlanDelivery != nil && c.PlanDelivery.Enabled && strings.TrimSpace(c.PlanDelivery.CompleteVerification) == "" {
		return errors.New("plan_delivery requires a supported complete_verification entrypoint")
	}
	if gate := c.CardVerification; gate != nil {
		if gate.Access != RoleAccessHost {
			return errors.New("card_verification requires explicit access: host for the configured command; agent access is unchanged")
		}
		entry, ok := c.Verification[gate.Entrypoint]
		if !ok {
			return errors.New("card_verification requires a configured verification entrypoint")
		}
		if !entry.RequireCurrentCandidate {
			return errors.New("card_verification requires require_current_candidate: true")
		}
		if c.PlanDelivery != nil && c.PlanDelivery.Enabled {
			return errors.New("card_verification applies to individual cards; plan_delivery owns its complete verification gate")
		}
	}
	for id, entrypoint := range c.Verification {
		if id == "" || strings.TrimSpace(id) != id || strings.TrimSpace(entrypoint.Command) == "" || strings.ContainsRune(entrypoint.Command, 0) || entrypoint.TimeoutSeconds <= 0 {
			return errors.New("verification requires named entrypoints with an executable and positive timeout_seconds")
		}
		validCommand := func(command string) bool {
			return strings.TrimSpace(command) == command && command != "" && !strings.ContainsAny(command, "\x00\r\n\t\\") && (!strings.Contains(command, "/") || path.IsAbs(command))
		}
		if !validCommand(entrypoint.Command) || len(entrypoint.ToolchainCommands) == 0 {
			return errors.New("verification requires explicit toolchain_commands and PATH names or absolute executable paths")
		}
		if entrypoint.Preparation != nil && (!validCommand(entrypoint.Preparation.Command) || len(entrypoint.DependencyPaths) == 0) {
			return errors.New("verification preparation requires one PATH or absolute executable and explicit dependency_paths")
		}
		if entrypoint.CurrentCandidateCheck != nil && !validCommand(entrypoint.CurrentCandidateCheck.Command) {
			return errors.New("verification current_candidate_check requires one PATH or absolute executable")
		}
		tools := map[string]bool{}
		for _, command := range entrypoint.ToolchainCommands {
			if !validCommand(command) || tools[command] {
				return errors.New("verification toolchain_commands must be unique PATH names or absolute executable paths")
			}
			tools[command] = true
		}
		if err := validateVerificationRuntimePaths(entrypoint.RuntimePaths, c.ProjectDir); err != nil {
			return fmt.Errorf("verification %q: %w", id, err)
		}
		arguments := append([]string(nil), entrypoint.Args...)
		if entrypoint.Preparation != nil {
			arguments = append(arguments, entrypoint.Preparation.Args...)
		}
		if entrypoint.CurrentCandidateCheck != nil {
			arguments = append(arguments, entrypoint.CurrentCandidateCheck.Args...)
		}
		for _, arg := range arguments {
			if strings.ContainsRune(arg, 0) {
				return errors.New("verification arguments cannot contain NUL")
			}
		}
		if len(entrypoint.InputPaths) == 0 {
			return errors.New("verification entrypoints require reviewed input_paths")
		}
		seen := map[string]bool{}
		for _, input := range append(append([]string(nil), entrypoint.InputPaths...), entrypoint.DependencyPaths...) {
			if input == "" || input == "." || path.IsAbs(input) || path.Clean(input) != input || input == ".." || strings.HasPrefix(input, "../") || strings.ContainsAny(input, "\\\x00\r\n") || seen[input] {
				return fmt.Errorf("verification path %q must be a unique canonical repository-relative file or directory", input)
			}
			for _, component := range strings.Split(input, "/") {
				if strings.EqualFold(component, ".git") {
					return errors.New("verification inputs cannot select Git administration")
				}
			}
			seen[input] = true
		}
		if entrypoint.Preparation != nil {
			if err := validateVerificationPreparationPaths(entrypoint, c.ProjectDir); err != nil {
				return fmt.Errorf("verification %q: %w", id, err)
			}
		}
		excluded := map[string]bool{}
		excludedRoots := map[string]bool{}
		for _, input := range entrypoint.DependencyExcludePaths {
			if input == "" || path.Clean(input) != input || strings.ContainsAny(input, "\\\x00\r\n") || excluded[input] {
				return errors.New("dependency exclusions must be unique canonical paths")
			}
			for _, component := range strings.Split(input, "/") {
				if strings.EqualFold(component, ".git") {
					return errors.New("dependency exclusions cannot select Git administration")
				}
			}
			inside := false
			for _, root := range entrypoint.DependencyPaths {
				if input == root && entrypoint.Preparation != nil {
					inside = true
					excludedRoots[root] = true
				}
				if strings.HasPrefix(input, root+"/") {
					inside = true
				}
			}
			if !inside {
				return errors.New("dependency exclusions must be inside a dependency root; excluding a complete cache root requires preparation")
			}
			excluded[input] = true
		}
		if len(excludedRoots) > 0 && len(excludedRoots) == len(entrypoint.DependencyPaths) {
			return errors.New("preparation cache exclusions must retain at least one nonexcluded executable dependency root")
		}
	}
	if c.PlanDelivery != nil && c.PlanDelivery.Enabled {
		if _, ok := c.Verification[c.PlanDelivery.CompleteVerification]; !ok {
			return errors.New("plan_delivery.complete_verification must name an operator-configured verification entrypoint")
		}
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
	if c.PlanDelivery != nil && c.PlanDelivery.ReviewerRole != "" {
		role := c.PlanDelivery.ReviewerRole
		if role != strings.TrimSpace(role) || c.RoleContract(role) != WorkRoleReviewer {
			return errors.New("plan_delivery.reviewer_role must name a configured reviewer profile")
		}
	}
	if err := validateRepositoryReferences(c); err != nil {
		return err
	}
	if err := ValidateReviewEvidencePaths(c.ReviewEvidencePaths); err != nil {
		return err
	}
	if err := validateTestSpecialist(c); err != nil {
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

// Runtime artifacts are selected by the operator, not discovered from the
// executable wrapper. Limit the catalog itself here; the collector separately
// enforces no-follow traversal and byte/file bounds on the selected content.
func validateVerificationPreparationPaths(entry VerificationEntrypoint, projectDir string) error {
	overlaps := func(first, second string) bool {
		return first == second || strings.HasPrefix(first, second+"/") || strings.HasPrefix(second, first+"/")
	}
	for index, dependency := range entry.DependencyPaths {
		for _, component := range strings.Split(dependency, "/") {
			switch strings.ToLower(component) {
			case ".git", ".github", ".codex", ".claude", ".pi", ".runner-state", "agents.md", "claude.md":
				return errors.New("verification preparation cannot select repository or agent control paths as mutable dependencies")
			}
		}
		for _, other := range entry.DependencyPaths[:index] {
			if overlaps(dependency, other) {
				return errors.New("verification preparation dependency roots cannot overlap")
			}
		}
		for _, input := range entry.InputPaths {
			if overlaps(dependency, input) {
				return errors.New("verification preparation dependency roots cannot overlap input_paths")
			}
		}
		if path.IsAbs(projectDir) {
			for _, runtimePath := range entry.RuntimePaths {
				if overlaps(path.Join(projectDir, dependency), runtimePath) {
					return errors.New("verification preparation dependency roots cannot overlap runtime_paths")
				}
			}
		}
	}
	// This is only the lexical configuration boundary. The launcher must check
	// the actual candidate directory, resolved runtime selections, tracked files
	// and no-follow identities before permitting preparation.
	return nil
}

func validateVerificationRuntimePaths(paths []string, projectDir string) error {
	if len(paths) > 64 {
		return errors.New("runtime_paths allows at most 64 selected runtime artifacts")
	}
	seen := map[string]bool{}
	for _, selected := range paths {
		if len(selected) > 4096 || !path.IsAbs(selected) || path.Clean(selected) != selected || strings.TrimSpace(selected) != selected || strings.ContainsAny(selected, "\\*?[]{}") || strings.ContainsFunc(selected, unicode.IsControl) {
			return errors.New("runtime_paths must contain canonical literal absolute artifact paths without control characters")
		}
		parts := strings.Split(strings.TrimPrefix(selected, "/"), "/")
		if len(parts) < 2 || (len(parts) == 2 && (parts[0] == "Users" || parts[0] == "home" || parts[0] == "Volumes")) {
			return errors.New("runtime_paths cannot select filesystem, user-home, or volume roots")
		}
		switch selected {
		case "/usr/local", "/opt/homebrew", "/System/Library", "/private/tmp", "/private/var":
			return errors.New("runtime_paths must select runtime artifacts, not shared installation or temporary roots")
		}
		if path.IsAbs(projectDir) && (path.Clean(projectDir) == selected || strings.HasPrefix(path.Clean(projectDir), selected+"/")) {
			return errors.New("runtime_paths cannot select the project root or an ancestor; use input_paths for repository inputs")
		}
		for _, component := range parts {
			if strings.EqualFold(component, ".git") {
				return errors.New("runtime_paths cannot select Git administration")
			}
		}
		for prior := range seen {
			if prior == selected || strings.HasPrefix(prior, selected+"/") || strings.HasPrefix(selected, prior+"/") {
				return errors.New("runtime_paths cannot contain duplicate or overlapping selections")
			}
		}
		seen[selected] = true
	}
	return nil
}

func NormalizeMergeMethod(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func ValidMergeMethod(value string) bool {
	switch NormalizeMergeMethod(value) {
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
