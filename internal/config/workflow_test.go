package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkflowTemplateIsCompleteAndJuniorReadable(t *testing.T) {
	cfg := explicitTestConfig()
	workflow := *cfg.Workflow
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default workflow is invalid: %v", err)
	}
	qa := workflow.Lanes["agent_qa"]
	if qa.Role != WorkRoleReviewer || qa.MaxQARetries != 3 || qa.Transitions[WorkflowOutcomeSuccess] != "pr_ready" || qa.Transitions[WorkflowOutcomeRejected] != "ready" || qa.Transitions[WorkflowOutcomeExhausted] != "blocked" {
		t.Fatalf("unexpected default QA lane %#v", qa)
	}
	if workflow.Lanes["pr_ready"].OnEnter != WorkflowActionPublishPR {
		t.Fatalf("PR Ready does not publish on entry: %#v", workflow.Lanes["pr_ready"])
	}
	if workflow.IntakeLane != "needs_assessment" || workflow.ApprovalLane != "backlog" || workflow.ActiveLane != "in_progress" {
		t.Fatalf("unexpected system lane references %#v", workflow)
	}
}

func TestRoleTemplateUsesPracticalTimeouts(t *testing.T) {
	roles := RoleTemplate(HarnessCodexCLI)
	for id, role := range roles {
		if role.Access != RoleAccessSandboxed {
			t.Fatalf("role %s access = %q, want safest default %q", id, role.Access, RoleAccessSandboxed)
		}
		if role.HarnessConfig != HarnessConfigModeIsolated {
			t.Fatalf("role %s harness config = %q, want safest default %q", id, role.HarnessConfig, HarnessConfigModeIsolated)
		}
	}
	if roles[WorkRolePlanner].TimeoutSeconds != 1200 {
		t.Fatalf("planner timeout = %d, want 1200 seconds", roles[WorkRolePlanner].TimeoutSeconds)
	}
	if roles[WorkRoleImplementer].TimeoutSeconds != 7200 {
		t.Fatalf("implementer timeout = %d, want 7200 seconds", roles[WorkRoleImplementer].TimeoutSeconds)
	}
	if roles[WorkRoleReviewer].TimeoutSeconds != 3600 {
		t.Fatalf("reviewer timeout = %d, want 3600 seconds", roles[WorkRoleReviewer].TimeoutSeconds)
	}
	if roles[WorkRolePlanner].PlanningSupport != "" || roles[WorkRoleImplementer].PlanningSupport != PlanningSupportStandard || roles[WorkRoleReviewer].PlanningSupport != PlanningSupportStandard {
		t.Fatalf("unexpected default planning support: %#v", roles)
	}
}

func TestPlanningSupportIsExplicitInheritedAndUnavailableToPlanner(t *testing.T) {
	cfg := explicitTestConfig()
	reviewer := cfg.Roles[WorkRoleReviewer]
	reviewer.PlanningSupport = PlanningSupportHigh
	cfg.Roles[WorkRoleReviewer] = reviewer
	cfg.Roles["guided_reviewer"] = RoleConfig{Extends: WorkRoleReviewer, Skills: []string{"runner-reviewer"}}
	profile, ok := cfg.RoleProfile("guided_reviewer")
	if !ok || profile.PlanningSupport != PlanningSupportHigh {
		t.Fatalf("custom reviewer did not inherit planning support: %#v", profile)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("high reviewer planning support was rejected: %v", err)
	}

	reviewer.PlanningSupport = "automatic"
	cfg.Roles[WorkRoleReviewer] = reviewer
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "standard or high") {
		t.Fatalf("unknown planning support was accepted: %v", err)
	}

	reviewer.PlanningSupport = PlanningSupportStandard
	cfg.Roles[WorkRoleReviewer] = reviewer
	planner := cfg.Roles[WorkRolePlanner]
	planner.PlanningSupport = PlanningSupportHigh
	cfg.Roles[WorkRolePlanner] = planner
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "implementer and reviewer") {
		t.Fatalf("planner planning support was accepted: %v", err)
	}
}

