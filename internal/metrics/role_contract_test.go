package metrics

import (
	"encoding/json"
	"os"
	"testing"
)

func TestStoreValidatesRecordedRoleContracts(t *testing.T) {
	for _, test := range []struct {
		contract string
		valid    bool
	}{
		{"", true}, {"planner", true}, {"implementer", true}, {"reviewer", true}, {"PRIVATE UNKNOWN ROLE", false},
	} {
		t.Run(test.contract, func(t *testing.T) {
			store := NewStore(privateMetricsPath(t))
			event := Event{Version: EventVersion, Kind: EventStarted, AttemptID: "attempt", Role: "custom", RoleContract: test.contract}
			if err := store.Append(event); (err == nil) != test.valid {
				t.Fatalf("append validity=%t, want %t: %v", err == nil, test.valid, err)
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.Path(), append(encoded, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			history, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if test.valid {
				if len(history.Attempts) != 1 || history.Attempts[0].RoleContract != test.contract || history.MalformedRecords != 0 {
					t.Fatalf("recorded contract lost: %#v", history)
				}
			} else if len(history.Attempts) != 0 || history.MalformedRecords != 1 {
				t.Fatalf("unknown contract admitted: %#v", history)
			}
		})
	}
}
