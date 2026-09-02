package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
)

type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ",") }
func (values *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("value cannot be blank")
	}
	*values = append(*values, value)
	return nil
}

func runRole(args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, "Usage: cortexium-runner role list|show|add|edit|remove [options]")
		fmt.Fprintln(stdout, "Custom roles inherit the planner, implementer, or reviewer execution contract through --extends.")
		return nil
	}
	switch args[0] {
	case "list":
		return runRoleList(args[1:], stdout)
	case "show":
		return runRoleShow(args[1:], stdout)
	case "add":
		return runRoleChange("add", args[1:], stdout)
	case "edit":
		return runRoleChange("edit", args[1:], stdout)
	case "remove":
		return runRoleRemove(args[1:], stdout)
	default:
		return fmt.Errorf("unknown role command %q; use role --help", args[0])
	}
}

type roleView struct {
	ID                        string            `json:"id"`
	Builtin                   bool              `json:"builtin"`
	Configured                bool              `json:"configured"`
	Contract                  string            `json:"contract"`
	ImplementerLadderPosition int               `json:"implementer_ladder_position,omitempty"`
	ImplementerLadder         []string          `json:"implementer_ladder,omitempty"`
	Definition                config.RoleConfig `json:"definition,omitempty"`
	Resolved                  config.RoleConfig `json:"resolved"`
}

func roleViews(cfg config.Config) []roleView {
	views := make([]roleView, 0, len(cfg.RoleIDs()))
	for _, id := range cfg.RoleIDs() {
		resolved, _ := cfg.RoleProfile(id)
		definition, configured := cfg.Roles[id]
		view := roleView{ID: id, Builtin: config.IsBuiltinRole(id), Configured: configured, Contract: cfg.RoleContract(id), Definition: definition, Resolved: resolved}
		for index, role := range cfg.ImplementerLadder {
			if role == id {
				view.ImplementerLadderPosition = index + 1
				view.ImplementerLadder = append([]string(nil), cfg.ImplementerLadder...)
				break
			}
		}
		views = append(views, view)
	}
	return views
}

func runRoleList(args []string, stdout io.Writer) error {
	flags := newFlagSet("role list", "cortexium-runner role list [--config PATH] [--json]", stdout)
	configPath := flags.String("config", "", "runner config; defaults to .cortexium/runner.json")
	jsonOutput := flags.Bool("json", false, "write role profiles as JSON")
	proceed, err := parseFlags(flags, args, "role list")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("role list does not accept positional arguments")
	}
	*configPath = resolveRunnerConfigPath(*configPath, "")
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load runner config: %w", err)
	}
	views := roleViews(cfg)
	if *jsonOutput {
		return writeJSON(stdout, views)
	}
	fmt.Fprintln(stdout, "Roles")
	for _, view := range views {
		kind := "custom"
		if view.Builtin {
			kind = "built-in"
		}
		fmt.Fprintf(stdout, "  %s (%s, %s) · %s/%s/%s · %s", view.ID, kind, view.Contract, view.Resolved.Harness, config.EffectiveRoleAccess(view.Resolved.Access), config.EffectiveHarnessConfigMode(view.Resolved.HarnessConfig), strings.Join(view.Resolved.Skills, ", "))
		if len(view.Resolved.MCPServers) > 0 {
			fmt.Fprintf(stdout, " · MCP: %s", strings.Join(view.Resolved.MCPServers, ", "))
		}
		fmt.Fprintln(stdout)
	}
	if len(cfg.ImplementerLadder) > 0 {
		fmt.Fprintf(stdout, "  Implementer ladder: %s\n", strings.Join(cfg.ImplementerLadder, " -> "))
	}
	return nil
}

