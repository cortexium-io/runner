package sqlite

import (
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
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatalf("general task physical attempt range lacks its versioned schema: %d %v", version, err)
	}
}

func TestGeneralTaskSchemaUpgradePreservesVersionThreeAttemptBytes(t *testing.T) {
	r := normalizedRun(2)
	s := openNormalized(t, r)
	var before []byte
	if err := s.db.QueryRow("SELECT data FROM attempts WHERE run_id=? AND position=1", r.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Recreate the actual previous physical constraint, retaining every byte.
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
	var name string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Open performs only the schema migration; it must preserve immutable data.
	s, err := Open(filepath.Dir(name), normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var after []byte
	if err := s.db.QueryRow("SELECT data FROM attempts WHERE run_id=? AND position=1", r.ID).Scan(&after); err != nil || string(after) != string(before) {
		t.Fatalf("schema migration rewrote retained attempt bytes: %v", err)
	}
}