func TestPiReasoningMayBeDisabledWithoutWeakeningOtherHarnessContracts(t *testing.T) {
	cfg := explicitTestConfig()
	enabled := true
	cfg.Harnesses = []HarnessConfig{{
		Kind: HarnessPiCLI, Command: "pi", Enabled: &enabled, WorkspaceWriteRoot: "/worktrees",
	}}
	cfg.Roles = RoleTemplate(HarnessPiCLI)
	implementer := cfg.Roles[WorkRoleImplementer]
	implementer.Reasoning = "off"
	cfg.Roles[WorkRoleImplementer] = implementer
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Pi reasoning off was rejected: %v", err)
	}

	cfg = explicitTestConfig()
	implementer = cfg.Roles[WorkRoleImplementer]
	implementer.Reasoning = "off"
	cfg.Roles[WorkRoleImplementer] = implementer
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "low, medium, high, or xhigh") {
		t.Fatalf("Codex reasoning off was accepted: %v", err)
	}
}

func TestPiReasoningPreservationIsOptionalInheritedAndRoleScoped(t *testing.T) {
	cfg := explicitTestConfig()
	enabled := true
	cfg.Harnesses = []HarnessConfig{{
		Kind: HarnessPiCLI, Command: "pi", Enabled: &enabled, WorkspaceWriteRoot: "/worktrees",
	}}
	cfg.Roles = RoleTemplate(HarnessPiCLI)
	preserve := true
	reviewer := cfg.Roles[WorkRoleReviewer]
	reviewer.PreserveReasoning = &preserve
	cfg.Roles[WorkRoleReviewer] = reviewer
	doNotPreserve := false
	cfg.Roles["fresh_context_reviewer"] = RoleConfig{Extends: WorkRoleReviewer, PreserveReasoning: &doNotPreserve}

	inherited, ok := cfg.RoleProfile(WorkRoleReviewer)
	if !ok || inherited.PreserveReasoning == nil || !*inherited.PreserveReasoning {
		t.Fatalf("Pi reviewer did not preserve its explicit setting: %#v", inherited)
	}
	overridden, ok := cfg.RoleProfile("fresh_context_reviewer")
	if !ok || overridden.PreserveReasoning == nil || *overridden.PreserveReasoning {
		t.Fatalf("custom reviewer did not override inherited reasoning preservation: %#v", overridden)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Pi reasoning preservation was rejected: %v", err)
	}
	runtime, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if execution := runtime.Execution(WorkRoleReviewer, HarnessPiCLI, cfg.ProjectDir); !execution.PreserveReasoning {
		t.Fatalf("Pi execution omitted reasoning preservation: %#v", execution)
	}
	if execution := runtime.Execution("fresh_context_reviewer", HarnessPiCLI, cfg.ProjectDir); execution.PreserveReasoning {
		t.Fatalf("explicit false did not reach Pi execution: %#v", execution)
	}

	cfg = explicitTestConfig()
	reviewer = cfg.Roles[WorkRoleReviewer]
	reviewer.PreserveReasoning = &preserve
	cfg.Roles[WorkRoleReviewer] = reviewer
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "requires the pi harness") {
		t.Fatalf("non-Pi reasoning preservation was accepted: %v", err)
	}
}

func TestRoleAccessAndHarnessConfigurationAreExplicitAndInherited(t *testing.T) {
	cfg := explicitTestConfig()
	reviewer := cfg.Roles[WorkRoleReviewer]
	reviewer.Access = RoleAccessHost
	reviewer.HarnessConfig = HarnessConfigModeInherit
	cfg.Roles[WorkRoleReviewer] = reviewer
	cfg.Roles["browser_reviewer"] = RoleConfig{Extends: WorkRoleReviewer, Skills: []string{"runner-reviewer"}}
	profile, ok := cfg.RoleProfile("browser_reviewer")
	if !ok || profile.Access != RoleAccessHost || profile.HarnessConfig != HarnessConfigModeInherit {
		t.Fatalf("custom reviewer did not inherit its execution policy: %#v", profile)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("reviewer host access was rejected: %v", err)
	}
	planner := cfg.Roles[WorkRolePlanner]
	planner.Access = RoleAccessHost
	cfg.Roles[WorkRolePlanner] = planner
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit planner host access was rejected: %v", err)
	}
	planner.HarnessConfig = "ambient"
	cfg.Roles[WorkRolePlanner] = planner
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "harness_config must be isolated or inherit") {
		t.Fatalf("unknown harness configuration mode was accepted: %v", err)
	}
}

