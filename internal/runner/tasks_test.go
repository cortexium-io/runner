package runner_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func taskSnapshot() runner.Snapshot {
	s := snapshot(policy())
	p := s.Projects["demo"]
	proc := p.Processes["demo"]
	proc.Inputs = map[string]string{"tasks": "json"}
	proc.MaxAttempts = 8
	proc.Steps = []runner.Step{
		{ID: "tasks", ForEach: &runner.TaskGroup{Items: runner.Binding{FromInput: "tasks"}, Inputs: map[string]string{"text": "text"}, Steps: []runner.Step{{ID: "work", Role: "worker", Inputs: map[string]runner.Binding{"text": {FromItem: "text"}}}}, Output: runner.Binding{FromStep: "work", Output: "result"}}, Routes: map[string]runner.Route{"ready": {Next: "finish", Approval: true}}},
		{ID: "finish", Role: "worker", Inputs: map[string]runner.Binding{"text": {FromStep: "tasks", Output: "last"}}},
	}
	proc.Outputs = map[string]runner.Binding{"result": {FromStep: "finish", Output: "result"}, "tasks": {FromStep: "tasks", Output: "results"}}
	p.Processes["demo"] = proc
	s.Projects["demo"] = p
	return s
}
func TestTaskExpansionDependenciesAndRestart(t *testing.T) {
	s := taskSnapshot()
	dir := t.TempDir()
	r := openRig(t, dir, s, nil)
	tasks := `[{"id":"second","depends_on":["first"],"inputs":{"text":"second result"}},{"id":"first","inputs":{"text":"first result"}}]`
	started, err := r.core.Start(runner.StartRequest{RequestID: "tasks", Project: "demo", Inputs: map[string]runner.Value{"tasks": {Inline: json.RawMessage(tasks)}}})
	if err != nil {
		t.Fatal(err)
	}
	waiting := await(t, r.core, started.ID, func(r runner.Run) bool { return r.Decision != nil || r.Terminal() })
	if waiting.Decision == nil {
		t.Fatal(waiting.Status, waiting.Reason)
	}
	if len(waiting.Attempts) != 3 || waiting.Process.Steps[waiting.Attempts[0].Step].ID != "tasks-first-work" || waiting.Process.Steps[waiting.Attempts[1].Step].ID != "tasks-second-work" {
		t.Fatal("dependencies ignored", waiting.Attempts)
	}
	r.close()
	r = openRig(t, dir, s, nil)
	defer r.close()
	if _, err := r.core.Decide(runner.DecisionRequest{RequestID: "continue", RunID: waiting.ID, DecisionID: waiting.Decision.ID, Choice: "approve"}); err != nil {
		t.Fatal(err)
	}
	done := await(t, r.core, waiting.ID, released)
	if done.Status != "Completed" || string(done.Outputs["result"].Inline) != `"second result"` || !strings.Contains(string(done.Outputs["tasks"].Inline), "first result") {
		t.Fatal(done.Status, done.Reason, done.Outputs)
	}
}
func TestTaskExpansionRejectsInvalidPlansBeforeExecution(t *testing.T) {
	for name, tasks := range map[string]string{
		"cycle":              `[{"id":"a","depends_on":["b"],"inputs":{"text":"a"}},{"id":"b","depends_on":["a"],"inputs":{"text":"b"}}]`,
		"missing dependency": `[{"id":"a","depends_on":["b"],"inputs":{"text":"a"}}]`,
		"duplicate":          `[{"id":"a","inputs":{"text":"a"}},{"id":"a","inputs":{"text":"b"}}]`,
		"wrong type":         `[{"id":"a","inputs":{"text":42}}]`,
		"invented role":      `[{"id":"a","role":"deploy","inputs":{"text":"a"}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			r := openRig(t, t.TempDir(), taskSnapshot(), nil)
			defer r.close()
			started, err := r.core.Start(runner.StartRequest{RequestID: "invalid", Project: "demo", Inputs: map[string]runner.Value{"tasks": {Inline: json.RawMessage(tasks)}}})
			if err != nil {
				t.Fatal(err)
			}
			done := await(t, r.core, started.ID, terminal)
			if done.Status != "Failed" || len(done.Attempts) != 0 {
				t.Fatal("invalid plan executed", done)
			}
		})
	}
}

// Artifact joins preserve the original producer instead of registering the
// join as a new owner of the same bytes.
func TestTaskJoinForwardsOwnedArtifact(t *testing.T) {
	s := taskSnapshot()
	p := s.Projects["demo"]
	role := p.Roles["worker"]
	role.Outputs["result"] = "artifact"
	role.Execution.Mode = "file"
	p.Roles["worker"] = role
	proc := p.Processes["demo"]
	proc.Steps = proc.Steps[:1]
	proc.Steps[0].Routes = nil
	proc.Outputs = map[string]runner.Binding{"patch": {FromStep: "tasks", Output: "last"}}
	p.Processes["demo"] = proc
	s.Projects["demo"] = p
	r := openRig(t, t.TempDir(), s, nil)
	defer r.close()
	started, err := r.core.Start(runner.StartRequest{RequestID: "artifact-tasks", Project: "demo", Inputs: map[string]runner.Value{"tasks": {Inline: json.RawMessage(`[{"id":"one","inputs":{"text":"owned content"}}]`)}}})
	if err != nil {
		t.Fatal(err)
	}
	done := await(t, r.core, started.ID, released)
	if done.Status != "Completed" {
		t.Fatal(done.Reason)
	}
	id := done.Outputs["patch"].ArtifactID
	art, err := r.store.Artifact(context.Background(), id)
	if err != nil || art.RunID != done.ID {
		t.Fatal(art, err)
	}
	b, err := r.disk.Read("artifacts/"+id, s.Instance.Storage.MaxArtifact)
	if err != nil || string(b) != "owned content" {
		t.Fatal(string(b), err)
	}
}
