package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestTaskResultRecordsPreservePublicIdentityAndLegacyReferences(t *testing.T) {
	r := retainedTaskSpecification()
	link := r.TaskGraph.Children[0]
	r.TaskGraph.Children = []RoleTaskLink{
		{Spec: RoleTask{ID: link.Spec.ID}, RunID: "old", Retired: true},
		{Spec: RoleTask{ID: link.Spec.ID}, RunID: "new"},
	}
	r.TaskGraph.Results = []TaskResultRef{
		{ID: link.Spec.ID, RunID: "old"},
		{ID: link.Spec.ID, RunID: "new"},
		{ID: "complete-legacy-reference", RunID: "legacy"},
	}
	r.TaskGraph.ResultInputs = []TaskResultRef{{ID: "sibling", RunID: "sibling-run"}}
	before := Clone(r)
	state, definition, err := MarshalRunRecord(r)
	if err != nil || !reflect.DeepEqual(before, r) {
		t.Fatal("disk normalization mutated caller", err)
	}
	var wire struct {
		Graph struct {
			Results []map[string]any `json:"results"`
			Inputs  []TaskResultRef  `json:"result_inputs"`
		} `json:"task_graph"`
	}
	if err := json.Unmarshal(state, &wire); err != nil {
		t.Fatal(err)
	}
	if _, exists := wire.Graph.Results[0]["id"]; exists {
		t.Fatal("retired child identity was duplicated")
	}
	if wire.Graph.Results[2]["id"] != "complete-legacy-reference" || !reflect.DeepEqual(wire.Graph.Inputs, r.TaskGraph.ResultInputs) {
		t.Fatal("legacy or sibling authority was altered")
	}
	loaded, err := UnmarshalRunRecord(state, definition)
	if err != nil || !reflect.DeepEqual(loaded.TaskGraph, r.TaskGraph) {
		t.Fatal("public identities or order changed", err)
	}
	public, _ := json.Marshal(loaded.TaskGraph.Results)
	if bytes.Count(public, []byte(`"id"`)) != 3 {
		t.Fatal("disk shape leaked to public results", string(public))
	}
}

func TestTaskResultRecordsRejectMissingOrAmbiguousNormalizedIdentity(t *testing.T) {
	r := retainedTaskSpecification()
	r.TaskGraph.Results = []TaskResultRef{{ID: r.TaskGraph.Children[0].Spec.ID, RunID: r.TaskGraph.Children[0].RunID}}
	invalid := Clone(r)
	invalid.TaskGraph.Results[0].ID = ""
	if _, _, err := MarshalRunRecord(invalid); err == nil {
		t.Fatal("invalid public identity became a valid normalized reference")
	}
	state, definition, err := MarshalRunRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, duplicate := range []bool{false, true} {
		var document map[string]json.RawMessage
		var graph map[string]json.RawMessage
		if json.Unmarshal(state, &document) != nil || json.Unmarshal(document["task_graph"], &graph) != nil {
			t.Fatal("invalid fixture")
		}
		children := []RoleTaskLink{}
		if duplicate {
			children = []RoleTaskLink{r.TaskGraph.Children[0], r.TaskGraph.Children[0]}
		}
		graph["children"], _ = json.Marshal(children)
		document["task_graph"], _ = json.Marshal(graph)
		bad, _ := json.Marshal(document)
		if _, err := UnmarshalRunRecord(bad, definition); err == nil {
			t.Fatal("normalized identity guessed absent/ambiguous ownership")
		}
	}
}

func TestTaskResultRecordsRetainedFrontierFitsUnchangedReserve(t *testing.T) {
	r := retainedTaskSpecification()
	r.TaskGraph.Children, r.TaskGraph.Results, r.Attempts = nil, nil, nil
	r.TaskGraph.Directive = strings.Repeat("Preserve canonical work and independently verify the goal. ", 30)
	for i := range 56 {
		id := fmt.Sprintf("correction-test-author-current-evidence-contract-reassessment-%02d", i)
		spec := RoleTask{ID: id, Role: "developer", Brief: "Preserve work; qualify only the changed contract."}
		r.TaskGraph.Children = append(r.TaskGraph.Children, RoleTaskLink{Spec: spec, RunID: fmt.Sprintf("run_%032d", i)})
		r.TaskGraph.Results = append(r.TaskGraph.Results, TaskResultRef{ID: id, RunID: fmt.Sprintf("run_%032d", i)})
		r.Attempts = append(r.Attempts, Attempt{
			ID: fmt.Sprintf("attempt_%032d", i),
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
		})
	}
	state, definition, err := MarshalRunRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	var graph map[string]json.RawMessage
	_ = json.Unmarshal(state, &document)
	_ = json.Unmarshal(document["task_graph"], &graph)
	graph["results"], _ = json.Marshal(r.TaskGraph.Results)
	document["task_graph"], _ = json.Marshal(graph)
	duplicated, _ := json.Marshal(document)
	const reserve = 20 << 10
	if len(state) >= reserve || len(duplicated) <= reserve || len(duplicated)-len(state) < 2500 {
		t.Fatalf("frontier did not fit unchanged reserve: normalized=%d duplicated=%d", len(state), len(duplicated))
	}
	loaded, err := UnmarshalRunRecord(state, definition)
	if err != nil || !reflect.DeepEqual(loaded.TaskGraph.Results, r.TaskGraph.Results) {
		t.Fatal("frontier lost public results", err)
	}
}