func TestHarnessIdentifiersUseProductNamesWithoutCompatibilityAliases(t *testing.T) {
	for _, kind := range []string{"codex", "claude", "pi"} {
		if !ValidHarnessKind(kind) {
			t.Fatalf("supported harness %q was rejected", kind)
		}
	}
	for _, legacy := range []string{"codex_cli", "claude_cli", "pi_cli"} {
		if ValidHarnessKind(legacy) {
			t.Fatalf("pre-release harness identifier %q remains accepted", legacy)
		}
	}
}

func TestPiInheritedHarnessConfigurationRequiresHostAccess(t *testing.T) {
	cfg := explicitTestConfig()
	planner := cfg.Roles[WorkRolePlanner]
	planner.Harness = HarnessPiCLI
	planner.HarnessConfig = HarnessConfigModeInherit
	cfg.Roles[WorkRolePlanner] = planner
	enabled := true
	cfg.Harnesses = append(cfg.Harnesses, HarnessConfig{Kind: HarnessPiCLI, Command: "pi", Enabled: &enabled, WorkspaceWriteRoot: "/tmp/worktrees"})
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "Pi cannot safely inherit") {
		t.Fatalf("sandboxed Pi inherited configuration was accepted: %v", err)
	}
	planner.Access = RoleAccessHost
	cfg.Roles[WorkRolePlanner] = planner
	if err := cfg.Validate(); err != nil {
		t.Fatalf("host Pi inherited configuration was rejected: %v", err)
	}
}

func TestPublicationWorkflowRequiresTerminalAndOutOfDateEvents(t *testing.T) {
	workflow := WorkflowTemplate(true)
	workflow.Events = workflow.Events[:len(workflow.Events)-1]
	cfg := explicitTestConfig()
	cfg.Workflow = &workflow
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), WorkflowEventPROutOfDate) {
		t.Fatalf("publication workflow accepted a missing out-of-date event: %v", err)
	}
}

func TestPublicationWorkflowRequiresDistinctMergedAndClosedOutcomes(t *testing.T) {
	workflow := WorkflowTemplate(true)
	for index := range workflow.Events {
		if workflow.Events[index].On == WorkflowEventPRClosed {
			workflow.Events[index].To = "done"
		}
	}
	cfg := explicitTestConfig()
	cfg.Workflow = &workflow
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "must target different lanes") {
		t.Fatalf("closed pull request could still share the successful merge lane: %v", err)
	}
}

func TestRoleProfilesSupportMultipleSkillsAndSafeExecutionOverrides(t *testing.T) {
	model := "configured-model"
	cfg := Config{
		Roles: RoleTemplate(HarnessCodexCLI),
	}
	cfg.Roles["security_reviewer"] = RoleConfig{
		Extends: "reviewer", Harness: HarnessClaudeCLI,
		Skills: []string{"runner-reviewer", "runner-planner"}, Model: &model, Reasoning: "xhigh", TimeoutSeconds: 2400,
	}
	profile, ok := cfg.RoleProfile("security_reviewer")
	if !ok || cfg.RoleContract("security_reviewer") != WorkRoleReviewer {
		t.Fatalf("custom reviewer role did not inherit its execution contract: %#v", profile)
	}
	if profile.Harness != HarnessClaudeCLI || strings.Join(profile.Skills, ",") != "runner-reviewer,runner-planner" || profile.Model == nil || *profile.Model != model || profile.Reasoning != "xhigh" || profile.TimeoutSeconds != 2400 {
		t.Fatalf("custom reviewer profile was not resolved: %#v", profile)
	}
}

