package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestActivityRejectsFreeTextAndNonHarnessPlacement(t *testing.T) {
	for _, test := range []struct{ name, kind, stage, activity string }{
		{"command payload", EventStageCompleted, StageHarnessRun, "secret command"},
		{"attempt", EventCompleted, "", "shell_started"},
		{"cleanup", EventStageCompleted, StageHarnessCleanup, "shell_started"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(filepath.Join(t.TempDir(), "history.jsonl"))
			event := Event{Version: EventVersion, AttemptID: "attempt", Kind: test.kind, Stage: test.stage, StageID: "stage", Outcome: StageOutcomeFailed, HarnessActivity: &HarnessActivity{Coverage: "observed", LastEventKind: test.activity}}
			if err := store.Append(event); err == nil {
				t.Fatal("invalid activity accepted")
			}
			data, _ := json.Marshal(event)
			if err := os.WriteFile(store.Path(), append(data, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			history, err := store.Read()
			if err != nil || history.MalformedRecords != 1 {
				t.Fatalf("invalid activity read: %+v %v", history, err)
			}
		})
	}
}
