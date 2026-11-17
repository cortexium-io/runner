package runner

import (
	"encoding/json"
	"errors"
)

// RunStorageRecord separates graph relationships from the bounded operational
// header. Attempts retain their existing separate rows in the same transaction.
type RunStorageRecord struct {
	State      []byte
	Definition []byte
	TaskLinks  []RoleTaskLinkRecord
}

// RoleTaskLinkRecord is one ordered storage relation, never a public task.
// Data contains either a compact child link or an explicit unmatched legacy
// result ID. ChildRunID is a parent-owned reservation, not a child-row FK.
type RoleTaskLinkRecord struct {
	ChildRunID     string
	Data           json.RawMessage
	ResultPosition *int
}

// TaskLinkRecordBytes includes both relation identities and ordered membership
// metadata, so a long legacy producer ID cannot bypass the per-row bound.
func TaskLinkRecordBytes(parent string, row RoleTaskLinkRecord) int {
	return len(parent) + len(row.ChildRunID) + len(row.Data) + 32
}

type taskLinkData struct {
	Spec           *RoleTask       `json:"spec,omitempty"`
	SpecSource     *TaskSpecSource `json:"spec_source,omitempty"`
	Retired        bool            `json:"retired,omitempty"`
	LegacyResultID string          `json:"legacy_result_id,omitempty"`
}

type taskLinkHeader struct {
	Links    *int `json:"task_link_count"`
	Children *int `json:"task_child_count"`
	Results  *int `json:"task_result_count"`
}