func TestRoleProfileCarriesExplicitCodexMCPGrantsIntoExecution(t *testing.T) {
	cfg := explicitTestConfig()
	reviewer := cfg.Roles[WorkRoleReviewer]
	reviewer.MCPServers = []string{"chrome_dev_tools"}
	cfg.Roles[WorkRoleReviewer] = reviewer
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit Codex MCP grant was rejected: %v", err)
	}
	runtime, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	execution := runtime.Execution(WorkRoleReviewer, HarnessCodexCLI, cfg.ProjectDir)
	if strings.Join(execution.MCPServers, ",") != "chrome_dev_tools" {
		t.Fatalf("reviewer MCP grant was not carried into execution: %#v", execution)
	}

	reviewer.MCPServers = []string{"bad/server"}
	cfg.Roles[WorkRoleReviewer] = reviewer
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "invalid server name") {
		t.Fatalf("unsafe MCP server name was accepted: %v", err)
	}
}

func TestMCPGrantsRejectHarnessesWithoutIsolatedServerInjection(t *testing.T) {
	cfg := explicitTestConfig()
	reviewer := cfg.Roles[WorkRoleReviewer]
	reviewer.Harness = HarnessClaudeCLI
	reviewer.MCPServers = []string{"browser"}
	cfg.Roles[WorkRoleReviewer] = reviewer
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "currently requires the codex harness") {
		t.Fatalf("unsupported Claude MCP grant was accepted: %v", err)
	}
}

func TestRoleMCPGrantsBecomeRequiredDoctorCapabilitiesWithoutDuplicates(t *testing.T) {
	cfg := explicitTestConfig()
	reviewer := cfg.Roles[WorkRoleReviewer]
	reviewer.MCPServers = []string{"chrome_dev_tools"}
	cfg.Roles[WorkRoleReviewer] = reviewer
	cfg.Roles["future_reviewer"] = RoleConfig{Extends: WorkRoleReviewer, MCPServers: []string{"future_browser"}}
	cfg.DoctorRequirements = []CapabilityRequirement{{
		ID: HarnessCodexCLI + "/chrome_dev_tools", Type: CapabilityTypeMCPServer,
	}}

	requirements := cfg.EffectiveDoctorRequirements()
	if len(requirements) != 5 || requirements[0].ID != HarnessCodexCLI+"/chrome_dev_tools" || requirements[0].Type != CapabilityTypeMCPServer || !requirements[0].Required {
		t.Fatalf("role MCP grant was not promoted to one required Doctor check: %#v", requirements)
	}
	runtime, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	if len(runtime.DoctorRequirements) != 5 || !runtime.DoctorRequirements[0].Required {
		t.Fatalf("runtime lost the effective Doctor requirement: %#v", runtime.DoctorRequirements)
	}
}

