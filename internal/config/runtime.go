package config

import (
	"sort"
	"strings"
)

// RuntimeConfig is the validated, fully resolved configuration consumed by the
// engine. Computed workflow and Project state is explicit rather than hidden in
// persisted JSON structs.
type RuntimeConfig struct {
	RunnerID             string
	ProjectDir           string
	Harnesses            []HarnessConfig
	Roles                map[string]RoleConfig
	RoleContracts        map[string]string
	PlannerImplementers  []string
	ImplementerLadder    []string
	Workflow             ResolvedWorkflow
	DoctorRequirements   []CapabilityRequirement
	RepositoryReferences []RepositoryReference
	MaxParallelism       int
	AdmissionBudget      *AdmissionBudgetConfig
	ResourceLimits       ResourceLimits
	GitHubProject        ProjectConfig
}

// ExecutionConfig is the role-specific harness contract passed to an execution
// adapter. It replaces the old practice of mutating a file config at runtime.
type ExecutionConfig struct {
	WorkspaceBaseRef        string
	RoleAccess              string
	HarnessConfigMode       string
	Harness                 HarnessConfig
	Skills                  []string
	MCPServers              []string
	SafeTools               bool
	PreserveReasoning       bool
	ResourceLimits          ResourceLimits
	RepositoryReferences    []RepositoryReference
	ReferenceProtectedRoots []string
}

func (c Config) Resolve() (RuntimeConfig, error) {
	if err := ValidateConfiguration(c); err != nil {
		return RuntimeConfig{}, err
	}
	roles := map[string]RoleConfig{}
	contracts := map[string]string{}
	for id := range c.resolvedRoles() {
		profile, _ := c.RoleProfile(id)
		roles[id] = profile
		contracts[id] = c.RoleContract(id)
	}
	var admissionBudget *AdmissionBudgetConfig
	if c.AdmissionBudget != nil {
		copy := *c.AdmissionBudget
		admissionBudget = &copy
	}
	return RuntimeConfig{
		RunnerID:             c.RunnerID,
		ProjectDir:           c.ProjectDir,
		Harnesses:            append([]HarnessConfig(nil), c.Harnesses...),
		Roles:                roles,
		RoleContracts:        contracts,
		PlannerImplementers:  append([]string(nil), c.PlannerImplementers...),
		ImplementerLadder:    append([]string(nil), c.ImplementerLadder...),
		Workflow:             c.resolvedWorkflow(),
		DoctorRequirements:   c.EffectiveDoctorRequirements(),
		RepositoryReferences: cloneRepositoryReferences(c.RepositoryReferences),
		MaxParallelism:       c.MaxParallelism,
		AdmissionBudget:      admissionBudget,
		ResourceLimits:       c.ResolveResourceLimits(),
		GitHubProject:        c.ResolveProject(),
	}, nil
}

// EffectiveDoctorRequirements includes capabilities implied by active role
// configuration. Safe browser access is inspected but remains optional unless
// the operator explicitly requires it for this project.
func (c Config) EffectiveDoctorRequirements() []CapabilityRequirement {
	requirements := append([]CapabilityRequirement(nil), c.DoctorRequirements...)
	byKey := make(map[string]int, len(requirements))
	for index, requirement := range requirements {
		byKey[strings.TrimSpace(requirement.Type)+"/"+strings.TrimSpace(requirement.ID)] = index
	}
	roleIDs := c.ExecutionRoleIDs()
	sort.Strings(roleIDs)
	for _, roleID := range roleIDs {
		profile, ok := c.RoleProfile(roleID)
		if !ok {
			continue
		}
		if strings.TrimSpace(profile.Harness) == HarnessCodexCLI {
			servers := append([]string(nil), profile.MCPServers...)
			sort.Strings(servers)
			for _, server := range servers {
				id := HarnessCodexCLI + "/" + strings.TrimSpace(server)
				key := CapabilityTypeMCPServer + "/" + id
				if index, exists := byKey[key]; exists {
					requirements[index].Required = true
					continue
				}
				reason := "required by role " + roleID
				byKey[key] = len(requirements)
				requirements = append(requirements, CapabilityRequirement{ID: id, Type: CapabilityTypeMCPServer, Required: true, Reason: &reason})
			}
		}
		if c.RoleSafeTools(roleID) {
			for _, tool := range []string{"node", "npm", "npx", "chrome"} {
				key := CapabilityTypeLocalTool + "/" + tool
				required := tool != "chrome"
				if index, exists := byKey[key]; exists {
					if required {
						requirements[index].Required = true
					}
					continue
				}
				reason := "available to Runner safe tools for role " + roleID
				if required {
					reason = "required by Runner safe tools for role " + roleID
				}
				byKey[key] = len(requirements)
				requirements = append(requirements, CapabilityRequirement{ID: tool, Type: CapabilityTypeLocalTool, Required: required, Reason: &reason})
			}
		}
	}
	return requirements
}

func (c RuntimeConfig) Harness(kind string) (HarnessConfig, bool) {
	for _, harness := range c.Harnesses {
		if strings.TrimSpace(harness.Kind) == kind && (harness.Enabled == nil || *harness.Enabled) {
			return harness, true
		}
	}
	return HarnessConfig{}, false
}

func (c RuntimeConfig) RoleProfile(id string) (RoleConfig, bool) {
	profile, ok := c.Roles[strings.TrimSpace(id)]
	return profile, ok
}

func (c RuntimeConfig) RoleContract(id string) string {
	return c.RoleContracts[strings.TrimSpace(id)]
}

