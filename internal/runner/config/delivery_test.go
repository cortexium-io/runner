package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func deliveryFixture(t *testing.T) (runner.Process, runner.Project) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "examples", "jacks-break")
	var p runner.Process
	data, err := os.ReadFile(filepath.Join(root, "process.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	project := runner.Project{Roles: map[string]runner.Role{}, Source: &runner.SourceSettings{Ref: "1111111111111111111111111111111111111111"}}
	for _, step := range p.Steps {
		if _, ok := project.Roles[step.Role]; ok {
			continue
		}
		var role runner.Role
		data, err = os.ReadFile(filepath.Join(root, "roles", step.Role+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &role); err != nil {
			t.Fatal(err)
		}
		if err = ready(*role.Execution); err != nil {
			t.Fatal(step.ID, err)
		}
		project.Roles[step.Role] = role
	}
	return p, project
}

func TestConnectedDeliveryCannotBypassCandidateOrRecoveryApprovals(t *testing.T) {
	p, project := deliveryFixture(t)
	validate := func(p runner.Process) error {
		return errors.Join(validateProcess(p, project), validateReleaseProcess(p, project))
	}
	if err := validate(p); err != nil {
		t.Fatal(err)
	}
	find := func(p *runner.Process, id string) *runner.Step {
		for i := range p.Steps {
			if p.Steps[i].ID == id {
				return &p.Steps[i]
			}
		}
		t.Fatal("missing step", id)
		return nil
	}
	for _, test := range []struct {
		name   string
		change func(*runner.Process)
	}{
		{
			"skip merge approval",
			func(p *runner.Process) { s := find(p, "prepare"); s.Routes["ready"] = runner.Route{Next: "merge"} },
		},
		{"skip production approval", func(p *runner.Process) { find(p, "backup").Routes["ready"] = runner.Route{Next: "stop"} }},
		{
			"skip restore approval",
			func(p *runner.Process) { find(p, "inspect-migration").Routes["ready"] = runner.Route{Next: "restore"} },
		},
		{"skip deployment recovery approval", func(p *runner.Process) {
			find(p, "inspect").Routes["ready"] = runner.Route{Next: "deploy", MaxTimes: 1}
		}},
		{
			"worker supplies release receipt",
			func(p *runner.Process) { find(p, "prepare").Inputs["receipt"] = runner.Binding{FromInput: "request"} },
		},
		{
			"worker jumps into production",
			func(p *runner.Process) { find(p, "owner").Routes["ready"] = runner.Route{Next: "stop"} },
		},
		{"unbounded retry", func(p *runner.Process) { find(p, "deploy").Routes["retry_safe"] = runner.Route{Next: "deploy"} }},
		{
			"forged candidate input",
			func(p *runner.Process) { find(p, "publish").Inputs["candidate"] = runner.Binding{FromInput: "request"} },
		},
		{"skip qualified migration input", func(p *runner.Process) {
			find(p, "pin").Inputs["receipt"] = runner.Binding{FromStep: "stop", Output: "receipt"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := runner.Clone(p)
			test.change(&bad)
			if err := validate(bad); err == nil {
				t.Fatal("unsafe delivery accepted")
			}
		})
	}
}
