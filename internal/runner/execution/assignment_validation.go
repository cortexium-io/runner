package execution

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

func (s *Service) ValidateAssignment(a runner.Assignment) error {
	if a.Setup.Profile.Harness == "codex" {
		_, _, err := nativePayload(a)
		return err
	}
	if a.Setup.Profile.Harness == "task-source" && a.SourceHandoff != nil {
		if a.TaskSource == nil || a.Setup.Profile.Mode != "pin" {
			return fmt.Errorf("source handoff requires ordinary pin authority")
		}
		candidate, _ := json.Marshal(runner.TaskCandidate{
			Identity:   a.TaskSource.Identity,
			RunID:      a.RunID,
			Repository: a.Repository,
			Base:       a.SourceHandoff.Source.Base,
			Commit:     strings.Repeat("f", 40),
			Tree:       strings.Repeat("f", 40),
			PatchID:    a.SourceHandoff.Source.PatchID,
		})
		// Budget the actual durable assignment and normal pin completion, plus
		// the existing maximum clipped error in its worst JSON-escaped form.
		ends := []*runner.ExecutionEnd{
			{Kind: "returned", Result: &runner.StepResult{Outcome: "changes_requested", Reason: runner.TaskScopeFeedback, Outputs: map[string]runner.Value{
				"candidate": {Inline: candidate},
				"manifest":  {ArtifactID: a.AttemptID + "/task-manifest.json"},
				"feedback":  runner.Text(runner.TaskScopeFeedback),
			}}},
			{Kind: "failed", Reason: strings.Repeat("\x00", 512) + "…"},
		}
		for _, end := range ends {
			data, err := json.Marshal(taskSourceJob{Assignment: a, PinIntent: true, Stopped: true, End: end})
			if err != nil {
				return err
			}
			if len(data) > int(min(a.Limits.MaxRecord, s.disk.Policy.Database.MaxRecord)) {
				return fmt.Errorf("%w: source handoff pin job and completion exceed max_record", runner.ErrCapacity)
			}
		}
	}
	return nil
}
