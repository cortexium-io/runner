package runner

import (
	"encoding/json"
	"testing"
)

func TestStepFallbackUsesBaselineThenLatestCorrectionEvidence(t *testing.T) {
	completed := func(step int, id string, superseded bool) Attempt {
		return Attempt{Step: step, Superseded: superseded, End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{"artifact": {ArtifactID: id}}}}}
	}
	r := Run{Process: Process{Steps: []Step{{ID: "baseline"}, {ID: "owner"}, {ID: "measure"}}}, Attempts: []Attempt{completed(0, "baseline/report", false)}}
	b := map[string]Binding{"evidence": {FromStep: "measure", Output: "artifact", Previous: true, Fallback: &Binding{FromStep: "baseline", Output: "artifact"}}}
	assert := func(want string) {
		t.Helper()
		values, err := bindings(r, b)
		if err != nil || values["evidence"].ArtifactID != want {
			t.Fatal("wrong retained evidence", values, err)
		}
	}
	assert("baseline/report")
	r.Attempts = append(r.Attempts, completed(2, "round-1/report", true), completed(2, "round-2/report", false))
	assert("round-2/report")
	r.Attempts[2].Superseded = true
	assert("round-2/report")
	binding := b["evidence"]
	binding.Previous = false
	b["evidence"] = binding
	assert("baseline/report")
	r.Attempts = nil
	if _, err := bindings(r, b); err == nil {
		t.Fatal("missing primary and fallback evidence silently accepted")
	}
}

func TestTaskExpansionPrefixesFallbackWithoutChangingTemplate(t *testing.T) {
	group := TaskGroup{
		Items:  Binding{FromInput: "items"},
		Inputs: map[string]string{},
		Steps: []Step{
			{ID: "baseline", Role: "work"},
			{ID: "owner", Role: "work", Inputs: map[string]Binding{"evidence": {FromStep: "measure", Output: "result", Fallback: &Binding{FromStep: "baseline", Output: "result"}}}},
			{ID: "measure", Role: "work"},
		},
		Output: Binding{FromStep: "measure", Output: "result"},
	}
	r := Run{
		Inputs:  map[string]Value{"items": {Inline: json.RawMessage(`[{"id":"one","inputs":{}},{"id":"two","inputs":{}}]`)}},
		Roles:   map[string]Role{"work": {Outputs: map[string]string{"result": "artifact"}}},
		Process: Process{Steps: []Step{{ID: "tasks", ForEach: &group}}},
	}
	if err := expandTasks(&r); err != nil {
		t.Fatal(err)
	}
	b := r.Process.Steps[1].Inputs["evidence"]
	second := r.Process.Steps[4].Inputs["evidence"]
	if b.FromStep != "tasks-one-measure" || b.Fallback.FromStep != "tasks-one-baseline" ||
		second.FromStep != "tasks-two-measure" || second.Fallback.FromStep != "tasks-two-baseline" ||
		b.Fallback == second.Fallback || group.Steps[1].Inputs["evidence"].Fallback.FromStep != "baseline" {
		t.Fatal("task fallback escaped its configured item or mutated the frozen template", b)
	}
}
