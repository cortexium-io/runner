package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	bundledskills "github.com/cortexium-io/runner/skills"
)

type harnessProfileRequirement struct {
	role   execution.RoleContract
	access string
}

func (i *Inspector) inspectHarness(ctx context.Context, descriptor HarnessDescriptor, homeErr error, requiredSkills []bundledskills.Skill) (HarnessInspection, []CapabilityState) {
	report := HarnessInspection{
		Kind: descriptor.Kind, DisplayName: descriptor.DisplayName, Command: descriptor.Command,
		Status: CapabilityMissing, Authentication: "not_inspected",
	}
	if harness, ok := i.cfg.Harness(descriptor.Kind); ok {
		report.ExecutionPolicy = harness.ExecutionPolicySummary()
	}
	capabilities := []CapabilityState{}
	path, err := i.lookPath(descriptor.Command)
	if err != nil {
		detail := descriptor.DisplayName + " executable not found in PATH"
		report.Detail = detail
		capabilities = append(capabilities, CapabilityState{ID: descriptor.Kind, Type: config.CapabilityTypeLocalTool, Status: CapabilityMissing, Detail: stringPtr(detail)})
		for _, skill := range requiredSkills {
			capabilities = append(capabilities, CapabilityState{ID: harnessSkillCapabilityID(descriptor.Kind, skill.ID), Type: config.CapabilityTypeSkill, Status: CapabilityMissing, Detail: stringPtr(descriptor.DisplayName + " is not installed")})
		}
		return report, capabilities
	}
	report.Path = path
	report.Status = CapabilityAvailable
	report.Detail = "executable found; authentication is managed by the harness and was not inspected"
	versionResult, versionErr := i.run.Run(ctx, path, descriptor.VersionArgs, "", 5*time.Second)
	if versionErr == nil {
		report.Version = firstNonEmptyLine(versionResult.Stdout, versionResult.Stderr)
	}
	version := report.Version
	if harness, ok := i.cfg.Harness(descriptor.Kind); ok {
		if invocationErr := i.inspectHarnessInvocationSupport(ctx, path, harness); invocationErr != nil {
			report.Status = CapabilityBlocked
			report.Detail = invocationErr.Error()
		}
	}
	capabilities = append(capabilities, CapabilityState{ID: descriptor.Kind, Type: config.CapabilityTypeLocalTool, Status: report.Status, Version: optionalString(version), Detail: stringPtr(report.Detail)})

	report.SkillsReady = homeErr == nil
	for _, skill := range requiredSkills {
		state := inspectHarnessSkill(descriptor, skill, homeErr)
		capabilities = append(capabilities, state)
		if state.Status != CapabilityAvailable {
			report.SkillsReady = false
		}
	}
	report.Ready = report.Status == CapabilityAvailable && report.SkillsReady
	if !report.SkillsReady {
		report.Detail += "; one or more bundled skills required by this harness's configured roles are missing or differ from the trusted version"
	}
	return report, capabilities
}

func (i *Inspector) requiredBundledSkills(kind string) []bundledskills.Skill {
	if !i.cfg.HasProject() {
		return i.catalog.List()
	}
	selected := map[string]struct{}{}
	for _, roleID := range i.cfg.ExecutionRoleIDs() {
		profile, ok := i.cfg.RoleProfile(roleID)
		if !ok || strings.TrimSpace(profile.Harness) != strings.TrimSpace(kind) {
			continue
		}
		for _, skillID := range profile.Skills {
			if _, bundled := i.catalog.Get(strings.TrimSpace(skillID)); bundled {
				selected[strings.TrimSpace(skillID)] = struct{}{}
			}
		}
	}
	result := make([]bundledskills.Skill, 0, len(selected))
	for _, skill := range i.catalog.List() {
		if _, ok := selected[skill.ID]; ok {
			result = append(result, skill)
		}
	}
	return result
}