func TestSandboxedHarnessesInheritSafeToolsWithExplicitOptOut(t *testing.T) {
	for _, harness := range []string{HarnessCodexCLI, HarnessClaudeCLI} {
		t.Run(harness, func(t *testing.T) {
			cfg := explicitTestConfig()
			cfg.Harnesses[0].Kind = harness
			cfg.Harnesses[0].Command = harness
			cfg.Roles = RoleTemplate(harness)
			if cfg.RoleSafeTools(WorkRolePlanner) || !cfg.RoleSafeTools(WorkRoleImplementer) || !cfg.RoleSafeTools(WorkRoleReviewer) {
				t.Fatalf("unexpected default safe-tool roles: planner=%t implementer=%t reviewer=%t", cfg.RoleSafeTools(WorkRolePlanner), cfg.RoleSafeTools(WorkRoleImplementer), cfg.RoleSafeTools(WorkRoleReviewer))
			}
			disabled := false
			reviewer := cfg.Roles[WorkRoleReviewer]
			reviewer.SafeTools = &disabled
			cfg.Roles[WorkRoleReviewer] = reviewer
			if cfg.RoleSafeTools(WorkRoleReviewer) {
				t.Fatal("explicit safe-tools opt-out was ignored")
			}
			runtime, err := cfg.Resolve()
			if err != nil {
				t.Fatalf("resolve safe-tool defaults: %v", err)
			}
			if !runtime.Execution(WorkRoleImplementer, harness, cfg.ProjectDir).SafeTools || runtime.Execution(WorkRoleReviewer, harness, cfg.ProjectDir).SafeTools {
				t.Fatal("runtime lost safe-tool default or opt-out")
			}
			if len(runtime.DoctorRequirements) != 4 {
				t.Fatalf("safe-tool defaults did not imply four local readiness checks: %#v", runtime.DoctorRequirements)
			}
			for _, requirement := range runtime.DoctorRequirements {
				if requirement.Type != CapabilityTypeLocalTool {
					continue
				}
				if requirement.ID == "chrome" && requirement.Required {
					t.Fatal("safe browser availability became a project requirement without operator intent")
				}
				if requirement.ID != "chrome" && !requirement.Required {
					t.Fatalf("safe development tool %q unexpectedly became optional", requirement.ID)
				}
			}
		})
	}

	cfgWithRequiredBrowser := explicitTestConfig()
	cfgWithRequiredBrowser.DoctorRequirements = []CapabilityRequirement{{ID: "chrome", Type: CapabilityTypeLocalTool, Required: true}}
	requiredBrowserRuntime, err := cfgWithRequiredBrowser.Resolve()
	if err != nil {
		t.Fatalf("resolve explicit browser requirement: %v", err)
	}
	for _, requirement := range requiredBrowserRuntime.DoctorRequirements {
		if requirement.ID == "chrome" && requirement.Type == CapabilityTypeLocalTool && !requirement.Required {
			t.Fatal("safe-tool inference weakened an explicit browser requirement")
		}
	}

	cfg := explicitTestConfig()
	cfg.Roles = RoleTemplate(HarnessPiCLI)
	enabled := true
	cfg.Harnesses = []HarnessConfig{{Kind: HarnessPiCLI, Command: "pi", Enabled: &enabled, WorkspaceWriteRoot: t.TempDir()}}
	if !cfg.RoleSafeTools(WorkRoleImplementer) || !cfg.RoleSafeTools(WorkRoleReviewer) {
		t.Fatal("Pi did not inherit Runner's controlled browser tools")
	}
	disabledSafeTools := false
	implementer := cfg.Roles[WorkRoleImplementer]
	implementer.Access = RoleAccessHost
	implementer.SafeTools = &disabledSafeTools
	cfg.Roles[WorkRoleImplementer] = implementer
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Pi rejected an explicit safe-tools opt-out: %v", err)
	}
	if cfg.RoleSafeTools(WorkRoleImplementer) {
		t.Fatal("Pi ignored the explicit safe-tools opt-out")
	}
}

func TestWorkflowLaneRoleTakesPrecedenceOverBuiltInContractRole(t *testing.T) {
	workflow := WorkflowTemplate(true)
	plan := workflow.Lanes["plan"]
	plan.Role = "product_planner"
	workflow.Lanes["plan"] = plan
	cfg := Config{
		Workflow: &workflow,
		Roles:    RoleTemplate(HarnessCodexCLI),
	}
	cfg.Roles["product_planner"] = RoleConfig{Extends: WorkRolePlanner, Skills: []string{"runner-planner", "product-planning"}}
	if got := cfg.RoleIDForContract(WorkRolePlanner); got != "product_planner" {
		t.Fatalf("planner contract resolved to %q instead of the role assigned to the Plan lane", got)
	}
}

func TestUnusedRoleDefinitionsDoNotAddRuntimeHarnessDependencies(t *testing.T) {
	workflow := WorkflowTemplate(true)
	cfg := Config{
		Workflow: &workflow,
		Roles:    RoleTemplate(HarnessCodexCLI),
	}
	cfg.Roles["future_reviewer"] = RoleConfig{Extends: WorkRoleReviewer, Harness: HarnessPiCLI, Skills: []string{"future-review"}}
	if got := strings.Join(cfg.ConfiguredRoleHarnesses(), ","); got != HarnessCodexCLI {
		t.Fatalf("unused role added harness dependencies: %q", got)
	}
}