// MarshalRunStorageRecord does not alter the public Run or derive reservations
// from returned actions. Only its explicitly retained Children/Results persist.
func MarshalRunStorageRecord(r Run) (RunStorageRecord, error) {
	state, definition, err := MarshalRunRecord(r)
	if err != nil {
		return RunStorageRecord{}, err
	}
	if r.TaskGraph == nil {
		return RunStorageRecord{State: state, Definition: definition}, nil
	}
	// The portable codec has already compacted canonical specification sources.
	var stored Run
	if err := json.Unmarshal(state, &stored); err != nil {
		return RunStorageRecord{}, err
	}
	if err := restoreTaskResultIDs(stored.TaskGraph); err != nil {
		return RunStorageRecord{}, err
	}
	links, err := taskLinkRecords(stored.TaskGraph)
	if err != nil {
		return RunStorageRecord{}, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(state, &document); err != nil {
		return RunStorageRecord{}, err
	}
	graph := *stored.TaskGraph
	children, results := len(graph.Children), len(graph.Results)
	graph.Children, graph.Results = nil, nil
	document["task_graph"], err = json.Marshal(graph)
	if err != nil {
		return RunStorageRecord{}, err
	}
	for key, value := range map[string]int{"task_link_count": len(links), "task_child_count": children, "task_result_count": results} {
		document[key], _ = json.Marshal(value)
	}
	state, err = json.Marshal(document)
	return RunStorageRecord{State: state, Definition: definition, TaskLinks: links}, err
}

func taskLinkRecords(g *TaskGraphState) ([]RoleTaskLinkRecord, error) {
	var rows []RoleTaskLinkRecord
	children := map[string]int{}
	for _, child := range g.Children {
		if child.RunID == "" || child.Spec.ID == "" {
			return nil, errors.New("retained task link needs its complete reservation identity")
		}
		if _, duplicate := children[child.RunID]; duplicate {
			return nil, errors.New("retained task links have duplicate child reservations")
		}
		children[child.RunID] = len(rows)
		spec := child.Spec
		data, err := json.Marshal(taskLinkData{Spec: &spec, SpecSource: child.SpecSource, Retired: child.Retired})
		if err != nil {
			return nil, err
		}
		rows = append(rows, RoleTaskLinkRecord{ChildRunID: child.RunID, Data: data})
	}
	seen := map[string]bool{}
	for position, result := range g.Results {
		if result.ID == "" || result.RunID == "" || seen[result.RunID] {
			return nil, errors.New("retained task results need complete unique producer identities")
		}
		seen[result.RunID] = true
		index, exists := children[result.RunID]
		if exists {
			if g.Children[index].Spec.ID != result.ID {
				return nil, errors.New("retained result differs from its reserved task identity")
			}
			rows[index].ResultPosition = &position
		} else {
			data, _ := json.Marshal(taskLinkData{LegacyResultID: result.ID})
			rows = append(rows, RoleTaskLinkRecord{ChildRunID: result.RunID, Data: data, ResultPosition: &position})
		}
	}
	return rows, nil
}

// LegacyTaskLinkRecords preserves the exact disk specification source when an
// inline schema-19 graph is normalized on its next ordinary save.
func LegacyTaskLinkRecords(state []byte) ([]RoleTaskLinkRecord, error) {
	var r Run
	if err := json.Unmarshal(state, &r); err != nil {
		return nil, err
	}
	if r.TaskGraph == nil {
		return nil, nil
	}
	if err := restoreTaskResultIDs(r.TaskGraph); err != nil {
		return nil, err
	}
	return taskLinkRecords(r.TaskGraph)
}

// RunStorageLinkCounts validates normalized metadata without exposing a partial
// Run. A negative link count identifies the unchanged inline legacy format.
func RunStorageLinkCounts(state []byte) (links, children, results int, err error) {
	var header taskLinkHeader
	if err = json.Unmarshal(state, &header); err != nil {
		return
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(state, &fields); err != nil {
		return
	}
	if fields["task_link_count"] == nil && fields["task_child_count"] == nil && fields["task_result_count"] == nil {
		return -1, 0, 0, nil
	}
	if header.Links == nil || header.Children == nil || header.Results == nil || *header.Links < 0 || *header.Children < 0 || *header.Results < 0 || *header.Children > *header.Links || *header.Results > *header.Links {
		return 0, 0, 0, errors.New("invalid normalized task-link counts")
	}
	return *header.Links, *header.Children, *header.Results, nil
}

// UnmarshalRunStorageRecord restores both canonical attempts and relationships
// before returning a complete public graph to Core or any transaction guard.
func UnmarshalRunStorageRecord(state, definition []byte, attempts []Attempt, links []RoleTaskLinkRecord) (Run, error) {
	var attemptHeader struct {
		Format int             `json:"record_format"`
		Count  *int            `json:"attempt_count"`
		Inline json.RawMessage `json:"attempts"`
	}
	if json.Unmarshal(state, &attemptHeader) != nil || attemptHeader.Format >= 3 && (attemptHeader.Count == nil || *attemptHeader.Count != len(attempts) || len(attemptHeader.Inline) != 0) || attemptHeader.Format == 0 && len(attempts) != 0 {
		return Run{}, errors.New("stored attempts need complete normalized hydration")
	}
	count, childCount, resultCount, err := RunStorageLinkCounts(state)
	if err != nil {
		return Run{}, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(state, &document); err != nil {
		return Run{}, err
	}
	if count >= 0 {
		var graphFields map[string]json.RawMessage
		if json.Unmarshal(document["task_graph"], &graphFields) != nil || graphFields == nil || graphFields["children"] != nil || graphFields["results"] != nil || len(links) != count {
			return Run{}, errors.New("normalized task graph is mixed, missing or incomplete")
		}
		var graph TaskGraphState
		if err := json.Unmarshal(document["task_graph"], &graph); err != nil {
			return Run{}, err
		}
		graph.Results = make([]TaskResultRef, resultCount)
		if resultCount == 0 {
			graph.Results = nil
		}
		seen := map[string]bool{}
		legacyPosition := -1
		for i, row := range links {
			var data taskLinkData
			if row.ChildRunID == "" || seen[row.ChildRunID] || json.Unmarshal(row.Data, &data) != nil {
				return Run{}, errors.New("invalid or duplicate normalized task-link identity")
			}
			seen[row.ChildRunID] = true
			id := data.LegacyResultID
			if i < childCount {
				if data.Spec == nil || data.Spec.ID == "" || id != "" {
					return Run{}, errors.New("normalized task child has no unique specification")
				}
				id = data.Spec.ID
				graph.Children = append(graph.Children, RoleTaskLink{Spec: *data.Spec, SpecSource: data.SpecSource, RunID: row.ChildRunID, Retired: data.Retired})
			} else if id == "" || data.Spec != nil || data.SpecSource != nil || data.Retired || row.ResultPosition == nil || *row.ResultPosition <= legacyPosition {
				return Run{}, errors.New("normalized legacy result is incomplete or unordered")
			} else {
				legacyPosition = *row.ResultPosition
			}
			if row.ResultPosition != nil {
				position := *row.ResultPosition
				if position < 0 || position >= resultCount || graph.Results[position].ID != "" {
					return Run{}, errors.New("normalized result position differs or is duplicated")
				}
				graph.Results[position] = TaskResultRef{ID: id, RunID: row.ChildRunID}
			}
		}
		for _, result := range graph.Results {
			if result.ID == "" {
				return Run{}, errors.New("normalized result membership is incomplete")
			}
		}
		document["task_graph"], _ = json.Marshal(graph)
		delete(document, "task_link_count")
		delete(document, "task_child_count")
		delete(document, "task_result_count")
		state, err = json.Marshal(document)
		if err != nil {
			return Run{}, err
		}
	} else if len(links) != 0 {
		return Run{}, errors.New("legacy graph has orphan normalized task links")
	}
	r, err := UnmarshalRunRecord(state, definition)
	if err != nil {
		return Run{}, err
	}
	if r.RecordFormat >= 3 {
		r.Attempts = attempts
	}
	if err := RestoreTaskSpecifications(&r); err != nil {
		return Run{}, err
	}
	return r, nil
}

// RetireTaskLinkRecord preserves the prior canonical spec_source byte identity.
func RetireTaskLinkRecord(row RoleTaskLinkRecord, retired bool) (RoleTaskLinkRecord, error) {
	var data taskLinkData
	if err := json.Unmarshal(row.Data, &data); err != nil || data.Spec == nil || data.Retired && !retired {
		return row, errors.New("retained task link cannot be reactivated or replaced")
	}
	data.Retired = retired
	var err error
	row.Data, err = json.Marshal(data)
	return row, err
}
