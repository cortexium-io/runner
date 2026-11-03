package runner

import "encoding/json"

type runAlias Run
type runDefinition struct {
	Process  Process           `json:"process"`
	Roles    map[string]Role   `json:"roles"`
	Profiles map[string]string `json:"profiles"`
}
type runRecord struct {
	runAlias
	Process  *Process          `json:"process,omitempty"`
	Roles    map[string]Role   `json:"roles,omitempty"`
	Profiles map[string]string `json:"profiles,omitempty"`
}

// Settings and current operational history are separately bounded documents in
// the same SQLite transaction. The existing database/run reservation covers
// both. Public Run values retain every definition, attempt and source fact.
func MarshalRunRecord(r Run) (state, definition []byte, err error) {
	state, err = json.Marshal(runRecord{runAlias: runAlias(r)})
	if err == nil {
		definition, err = json.Marshal(runDefinition{r.Process, r.Roles, r.Profiles})
	}
	return
}

func UnmarshalRunRecord(state, definition []byte) (Run, error) {
	var r Run
	if err := json.Unmarshal(state, &r); err != nil {
		return r, err
	}
	// Version-one rows retain their original complete JSON. Opening an upgraded
	// database does not rewrite or discard any historical record.
	if len(definition) > 0 {
		var d runDefinition
		if err := json.Unmarshal(definition, &d); err != nil {
			return r, err
		}
		r.Process, r.Roles, r.Profiles = d.Process, d.Roles, d.Profiles
	}
	return r, nil
}
