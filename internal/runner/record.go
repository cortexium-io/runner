package runner

import (
	"encoding/json"
	"errors"
)

type runAlias Run
type runDefinition struct {
	Process  Process           `json:"process"`
	Roles    map[string]Role   `json:"roles"`
	Profiles map[string]string `json:"profiles"`
}
type runRecord struct {
	runAlias
	Process      *Process          `json:"process,omitempty"`
	Roles        map[string]Role   `json:"roles,omitempty"`
	Profiles     map[string]string `json:"profiles,omitempty"`
	AttemptCount *int              `json:"attempt_count,omitempty"`
}

// Settings and current operational state are separately bounded documents.
// Format three stores each attempt separately in the same SQLite transaction;
// public Run values still retain every definition, attempt and source fact.
func MarshalRunRecord(r Run) (state, definition []byte, err error) {
	if r.RecordFormat != 0 && r.RecordFormat != 3 && r.RecordFormat != 4 && r.RecordFormat != 5 && r.RecordFormat != 6 {
		return nil, nil, errors.New("unsupported run record format")
	}
	if (r.RecordFormat == 4 || r.RecordFormat == 5) && r.UnpublishedCardRecovery == nil {
		return nil, nil, errors.New("recovery record is missing its one-use audit")
	}
	if err := validateGuidesMediaRecord(r); err != nil {
		return nil, nil, err
	}
	var count *int
	if r.RecordFormat >= 3 {
		n := len(r.Attempts)
		count = &n
		r.Attempts = nil
	}
	state, err = json.Marshal(runRecord{runAlias: runAlias(r), AttemptCount: count})
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
	if r.RecordFormat != 0 && r.RecordFormat != 3 && r.RecordFormat != 4 && r.RecordFormat != 5 && r.RecordFormat != 6 {
		return r, errors.New("unsupported run record format")
	}
	if r.RecordFormat >= 3 && len(definition) == 0 {
		return r, errors.New("normalized run definition is missing")
	}
	if (r.RecordFormat == 4 || r.RecordFormat == 5) && r.UnpublishedCardRecovery == nil {
		return r, errors.New("recovery record is missing its one-use audit")
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
	if err := validateGuidesMediaRecord(r); err != nil {
		return r, err
	}
	return r, nil
}
