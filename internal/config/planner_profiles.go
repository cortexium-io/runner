package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// planImplementationProfileDigests binds the actual configured execution
// policy, not just a mutable profile name. Include every reachable ladder step
// because the authenticated rejection count can select one without new approval.
// This projection contains only Runner settings, never provider credentials or
// inherited provider configuration. Descriptive prose is not execution policy.
func (c Config) planImplementationProfileDigests() map[string]string {
	allowed := append([]string(nil), c.PlannerImplementers...)
	if len(allowed) == 0 {
		allowed = []string{c.AttemptRole(c.RoleIDForContract(WorkRoleImplementer), 0)}
	}
	type profileBinding struct {
		ID      string        `json:"id"`
		Profile RoleConfig    `json:"profile"`
		Harness HarnessConfig `json:"harness"`
	}
	result := make(map[string]string, len(allowed))
	for _, selected := range allowed {
		steps := []string{selected}
		if len(c.ImplementerLadder) > 0 {
			start := slices.Index(c.ImplementerLadder, selected)
			if start < 0 {
				continue
			}
			steps = c.ImplementerLadder[start:]
		}
		profiles := make([]profileBinding, 0, len(steps))
		for _, id := range steps {
			profile, ok := c.RoleProfile(id)
			if !ok || c.RoleContract(id) != WorkRoleImplementer {
				break
			}
			harness, ok := c.Harness(profile.Harness)
			if !ok {
				break
			}
			profile.Description, profile.Extends = "", ""
			profile.Access = EffectiveRoleAccess(profile.Access)
			profile.HarnessConfig = EffectiveHarnessConfigMode(profile.HarnessConfig)
			profile.TaskGranularity = EffectiveTaskGranularity(profile.TaskGranularity)
			safeTools := c.RoleSafeTools(id)
			profile.SafeTools = &safeTools
			preserveReasoning := profile.PreserveReasoning != nil && *profile.PreserveReasoning
			profile.PreserveReasoning = &preserveReasoning
			// Enabled is already resolved by Harness. Role-owned model, reasoning
			// and timeout are in Profile (Harness's runtime fields are json:"-").
			harness.Enabled = nil
			profiles = append(profiles, profileBinding{ID: id, Profile: profile, Harness: harness})
		}
		if len(profiles) != len(steps) {
			continue
		}
		encoded, _ := json.Marshal(struct {
			Profiles             []profileBinding      `json:"profiles"`
			RepositoryReferences []RepositoryReference `json:"repository_references"`
			ReviewEvidencePaths  []string              `json:"review_evidence_paths"`
		}{profiles, c.RepositoryReferences, c.ReviewEvidencePaths})
		result[selected] = fmt.Sprintf("v1:%x", sha256.Sum256(encoded))
	}
	return result
}

func validatePlannerImplementers(c Config) error {
	seen := map[string]bool{}
	for _, id := range c.PlannerImplementers {
		if id == "" || id != strings.TrimSpace(id) || seen[id] {
			return fmt.Errorf("planner_implementers contains an empty, noncanonical, or duplicate profile %q", id)
		}
		seen[id] = true
		profile, ok := c.RoleProfile(id)
		if !ok || c.RoleContract(id) != WorkRoleImplementer {
			return fmt.Errorf("planner_implementers profile %q must use the implementer contract", id)
		}
		if strings.TrimSpace(profile.Description) == "" {
			return fmt.Errorf("planner_implementers profile %q needs a description of suitable tasks", id)
		}
		if len(c.ImplementerLadder) > 0 && !slices.Contains(c.ImplementerLadder, id) {
			return fmt.Errorf("planner_implementers profile %q must appear in implementer_ladder when configured", id)
		}
	}
	return nil
}

// SelectedImplementer validates the approved starting profile against the current
// operator allowlist. Configuration removal fails closed rather than silently
// substituting a different execution policy.
func (c RuntimeConfig) SelectedImplementer(defaultRole, selected string, failures int) (string, error) {
	if c.RoleContract(defaultRole) != WorkRoleImplementer {
		return defaultRole, nil
	}
	if selected == "" {
		return c.AttemptRole(defaultRole, failures), nil
	}
	if len(c.PlannerImplementers) == 0 && selected == c.AttemptRole(defaultRole, 0) {
		return c.AttemptRole(defaultRole, failures), nil
	}
	if !slices.Contains(c.PlannerImplementers, selected) || c.RoleContract(selected) != WorkRoleImplementer {
		return "", fmt.Errorf("approved implementation profile %q is not available in planner_implementers", selected)
	}
	if len(c.ImplementerLadder) == 0 {
		return selected, nil
	}
	start := slices.Index(c.ImplementerLadder, selected)
	if start < 0 {
		return "", fmt.Errorf("approved implementation profile %q is absent from implementer_ladder", selected)
	}
	return ladderRole(selected, failures, c.ImplementerLadder[start:]), nil
}
