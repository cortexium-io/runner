package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

func nativeReady(p runner.Profile) error {
	if p.Native == nil {
		return errors.New("native execution needs an explicit native profile")
	}
	if err := p.Native.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(p.Model) == "" || len(p.Model) > 100 {
		return errors.New("native execution needs an explicit model")
	}
	if p.Access != "input-only" && p.Access != "workspace-read" && p.Access != "workspace-write" {
		return errors.New("native access must be input-only, workspace-read or workspace-write")
	}
	if p.Native.Harness == "claude" && p.Access == "workspace-write" {
		return errors.New("the selected Claude adapter supports read-only tools, not workspace writes")
	}
	if p.Native.Harness == "claude" && p.Access == "input-only" && p.Native.Tools.Mode != "none" {
		return errors.New("Claude input-only execution requires tools:none; selected file/web tools need workspace-read")
	}
	if p.Reasoning != "" && p.Reasoning != "low" && p.Reasoning != "medium" && p.Reasoning != "high" && p.Reasoning != "xhigh" && !(p.Native.Harness == "claude" && p.Reasoning == "max") {
		return errors.New("native reasoning must match the selected harness: Codex low/medium/high/xhigh; Claude also supports max")
	}
	if p.Command != nil || p.Mode != "" || p.Delay != "" || p.Input != "" || p.Output != "" || p.Text != "" || p.LogBytes != 0 {
		return errors.New("native profiles cannot also select deterministic or command execution")
	}
	if len(p.Instructions) > 32<<10 || p.MaxInput < 128 || p.MaxInput > 256<<10 || p.MaxOutput < 128 || p.MaxOutput > 128<<10 {
		return errors.New("native instructions must fit 32KiB, input 128B–256KiB and output 128B–128KiB")
	}
	return nil
}

// Role task policy declares authority, rather than embedding a particular
// software workflow in the configuration validator. Admission validates each
// actual proposal against current capacity before reserving child identities.
func validateRoleTasks(process runner.Process, project runner.Project) error {
	p := process.Tasks
	if p == nil {
		return nil
	}
	if len(p.Roles) < 1 || len(p.Roles) > 128 || p.MaxTasks < 1 || p.MaxTasks > 4096 || p.MaxDepth < 1 || p.MaxDepth > 32 || p.MaxRevisions < 0 || p.MaxRevisions > 128 || p.MaxConcurrent < 1 || p.MaxConcurrent > 32 {
		return errors.New("tasks policy needs declared roles and bounded task/depth/revision/concurrency settings")
	}
	if len(process.Steps) != 1 {
		return errors.New("general role tasks need one root role step; compose work through child tasks")
	}
	profilesSupported := func(name string, role runner.Role) error {
		profiles := role.Profiles
		if role.Execution != nil {
			profiles = map[string]runner.Profile{"default": *role.Execution}
		}
		for _, profile := range profiles {
			if err := runner.ValidateRoleTaskProfile(profile); err != nil {
				return fmt.Errorf("general task role %s: %w", name, err)
			}
		}
		return nil
	}
	seen := map[string]bool{}
	for _, name := range p.Roles {
		role, ok := project.Roles[name]
		if !validID(name) || seen[name] || !ok || strings.TrimSpace(role.Purpose) == "" {
			return fmt.Errorf("tasks role %q must be a unique configured role with a purpose", name)
		}
		if _, err := role.Profile(project.Profiles[name]); err != nil {
			return err
		}
		if err := profilesSupported(name, role); err != nil {
			return err
		}
		seen[name] = true
	}
	for _, step := range process.Steps {
		if err := profilesSupported(step.Role, project.Roles[step.Role]); err != nil {
			return err
		}
		if step.ForEach != nil {
			return errors.New("general role tasks cannot mix with legacy task-group expansion")
		}
	}
	return nil
}
