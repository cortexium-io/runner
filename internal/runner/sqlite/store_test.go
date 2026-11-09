package sqlite

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestDefinitionUpgradePreservesLegacyHistoryAndBoundedDocuments(t *testing.T) {
	root := t.TempDir()
	legacy := runner.Run{
		ID:        "legacy",
		ProjectID: "demo",
		Status:    "Completed",
		Process:   runner.Process{ID: "original"},
		Roles:     map[string]runner.Role{"owner": {ID: "owner", Purpose: "original settings"}},
		Attempts:  []runner.Attempt{{ID: "original-attempt", State: runner.Finished, Released: true}},
	}
	data, _ := json.Marshal(legacy)
	db, err := sql.Open("sqlite", filepath.Join(root, "process-core.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`BEGIN;
CREATE TABLE runs(id TEXT PRIMARY KEY, project TEXT NOT NULL, data BLOB NOT NULL);
CREATE TABLE requests(key TEXT PRIMARY KEY, kind TEXT NOT NULL, args BLOB NOT NULL, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE);
CREATE TABLE artifacts(id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), project TEXT NOT NULL, bytes INTEGER NOT NULL);
CREATE TABLE artifact_uses(artifact TEXT NOT NULL REFERENCES artifacts(id), run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE, PRIMARY KEY(artifact,run_id));
PRAGMA user_version=1; COMMIT;`)
	if err == nil {
		_, err = db.Exec("INSERT INTO runs(id,project,data) VALUES(?,?,?)", legacy.ID, legacy.ProjectID, data)
	}
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(root, 0700)
	policy := runner.DefaultStorage()
	policy.Database.MaxRecord = 8 << 10
	s, err := Open(root, policy)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err := s.Load(t.Context())
	if err != nil || len(loaded) != 1 || !reflect.DeepEqual(loaded[0], legacy) {
		t.Fatal("legacy history changed", err, loaded)
	}
	var retained []byte
	if err = s.db.QueryRow("SELECT data FROM runs WHERE id=?", legacy.ID).Scan(&retained); err != nil || string(retained) != string(data) {
		t.Fatal("upgrade rewrote old history", err)
	}
	fresh := runner.Clone(legacy)
	fresh.ID = "fresh"
	fresh.Roles["owner"] = runner.Role{ID: "owner", Purpose: strings.Repeat("s", 5000)}
	fresh.Reason = strings.Repeat("r", 5000)
	if err = s.Save(t.Context(), fresh, nil, nil); err != nil {
		t.Fatal("separately bounded settings/history rejected", err)
	}
	loaded, err = s.Load(t.Context())
	if err != nil || len(loaded) != 2 || !reflect.DeepEqual(loaded[0], fresh) || !reflect.DeepEqual(loaded[1], legacy) {
		t.Fatal("settings/history were not restored losslessly", err)
	}
	fresh.Reason = strings.Repeat("r", 9<<10)
	if err = s.Save(t.Context(), fresh, nil, nil); err == nil {
		t.Fatal("oversized operational record accepted")
	}
	fresh.Reason = ""
	fresh.Roles["owner"] = runner.Role{ID: "owner", Purpose: strings.Repeat("s", 9<<10)}
	if err = s.Save(t.Context(), fresh, nil, nil); err == nil {
		t.Fatal("oversized settings record accepted")
	}
}
