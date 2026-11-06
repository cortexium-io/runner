package execution

import "github.com/cortexium-io/runner/internal/runner"

func (s *Service) ValidateAssignment(a runner.Assignment) error {
	if a.Setup.Profile.Harness == "codex" {
		_, _, err := nativePayload(a)
		return err
	}
	return nil
}
