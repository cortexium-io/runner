package config

import (
	"errors"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestGoCheckPreparedInputProfileKeepsFrozenDefinitionsCompatible(t *testing.T) {
	for _, file := range []string{"retained-investigation.json", "measured-investigation.json"} {
		t.Run(file, func(t *testing.T) {
			p, project := plannedFixture(t, "jacks-break", file)
			role := project.Roles["planned-go-check"]
			if len(role.Execution.Command.Args) != 19 {
				t.Fatal("new planned check does not bind prepared input")
			}
			if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
				t.Fatal("new prepared-input profile rejected", err)
			}
			role.Execution.Command.Args = role.Execution.Command.Args[:13]
			project.Roles[role.ID] = role
			if err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project)); err != nil {
				t.Fatal("frozen thirteen-argument profile rejected", err)
			}
		})
	}
}

func TestGoCheckPreparedInputProfileRejectsMetadataSubstitutionAndPartialFlags(t *testing.T) {
	base, original := plannedFixture(t, "jacks-break", "retained-investigation.json")
	for name, change := range map[string]func(*runner.Command){
		"caller base":               func(c *runner.Command) { c.Args[14] = "{text.base}" },
		"caller artifact ID":        func(c *runner.Command) { c.Args[16] = "{text.patch}" },
		"source instead of patch":   func(c *runner.Command) { c.Args[18] = "{workspace}" },
		"partial flags":             func(c *runner.Command) { c.Args = c.Args[:17] },
		"duplicate flag":            func(c *runner.Command) { c.Args = append(c.Args, "--source-base", "HEAD") },
		"metadata flag rename":      func(c *runner.Command) { c.Args[13] = "--source" },
		"skip selected tests":       func(c *runner.Command) { c.Args[6] = "^$" },
		"unbound input environment": func(c *runner.Command) { c.Env["SOURCE_BASE"] = "HEAD" },
	} {
		t.Run(name, func(t *testing.T) {
			project := runner.Clone(original)
			role := project.Roles["planned-go-check"]
			change(role.Execution.Command)
			project.Roles[role.ID] = role
			if errors.Join(validateProcess(base, project), validateReleaseProcess(base, project)) == nil {
				t.Fatal("unsupported prepared-input authority accepted")
			}
		})
	}
}
