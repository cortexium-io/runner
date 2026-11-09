package config

import (
	"path/filepath"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

func measurementHistoryProfile(project runner.Project, step runner.Step, initial bool) bool {
	role := project.Roles[step.Role]
	p := taskProfile(project, step)
	if p.Command == nil || len(p.Command.Args) == 0 || !filepath.IsAbs(p.Command.Args[0]) || filepath.Base(p.Command.Args[0]) != "runner-measure-history" || step.ForEach != nil || step.Reuse || step.Escalation != nil || step.AttemptTimeout != "" {
		return false
	}
	inputs := map[string]string{"previous": "artifact", "measurement": "artifact"}
	args := []string{p.Command.Args[0], "--source", "{workspace}", "--previous", "{input.previous}", "--measurement", "{input.measurement}", "--output", "{scratch}/measurement-history.json"}
	if initial {
		inputs = map[string]string{"patch": "artifact", "checks": "artifact", "measurement1": "artifact", "measurement2": "artifact", "measurement3": "artifact", "measurement4": "artifact"}
		args = []string{p.Command.Args[0], "--source", "{workspace}", "--checks", "{input.checks}", "--measurement", "{input.measurement1}", "--measurement", "{input.measurement2}", "--measurement", "{input.measurement3}", "--measurement", "{input.measurement4}", "--output", "{scratch}/measurement-history.json"}
	}
	want := runner.Profile{Harness: "command", Access: "workspace-read", Command: &runner.Command{Args: args, Artifact: "measurement-history.json"}}
	return reflect.DeepEqual(p, want) && reflect.DeepEqual(role.Inputs, inputs) && reflect.DeepEqual(role.Outputs, map[string]string{"check": "text", "artifact": "artifact"})
}

func historyAppendStep(p runner.Process, project runner.Project, index int, first, measure, review string) bool {
	if index < 0 || index >= len(p.Steps) {
		return false
	}
	s := p.Steps[index]
	return measurementHistoryProfile(project, s, false) && reflect.DeepEqual(s.Inputs, map[string]runner.Binding{
		"previous":    {FromStep: s.ID, Output: "artifact", Previous: true, Fallback: &runner.Binding{FromStep: first, Output: "artifact"}},
		"measurement": {FromStep: measure, Output: "artifact"},
	}) && reflect.DeepEqual(s.Routes, map[string]runner.Route{"ready": {Next: review}, "blocked": {Next: "$fail"}})
}
