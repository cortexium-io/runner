package config

import (
	"errors"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

func commandRoleContract(role runner.Role, profile runner.Profile) error {
	if strings.TrimSpace(role.Purpose) == "" || profile.Command == nil {
		return errors.New("command roles need an explicit purpose and configured command")
	}
	for _, kind := range role.Inputs {
		if kind != "text" && kind != "artifact" {
			return errors.New("command inputs must be text or retained artifacts")
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
