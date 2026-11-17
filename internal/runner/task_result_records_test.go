package runner

import (
	"bytes"
	"encoding/json"
	"reflect"
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
	if wire.Graph.Results[0]["id"] != link.Spec.ID {
		t.Fatal("portable result lost its explicit public identity")
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
		graph["results"] = json.RawMessage(`[{"run_id":"unmaterialized-child"}]`)
		document["task_graph"], _ = json.Marshal(graph)
		bad, _ := json.Marshal(document)
		if _, err := UnmarshalRunRecord(bad, definition); err == nil {
			t.Fatal("normalized identity guessed absent/ambiguous ownership")
		}
	}
}
