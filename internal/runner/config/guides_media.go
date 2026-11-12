package config

import (
	"errors"
	"reflect"

	"github.com/cortexium-io/runner/internal/guidesmedia"
	"github.com/cortexium-io/runner/internal/runner"
)

func closedGuidesImportRole(role runner.Role, selected string) bool {
	p, err := role.Profile(selected)
	return err == nil && p.Harness == "command" && p.Mode == "guides-import" && reflect.DeepEqual(role.Inputs, map[string]string{"request": "text"}) && reflect.DeepEqual(role.Outputs, guidesmedia.OutputContract())
}

func validateGuidesProcess(p runner.Process, project runner.Project) error {
	if p.GuidesMedia != "" || p.ID == "publish-guides-media" {
		// Source mechanics do not qualify a new native producer or complete release
		// graph. Never admit a partial media route through a legacy puzzle suffix.
		return errors.New("complete guide publication is unavailable: native producer preservation/cleanup and its complete typed qualification suffix remain unqualified")
	}
	if p.ID != "stage-guides-media" {
		for _, s := range p.Steps {
			if taskProfile(project, s).Mode == "guides-import" {
				return errors.New("guide importer is restricted to its fixed staging process")
			}
		}
		return nil
	}
	if project.ID != "jacks-break" || p.MaxAttempts != 1 || p.MaxAttemptsPerStep != 1 || p.InfrastructureRetries != 0 || p.AttemptTimeout != "20m" || !reflect.DeepEqual(p.Inputs, map[string]string{"request": "text"}) || len(p.Steps) != 1 {
		return errors.New("guide staging process differs from its closed one-attempt contract")
	}
	s := p.Steps[0]
	if s.ID != "import" || s.ForEach != nil || s.Reuse || s.Escalation != nil || s.AttemptTimeout != "" || !closedGuidesImportRole(project.Roles[s.Role], project.Profiles[s.Role]) || !reflect.DeepEqual(s.Inputs, map[string]runner.Binding{"request": {FromInput: "request"}}) || !reflect.DeepEqual(s.Routes, map[string]runner.Route{"ready": {Next: "$complete"}, "blocked": {Next: "$fail"}}) || !reflect.DeepEqual(p.Outputs, map[string]runner.Binding{"media_index": {FromStep: "import", Output: "media_index"}}) {
		return errors.New("guide staging must collect the whole fixed set without recovery/retry or arbitrary outputs")
	}
	return nil
}