func (i *Inspector) inspectHarnessInvocationSupport(ctx context.Context, path string, harness config.HarnessConfig) error {
	requiredFlags := []string{}
	rootFlags := []string{}
	helpArgs := []string{"--help"}
	modelRequired, reasoningRequired := i.configuredHarnessOptions(harness.Kind)
	requirements := []harnessProfileRequirement{{role: execution.RoleProbe, access: config.RoleAccessSandboxed}}
	for _, roleID := range i.cfg.ExecutionRoleIDs() {
		profile, ok := i.cfg.RoleProfile(roleID)
		if !ok || strings.TrimSpace(profile.Harness) != strings.TrimSpace(harness.Kind) {
			continue
		}
		contract := execution.RoleContract(i.cfg.RoleContract(roleID))
		requirement := harnessProfileRequirement{role: contract, access: config.EffectiveRoleAccess(profile.Access)}
		if !containsProfileRequirement(requirements, requirement.role, requirement.access) {
			requirements = append(requirements, requirement)
		}
	}
	for _, requirement := range requirements {
		root, command, err := execution.RequiredHarnessFlags(harness.Kind, requirement.role, requirement.access)
		if err != nil {
			return fmt.Errorf("unsupported %s %s/%s execution profile: %w", harness.Kind, requirement.role, requirement.access, err)
		}
		rootFlags = appendUniqueStrings(rootFlags, root...)
		requiredFlags = appendUniqueStrings(requiredFlags, command...)
	}
	switch harness.Kind {
	case config.HarnessCodexCLI:
		helpArgs = []string{"exec", "--help"}
		if modelRequired {
			requiredFlags = append(requiredFlags, "--model")
		}
		if err := i.inspectHarnessHelpFlags(ctx, path, harness.Kind, []string{"--help"}, rootFlags); err != nil {
			return err
		}
	case config.HarnessClaudeCLI:
		if modelRequired {
			requiredFlags = append(requiredFlags, "--model")
		}
		if reasoningRequired {
			requiredFlags = append(requiredFlags, "--effort")
		}
	case config.HarnessPiCLI:
		requiredFlags = appendUniqueStrings(requiredFlags, "--extension")
		if modelRequired {
			requiredFlags = append(requiredFlags, "--model")
		}
		if reasoningRequired {
			requiredFlags = append(requiredFlags, "--thinking")
		}
	}
	return i.inspectHarnessHelpFlags(ctx, path, harness.Kind, helpArgs, requiredFlags)
}

func containsProfileRequirement(values []harnessProfileRequirement, wantedRole execution.RoleContract, wantedAccess string) bool {
	for _, value := range values {
		if value.role == wantedRole && value.access == wantedAccess {
			return true
		}
	}
	return false
}

func appendUniqueStrings(values []string, additions ...string) []string {
	for _, addition := range additions {
		found := false
		for _, value := range values {
			found = found || value == addition
		}
		if !found {
			values = append(values, addition)
		}
	}
	return values
}

func (i *Inspector) inspectHarnessHelpFlags(ctx context.Context, path, kind string, helpArgs, requiredFlags []string) error {
	if len(requiredFlags) == 0 {
		return nil
	}
	result, err := i.run.Run(ctx, path, helpArgs, "", 5*time.Second)
	if err != nil {
		return fmt.Errorf("cannot verify that installed %s supports Runner's non-interactive invocation", kind)
	}
	help := result.Stdout + "\n" + result.Stderr
	for _, flag := range requiredFlags {
		if !strings.Contains(help, flag) {
			return fmt.Errorf("installed %s does not advertise %s required by Runner's non-interactive invocation", kind, flag)
		}
	}
	return nil
}

func (i *Inspector) configuredHarnessOptions(kind string) (modelRequired bool, reasoningRequired bool) {
	for _, roleID := range i.cfg.ExecutionRoleIDs() {
		profile, ok := i.cfg.RoleProfile(roleID)
		if !ok || strings.TrimSpace(profile.Harness) != strings.TrimSpace(kind) {
			continue
		}
		modelRequired = modelRequired || profile.Model != nil && strings.TrimSpace(*profile.Model) != ""
		reasoningRequired = reasoningRequired || strings.TrimSpace(profile.Reasoning) != ""
	}
	return modelRequired, reasoningRequired
}

func inspectHarnessSkill(descriptor HarnessDescriptor, skill bundledskills.Skill, homeErr error) CapabilityState {
	id := harnessSkillCapabilityID(descriptor.Kind, skill.ID)
	state := CapabilityState{ID: id, Type: config.CapabilityTypeSkill, Status: CapabilityMissing, Version: stringPtr(skill.Version)}
	if homeErr != nil {
		state.Status = CapabilityBlocked
		state.Detail = stringPtr("cannot resolve native skill directory: " + homeErr.Error())
		return state
	}
	path := filepath.Join(descriptor.SkillRoot, skill.ID, "SKILL.md")
	content, err := os.ReadFile(path)
	if err != nil {
		state.Detail = stringPtr("bundled role skill is not installed at " + path)
		return state
	}
	if string(content) != string(skill.Content) {
		state.Status = CapabilityBlocked
		state.Detail = stringPtr("installed skill differs from the bundled trusted version at " + path)
		return state
	}
	state.Status = CapabilityAvailable
	state.Detail = stringPtr("bundled role skill is installed at " + path)
	return state
}

