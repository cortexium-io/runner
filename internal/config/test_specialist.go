package config

import (
	"fmt"
	"path"
	"strings"
)

func validateTestSpecialist(c Config) error {
	policy := c.TestSpecialist
	if policy == nil {
		return nil
	}
	if len(policy.AllowedPaths) > 32 || (policy.Enabled && len(policy.AllowedPaths) == 0) {
		return fmt.Errorf("test_specialist requires 1–32 explicit test/fixture paths when enabled")
	}
	if err := ValidateReviewEvidencePaths(policy.AllowedPaths); err != nil {
		return fmt.Errorf("test_specialist.allowed_paths: %w", err)
	}
	for _, selected := range policy.AllowedPaths {
		base := strings.ToLower(path.Base(selected))
		if path.Ext(base) == "" || strings.Contains(base, ".config.") || strings.HasPrefix(base, "tsconfig") || strings.HasPrefix(base, "jsconfig") {
			return fmt.Errorf("test_specialist.allowed_paths requires exact test/fixture files, not directory or configuration grants: %s", selected)
		}
		for _, part := range strings.Split(selected, "/") {
			if strings.HasPrefix(part, ".") || strings.EqualFold(part, "node_modules") {
				return fmt.Errorf("test_specialist.allowed_paths cannot select administration, hidden controls or dependencies: %s", selected)
			}
		}
		switch base {
		case "agents.md", "claude.md", "gemini.md", "package.json", "package-lock.json", "go.mod", "go.sum", "makefile", "cargo.toml", "cargo.lock", "pyproject.toml", "requirements.txt":
			return fmt.Errorf("test_specialist.allowed_paths cannot select control or dependency files: %s", selected)
		}
	}
	if policy.Enabled {
		for _, id := range c.ExecutionRoleIDs() {
			if c.RoleContract(id) != WorkRoleImplementer {
				continue
			}
			profile, ok := c.RoleProfile(id)
			if !ok || (profile.Harness != HarnessCodexCLI && profile.Harness != HarnessClaudeCLI) {
				return fmt.Errorf("test_specialist requires a native Codex or Claude adapter for every reachable implementer; roles.%s uses %q (disable the specialist or explicitly configure a supported profile)", id, profile.Harness)
			}
		}
	}
	return nil
}

func cloneTestSpecialist(policy *TestSpecialistConfig) *TestSpecialistConfig {
	// An explicitly disabled policy has exactly the absent policy's effective
	// authority; staging its paths must not invalidate existing approvals.
	if policy == nil || !policy.Enabled {
		return nil
	}
	return &TestSpecialistConfig{Enabled: true, AllowedPaths: append([]string(nil), policy.AllowedPaths...)}
}
