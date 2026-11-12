package config

import (
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func localSaaS(t *testing.T) (runner.Process, runner.Project) {
	t.Helper()
	return plannedFixture(t, "cortexium-local", "processes/develop-local.json")
}

func TestCompleteLocalSaaSGraphCannotSkipIndependentGates(t *testing.T) {
	p, project := localSaaS(t)
	if err := validateProcess(p, project); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*runner.Process, *runner.Project)
	}{
		{"skip source review", func(p *runner.Process, _ *runner.Project) { p.Steps[5].Routes["ready"] = runner.Route{Next: "browser"} }},
		{"skip image review", func(p *runner.Process, _ *runner.Project) {
			p.Steps[7].Routes["ready"] = runner.Route{Next: "integrate"}
		}},
		{"reused image proof", func(p *runner.Process, _ *runner.Project) { p.Steps[8].Reuse = true }},
		{"foreign evidence", func(p *runner.Process, _ *runner.Project) {
			p.Steps[8].Inputs["evidence"] = runner.Binding{FromStep: "check", Output: "artifact"}
		}},
		{"network during tests", func(_ *runner.Process, p *runner.Project) { p.Roles["local-check"].Execution.Command.Network = true }},
		{"writable reviewer", func(_ *runner.Process, p *runner.Project) {
			p.Roles["local-review"].Execution.Access = "workspace-write"
		}},
		{"extra correction", func(p *runner.Process, _ *runner.Project) {
			p.Steps[8].Routes["changes_requested"] = runner.Route{Next: "owner", MaxTimes: 4}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, project := localSaaS(t)
			tc.change(&p, &project)
			if err := validateProcess(p, project); err == nil {
				t.Fatal("weakened local route accepted")
			}
		})
	}
}
