package config

import (
	"errors"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

// Release credentials are available only to this closed, model-free sequence.
// In particular neither intake nor a task template can start at a mutation.
func validateReleaseProcess(p runner.Process, project runner.Project) error {
	actions := []string{"prepare", "merge", "backup", "stop", "migrate", "pin", "deploy", "verify"}
	selected := make([]runner.Profile, len(p.Steps))
	usesRelease := false
	for i, step := range p.Steps {
		profile, err := project.Roles[step.Role].Profile(project.Profiles[step.Role])
		if err == nil {
			selected[i] = profile
			usesRelease = usesRelease || profile.Harness == "release"
		}
	}
	if !usesRelease {
		return nil
	}
	bad := errors.New("release requires prepare → approved merge → backup → approved stop → migrate → pin → deploy → verify, immutable receipt bindings and no retries")
	if len(p.Steps) != len(actions) || len(p.Inputs) != 0 || p.MaxAttempts != len(actions) || p.MaxAttemptsPerStep != 1 || len(p.Outputs) != 1 {
		return bad
	}
	for i, step := range p.Steps {
		if selected[i].Harness != "release" || selected[i].Mode != actions[i] || step.ForEach != nil || step.Reuse || step.Escalation != nil || len(step.Routes) != 2 || step.Routes["blocked"] != (runner.Route{Next: "$fail"}) {
			return bad
		}
		next := "$complete"
		if i+1 < len(actions) {
			next = p.Steps[i+1].ID
		}
		if step.Routes["ready"] != (runner.Route{Next: next, Approval: i == 0 || i == 2}) {
			return bad
		}
		if i == 0 {
			if len(step.Inputs) != 0 {
				return bad
			}
		} else if !reflect.DeepEqual(step.Inputs, map[string]runner.Binding{"receipt": {FromStep: p.Steps[i-1].ID, Output: "receipt"}}) {
			return bad
		}
	}
	if !reflect.DeepEqual(p.Outputs, map[string]runner.Binding{"receipt": {FromStep: p.Steps[7].ID, Output: "receipt"}}) {
		return bad
	}
	return nil
}