func TestImplementerLadderIsOptionalBoundedAndActivatesItsProfiles(t *testing.T) {
	cfg := explicitTestConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config without implementer ladder is invalid: %v", err)
	}
	luna, sol := "gpt-luna", "gpt-sol"
	cfg.Roles["implementer_luna"] = RoleConfig{Extends: WorkRoleImplementer, Model: &luna}
	cfg.Roles["implementer_sol"] = RoleConfig{Extends: WorkRoleImplementer, Model: &sol}
	cfg.ImplementerLadder = []string{WorkRoleImplementer, "implementer_luna"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("two-profile implementer ladder is invalid: %v", err)
	}
	if got := strings.Join(cfg.ExecutionRoleIDs(), ","); !strings.Contains(got, "implementer_luna") || strings.Contains(got, "implementer_sol") {
		t.Fatalf("execution roles did not distinguish active and unused profiles: %q", got)
	}
	runtime, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("resolve implementer ladder: %v", err)
	}
	if strings.Join(runtime.ImplementerLadder, ",") != "implementer,implementer_luna" {
		t.Fatalf("runtime lost implementer ladder: %#v", runtime.ImplementerLadder)
	}

	cfg.ImplementerLadder = []string{WorkRoleImplementer, "implementer_luna", "implementer_sol"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("three-profile ladder within the QA retry budget is invalid: %v", err)
	}
	cfg.Roles["implementer_extra"] = RoleConfig{Extends: WorkRoleImplementer}
	cfg.ImplementerLadder = append(cfg.ImplementerLadder, "implementer_extra")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("ladder using the initial attempt plus all QA retries is invalid: %v", err)
	}
	cfg.Roles["implementer_overflow"] = RoleConfig{Extends: WorkRoleImplementer}
	cfg.ImplementerLadder = append(cfg.ImplementerLadder, "implementer_overflow")
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "at most 4 implementation attempts") {
		t.Fatalf("unreachable ladder profile was accepted: %v", err)
	}
}

func TestImplementerLadderRejectsAmbiguousOrInvalidProfiles(t *testing.T) {
	tests := []struct {
		name   string
		ladder []string
		want   string
		setup  func(*Config)
	}{
		{name: "one role", ladder: []string{WorkRoleImplementer}, want: "at least two roles"},
		{name: "wrong first role", ladder: []string{"implementer_luna", WorkRoleImplementer}, want: "implementer_ladder[0]", setup: addLunaRole},
		{name: "duplicate", ladder: []string{WorkRoleImplementer, WorkRoleImplementer}, want: "duplicate"},
		{name: "undefined", ladder: []string{WorkRoleImplementer, "missing"}, want: "undefined role"},
		{name: "wrong contract", ladder: []string{WorkRoleImplementer, "reviewer_copy"}, want: "implementer contract", setup: func(cfg *Config) {
			cfg.Roles["reviewer_copy"] = RoleConfig{Extends: WorkRoleReviewer}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := explicitTestConfig()
			if test.setup != nil {
				test.setup(&cfg)
			}
			cfg.ImplementerLadder = test.ladder
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid ladder was accepted: %v", err)
			}
		})
	}
}

func addLunaRole(cfg *Config) {
	model := "gpt-luna"
	cfg.Roles["implementer_luna"] = RoleConfig{Extends: WorkRoleImplementer, Model: &model}
}

func TestPartialBuiltInRoleDoesNotInheritHiddenDefaults(t *testing.T) {
	cfg := explicitTestConfig()
	cfg.Roles[WorkRoleReviewer] = RoleConfig{Reasoning: "medium"}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "harness") {
		t.Fatalf("partial role unexpectedly inherited hidden runtime settings: %v", err)
	}
}

func TestWorkflowValidationExplainsBrokenReviewerRouting(t *testing.T) {
	cfg := explicitTestConfig()
	workflow := WorkflowTemplate(true)
	qa := workflow.Lanes["agent_qa"]
	qa.MaxQARetries = 0
	delete(qa.Transitions, WorkflowOutcomeExhausted)
	workflow.Lanes["agent_qa"] = qa
	cfg.Workflow = &workflow
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "max_qa_retries") {
		t.Fatalf("broken reviewer lane returned unclear validation: %v", err)
	}
}

