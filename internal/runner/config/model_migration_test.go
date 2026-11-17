package config

import (
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

// New configured roles use 6.2; loading a retained definition must still accept
// its original 6.1 model without widening any other delivery-profile constraint.
func TestDeliveryModelMigrationPreservesRetainedProfiles(t *testing.T) {
	for _, file := range []string{"planned-frontend.json", "measured-investigation.json", "retained-investigation.json"} {
		t.Run(file, func(t *testing.T) {
			process, original := plannedFixture(t, "jacks-break", file)
			for _, model := range []string{"gpt-6.2-sol", "gpt-6.1-sol", "unapproved-model"} {
				t.Run(model, func(t *testing.T) {
					project := runner.Clone(original)
					for id, role := range project.Roles {
						if role.Execution.Harness == "codex" {
							role.Execution.Model = model
							project.Roles[id] = role
						}
					}
					err := validateReleaseProcess(process, project)
					if (err == nil) != (model != "unapproved-model") {
						t.Fatalf("delivery model %q: %v", model, err)
					}
				})
			}
		})
	}
}
