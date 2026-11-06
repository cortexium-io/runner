package config

import (
	"errors"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

func sourceAdoptionContract(role runner.Role) error {
	if !reflect.DeepEqual(role.Inputs, map[string]string{"receipt": "json", "patch": "artifact"}) || !reflect.DeepEqual(role.Outputs, map[string]string{"receipt": "json", "patch": "artifact"}) {
		return errors.New("source-adopt requires one authenticated publication and matching retained source patch")
	}
	return nil
}

// A retained source may be used only after authenticated adoption. An optional
// pristine benchmark runs first and cannot see that candidate patch.
func sourceAdoptionPrefix(p runner.Process, project runner.Project, index int) (int, error) {
	bad := errors.New("retained planned delivery requires exact source adoption before readiness/models and a pristine optional baseline")
	if index < 0 || index > 1 || index+1 >= len(p.Steps) || p.MaxAttempts != 64 || p.Inputs["publication"] != "json" || p.Inputs["patch"] != "artifact" {
		return 0, bad
	}
	adopt := p.Steps[index]
	if sourceAdoptionContract(project.Roles[adopt.Role]) != nil || adopt.ForEach != nil || adopt.Reuse || adopt.Escalation != nil ||
		!reflect.DeepEqual(adopt.Inputs, map[string]runner.Binding{"receipt": {FromInput: "publication"}, "patch": {FromInput: "patch"}}) ||
		!reflect.DeepEqual(adopt.Routes, map[string]runner.Route{"ready": {Next: p.Steps[index+1].ID}, "blocked": {Next: "$fail"}}) {
		return 0, bad
	}
	if index == 1 {
		baseline := p.Steps[0]
		profile := taskProfile(project, baseline)
		if profile.Harness != "command" || profile.Access != "workspace-read" || profile.Command == nil || profile.Command.Network || baseline.ForEach != nil || baseline.Reuse || baseline.Escalation != nil ||
			!reflect.DeepEqual(baseline.Routes, map[string]runner.Route{"ready": {Next: adopt.ID}, "continue": {Next: adopt.ID}}) {
			return 0, bad
		}
		for name, binding := range baseline.Inputs {
			if project.Roles[baseline.Role].Inputs[name] == "artifact" || binding.FromCandidate || binding.Previous || binding.Fallback != nil {
				return 0, bad
			}
		}
	}
	return index + 1, nil
}