func runRoleShow(args []string, stdout io.Writer) error {
	name, args := leadingRoleName(args)
	flags := newFlagSet("role show", "cortexium-runner role show NAME [--config PATH] [--json]", stdout)
	configPath := flags.String("config", "", "runner config; defaults to .cortexium/runner.json")
	jsonOutput := flags.Bool("json", false, "write the role profile as JSON")
	proceed, err := parseFlags(flags, args, "role show")
	if err != nil || !proceed {
		return err
	}
	if name == "" && flags.NArg() == 1 {
		name = flags.Arg(0)
	} else if flags.NArg() != 0 {
		return errors.New("role show requires exactly one role name")
	}
	if name == "" {
		return errors.New("role show requires exactly one role name")
	}
	*configPath = resolveRunnerConfigPath(*configPath, "")
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load runner config: %w", err)
	}
	id := strings.TrimSpace(name)
	for _, view := range roleViews(cfg) {
		if view.ID != id {
			continue
		}
		if *jsonOutput {
			return writeJSON(stdout, view)
		}
		fmt.Fprintf(stdout, "Role: %s\nContract: %s\nBuilt-in: %t\nHarness: %s\nAccess: %s\nHarness configuration: %s\nSkills: %s\nReasoning: %s\nTimeout: %s\n", view.ID, view.Contract, view.Builtin, view.Resolved.Harness, config.EffectiveRoleAccess(view.Resolved.Access), config.EffectiveHarnessConfigMode(view.Resolved.HarnessConfig), strings.Join(view.Resolved.Skills, ", "), view.Resolved.Reasoning, time.Duration(view.Resolved.TimeoutSeconds)*time.Second)
		if view.Contract == config.WorkRoleImplementer || view.Contract == config.WorkRoleReviewer {
			fmt.Fprintf(stdout, "Planner task sizing: %s\n", taskSizingLabel(view.Resolved.PlanningSupport))
		}
		if view.Resolved.Harness == config.HarnessPiCLI {
			fmt.Fprintf(stdout, "Preserve reasoning across Pi turns: %t\n", view.Resolved.PreserveReasoning != nil && *view.Resolved.PreserveReasoning)
		}
		fmt.Fprintf(stdout, "Safe development tools: %t\n", cfg.RoleSafeTools(view.ID))
		if view.Resolved.Model != nil {
			fmt.Fprintf(stdout, "Model: %s\n", *view.Resolved.Model)
		}
		if len(view.Resolved.MCPServers) > 0 {
			fmt.Fprintf(stdout, "MCP servers: %s\n", strings.Join(view.Resolved.MCPServers, ", "))
		}
		if view.Definition.Extends != "" {
			fmt.Fprintf(stdout, "Extends: %s\n", view.Definition.Extends)
		}
		if view.ImplementerLadderPosition > 0 {
			fmt.Fprintf(stdout, "Implementer ladder: %d/%d (%s)\n", view.ImplementerLadderPosition, len(view.ImplementerLadder), strings.Join(view.ImplementerLadder, " -> "))
		}
		return nil
	}
	return fmt.Errorf("role %q is not defined", id)
}

