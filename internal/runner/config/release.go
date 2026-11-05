package config

import (
	"errors"
	"github.com/cortexium-io/runner/internal/runner"
	"reflect"
)

// Credential-bearing delivery is a closed suffix. Ordinary coding/correction
// steps may precede it, but cannot jump into it or supply a release receipt.
func validateReleaseProcess(p runner.Process, project runner.Project) error {
	start := -1
	selected := make([]runner.Profile, len(p.Steps))
	for i, step := range p.Steps {
		profile, err := project.Roles[step.Role].Profile(project.Profiles[step.Role])
		if err == nil {
			selected[i] = profile
			if start < 0 && profile.Harness == "release" && profile.Mode != "readiness" {
				start = i
			}
		}
	}
	if start < 0 {
		for _, profile := range selected {
			if profile.Harness == "release" {
				return errors.New("delivery readiness must lead to the complete delivery process")
			}
		}
		return nil
	}
	actions := []string{"prepare", "merge", "backup", "stop", "migrate", "pin", "deploy", "verify"}
	adopted := selected[start].Mode == "adopt"
	connected := selected[start].Mode == "publish" || adopted
	bad := errors.New("release requires immutable ordered qualification/merge/backup/production gates; coding may enter only through candidate publication")
	readiness := 0
	// An investigation may measure committed source or one explicitly supplied
	// retained scaffold before admission. Every model turn still follows admission.
	if connected && !adopted && selected[0].Harness == "command" {
		patchType, retained := p.Inputs["patch"]
		if retained && (patchType != "artifact" || p.Steps[0].Inputs["patch"] != (runner.Binding{FromInput: "patch"})) {
			return bad
		}
		for name, binding := range p.Steps[0].Inputs {
			if binding.FromCandidate || binding.Previous || name == "patch" && !retained || project.Roles[p.Steps[0].Role].Inputs[name] == "artifact" && (name != "patch" || !retained) {
				return bad
			}
		}
		if len(p.Steps) < 3 || selected[0].Access != "workspace-read" || p.Steps[0].ForEach != nil || p.Steps[0].Reuse || p.Steps[0].Escalation != nil ||
			!reflect.DeepEqual(p.Steps[0].Routes, map[string]runner.Route{
				"continue": {Next: p.Steps[1].ID},
				"ready":    {Next: p.Steps[1].ID},
			}) {
			return bad
		}
		readiness = 1
	}
	owner := readiness + 1
	if connected {
		if adopted {
			if start != 0 || p.MaxAttempts != 16 || !reflect.DeepEqual(p.Inputs, map[string]string{"publication": "json"}) {
				return bad
			}
		} else if start <= owner || selected[readiness].Harness != "release" || selected[readiness].Mode != "readiness" || len(p.Steps[readiness].Inputs) != 0 || !reflect.DeepEqual(p.Steps[readiness].Routes, map[string]runner.Route{"ready": {Next: p.Steps[owner].ID}, "blocked": {Next: "$fail"}}) || selected[owner].Harness != "codex" || selected[owner].Access != "workspace-write" {
			return bad
		}
		actions = append([]string{selected[start].Mode, "qualify"}, actions...)
		actions = append(actions[:len(actions)-1], "inspect", "verify")
		for i, action := range actions {
			if action == "migrate" {
				actions = append(actions[:i+1], append([]string{"inspect-migration", "restore"}, actions[i+1:]...)...)
				break
			}
		}
	}
	if len(p.Steps)-start != len(actions) || p.MaxAttemptsPerStep != 1 || (!connected && (start != 0 || len(p.Inputs) != 0 || p.MaxAttempts != 8)) {
		return bad
	}
	if len(p.Outputs) != 1 || !reflect.DeepEqual(p.Outputs["receipt"], runner.Binding{FromStep: p.Steps[len(p.Steps)-1].ID, Output: "receipt"}) {
		return bad
	}
	if connected && !adopted {
		if start == 0 || selected[start-1].Harness != "git-candidate" {
			return bad
		}
		for _, step := range p.Steps[:start] {
			for _, route := range step.Routes {
				if route.Next == "$complete" {
					return bad
				}
				for _, target := range p.Steps[start+1:] {
					if route.Next == target.ID {
						return bad
					}
				}
			}
		}
	}
	for offset, action := range actions {
		i := start + offset
		step := p.Steps[i]
		count := 2
		if connected && action == "deploy" {
			count = 4
		}
		if connected && action == "migrate" {
			count = 3
		}
		if connected && action == "qualify" {
			count = 3
		}
		if selected[i].Harness != "release" || selected[i].Mode != action || step.ForEach != nil || step.Reuse || step.Escalation != nil || len(step.Routes) != count || step.Routes["blocked"] != (runner.Route{Next: "$fail"}) {
			return bad
		}
		next := "$complete"
		if i+1 < len(p.Steps) {
			next = p.Steps[i+1].ID
		}
		approval := action == "prepare" || action == "backup"
		maxTimes := 0
		if connected && action == "qualify" {
			correction := runner.Route{Next: p.Steps[owner].ID, MaxTimes: 1}
			if adopted {
				correction = runner.Route{Next: "$fail"}
			}
			if step.Routes["changes_requested"] != correction {
				return bad
			}
		}
		if connected && action == "migrate" {
			next = p.Steps[i+3].ID
			if step.Routes["recovery_required"] != (runner.Route{Next: p.Steps[i+1].ID}) {
				return bad
			}
		}
		if connected && action == "inspect-migration" {
			approval = true
		}
		if connected && action == "deploy" {
			next = p.Steps[len(p.Steps)-1].ID
			if step.Routes["retry_safe"] != (runner.Route{Next: step.ID, MaxTimes: 1}) || step.Routes["recovery_required"] != (runner.Route{Next: p.Steps[i+1].ID}) {
				return bad
			}
		}
		if connected && action == "inspect" {
			next = p.Steps[i-1].ID
			approval = true
			maxTimes = 1
		}
		if action == "restore" {
			if !reflect.DeepEqual(step.Routes, map[string]runner.Route{"restored": {Next: "$fail"}, "blocked": {Next: "$fail"}}) {
				return bad
			}
		} else if step.Routes["ready"] != (runner.Route{Next: next, Approval: approval, MaxTimes: maxTimes}) {
			return bad
		}
		if offset == 0 {
			if adopted {
				if !reflect.DeepEqual(step.Inputs, map[string]runner.Binding{"receipt": {FromInput: "publication"}}) {
					return bad
				}
			} else if connected {
				empty := runner.Value{Inline: []byte(`{}`)}
				if !reflect.DeepEqual(step.Inputs, map[string]runner.Binding{
					"candidate": {FromStep: p.Steps[start-1].ID, Output: "candidate"},
					"previous":  {FromStep: step.ID, Output: "receipt", Previous: true, Default: &empty},
				}) {
					return bad
				}
			} else if len(step.Inputs) != 0 {
				return bad
			}
		} else {
			previous := i - 1
			if connected && action == "pin" {
				previous = i - 3
			}
			if connected && action == "verify" {
				previous = i - 2
			}
			inputs := map[string]runner.Binding{"receipt": {FromStep: p.Steps[previous].ID, Output: "receipt"}}
			if connected && action == "deploy" {
				empty := runner.Value{Inline: []byte(`{}`)}
				inputs["recovery"] = runner.Binding{FromStep: p.Steps[i+1].ID, Output: "receipt", Previous: true, Default: &empty}
				inputs["previous"] = runner.Binding{FromStep: step.ID, Output: "receipt", Previous: true, Default: &empty}
			}
			if !reflect.DeepEqual(step.Inputs, inputs) {
				return bad
			}
		}
	}
	return nil
}