func TestWorkflowConfigurationRejectsRemovedRejectLimit(t *testing.T) {
	data, err := json.Marshal(explicitTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(string(data), `"max_qa_retries":3`, `"reject_limit":3`, 1)
	if legacy == string(data) {
		t.Fatal("test config did not contain max_qa_retries")
	}
	if _, err := decodeConfig([]byte(legacy)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("removed reject_limit was accepted: %v", err)
	}
}

func TestWorkflowProjectRuntimeUsesLanesAndNoRoleField(t *testing.T) {
	workflow := WorkflowTemplate(true)
	ready := workflow.Lanes["ready"]
	ready.Name = "Build"
	workflow.Lanes["ready"] = ready
	cfg := explicitTestConfig()
	cfg.Workflow = &workflow
	project := cfg.ResolveProject()
	if project.ReadyStatus != "Build" || project.RunningStatus != "In Progress" || project.QAStatus != "Agent QA" || project.TransitionField != RunnerTransitionFieldName {
		t.Fatalf("workflow was not projected into the GitHub source: %#v", project)
	}
	if !containsNormalized(project.RequiredStatuses, "Plan") || !containsNormalized(project.AgentStatuses, "Build") || !containsNormalized(project.AgentStatuses, "Agent QA") {
		t.Fatalf("workflow status sets are incomplete: required=%#v agents=%#v", project.RequiredStatuses, project.AgentStatuses)
	}
}

func TestHarnessCommandIsExecutableNotShellConfiguration(t *testing.T) {
	base := explicitTestConfig()
	base.Harnesses[0].Command = "/Applications/Codex CLI/codex"
	if err := base.Validate(); err != nil {
		t.Fatalf("absolute executable path with spaces should be accepted: %v", err)
	}
	base.Harnesses[0].Command = "codex\n--dangerously-extra-argument"
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "one executable") {
		t.Fatalf("command containing control characters was accepted: %v", err)
	}
}

func TestHarnessExecutionPolicyIsRejected(t *testing.T) {
	data, err := json.Marshal(explicitTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	malicious := strings.Replace(string(data), `"command":"codex"`, `"command":"codex","execution_policy":{"mode":"danger-full-access"}`, 1)
	if _, err := decodeConfig([]byte(malicious)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("operator-controlled execution policy was accepted: %v", err)
	}
}

func TestConfigurationRejectsUnpinnedRoleSkills(t *testing.T) {
	cfg := explicitTestConfig()
	reviewer := cfg.Roles[WorkRoleReviewer]
	reviewer.Skills = []string{"custom-reviewer"}
	cfg.Roles[WorkRoleReviewer] = reviewer
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported unpinned skill") {
		t.Fatalf("unpinned role skill was accepted: %v", err)
	}
}

func TestConfigurationRequiresExplicitBoundedParallelism(t *testing.T) {
	for _, value := range []int{0, MaxSupportedParallelism + 1} {
		cfg := explicitTestConfig()
		cfg.MaxParallelism = value
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "max_parallelism") {
			t.Fatalf("max_parallelism %d was accepted: %v", value, err)
		}
	}
}

func TestConfigurationValidatesOptionalAdmissionBudget(t *testing.T) {
	tests := []AdmissionBudgetConfig{
		{WindowSeconds: 0, MaxAttempts: 1},
		{WindowSeconds: 3600},
		{WindowSeconds: 3600, MaxReportedTokens: -1},
		{WindowSeconds: 1<<63 - 1, MaxAttempts: 1},
	}
	for _, budget := range tests {
		cfg := explicitTestConfig()
		cfg.AdmissionBudget = &budget
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "admission_budget") {
			t.Fatalf("invalid admission budget was accepted: %#v error=%v", budget, err)
		}
	}
	cfg := explicitTestConfig()
	cfg.AdmissionBudget = &AdmissionBudgetConfig{WindowSeconds: 86400, MaxAttempts: 12, MaxHarnessSeconds: 7200}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid admission budget was rejected: %v", err)
	}
}