func runRoleChange(action string, args []string, stdout io.Writer) error {
	name, args := leadingRoleName(args)
	usage := "cortexium-runner role " + action + " NAME [--config PATH] [role options]"
	if action == "edit" {
		usage = "cortexium-runner role edit (NAME | --all) [--config PATH] [role options]"
	}
	flags := newFlagSet("role "+action, usage, stdout)
	configPath := flags.String("config", "", "trusted operator config path; defaults to .cortexium/runner.json")
	all := false
	if action == "edit" {
		flags.BoolVar(&all, "all", false, "edit harness configuration for all built-in roles atomically")
	}
	extends := flags.String("extends", "", "parent role; custom roles must inherit a planner, implementer, or reviewer role")
	harness := flags.String("harness", "", "harness override: codex, claude, or pi")
	access := flags.String("access", "", "access override: sandboxed or host")
	harnessConfig := flags.String("harness-config", "", "harness configuration override: isolated or inherit")
	model := flags.String("model", "", "model override for the selected harness")
	reasoning := flags.String("reasoning", "", "reasoning effort override")
	planningSupport := flags.String("planning-support", "", "task sizing override: standard (regular) or high (small)")
	timeout := flags.Duration("timeout", 0, "execution timeout override")
	clearModel := flags.Bool("clear-model", false, "remove the model override")
	clearSkills := flags.Bool("clear-skills", false, "remove skill overrides and inherit from the parent")
	clearMCPServers := flags.Bool("clear-mcp-servers", false, "remove MCP server overrides and inherit from the parent")
	clearRuntime := flags.Bool("clear-runtime-overrides", false, "remove harness, access, harness configuration, model, reasoning, Pi reasoning preservation, and timeout overrides")
	clearPlanningSupport := flags.Bool("clear-planning-support", false, "remove the task sizing override and inherit from the parent")
	preserveReasoning := flags.Bool("preserve-reasoning", false, "preserve reasoning from earlier Pi assistant turns")
	noPreserveReasoning := flags.Bool("no-preserve-reasoning", false, "keep only the most recent Pi reasoning (default)")
	clearPreserveReasoning := flags.Bool("clear-preserve-reasoning", false, "remove the Pi reasoning-preservation override and inherit from the parent")
	clearImplementerLadder := flags.Bool("clear-implementer-ladder", false, "disable the implementer ladder")
	safeTools := flags.Bool("safe-tools", false, "enable Runner's bounded package and local-browser tools")
	noSafeTools := flags.Bool("no-safe-tools", false, "disable Runner's bounded package and local-browser tools")
	var skills stringListFlag
	var mcpServers stringListFlag
	var nextImplementers stringListFlag
	flags.Var(&skills, "skill", "skill override; may be repeated")
	flags.Var(&mcpServers, "mcp-server", "trusted MCP server grant; may be repeated")
	flags.Var(&nextImplementers, "next-implementer", "next implementer role after a QA rejection; may be repeated in escalation order")
	proceed, err := parseFlags(flags, args, "role "+action)
	if err != nil || !proceed {
		return err
	}
	visited := map[string]bool{}
	flags.Visit(func(value *flag.Flag) { visited[value.Name] = true })
	if all {
		if name != "" || flags.NArg() != 0 {
			return errors.New("role edit --all does not accept a role name")
		}
		if !visited["harness-config"] {
			return errors.New("role edit --all requires --harness-config")
		}
		for option := range visited {
			switch option {
			case "all", "config", "harness-config":
			default:
				return fmt.Errorf("role edit --all supports only --harness-config, not --%s", option)
			}
		}
	} else {
		if name == "" && flags.NArg() == 1 {
			name = flags.Arg(0)
		} else if flags.NArg() != 0 {
			return fmt.Errorf("role %s requires exactly one role name", action)
		}
		if name == "" {
			return fmt.Errorf("role %s requires exactly one role name", action)
		}
	}
	if *clearSkills && len(skills) > 0 {
		return errors.New("--clear-skills and --skill cannot be used together")
	}
	if *clearMCPServers && len(mcpServers) > 0 {
		return errors.New("--clear-mcp-servers and --mcp-server cannot be used together")
	}
	if *clearModel && strings.TrimSpace(*model) != "" {
		return errors.New("--clear-model and --model cannot be used together")
	}
	if *clearPlanningSupport && strings.TrimSpace(*planningSupport) != "" {
		return errors.New("--clear-planning-support and --planning-support cannot be used together")
	}
	if *safeTools && *noSafeTools {
		return errors.New("--safe-tools and --no-safe-tools cannot be used together")
	}
	if *preserveReasoning && *noPreserveReasoning {
		return errors.New("--preserve-reasoning and --no-preserve-reasoning cannot be used together")
	}
	if *clearPreserveReasoning && (*preserveReasoning || *noPreserveReasoning) {
		return errors.New("--clear-preserve-reasoning cannot be combined with a reasoning-preservation value")
	}
	if *clearImplementerLadder && len(nextImplementers) > 0 {
		return errors.New("--clear-implementer-ladder and --next-implementer cannot be used together")
	}
	if action == "add" && (*clearImplementerLadder || len(nextImplementers) > 0) {
		return errors.New("implementer ladder options require role edit on the workflow implementer role")
	}
	if visited["harness-config"] && !config.ValidHarnessConfigMode(*harnessConfig) {
		return errors.New("--harness-config must be isolated or inherit")
	}
	*configPath = resolveRunnerConfigPath(*configPath, "")
	cfg, err := config.LoadTrustedConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load runner config: %w", err)
	}
	if all {
		return editAllBuiltinRoleHarnessConfiguration(*configPath, cfg, config.EffectiveHarnessConfigMode(*harnessConfig), stdout)
	}
	id := strings.TrimSpace(name)
	if !config.ValidRoleID(id) {
		return fmt.Errorf("role id %q must use lowercase letters, numbers, and underscores", id)
	}
	definition, configured := cfg.Roles[id]
	if action == "add" {
		if _, exists := cfg.RoleProfile(id); exists {
			return fmt.Errorf("role %q already exists", id)
		}
		if strings.TrimSpace(*extends) == "" {
			return errors.New("role add requires --extends")
		}
		definition = config.RoleConfig{}
	} else if !configured {
		if !config.IsBuiltinRole(id) {
			return fmt.Errorf("role %q is not configured", id)
		}
		definition = config.RoleConfig{}
	}
	if visited["extends"] {
		if config.IsBuiltinRole(id) && strings.TrimSpace(*extends) != "" {
			return errors.New("built-in role contracts cannot extend another role")
		}
		definition.Extends = strings.TrimSpace(*extends)
	}
	if action == "add" {
		definition.Extends = strings.TrimSpace(*extends)
	}
	if *clearRuntime {
		definition.Harness, definition.Access, definition.HarnessConfig, definition.SafeTools, definition.Model, definition.Reasoning, definition.PreserveReasoning, definition.TimeoutSeconds = "", "", "", nil, nil, "", nil, 0
	}
	if visited["harness"] {
		definition.Harness = strings.TrimSpace(*harness)
	}
	if visited["access"] {
		if !config.ValidRoleAccess(*access) {
			return errors.New("--access must be sandboxed or host")
		}
		definition.Access = config.EffectiveRoleAccess(*access)
	}
	if visited["harness-config"] {
		definition.HarnessConfig = config.EffectiveHarnessConfigMode(*harnessConfig)
	}
	if *safeTools || *noSafeTools {
		value := *safeTools
		definition.SafeTools = &value
	}
	if visited["model"] {
		value := strings.TrimSpace(*model)
		definition.Model = &value
	}
	if *clearModel {
		definition.Model = nil
	}
	if visited["reasoning"] {
		definition.Reasoning = strings.TrimSpace(*reasoning)
	}
	if *preserveReasoning || *noPreserveReasoning {
		value := *preserveReasoning
		definition.PreserveReasoning = &value
	}
	if *clearPreserveReasoning {
		definition.PreserveReasoning = nil
	}
	if visited["planning-support"] {
		if !config.ValidPlanningSupport(*planningSupport) {
			return errors.New("--planning-support must be standard or high")
		}
		definition.PlanningSupport = config.EffectivePlanningSupport(*planningSupport)
	}
	if *clearPlanningSupport {
		definition.PlanningSupport = ""
	}
	if visited["timeout"] {
		if *timeout <= 0 || *timeout%time.Second != 0 {
			return errors.New("--timeout must be a positive whole number of seconds")
		}
		definition.TimeoutSeconds = int(*timeout / time.Second)
	}
	if len(skills) > 0 {
		definition.Skills = append([]string(nil), skills...)
	} else if *clearSkills {
		definition.Skills = nil
	}
	if len(mcpServers) > 0 {
		definition.MCPServers = append([]string(nil), mcpServers...)
	} else if *clearMCPServers {
		definition.MCPServers = nil
	}
	if cfg.Roles == nil {
		cfg.Roles = map[string]config.RoleConfig{}
	}
	cfg.Roles[id] = definition
	if *clearImplementerLadder {
		if cfg.RoleContract(id) != config.WorkRoleImplementer || cfg.RoleIDForContract(config.WorkRoleImplementer) != id {
			return errors.New("--clear-implementer-ladder requires the workflow implementer role")
		}
		cfg.ImplementerLadder = nil
	}
	if len(nextImplementers) > 0 {
		if cfg.RoleContract(id) != config.WorkRoleImplementer || cfg.RoleIDForContract(config.WorkRoleImplementer) != id {
			return errors.New("--next-implementer requires the workflow implementer role")
		}
		cfg.ImplementerLadder = append([]string{id}, nextImplementers...)
	}
	if err := config.SaveConfig(*configPath, cfg); err != nil {
		return fmt.Errorf("save role %q: %w", id, err)
	}
	resolved, _ := cfg.RoleProfile(id)
	verb := "Added"
	if action == "edit" {
		verb = "Edited"
	}
	fmt.Fprintf(stdout, "%s role %s (%s contract, %s harness, %s access, %s configuration).\n", verb, id, cfg.RoleContract(id), resolved.Harness, config.EffectiveRoleAccess(resolved.Access), config.EffectiveHarnessConfigMode(resolved.HarnessConfig))
	if config.EffectiveRoleAccess(resolved.Access) == config.RoleAccessHost && config.EffectiveHarnessConfigMode(resolved.HarnessConfig) == config.HarnessConfigModeInherit {
		fmt.Fprintln(stdout, "WARNING: this role now inherits ambient tools and configuration with unrestricted host access.")
	}
	return nil
}

