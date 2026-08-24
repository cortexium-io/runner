package config

import "strings"

const (
	HarnessCodexCLI  = "codex"
	HarnessClaudeCLI = "claude"
	HarnessPiCLI     = "pi"

	WorkRolePlanner     = "planner"
	WorkRoleImplementer = "implementer"
	WorkRoleReviewer    = "reviewer"

	RoleAccessSandboxed = "sandboxed"
	RoleAccessHost      = "host"

	PlanningSupportStandard = "standard"
	PlanningSupportHigh     = "high"

	CapabilityTypeLocalTool = "local_tool"
	CapabilityTypeSkill     = "skill"
	CapabilityTypeMCPServer = "mcp_server"
	CapabilityTypeProfile   = "config_profile"

	CodexSandboxReadOnly         = "read-only"
	CodexSandboxWorkspaceWrite   = "workspace-write"
	CodexSandboxDangerFullAccess = "danger-full-access"

	CodexApprovalNever = "never"

	ClaudePermissionDontAsk = "dontAsk"
)

func EffectiveRoleAccess(value string) string {
	if strings.TrimSpace(value) == "" {
		return RoleAccessSandboxed
	}
	return strings.TrimSpace(value)
}

func ValidRoleAccess(value string) bool {
	switch EffectiveRoleAccess(value) {
	case RoleAccessSandboxed, RoleAccessHost:
		return true
	default:
		return false
	}
}

func EffectivePlanningSupport(value string) string {
	if strings.TrimSpace(value) == "" {
		return PlanningSupportStandard
	}
	return strings.TrimSpace(value)
}

func ValidPlanningSupport(value string) bool {
	switch EffectivePlanningSupport(value) {
	case PlanningSupportStandard, PlanningSupportHigh:
		return true
	default:
		return false
	}
}

type CapabilityRequirement struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Required bool    `json:"required"`
	Reason   *string `json:"reason,omitempty"`
}

func ValidHarnessKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case HarnessCodexCLI, HarnessClaudeCLI, HarnessPiCLI:
		return true
	default:
		return false
	}
}

func HarnessSupportsSafeTools(kind string) bool {
	switch strings.TrimSpace(kind) {
	case HarnessCodexCLI, HarnessClaudeCLI, HarnessPiCLI:
		return true
	default:
		return false
	}
}

func normalizeProjectKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}
