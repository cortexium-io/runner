package config

import (
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestWorkflowRoutesAndTaskContracts(t *testing.T) {
	role := runner.Role{ID: "worker", Inputs: map[string]string{"text": "text"}, Outputs: map[string]string{"result": "text"}}
	p := runner.Project{Roles: map[string]runner.Role{"worker": role}}
	empty := runner.Text("")
	base := runner.Process{ID: "work", Inputs: map[string]string{"text": "text"}, Steps: []runner.Step{
		{ID: "owner", Role: "worker", Inputs: map[string]runner.Binding{"text": {FromStep: "check", Output: "result", Default: &empty}}},
		{ID: "check", Role: "worker", Inputs: map[string]runner.Binding{"text": {FromStep: "owner", Output: "result"}}, Routes: map[string]runner.Route{"changes_requested": {Next: "owner", MaxTimes: 2}, "ready": {Next: "$complete"}}},
	}, Outputs: map[string]runner.Binding{"result": {FromStep: "check", Output: "result"}}, MaxAttempts: 8, MaxAttemptsPerStep: 1, AttemptTimeout: "1m"}
	if err := validateProcess(base, p); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*runner.Process){
		"unbounded return": func(v *runner.Process) { v.Steps[1].Routes["changes_requested"] = runner.Route{Next: "owner"} },
		"unknown route":    func(v *runner.Process) { v.Steps[1].Routes["ready"] = runner.Route{Next: "missing"} },
		"wrong default": func(v *runner.Process) {
			value := runner.Value{Inline: []byte(`42`)}
			v.Steps[0].Inputs["text"] = runner.Binding{FromStep: "check", Output: "result", Default: &value}
		},
		"missing first value": func(v *runner.Process) {
			v.Steps[0].Inputs["text"] = runner.Binding{FromStep: "check", Output: "result"}
		},
		"undeclared help":   func(v *runner.Process) { v.Steps[1].Routes["needs_help"] = runner.Route{Next: "owner", MaxTimes: 1} },
		"literal injection": func(v *runner.Process) { v.Steps[0].Inputs["text"] = runner.Binding{Literal: &empty} },
	} {
		t.Run(name, func(t *testing.T) {
			v := runner.Clone(base)
			change(&v)
			if err := validateProcess(v, p); err == nil {
				t.Fatal("invalid control flow accepted")
			}
		})
	}
	group := runner.Process{ID: "tasks", Inputs: map[string]string{"tasks": "json"}, Steps: []runner.Step{{ID: "tasks", ForEach: &runner.TaskGroup{Items: runner.Binding{FromInput: "tasks"}, Inputs: map[string]string{"text": "text"}, Steps: []runner.Step{{ID: "work", Role: "worker", Inputs: map[string]runner.Binding{"text": {FromItem: "text"}}}}, Output: runner.Binding{FromStep: "work", Output: "result"}}}}, Outputs: map[string]runner.Binding{"result": {FromStep: "tasks", Output: "last"}}, MaxAttempts: 8, MaxAttemptsPerStep: 1, AttemptTimeout: "1m"}
	if err := validateProcess(group, p); err != nil {
		t.Fatal(err)
	}
	invalid := runner.Clone(group)
	invalid.Steps[0].ForEach.Steps[0].Inputs["text"] = runner.Binding{FromItem: "unknown"}
	if err := validateProcess(invalid, p); err == nil {
		t.Fatal("unknown item binding accepted")
	}
}