func editAllBuiltinRoleHarnessConfiguration(path string, cfg config.Config, mode string, stdout io.Writer) error {
	if cfg.Roles == nil {
		return errors.New("runner config has no built-in roles to edit")
	}
	for _, id := range config.BuiltinRoleIDs() {
		definition, configured := cfg.Roles[id]
		if !configured {
			return fmt.Errorf("built-in role %q is not configured", id)
		}
		definition.HarnessConfig = mode
		cfg.Roles[id] = definition
	}
	if err := config.SaveConfig(path, cfg); err != nil {
		return fmt.Errorf("save built-in role harness configuration: %w", err)
	}
	fmt.Fprintf(stdout, "Edited harness configuration for all built-in roles: %s.\n", mode)
	unrestricted := []string{}
	for _, id := range config.BuiltinRoleIDs() {
		resolved, _ := cfg.RoleProfile(id)
		fmt.Fprintf(stdout, "  %s: %s/%s/%s\n", id, resolved.Harness, config.EffectiveRoleAccess(resolved.Access), config.EffectiveHarnessConfigMode(resolved.HarnessConfig))
		if config.EffectiveRoleAccess(resolved.Access) == config.RoleAccessHost && config.EffectiveHarnessConfigMode(resolved.HarnessConfig) == config.HarnessConfigModeInherit {
			unrestricted = append(unrestricted, id)
		}
	}
	if len(unrestricted) > 0 {
		fmt.Fprintf(stdout, "WARNING: %s now inherit ambient tools and configuration with unrestricted host access.\n", strings.Join(unrestricted, ", "))
	}
	fmt.Fprintln(stdout, "Custom roles keep explicit overrides and otherwise inherit these built-in role settings.")
	return nil
}

