package setup

import (
	"fmt"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
)

type missingRoleHarness struct {
	Role        string
	Kind        string
	DisplayName string
	Skill       string
}

func roleHarnessReadiness(cfg config.Config, harnesses []HarnessInspection, capabilities []CapabilityState) (bool, []missingRoleHarness) {
	if !cfg.HasProject() {
		return true, nil
	}
	byKind := map[string]HarnessInspection{}
	for _, harness := range harnesses {
		byKind[harness.Kind] = harness
	}
	missing := []missingRoleHarness{}
	for _, role := range cfg.ExecutionRoleIDs() {
		profile, ok := cfg.RoleProfile(role)
		if !ok {
			continue
		}
		kind := strings.TrimSpace(profile.Harness)
		harness, harnessOK := byKind[kind]
		for _, skill := range profile.Skills {
			if harnessOK && harness.Status == CapabilityAvailable && capabilityAvailable(capabilities, config.CapabilityTypeSkill, harnessSkillCapabilityID(kind, skill)) {
				continue
			}
			displayName := map[string]string{config.HarnessCodexCLI: "Codex CLI", config.HarnessClaudeCLI: "Claude Code", config.HarnessPiCLI: "Pi CLI"}[kind]
			if displayName == "" {
				displayName = kind
			}
			missing = append(missing, missingRoleHarness{Role: role, Kind: kind, DisplayName: displayName, Skill: skill})
		}
	}
	return len(missing) == 0, missing
}

func doctorRecommendations(capabilities []CapabilityState, harnesses []HarnessInspection, missing []config.CapabilityRequirement, sourceReady, projectReady, recommendBundledSetup bool) []string {
	result := []string{}
	if !capabilityAvailable(capabilities, config.CapabilityTypeLocalTool, "git") {
		result = append(result, "Install Git from https://git-scm.com/downloads and ensure `git` is available in PATH, then run doctor again.")
	}
	if !capabilityAvailable(capabilities, config.CapabilityTypeLocalTool, "gh") {
		result = append(result, "Install GitHub CLI from https://github.com/cli/cli#installation, authenticate it for the intended GitHub account, then run doctor again.")
	} else if !capabilityAvailable(capabilities, config.CapabilityTypeProfile, "github_api") {
		result = append(result, "Run `gh auth login`, then grant the `project` scope with `gh auth refresh -s project`.")
	}
	installed, ready := 0, 0
	for _, harness := range harnesses {
		if harness.Status == CapabilityAvailable {
			installed++
		}
		if harness.Ready {
			ready++
		}
	}
	if installed == 0 {
		result = append(result, "Install a supported AI harness and authenticate it using its native setup flow; Runner does not install or configure AI harnesses.")
	} else if ready == 0 && recommendBundledSetup {
		result = append(result, "Re-run `cortexium-runner init` to install the bundled planner, implementer, and reviewer skills.")
	}
	if !projectReady {
		result = append(result, "Run doctor from a local Git repository or pass `--project-dir /path/to/repository`.")
	}
	if !sourceReady {
		result = append(result, "Check GitHub CLI access and the configured Project Kanban board and Status field/options; no webhook or inbound listener is required.")
	}
	for _, requirement := range missing {
		result = append(result, fmt.Sprintf("Provide required %s capability %q, then run doctor again.", requirement.Type, requirement.ID))
	}
	return result
}
