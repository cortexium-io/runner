package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func retainedTaskLinks(n int) Run {
	r := retainedTaskSpecification()
	r.TaskGraph.Children, r.TaskGraph.Results, r.Attempts = nil, nil, nil
	for i := range n {
		spec := RoleTask{ID: fmt.Sprintf("repair-%d", i%3), Role: "developer", Brief: fmt.Sprintf("Canonical correction %d", i)}
		run := fmt.Sprintf("reserved_%06d", i)
		r.TaskGraph.Children = append(r.TaskGraph.Children, RoleTaskLink{Spec: spec, RunID: run, Retired: i < n-3})
		r.TaskGraph.Results = append(r.TaskGraph.Results, TaskResultRef{ID: spec.ID, RunID: run})
		r.Attempts = append(r.Attempts, Attempt{ID: fmt.Sprintf("admitted_%06d", i), State: Finished, Released: true, End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", TaskAction: &TaskAction{Kind: "delegate", Tasks: []RoleTask{spec}}}}})
	}
	return r
}

func TestTaskLinkStorageLongHistoryRetainsCompletePublicGraphAndUnchangedReserve(t *testing.T) {
	r := retainedTaskLinks(500)
	r.TaskGraph.Results = append([]TaskResultRef{{ID: "explicit-legacy", RunID: "unmatched-legacy"}}, r.TaskGraph.Results...)
	r.TaskGraph.ResultInputs = []TaskResultRef{{ID: "sibling", RunID: "sibling_run"}}
	before := Clone(r)
	portable, _, err := MarshalRunRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	record, err := MarshalRunStorageRecord(r)
	if err != nil || !reflect.DeepEqual(before, r) {
		t.Fatal("storage encoder mutated public work", err)
	}
	if len(portable) <= 20<<10 || len(record.State) > 20<<10 || len(record.TaskLinks) != 501 {
		t.Fatal("history still consumes operational reserve", len(portable), len(record.State), err)
	}
	if err := admitRunRecord(r, 40<<10); err != nil {
		t.Fatal("long history blocks the next check/review frontier", err)
	}
	loaded, err := UnmarshalRunStorageRecord(record.State, record.Definition, r.Attempts, record.TaskLinks)
	if err != nil || !reflect.DeepEqual(loaded.TaskGraph, r.TaskGraph) || !reflect.DeepEqual(loaded.Attempts, r.Attempts) {
		t.Fatal("storage changed source/result identity, order or membership", err)
	}
	if _, err := UnmarshalRunRecord(record.State, record.Definition); err == nil {
		t.Fatal("partial graph escaped through legacy decoder")
	}
	// A returned action whose proposal was never applied cannot mint a child.
	r.Attempts = append(r.Attempts, Attempt{ID: "unapplied", End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", TaskAction: &TaskAction{Kind: "delegate", Tasks: []RoleTask{{ID: "not-admitted", Role: "developer", Brief: "Unapplied proposal"}}}}}})
	unchanged, err := MarshalRunStorageRecord(r)
	if err != nil || !reflect.DeepEqual(unchanged.TaskLinks, record.TaskLinks) {
		t.Fatal("returned action manufactured reservation authority", err)
	}
	if bytes.Contains(record.State, []byte("reserved_")) {
		t.Fatal("relationship history remains inline")
	}
}

func TestTaskLinkStorageRejectsIncompleteMixedOrAmbiguousGraphs(t *testing.T) {
	r := retainedTaskLinks(3)
	record, err := MarshalRunStorageRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"missing-row", "duplicate-run", "duplicate-result", "result-hole", "missing-attempt", "wrong-spec-source", "mixed-inline", "missing-count", "null-count"} {
		t.Run(fault, func(t *testing.T) {
			state, links, attempts := append([]byte(nil), record.State...), Clone(record.TaskLinks), Clone(r.Attempts)
			switch fault {
			case "missing-row":
				links = links[:2]
			case "duplicate-run":
				links[1].ChildRunID = links[0].ChildRunID
			case "duplicate-result":
				links[1].ResultPosition = links[0].ResultPosition
			case "result-hole":
				links[1].ResultPosition = nil
			case "missing-attempt":
				attempts = attempts[:2]
			case "wrong-spec-source":
				links[0].Data = json.RawMessage(`{"spec":{"id":"repair-0"},"spec_source":{"attempt_id":"unapplied"}}`)
			default:
				var doc map[string]json.RawMessage
				_ = json.Unmarshal(state, &doc)
				if fault == "missing-count" {
					delete(doc, "task_link_count")
				} else if fault == "null-count" {
					doc["task_link_count"] = json.RawMessage(`null`)
				} else {
					var graph map[string]json.RawMessage
					_ = json.Unmarshal(doc["task_graph"], &graph)
					graph["children"] = json.RawMessage(`[]`)
					doc["task_graph"], _ = json.Marshal(graph)
				}
				state, _ = json.Marshal(doc)
			}
			if _, err := UnmarshalRunStorageRecord(state, record.Definition, attempts, links); err == nil {
				t.Fatal("invalid normalized graph accepted")
			}
		})
	}
}