func TestAutoMergeRequiresPullRequestPublicationLane(t *testing.T) {
	cfg := explicitTestConfig()
	cfg.GitHubProject.AutoMerge = true
	workflow := cloneWorkflow(*cfg.Workflow)
	publication := workflow.Lanes["pr_ready"]
	publication.OnEnter = ""
	workflow.Lanes["pr_ready"] = publication
	cfg.Workflow = &workflow

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "auto_merge requires a workflow publication lane") {
		t.Fatalf("auto merge without publication lane was accepted: %v", err)
	}
}

func TestConfigurationRequiresExplicitBaseUpdateReviewPolicy(t *testing.T) {
	skipReview := false
	for _, policy := range []*bool{nil, &skipReview} {
		cfg := explicitTestConfig()
		workflow := cloneWorkflow(*cfg.Workflow)
		for index := range workflow.Events {
			if workflow.Events[index].On == WorkflowEventPROutOfDate {
				workflow.Events[index].RequireReview = policy
			}
		}
		cfg.Workflow = &workflow
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "require_review") {
			t.Fatalf("unsafe base-update review policy was accepted: %v", err)
		}
	}
}

func TestConfigurationValidatesAndDefaultsMergeMethod(t *testing.T) {
	cfg := explicitTestConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("legacy empty merge method: %v", err)
	}
	if got := cfg.ResolveProject().MergeMethod; got != MergeMethodMerge {
		t.Fatalf("legacy merge method = %q, want %q", got, MergeMethodMerge)
	}
	cfg.GitHubProject.MergeMethod = MergeMethodSquash
	if err := cfg.Validate(); err != nil {
		t.Fatalf("squash merge method: %v", err)
	}
	cfg.GitHubProject.MergeMethod = "octopus"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "merge_method") {
		t.Fatalf("unsupported merge method was accepted: %v", err)
	}
}

func TestConfigurationValidatesAutonomousIssueAuthors(t *testing.T) {
	cfg := explicitTestConfig()
	cfg.GitHubProject.AutonomousIssueIntake = &AutonomousIssueIntakeConfig{TrustedAuthors: []string{"Dan", " dan "}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("case-insensitive duplicate trusted author was accepted: %v", err)
	}
	cfg.GitHubProject.AutonomousIssueIntake.TrustedAuthors = []string{" "}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "cannot be blank") {
		t.Fatalf("blank trusted author was accepted: %v", err)
	}
	cfg.GitHubProject.AutonomousIssueIntake.TrustedAuthors = []string{"dan"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid autonomous issue policy was rejected: %v", err)
	}
}

func TestRunnerActivityForRoleContract(t *testing.T) {
	for contract, expected := range map[string]string{
		WorkRolePlanner: "Planning", WorkRoleImplementer: "Implementing", WorkRoleReviewer: "Reviewing", "custom": "Running",
	} {
		if actual := RunnerActivityForRoleContract(contract); actual != expected {
			t.Fatalf("activity for %q = %q, want %q", contract, actual, expected)
		}
	}
}

func explicitTestConfig() Config {
	enabled := true
	workflow := WorkflowTemplate(true)
	return Config{
		ConfigVersion:  ConfigVersion,
		RunnerID:       "runner",
		ProjectDir:     "/project",
		MaxParallelism: 1,
		Harnesses: []HarnessConfig{{
			Kind: HarnessCodexCLI, Command: "codex", Enabled: &enabled,
			WorkspaceWriteRoot: "/worktrees",
		}},
		Roles:    RoleTemplate(HarnessCodexCLI),
		Workflow: &workflow,
		GitHubProject: &GitHubProjectConfig{
			Owner: "example", Number: 7, IntakeRepository: "example/repo", IntakeLabel: "needs-assessment",
			ResultField: "Runner Result", ApprovalField: "Runner Approval", PhaseField: "Runner Phase",
			QAFailuresField: "QA Failures", BranchField: "Runner Branch", PullRequestField: "Pull Request",
			QACommitField: "QA Commit", BaseBranch: "main", RemoteName: "origin",
		},
	}
}

func containsNormalized(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(wanted)) {
			return true
		}
	}
	return false
}
