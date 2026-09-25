package config

import "testing"

func TestCardVerificationRequiresExplicitOperatorGrant(t *testing.T) {
	for _, tc := range []struct {
		name  string
		gate  *CardVerificationConfig
		plan  bool
		valid bool
	}{
		{"disabled", nil, false, true},
		{"explicit command grant", &CardVerificationConfig{Entrypoint: "browser", Access: RoleAccessHost}, false, true},
		{"missing access", &CardVerificationConfig{Entrypoint: "browser"}, false, false},
		{"sandbox is not host authorization", &CardVerificationConfig{Entrypoint: "browser", Access: RoleAccessSandboxed}, false, false},
		{"unknown command", &CardVerificationConfig{Entrypoint: "missing", Access: RoleAccessHost}, false, false},
		{"plan has its own gate", &CardVerificationConfig{Entrypoint: "browser", Access: RoleAccessHost}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := explicitTestConfig()
			cfg.CardVerification = tc.gate
			cfg.Verification = map[string]VerificationEntrypoint{"browser": {Command: "/bin/true", ToolchainCommands: []string{"/bin/true"}, TimeoutSeconds: 30, InputPaths: []string{"src"}, RequireCurrentCandidate: true}}
			if tc.plan {
				cfg.PlanDelivery = &PlanDeliveryConfig{Enabled: true, CompleteVerification: "browser"}
			}
			resolved, err := cfg.Resolve()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if err == nil {
				if resolved.CardVerification != tc.gate {
					t.Fatal("gate lost during resolution")
				}
				for _, role := range []string{WorkRoleImplementer, WorkRoleReviewer} {
					if EffectiveRoleAccess(resolved.Roles[role].Access) != RoleAccessSandboxed {
						t.Fatal("command grant widened agent access")
					}
				}
			}
		})
	}
}
