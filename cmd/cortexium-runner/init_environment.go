package main

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/setup"
)

func prepareInitTools(cfg config.Config, dryRun bool, stdout io.Writer) error {
	harnesses, err := configuredHarnesses(cfg)
	if err != nil {
		return err
	}
	sort.Strings(harnesses)
	fmt.Fprintf(stdout, "Selected harnesses: %s\n", strings.Join(harnesses, ", "))
	for _, kind := range harnesses {
		if harness, ok := cfg.Harness(kind); ok {
			fmt.Fprintf(stdout, "  %s execution policy: %s\n", kind, harness.ExecutionPolicySummary())
		}
	}
	fmt.Fprintln(stdout, "Selected roles:")
	for _, role := range config.BuiltinRoleIDs() {
		profile, ok := cfg.RoleProfile(role)
		if !ok {
			continue
		}
		model := "harness-native"
		if profile.Model != nil && strings.TrimSpace(*profile.Model) != "" {
			model = strings.TrimSpace(*profile.Model)
		}
		taskGranularity := ""
		if role == config.WorkRoleImplementer || role == config.WorkRoleReviewer {
			taskGranularity = " · task sizing " + taskSizingLabel(profile.TaskGranularity)
		}
		mode := config.EffectiveHarnessConfigMode(profile.HarnessConfig)
		fmt.Fprintf(stdout, "  %s: %s · model %s · reasoning %s · access %s · harness config %s%s\n", role, profile.Harness, model, profile.Reasoning, config.EffectiveRoleAccess(profile.Access), mode, taskGranularity)
		if config.EffectiveRoleAccess(profile.Access) == config.RoleAccessHost && mode == config.HarnessConfigModeInherit {
			fmt.Fprintln(stdout, "    WARNING: this role inherits ambient tools and configuration with unrestricted host access")
		}
	}
	if len(cfg.ImplementerLadder) > 0 {
		fmt.Fprintf(stdout, "Implementer ladder: %s\n", strings.Join(cfg.ImplementerLadder, " -> "))
	}
	fmt.Fprintf(stdout, "Concurrent agent work: up to %d independent card(s)\n", cfg.MaxParallelism)
	writeAdmissionBudgetSummary(stdout, cfg.AdmissionBudget)
	mergeMode := "human review and merge"
	if cfg.GitHubProject != nil && cfg.GitHubProject.AutoMerge {
		mergeMode = fmt.Sprintf("automatic %s after GitHub requirements pass", config.NormalizeMergeMethod(cfg.GitHubProject.MergeMethod))
	}
	fmt.Fprintf(stdout, "Pull request merge: %s\n", mergeMode)
	requiredTools, err := setup.RequiredToolsForHarnesses(harnesses)
	if err != nil {
		return err
	}
	requiredTools = respectConfiguredHarnessCommands(requiredTools, cfg)
	prerequisites := setup.NewPrerequisiteInstaller()
	steps, err := prerequisites.Plan(requiredTools)
	if err != nil {
		return err
	}
	if len(steps) == 0 {
		return nil
	}
	fmt.Fprintln(stdout, "Missing prerequisites:")
	for _, step := range steps {
		fmt.Fprintf(stdout, "  - %s: %s\n", step.Tool, step.Instruction)
	}
	if dryRun {
		return nil
	}
	return fmt.Errorf("missing prerequisites; install the tools above with their official distribution or trusted local package manager, then rerun init")
}

func taskSizingLabel(value string) string {
	if config.EffectiveTaskGranularity(value) == config.TaskGranularitySmall {
		return "small"
	}
	return "regular"
}

func writeAdmissionBudgetSummary(stdout io.Writer, budget *config.AdmissionBudgetConfig) {
	if budget == nil {
		fmt.Fprintln(stdout, "Agent admission: no rolling budget configured")
		return
	}
	ceilings := make([]string, 0, 4)
	if budget.MaxAttempts > 0 {
		ceilings = append(ceilings, fmt.Sprintf("%d attempt(s)", budget.MaxAttempts))
	}
	if budget.MaxHarnessSeconds > 0 {
		ceilings = append(ceilings, formatStatusDuration(time.Duration(budget.MaxHarnessSeconds)*time.Second)+" harness time")
	}
	if budget.MaxReportedTokens > 0 {
		ceilings = append(ceilings, fmt.Sprintf("%d reported tokens", budget.MaxReportedTokens))
	}
	if budget.MaxReportedCostUSD != nil {
		ceilings = append(ceilings, fmt.Sprintf("$%.4f reported cost", *budget.MaxReportedCostUSD))
	}
	fmt.Fprintf(stdout, "Agent admission: rolling %s window · %s\n", formatStatusDuration(time.Duration(budget.WindowSeconds)*time.Second), strings.Join(ceilings, " · "))
}

func installInitSkills(cfg config.Config, configPath string, force, dryRun bool, stdout io.Writer) error {
	harnesses, err := configuredHarnesses(cfg)
	if err != nil {
		return err
	}
	sort.Strings(harnesses)
	if dryRun {
		fmt.Fprintf(stdout, "  Bundled role skills: install or update for %s\n", strings.Join(harnesses, ", "))
		return nil
	}
	action := "Installing bundled Runner role skills…"
	if force {
		action = "Installing or repairing bundled Runner role skills…"
	}
	writeProgress(stdout, action)
	results, err := setup.NewSkillInstaller().InstallConfigured(cfg, force)
	for _, result := range results {
		fmt.Fprintf(stdout, "%s: %s for %s (%s)\n", result.Status, result.Skill, result.Harness, result.Path)
	}
	if errors.Is(err, setup.ErrDifferingSkill) && !force {
		command := fmt.Sprintf("cortexium-runner doctor --config %q --fix", configPath)
		return fmt.Errorf("%w; the differing file was left unchanged—review it, then run `%s`", err, command)
	}
	return err
}

func respectConfiguredHarnessCommands(tools []string, cfg config.Config) []string {
	canonicalCommands := map[string]string{
		config.HarnessCodexCLI: "codex", config.HarnessClaudeCLI: "claude", config.HarnessPiCLI: "pi",
	}
	replaced := map[string]bool{}
	for _, harness := range cfg.Harnesses {
		command := strings.TrimSpace(harness.Command)
		if canonical := canonicalCommands[strings.TrimSpace(harness.Kind)]; command != "" && command != canonical {
			replaced[canonical] = true
		}
	}
	result := make([]string, 0, len(tools))
	for _, tool := range tools {
		if !replaced[tool] {
			result = append(result, tool)
		}
	}
	return result
}

func configuredHarnesses(cfg config.Config) ([]string, error) {
	if !cfg.HasProject() {
		return nil, fmt.Errorf("runner config must define a GitHub Project before init can prepare harnesses")
	}
	result := uniqueStrings(cfg.ConfiguredRoleHarnesses())
	for _, kind := range result {
		if !config.ValidHarnessKind(kind) {
			return nil, fmt.Errorf("unsupported harness %q", kind)
		}
	}
	return result, nil
}
