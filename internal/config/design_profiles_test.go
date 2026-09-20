package config

import (
	"slices"
	"strings"
	"testing"
)

func TestInteractionDesignProfilesRetainBaseContracts(t *testing.T) {
	for _, contract := range BuiltinRoleIDs() {
		t.Run(contract, func(t *testing.T) {
			cfg := explicitTestConfig()
			base := cfg.Roles[contract]
			id := "design_" + contract
			cfg.Roles[id] = RoleConfig{Extends: contract, Skills: []string{"runner-" + contract, "runner-interaction-design"}}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			profile, ok := cfg.RoleProfile(id)
			if !ok || cfg.RoleContract(id) != contract || profile.Access != base.Access || profile.Harness != base.Harness || profile.TimeoutSeconds != base.TimeoutSeconds || profile.Reasoning != base.Reasoning || !slices.Equal(profile.Skills, cfg.Roles[id].Skills) {
				t.Fatalf("design specialty changed contract: %#v", profile)
			}
			if slices.Contains(cfg.Roles[contract].Skills, "runner-interaction-design") {
				t.Fatal("changed default role")
			}
			cfg.Roles[id] = RoleConfig{Extends: contract, Skills: []string{"runner-interaction-design"}}
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "must retain runner-"+contract) {
				t.Fatalf("lost ordinary role policy: %v", err)
			}
		})
	}
}
