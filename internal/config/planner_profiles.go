package config

import (
	"fmt"
	"slices"
	"strings"
)

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
