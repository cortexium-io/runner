package runner

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func retainedTaskSpecification() Run {
	spec := RoleTask{
		ID:         "repair",
		Role:       "developer",
		Brief:      strings.Repeat("Canonical retained task requirements. ", 50),
		Acceptance: []string{"Preserve state and verify the changed behavior"},
		WritePaths: []string{"src"},
	}
	return Run{
		ID:           "root",
		RecordFormat: 9,
		Process:      Process{MaxAttempts: 64},
		TaskGraph: &TaskGraphState{
			RootRunID: "root",
			TaskID:    "root",
			Brief:     "Original goal",
			Children: []RoleTaskLink{
				{Spec: spec, RunID: "unmaterialized-child"},
			},
		},
		Attempts: []Attempt{
			{
				ID: "admitting-attempt",
				End: &ExecutionEnd{
					Kind: "returned",
					Result: &StepResult{
						Outcome: "ready",
						TaskAction: &TaskAction{
							Kind:  "delegate",
							Tasks: []RoleTask{spec},
						},
					},
				},
			},
		},
	}
}

func TestTaskSpecificationRecordReusesAdmittedAttempt(t *testing.T) {
	r := retainedTaskSpecification()
	before := Clone(r)
	state, definition, err := MarshalRunRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r, before) || bytes.Contains(state, []byte("Canonical retained")) || !bytes.Contains(state, []byte(`"spec_source":{"attempt_id":"admitting-attempt"}`)) {
		t.Fatal("normalization mutated public state or duplicated the admitted specification")
	}
	loaded, err := UnmarshalRunRecord(state, definition)
	if err != nil {
		t.Fatal(err)
	}
	if err = RestoreTaskSpecifications(&loaded); err == nil {
		t.Fatal("unavailable parent attempt was silently replaced")
	}
	loaded.Attempts = Clone(r.Attempts)
	if err = RestoreTaskSpecifications(&loaded); err != nil || !reflect.DeepEqual(loaded.TaskGraph, r.TaskGraph) {
		t.Fatal("retained task did not restore without a child row", err)
	}
}

func TestTaskSpecificationNormalizationKeepsUnmatchedLegacyAndRejectedProposals(t *testing.T) {
	for _, change := range []func(*Run){
		func(r *Run) { r.Attempts[0].End.Kind = "failed" },
		func(r *Run) { r.Attempts[0].End.Result.TaskAction.Tasks[0].Brief = "Different proposal" },
	} {
		r := retainedTaskSpecification()
		change(&r)
		state, _, err := MarshalRunRecord(r)
		if err != nil || !bytes.Contains(state, []byte("Canonical retained")) || bytes.Contains(state, []byte("spec_source")) {
			t.Fatal("unmatched or rejected task lost its authoritative inline specification", err)
		}
	}
}

func TestTaskSpecificationRestorationRejectsInvalidReferences(t *testing.T) {
	for name, change := range map[string]func(*Run){
		"missing attempt": func(r *Run) { r.Attempts = nil },
		"failed proposal": func(r *Run) { r.Attempts[0].End.Kind = "failed" },
		"wrong task":      func(r *Run) { r.Attempts[0].End.Result.TaskAction.Tasks[0].ID = "other" },
		"ambiguous task": func(r *Run) {
			action := r.Attempts[0].End.Result.TaskAction
			action.Tasks = append(action.Tasks, action.Tasks[0])
		},
		"mixed inline specification": func(r *Run) { r.TaskGraph.Children[0].Spec.Brief = "extra" },
	} {
		t.Run(name, func(t *testing.T) {
			r := retainedTaskSpecification()
			if err := compactTaskSpecifications(&r); err != nil {
				t.Fatal(err)
			}
			change(&r)
			if err := RestoreTaskSpecifications(&r); err == nil {
				t.Fatal("invalid reference was accepted")
			}
		})
	}
}
