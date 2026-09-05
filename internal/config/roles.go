package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	bundledskills "github.com/cortexium-io/runner/skills"
)

// RoleTemplate returns a complete persisted role set for a harness selected by
// init after capability discovery. Runtime configuration never falls back to
// this template.
func RoleTemplate(harness string) map[string]RoleConfig {
	return map[string]RoleConfig{
		WorkRolePlanner: {
			Harness: harness, Access: RoleAccessSandboxed, HarnessConfig: HarnessConfigModeIsolated, Skills: []string{"runner-planner"}, Reasoning: "high", TimeoutSeconds: 1200,
		},
		WorkRoleImplementer: {
			Harness: harness, Access: RoleAccessSandboxed, HarnessConfig: HarnessConfigModeIsolated, Skills: []string{"runner-implementer"}, Reasoning: "high", TaskGranularity: TaskGranularityStandard, TimeoutSeconds: 7200,
		},
		WorkRoleReviewer: {
			Harness: harness, Access: RoleAccessSandboxed, HarnessConfig: HarnessConfigModeIsolated, Skills: []string{"runner-reviewer"}, Reasoning: "high", TaskGranularity: TaskGranularityStandard, TimeoutSeconds: 3600,
		},
	}
}

func BuiltinRoleIDs() []string {
	return []string{WorkRolePlanner, WorkRoleImplementer, WorkRoleReviewer}
}

func IsBuiltinRole(id string) bool {
	switch strings.TrimSpace(id) {
	case WorkRolePlanner, WorkRoleImplementer, WorkRoleReviewer:
		return true
	default:
		return false
	}
}