func runRoleRemove(args []string, stdout io.Writer) error {
	name, args := leadingRoleName(args)
	flags := newFlagSet("role remove", "cortexium-runner role remove NAME [--config PATH]", stdout)
	configPath := flags.String("config", "", "trusted operator config path; defaults to .cortexium/runner.json")
	proceed, err := parseFlags(flags, args, "role remove")
	if err != nil || !proceed {
		return err
	}
	if name == "" && flags.NArg() == 1 {
		name = flags.Arg(0)
	} else if flags.NArg() != 0 {
		return errors.New("role remove requires exactly one role name")
	}
	if name == "" {
		return errors.New("role remove requires exactly one role name")
	}
	*configPath = resolveRunnerConfigPath(*configPath, "")
	id := strings.TrimSpace(name)
	cfg, err := config.LoadTrustedConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load runner config: %w", err)
	}
	if config.IsBuiltinRole(id) {
		return errors.New("built-in role contracts cannot be removed; use role edit to change an override")
	}
	if _, exists := cfg.Roles[id]; !exists {
		return fmt.Errorf("role %q is not configured", id)
	}
	for laneID, lane := range cfg.EffectiveWorkflow().Lanes {
		if lane.Role == id {
			return fmt.Errorf("role %q is used by workflow lane %q", id, laneID)
		}
	}
	for _, role := range cfg.ImplementerLadder {
		if role == id {
			return fmt.Errorf("role %q is used by implementer_ladder; edit or clear the ladder first", id)
		}
	}
	dependents := []string{}
	for roleID, role := range cfg.Roles {
		if strings.TrimSpace(role.Extends) == id {
			dependents = append(dependents, roleID)
		}
	}
	sort.Strings(dependents)
	if len(dependents) > 0 {
		return fmt.Errorf("role %q is inherited by %s", id, strings.Join(dependents, ", "))
	}
	delete(cfg.Roles, id)
	if err := config.SaveConfig(*configPath, cfg); err != nil {
		return fmt.Errorf("remove role %q: %w", id, err)
	}
	fmt.Fprintf(stdout, "Removed role %s.\n", id)
	return nil
}

func leadingRoleName(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return strings.TrimSpace(args[0]), args[1:]
	}
	return "", args
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