func (c RuntimeConfig) RoleIDForContract(contract string) string {
	preferredLane := ""
	switch strings.TrimSpace(contract) {
	case WorkRolePlanner:
		preferredLane = c.Workflow.PlanLane
	case WorkRoleImplementer:
		preferredLane = c.Workflow.ReadyLane
	}
	if role := strings.TrimSpace(c.Workflow.Lanes[preferredLane].Role); role != "" && c.RoleContract(role) == contract {
		return role
	}
	laneIDs := make([]string, 0, len(c.Workflow.Lanes))
	for id := range c.Workflow.Lanes {
		laneIDs = append(laneIDs, id)
	}
	sort.Strings(laneIDs)
	for _, laneID := range laneIDs {
		role := strings.TrimSpace(c.Workflow.Lanes[laneID].Role)
		if role != "" && c.RoleContract(role) == contract {
			return role
		}
	}
	ids := make([]string, 0, len(c.Roles))
	for id := range c.Roles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if c.RoleContract(id) == contract {
			return id
		}
	}
	return ""
}

func (c RuntimeConfig) ExecutionRoleIDs() []string {
	seen := map[string]bool{}
	result := []string{}
	for _, lane := range c.Workflow.Lanes {
		role := strings.TrimSpace(lane.Role)
		if role != "" && !seen[role] {
			seen[role] = true
			result = append(result, role)
		}
	}
	for _, rawRole := range append(append([]string(nil), c.ImplementerLadder...), c.PlannerImplementers...) {
		role := strings.TrimSpace(rawRole)
		if role != "" && !seen[role] {
			seen[role] = true
			result = append(result, role)
		}
	}
	sort.Strings(result)
	return result
}

func (c RuntimeConfig) AttemptRole(role string, qaFailures int) string {
	role = strings.TrimSpace(role)
	if c.RoleContract(role) != WorkRoleImplementer {
		return role
	}
	return ladderRole(role, qaFailures, c.ImplementerLadder)
}

func (c RuntimeConfig) Lane(id string) (ResolvedWorkflowLane, bool) {
	lane, ok := c.Workflow.Lanes[strings.TrimSpace(id)]
	return lane, ok
}

func (c RuntimeConfig) LaneIDForStatus(status string) string {
	for id, lane := range c.Workflow.Lanes {
		if strings.EqualFold(strings.TrimSpace(lane.Name), strings.TrimSpace(status)) {
			return id
		}
	}
	return ""
}

func (c RuntimeConfig) LaneStatus(id string) string {
	if lane, ok := c.Lane(id); ok {
		return strings.TrimSpace(lane.Name)
	}
	return ""
}

func (c RuntimeConfig) WorkflowEventFor(name string) (WorkflowEvent, bool) {
	for _, event := range c.Workflow.Events {
		if event.On == name {
			return event, true
		}
	}
	return WorkflowEvent{}, false
}

func (c RuntimeConfig) PublicationLaneID() string {
	for id, lane := range c.Workflow.Lanes {
		if lane.OnEnter == WorkflowActionPublishPR {
			return id
		}
	}
	return ""
}

func (c RuntimeConfig) AgentLaneIDs() []string {
	result := []string{}
	for id, lane := range c.Workflow.Lanes {
		if strings.TrimSpace(lane.Role) != "" {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}

func (c RuntimeConfig) EffectiveWorkflow() ResolvedWorkflow {
	return cloneResolvedWorkflow(c.Workflow)
}

func (c RuntimeConfig) Execution(role, harness, workingDir string) ExecutionConfig {
	profile, roleExists := c.RoleProfile(role)
	resolved, exists := c.Harness(harness)
	if !exists || !roleExists || strings.TrimSpace(profile.Harness) != strings.TrimSpace(harness) {
		return ExecutionConfig{}
	}
	resolved.WorkingDir = workingDir
	resolved.TimeoutSeconds = profile.TimeoutSeconds
	resolved.Model = profile.Model
	resolved.ReasoningEffort = strings.TrimSpace(profile.Reasoning)
	execution := ExecutionConfig{
		WorkspaceBaseRef:  strings.TrimSpace(c.GitHubProject.RemoteName) + "/" + strings.TrimSpace(c.GitHubProject.BaseBranch),
		RoleAccess:        EffectiveRoleAccess(profile.Access),
		HarnessConfigMode: EffectiveHarnessConfigMode(profile.HarnessConfig),
		Harness:           resolved,
		Skills:            append([]string(nil), profile.Skills...),
		MCPServers:        append([]string(nil), profile.MCPServers...),
		SafeTools:         c.roleSafeTools(role),
		PreserveReasoning: profile.PreserveReasoning != nil && *profile.PreserveReasoning,
		ResourceLimits:    c.ResourceLimits,
	}
	contract := c.RoleContract(role)
	if len(c.RepositoryReferences) > 0 && (contract == WorkRolePlanner || contract == WorkRoleImplementer || contract == WorkRoleReviewer) {
		execution.RepositoryReferences = cloneRepositoryReferences(c.RepositoryReferences)
		execution.ReferenceProtectedRoots = repositoryReferenceProtectedRoots(c.ProjectDir, c.Harnesses)
	}
	return execution
}

func (c RuntimeConfig) roleSafeTools(role string) bool {
	profile, ok := c.RoleProfile(role)
	if !ok {
		return false
	}
	if profile.SafeTools != nil {
		return *profile.SafeTools
	}
	contract := c.RoleContract(role)
	return HarnessSupportsSafeTools(profile.Harness) && (contract == WorkRoleImplementer || contract == WorkRoleReviewer)
}
