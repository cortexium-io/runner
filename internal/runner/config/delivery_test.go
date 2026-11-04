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

func TestReviewedReleaseEntryKeepsQualificationAndApprovalGates(t *testing.T) {
	_, project := deliveryFixture(t)
	root := filepath.Join("..", "..", "..", "examples", "jacks-break")
	var p runner.Process
	data, err := os.ReadFile(filepath.Join(root, "release-reviewed.json"))
	if err != nil || json.Unmarshal(data, &p) != nil {
		t.Fatal("reviewed release example unavailable", err)
	}
	var role runner.Role
	data, err = os.ReadFile(filepath.Join(root, "roles", "delivery-adopt.json"))
	if err != nil || json.Unmarshal(data, &role) != nil {
		t.Fatal("adoption role unavailable", err)
	}
	project.Roles[role.ID] = role
	validate := func(p runner.Process) error {
		return errors.Join(validateProcess(p, project), validateReleaseProcess(p, project))
	}
	if err = validate(p); err != nil {
		t.Fatal(err)
	}
	for _, step := range p.Steps {
		if project.Roles[step.Role].Execution.Harness != "release" {
			t.Fatal("reviewed release introduced model work")
		}
	}
	for _, change := range []func(*runner.Process){
		func(p *runner.Process) { p.Steps = p.Steps[1:] },
		func(p *runner.Process) { p.Steps[1].Routes["ready"] = runner.Route{Next: "merge"} },
		func(p *runner.Process) { p.Steps[2].Routes["ready"] = runner.Route{Next: "merge"} },
		func(p *runner.Process) { p.Steps[4].Routes["ready"] = runner.Route{Next: "stop"} },
		func(p *runner.Process) { p.Steps[1].Inputs["receipt"] = runner.Binding{FromInput: "publication"} },
	} {
		bad := runner.Clone(p)
		change(&bad)
		if validate(bad) == nil {
			t.Fatal("reviewed entry bypassed its closed release sequence")
		}
	}
}

func TestRetainedInvestigationMeasuresBeforeModelsAndKeepsReleaseGates(t *testing.T) {
	_, project := deliveryFixture(t)
	root := filepath.Join("..", "..", "..", "examples", "jacks-break")
	var p runner.Process
	data, err := os.ReadFile(filepath.Join(root, "investigate-retained.json"))
	if err != nil || json.Unmarshal(data, &p) != nil {
		t.Fatal("retained investigation unavailable", err)
	}
	for _, id := range []string{"delivery-baseline", "investigation-owner", "investigation-review"} {
		var role runner.Role
		data, err = os.ReadFile(filepath.Join(root, "roles", id+".json"))
		if err != nil || json.Unmarshal(data, &role) != nil {
			t.Fatal("investigation role unavailable", err)
		}
		project.Roles[id] = role
	}
	validate := func(p runner.Process) error {
		return errors.Join(validateProcess(p, project), validateReleaseProcess(p, project))
	}
	if err = validate(p); err != nil {
		t.Fatal(err)
	}
	if p.Steps[2].Inputs["baseline"] != (runner.Binding{FromStep: "baseline", Output: "check"}) || p.Steps[5].Inputs["baseline_measurements"] != (runner.Binding{FromStep: "baseline", Output: "artifact"}) {
		t.Fatal("owner did not receive real baseline feedback")
	}
	for _, change := range []func(*runner.Process){
		func(p *runner.Process) { p.Steps[0].Routes["continue"] = runner.Route{Next: "owner"} },
		func(p *runner.Process) { p.Steps[1].Routes["ready"] = runner.Route{Next: "publish"} },
		func(p *runner.Process) { p.Steps[0].Inputs["patch"] = runner.Binding{FromCandidate: true} },
		func(p *runner.Process) {
			p.Steps[8].Routes["changes_requested"] = runner.Route{Next: "baseline", MaxTimes: 1}
		},
	} {
		bad := runner.Clone(p)
		change(&bad)
		if validate(bad) == nil {
			t.Fatal("investigation bypassed admission/source/repair identity")
		}
	}
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
