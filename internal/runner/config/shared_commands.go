package config

import (
	"errors"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

// Retry can select any frozen profile, including profiles in source-card steps.
func sourceSharedProfiles(steps []runner.Step, project runner.Project) bool {
	for _, step := range steps {
		if step.ForEach != nil {
			if sourceSharedProfiles(step.ForEach.Steps, project) {
				return true
			}
			continue
		}
		role := project.Roles[step.Role]
		profiles := role.Profiles
		if role.Execution != nil {
			profiles = map[string]runner.Profile{"default": *role.Execution}
		}
		for _, profile := range profiles {
			if profile.Harness == "command" && profile.Mode == "shared" {
				return true
			}
		}
	}
	return false
}

func commandRoleContract(role runner.Role, profile runner.Profile) error {
	if strings.TrimSpace(role.Purpose) == "" || profile.Command == nil {
		return errors.New("command roles need an explicit purpose and configured command")
	}
	for _, kind := range role.Inputs {
		if kind != "text" && kind != "artifact" {
			return errors.New("command inputs must be text or retained artifacts")
		}
	}
	values := append([]string{}, profile.Command.Args...)
	for _, value := range profile.Command.Env {
		values = append(values, value)
	}
	for _, value := range values {
		if _, _, err := runner.CommandDependencyInput(value); err != nil {
			return err
		}
		name, err := runner.CommandArtifactInput(value)
		if err != nil {
			return err
		}
		if name != "" && role.Inputs[name] != "artifact" {
			return errors.New("command artifact file bindings require declared artifact inputs")
		}
	}
	want := 1
	if profile.Command.Artifact != "" {
		want = 2
		if role.Outputs["artifact"] != "artifact" {
			return errors.New("command artifact needs a declared artifact output")
		}
	}
	if len(role.Outputs) != want || role.Outputs["check"] != "text" {
		return errors.New("command needs a check text output and only its optional artifact output")
	}
	return nil
}

func unsupportedSharedBinding(value string) bool {
	for _, prefix := range []string{"{text.", "{input.", "{artifact_id.", "{source.base}"} {
		if strings.Contains(value, prefix) {
			return true
		}
	}
	return false
}