func (i *Inspector) inspectMCP(ctx context.Context, descriptors []HarnessDescriptor, id string) CapabilityState {
	state := CapabilityState{ID: id, Type: config.CapabilityTypeMCPServer, Status: CapabilityMissing}
	kind, name, ok := strings.Cut(strings.TrimSpace(id), "/")
	if !ok || !config.ValidHarnessKind(kind) || strings.TrimSpace(name) == "" {
		state.Status = CapabilityBlocked
		state.Detail = stringPtr("MCP capability id must use <harness_kind>/<server_name>")
		return state
	}
	var descriptor *HarnessDescriptor
	for index := range descriptors {
		if descriptors[index].Kind == kind {
			descriptor = &descriptors[index]
			break
		}
	}
	if descriptor == nil {
		state.Detail = stringPtr("selected harness is not configured")
		return state
	}
	path, err := i.lookPath(descriptor.Command)
	if err != nil {
		state.Detail = stringPtr(descriptor.DisplayName + " executable not found in PATH")
		return state
	}
	switch kind {
	case config.HarnessCodexCLI:
		result, runErr := i.run.Run(ctx, path, []string{"mcp", "list", "--json"}, "", 10*time.Second)
		if runErr != nil {
			state.Status = CapabilityBlocked
			state.Detail = stringPtr("cannot inspect Codex MCP configuration")
			return state
		}
		var servers []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		}
		if err := json.Unmarshal([]byte(result.Stdout), &servers); err != nil {
			state.Status = CapabilityBlocked
			state.Detail = stringPtr("Codex returned malformed MCP configuration")
			return state
		}
		for _, server := range servers {
			if server.Name == name && server.Enabled {
				state.Status = CapabilityAvailable
				state.Detail = stringPtr("MCP server is configured and enabled for Codex")
				return state
			}
		}
		state.Detail = stringPtr("MCP server is not configured and enabled for Codex")
	case config.HarnessClaudeCLI:
		result, runErr := i.run.Run(ctx, path, []string{"mcp", "list"}, "", 15*time.Second)
		if runErr != nil {
			state.Status = CapabilityBlocked
			state.Detail = stringPtr("cannot inspect Claude MCP configuration")
			return state
		}
		if claudeMCPAvailable(result.Stdout+"\n"+result.Stderr, name) {
			state.Status = CapabilityAvailable
			state.Detail = stringPtr("MCP server reports a successful Claude Code connection")
			return state
		}
		state.Detail = stringPtr("MCP server does not report a successful Claude Code connection")
	case config.HarnessPiCLI:
		state.Status = CapabilityBlocked
		state.Detail = stringPtr("Pi MCP readiness requires a project-specific extension check; no MCP is required by the configured profile")
	}
	return state
}

func claudeMCPAvailable(output, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, line := range strings.Split(strings.ToLower(output), "\n") {
		entry := line
		if separator := strings.Index(entry, ":"); separator >= 0 {
			entry = entry[:separator]
		}
		fields := strings.FieldsFunc(entry, func(r rune) bool {
			return !(r == '-' || r == '_' || r == '.' || r == ':' || r == '/' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
		})
		named := false
		for _, field := range fields {
			field = strings.Trim(field, ":/")
			named = named || field == name
		}
		if named && claudeMCPSuccessStatus(line) {
			return true
		}
	}
	return false
}

func claudeMCPSuccessStatus(line string) bool {
	status := line
	if separator := strings.LastIndex(status, " - "); separator >= 0 {
		status = status[separator+3:]
	} else if separator := strings.Index(status, ":"); separator >= 0 {
		status = status[separator+1:]
	}
	words := strings.FieldsFunc(status, func(r rune) bool {
		return !(r >= 'a' && r <= 'z')
	})
	if len(words) != 1 {
		return false
	}
	switch words[0] {
	case "connected", "success", "succeeded", "successful":
		return true
	default:
		return false
	}
}
