package sqlite

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestGeneralTaskHistoryExceedsLegacy128WithoutChangingLegacyBound(t *testing.T) {
	r := normalizedRun(129)
	r.RecordFormat = 9
	r.Process.MaxAttempts = 256
	r.Process.Tasks = &runner.TaskPolicy{Roles: []string{"owner"}, MaxTasks: 64, MaxDepth: 4, MaxRevisions: 4, MaxConcurrent: 2}
	r.TaskGraph = &runner.TaskGraphState{RootRunID: r.ID, TaskID: "root", Brief: "A retained general goal"}
	s := openNormalized(t, r)
	loaded, err := s.Load(t.Context())
	if err != nil || len(loaded) != 1 || !reflect.DeepEqual(loaded[0], r) {
		t.Fatalf("general task lost normalized history beyond 128: %v", err)
	}
	legacy := runner.Clone(r)
	legacy.ID, legacy.RecordFormat, legacy.TaskGraph, legacy.Process.Tasks = "legacy", 3, nil, nil
	if err := s.Save(t.Context(), legacy, nil, nil); err == nil {
		t.Fatal("new general-task storage range silently widened the legacy attempt allowance")
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 12 {
		t.Fatalf("general task physical attempt range lacks its versioned schema: %d %v", version, err)
	}
}

func TestGeneralTaskSchemaUpgradePreservesRetainedBytes(t *testing.T) {
	for _, version := range []int{3, 4, 5, 6, 7, 8, 9, 10, 11} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			r := normalizedRun(2)
			s := openNormalized(t, r)
			query := "SELECT runs.data,runs.definition,attempts.data FROM runs JOIN attempts ON attempts.run_id=runs.id WHERE runs.id=? AND attempts.position=1"
			var state, definition, attempt []byte
			if err := s.db.QueryRow(query, r.ID).Scan(&state, &definition, &attempt); err != nil {
				t.Fatal(err)
			}
			if version == 3 {
				// Recreate the previous physical constraint, retaining every byte.
				if _, err := s.db.Exec(`BEGIN;
ALTER TABLE attempts RENAME TO attempts_old;
CREATE TABLE attempts(run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
position INTEGER NOT NULL CHECK(position>=0 AND position<128), id TEXT NOT NULL UNIQUE, data BLOB NOT NULL,
PRIMARY KEY(run_id,position));
INSERT INTO attempts SELECT run_id,position,id,data FROM attempts_old;
DROP TABLE attempts_old;
PRAGMA user_version=3; COMMIT;`); err != nil {
					t.Fatal(err)
				}
			} else if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
				t.Fatal(err)
			}
			var name string
			if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&name); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			// Neither physical widening nor the native-policy marker rewrites rows.
			s, err := Open(filepath.Dir(name), normalizedPolicy())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var gotState, gotDefinition, gotAttempt []byte
			if err := s.db.QueryRow(query, r.ID).Scan(&gotState, &gotDefinition, &gotAttempt); err != nil || string(gotState) != string(state) || string(gotDefinition) != string(definition) || string(gotAttempt) != string(attempt) {
				t.Fatalf("schema migration rewrote retained data: %v", err)
			}
			var marker int
			if err := s.db.QueryRow("PRAGMA user_version").Scan(&marker); err != nil || marker != 12 {
				t.Fatalf("native-policy downgrade guard missing: version=%d error=%v", marker, err)
			}
		})
	}
}

func TestGeneralTaskCanonicalRootGoalReloadAndOrdinarySave(t *testing.T) {
	r := normalizedRun(2)
	r.RecordFormat = 9
	r.Process.Tasks = &runner.TaskPolicy{Roles: []string{"owner"}, MaxTasks: 4, MaxDepth: 2, MaxConcurrent: 2}
	r.Inputs = map[string]runner.Value{"request": runner.Text("The complete immutable agreement")}
	r.TaskGraph = &runner.TaskGraphState{RootRunID: r.ID, TaskID: "root", Brief: "The complete immutable agreement"}
	s := openNormalized(t, r)
	check := func() {
		t.Helper()
		var state []byte
		if err := s.db.QueryRow("SELECT data FROM runs WHERE id=?", r.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		var stored runner.Run
		if err := json.Unmarshal(state, &stored); err != nil || stored.TaskGraph.Brief != "" {
			t.Fatal("SQLite retained another root-goal copy", err)
		}
		loaded, err := s.Load(t.Context())
		if err != nil || len(loaded) != 1 || !reflect.DeepEqual(loaded[0], r) {
			t.Fatal("SQLite reload lost public root-goal state", err)
		}
		if err := s.Save(t.Context(), loaded[0], nil, nil); err != nil {
			t.Fatal("ordinary save rejected the reconstituted root", err)
		}
	}
	check()
	var name string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = Open(filepath.Dir(name), normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check()
}