func (c Config) RoleIDs() []string {
	roles := c.resolvedRoles()
	ids := make([]string, 0, len(roles))
	for id := range roles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func ValidRoleID(id string) bool { return validWorkflowID(id) }

func (c Config) resolvedRoles() map[string]RoleConfig {
	return cloneRoles(c.Roles)
}

func (c Config) RoleProfile(id string) (RoleConfig, bool) {
	roles := c.resolvedRoles()
	current := strings.TrimSpace(id)
	profile, ok := roles[current]
	if !ok {
		return RoleConfig{}, false
	}
	chain := []RoleConfig{profile}
	seen := map[string]struct{}{current: {}}
	for strings.TrimSpace(profile.Extends) != "" {
		current = strings.TrimSpace(profile.Extends)
		if _, exists := seen[current]; exists {
			return RoleConfig{}, false
		}
		seen[current] = struct{}{}
		parent, exists := roles[current]
		if !exists {
			return RoleConfig{}, false
		}
		profile = parent
		chain = append(chain, parent)
	}
	resolved := chain[len(chain)-1]
	resolved.Extends = ""
	for index := len(chain) - 2; index >= 0; index-- {
		resolved = mergeRoleProfile(resolved, chain[index])
	}
	return resolved, true
}

// RoleSafeTools reports whether Runner's bounded development-tool profile is
// active. Codex, Claude, and Pi implementers and reviewers inherit it by
// default; an explicit false disables the bounded package/browser grants. Pi's
// separate host-access requirement remains unchanged.
func (c Config) RoleSafeTools(id string) bool {
	profile, ok := c.RoleProfile(id)
	if !ok {
		return false
	}
	if profile.SafeTools != nil {
		return *profile.SafeTools
	}
	contract := c.RoleContract(id)
	return HarnessSupportsSafeTools(profile.Harness) && (contract == WorkRoleImplementer || contract == WorkRoleReviewer)
}

func (c Config) RoleContract(id string) string {
	roles := c.resolvedRoles()
	current := strings.TrimSpace(id)
	seen := map[string]struct{}{}
	for current != "" {
		if current == WorkRolePlanner || current == WorkRoleImplementer || current == WorkRoleReviewer {
			return current
		}
		if _, exists := seen[current]; exists {
			return ""
		}
		seen[current] = struct{}{}
		profile, exists := roles[current]
		if !exists {
			return ""
		}
		current = strings.TrimSpace(profile.Extends)
	}
	return ""
}

func (c Config) RoleIDForContract(contract string) string {
	workflow := c.resolvedWorkflow()
	preferredLane := ""
	switch strings.TrimSpace(contract) {
	case WorkRolePlanner:
		preferredLane = workflow.PlanLane
	case WorkRoleImplementer:
		preferredLane = workflow.ReadyLane
	}
	if role := strings.TrimSpace(workflow.Lanes[preferredLane].Role); role != "" && c.RoleContract(role) == contract {
		return role
	}
	laneIDs := make([]string, 0, len(workflow.Lanes))
	for id := range workflow.Lanes {
		laneIDs = append(laneIDs, id)
	}
	sort.Strings(laneIDs)
	for _, laneID := range laneIDs {
		role := strings.TrimSpace(workflow.Lanes[laneID].Role)
		if role != "" && c.RoleContract(role) == contract {
			return role
		}
	}

	ids := make([]string, 0, len(c.resolvedRoles()))
	for id := range c.resolvedRoles() {
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

func (c Config) ConfiguredRoleHarnesses() []string {
	seen := map[string]bool{}
	result := []string{}
	for _, id := range c.ExecutionRoleIDs() {
		profile, ok := c.RoleProfile(id)
		if !ok || seen[profile.Harness] {
			continue
		}
		seen[profile.Harness] = true
		result = append(result, profile.Harness)
	}
	return result
}

// ExecutionRoleIDs returns every role that Runner may launch. It includes
// workflow roles and optional implementer-ladder profiles.
func (c Config) ExecutionRoleIDs() []string {
	seen := map[string]bool{}
	result := []string{}
	for _, id := range append(append(c.WorkflowRoleIDs(), c.ImplementerLadder...), c.PlannerImplementers...) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

// AttemptRole selects the persisted execution profile for a work item. Only
// implementers may vary, and only the authenticated QA failure count advances
// their optional ladder.
func (c Config) AttemptRole(role string, qaFailures int) string {
	role = strings.TrimSpace(role)
	if c.RoleContract(role) != WorkRoleImplementer {
		return role
	}
	return ladderRole(role, qaFailures, c.ImplementerLadder)
}

func ladderRole(role string, qaFailures int, ladder []string) string {
	if len(ladder) == 0 {
		return role
	}
	if qaFailures < 0 {
		qaFailures = 0
	}
	if qaFailures >= len(ladder) {
		qaFailures = len(ladder) - 1
	}
	return strings.TrimSpace(ladder[qaFailures])
}

func (c Config) WorkflowRoleIDs() []string {
	seen := map[string]bool{}
	result := []string{}
	for _, lane := range c.resolvedWorkflow().Lanes {
		role := strings.TrimSpace(lane.Role)
		if role == "" || seen[role] {
			continue
		}
		seen[role] = true
		result = append(result, role)
	}
	sort.Strings(result)
	return result
}

func validateRoleConfigs(c Config, roles map[string]RoleConfig) error {
	active := map[string]bool{}
	for _, id := range c.ExecutionRoleIDs() {
		active[id] = true
	}
	implementerWorkspaceRoot := ""
	for id := range roles {
		if !validWorkflowID(id) {
			return fmt.Errorf("role id %q must use lowercase letters, numbers, and underscores", id)
		}
		if c.RoleContract(id) == "" {
			return fmt.Errorf("roles.%s must be planner, implementer, reviewer, or extend one of them", id)
		}
		profile, ok := c.RoleProfile(id)
		if !ok {
			return fmt.Errorf("roles.%s has an invalid or cyclic extends chain", id)
		}
		if !ValidHarnessKind(profile.Harness) {
			return fmt.Errorf("roles.%s.harness must be codex, claude, or pi", id)
		}
		if !ValidRoleAccess(profile.Access) {
			return fmt.Errorf("roles.%s.access must be sandboxed or host", id)
		}
		profile.Access = EffectiveRoleAccess(profile.Access)
		if !ValidHarnessConfigMode(profile.HarnessConfig) {
			return fmt.Errorf("roles.%s.harness_config must be isolated or inherit", id)
		}
		profile.HarnessConfig = EffectiveHarnessConfigMode(profile.HarnessConfig)
		if profile.Harness == HarnessPiCLI && profile.HarnessConfig == HarnessConfigModeInherit && profile.Access != RoleAccessHost {
			return fmt.Errorf("roles.%s must use host access because Pi cannot safely inherit ambient configuration in sandboxed mode", id)
		}
		if active[id] && c.harnessDisabled(profile.Harness) {
			return fmt.Errorf("roles.%s uses harness %q, but that harness is disabled", id, profile.Harness)
		}
		if len(profile.Skills) == 0 {
			return fmt.Errorf("roles.%s.skills must contain at least one skill", id)
		}
		seenSkills := map[string]struct{}{}
		for _, skill := range profile.Skills {
			if !bundledskills.ValidID(skill) {
				return fmt.Errorf("roles.%s.skills contains invalid Agent Skills name %q", id, skill)
			}
			if _, pinned := (bundledskills.EmbeddedCatalog{}).Get(skill); !pinned {
				return fmt.Errorf("roles.%s.skills contains unsupported unpinned skill %q; privileged launches accept only Runner's embedded bundled skills", id, skill)
			}
			if _, exists := seenSkills[skill]; exists {
				return fmt.Errorf("roles.%s.skills contains duplicate %q", id, skill)
			}
			seenSkills[skill] = struct{}{}
		}
		seenMCPServers := map[string]struct{}{}
		for _, server := range profile.MCPServers {
			server = strings.TrimSpace(server)
			if !ValidMCPServerName(server) {
				return fmt.Errorf("roles.%s.mcp_servers contains invalid server name %q", id, server)
			}
			if _, exists := seenMCPServers[server]; exists {
				return fmt.Errorf("roles.%s.mcp_servers contains duplicate %q", id, server)
			}
			seenMCPServers[server] = struct{}{}
		}
		if len(profile.MCPServers) > 0 && profile.Harness != HarnessCodexCLI {
			return fmt.Errorf("roles.%s.mcp_servers currently requires the codex harness", id)
		}
		if profile.SafeTools != nil && *profile.SafeTools && !HarnessSupportsSafeTools(profile.Harness) {
			return fmt.Errorf("roles.%s.safe_tools requires a harness with Runner-owned development-tool controls", id)
		}
		if profile.SafeTools != nil && *profile.SafeTools && c.RoleContract(id) == WorkRolePlanner {
			return fmt.Errorf("roles.%s.safe_tools cannot be enabled because planners remain read-only", id)
		}
		if profile.Reasoning != "" && !validReasoningEffort(profile.Harness, profile.Reasoning) {
			if profile.Harness == HarnessCodexCLI {
				return fmt.Errorf("roles.%s.reasoning must be low, medium, high, xhigh, or max for Codex", id)
			}
			if profile.Harness == HarnessPiCLI {
				return fmt.Errorf("roles.%s.reasoning must be off, low, medium, high, or xhigh for Pi", id)
			}
			return fmt.Errorf("roles.%s.reasoning must be low, medium, high, or xhigh", id)
		}
		if profile.PreserveReasoning != nil && profile.Harness != HarnessPiCLI {
			return fmt.Errorf("roles.%s.preserve_reasoning requires the pi harness", id)
		}
		if profile.TaskGranularity != "" && c.RoleContract(id) == WorkRolePlanner {
			return fmt.Errorf("roles.%s.task_granularity applies only to implementer and reviewer contracts", id)
		}
		if !ValidTaskGranularity(profile.TaskGranularity) {
			return fmt.Errorf("roles.%s.task_granularity must be standard or small", id)
		}
		if profile.Model != nil && strings.TrimSpace(*profile.Model) == "" {
			return fmt.Errorf("roles.%s.model cannot be blank", id)
		}
		if profile.TimeoutSeconds < 0 {
			return fmt.Errorf("roles.%s.timeout_seconds cannot be negative", id)
		}
		if profile.TimeoutSeconds == 0 {
			return fmt.Errorf("roles.%s.timeout_seconds is required", id)
		}
		if active[id] {
			harness, exists := c.Harness(profile.Harness)
			if !exists {
				return fmt.Errorf("roles.%s uses harness %q, but no enabled harness configuration exists", id, profile.Harness)
			}
			if c.RoleContract(id) == WorkRoleImplementer {
				root := strings.TrimSpace(harness.WorkspaceWriteRoot)
				if root == "" {
					return fmt.Errorf("harness %q workspace_write_root is required for implementer role %q", profile.Harness, id)
				}
				absoluteRoot, err := filepath.Abs(root)
				if err != nil {
					return fmt.Errorf("resolve harness %q workspace_write_root for implementer role %q: %w", profile.Harness, id, err)
				}
				absoluteRoot = filepath.Clean(absoluteRoot)
				if implementerWorkspaceRoot != "" && implementerWorkspaceRoot != absoluteRoot {
					return errors.New("all active implementer roles must use one workspace_write_root so rework preserves the card's workspace identity")
				}
				implementerWorkspaceRoot = absoluteRoot
			}
		}
	}
	return nil
}

func (c Config) harnessDisabled(kind string) bool {
	for _, harness := range c.Harnesses {
		if strings.TrimSpace(harness.Kind) == strings.TrimSpace(kind) && harness.Enabled != nil && !*harness.Enabled {
			return true
		}
	}
	return false
}

func mergeRoleProfile(parent, child RoleConfig) RoleConfig {
	result := parent
	result.Extends = ""
	if child.Harness != "" {
		result.Harness = child.Harness
	}
	if child.Access != "" {
		result.Access = child.Access
	}
	if child.HarnessConfig != "" {
		result.HarnessConfig = child.HarnessConfig
	}
	if child.SafeTools != nil {
		value := *child.SafeTools
		result.SafeTools = &value
	}
	if len(child.Skills) > 0 {
		result.Skills = append([]string(nil), child.Skills...)
	}
	if len(child.MCPServers) > 0 {
		result.MCPServers = append([]string(nil), child.MCPServers...)
	}
	if child.Model != nil {
		value := *child.Model
		result.Model = &value
	}
	if child.Description != "" {
		result.Description = child.Description
	}
	if child.Reasoning != "" {
		result.Reasoning = child.Reasoning
	}
	if child.PreserveReasoning != nil {
		value := *child.PreserveReasoning
		result.PreserveReasoning = &value
	}
	if child.TaskGranularity != "" {
		result.TaskGranularity = child.TaskGranularity
	}
	if child.TimeoutSeconds > 0 {
		result.TimeoutSeconds = child.TimeoutSeconds
	}
	return result
}

func cloneRoles(input map[string]RoleConfig) map[string]RoleConfig {
	result := make(map[string]RoleConfig, len(input))
	for id, role := range input {
		role.Skills = append([]string(nil), role.Skills...)
		role.MCPServers = append([]string(nil), role.MCPServers...)
		if role.Model != nil {
			value := *role.Model
			role.Model = &value
		}
		if role.SafeTools != nil {
			value := *role.SafeTools
			role.SafeTools = &value
		}
		if role.PreserveReasoning != nil {
			value := *role.PreserveReasoning
			role.PreserveReasoning = &value
		}
		result[id] = role
	}
	return result
}

func ValidMCPServerName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character != '-' && character != '_' && (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
