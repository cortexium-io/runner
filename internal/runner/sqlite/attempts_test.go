package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func normalizedPolicy() runner.StoragePolicy {
	p := runner.DefaultStorage()
	p.Database.MaxSize = 48 << 20
	p.Database.MaxRecord = 40 << 10
	p.Database.MaxRuns = 128
	return p
}

func normalizedRun(n int) runner.Run {
	r := runner.Run{
		ID: "normalized", ProjectID: "demo", RecordFormat: 3, Status: "Completed",
		Process: runner.Process{ID: "bounded", MaxAttempts: 64},
		Roles:   map[string]runner.Role{"owner": {ID: "owner", Purpose: "frozen settings"}},
		Created: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
	}
	for i := range n {
		id := fmt.Sprintf("attempt_%03d", i)
		r.Attempts = append(r.Attempts, runner.Attempt{
			ID: id, Step: i % 6, Visit: i / 6, State: runner.Finished, Released: true,
			Started:  r.Created.Add(time.Duration(i) * time.Minute),
			Deadline: r.Created.Add(time.Duration(i+20) * time.Minute),
			Source:   &runner.SourceResult{Repository: "/owned/repository", Base: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40), AttemptID: id, PatchID: id + "/changes.patch"},
			End:      &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: "ready", Outputs: map[string]runner.Value{"report": runner.Text(strings.Repeat("actual bounded evidence ", 80))}}},
		})
	}
	return r
}

func openNormalized(t *testing.T, r runner.Run) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state"), normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Save(t.Context(), r, nil, nil); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNormalizedAttemptHistorySurvives64AttemptsAndRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	p := normalizedPolicy()
	s, err := Open(root, p)
	if err != nil {
		t.Fatal(err)
	}
	r := normalizedRun(64)
	whole, _ := json.Marshal(r)
	if len(whole) <= int(p.Database.MaxRecord) {
		t.Fatal("fixture does not reproduce the aggregate history limit")
	}
	prefix := runner.Clone(r)
	prefix.Attempts = prefix.Attempts[:63]
	prefix.Status = "Running"
	prefix.Attempts[62].State = runner.Running
	prefix.Attempts[62].End = nil
	prefix.Attempts[62].Released = false
	if err = s.Save(t.Context(), prefix, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Save(t.Context(), r, nil, nil); err != nil {
		t.Fatal("64 bounded attempts rejected", err)
	}
	var state, definition []byte
	var count, largest int
	if err = s.db.QueryRow("SELECT data,definition FROM runs WHERE id=?", r.ID).Scan(&state, &definition); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(state, &fields) != nil || fields["attempts"] != nil || string(fields["attempt_count"]) != "64" {
		t.Fatal("attempts were not normalized", string(state))
	}
	if err = s.db.QueryRow("SELECT count(*),max(length(data)) FROM attempts WHERE run_id=?", r.ID).Scan(&count, &largest); err != nil || count != 64 || largest > int(p.Database.MaxRecord) || len(state) > int(p.Database.MaxRecord) || len(definition) > int(p.Database.MaxRecord) {
		t.Fatal("document bounds changed", err, count, largest, len(state), len(definition))
	}
	if s.maxRuns != 128 || s.policy.Database.MaxSize != 48<<20 {
		t.Fatal("normalization changed the retained-run/database allowance")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err := s.Load(t.Context())
	if err != nil || len(loaded) != 1 || !reflect.DeepEqual(loaded[0], r) {
		t.Fatal("normalized source/results/history changed after restart", err)
	}
	if err = s.Delete(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM attempts").Scan(&count); err != nil || count != 0 {
		t.Fatal("explicit run deletion left attempt rows", err, count)
	}
}

func TestAttemptSchemaUpgradePreservesVersionTwoBytes(t *testing.T) {
	root := t.TempDir()
	r := normalizedRun(2)
	r.RecordFormat = 0
	state, definition, err := runner.MarshalRunRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "process-core.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`BEGIN;
CREATE TABLE runs(id TEXT PRIMARY KEY, project TEXT NOT NULL, data BLOB NOT NULL, definition BLOB);
CREATE TABLE requests(key TEXT PRIMARY KEY, kind TEXT NOT NULL, args BLOB NOT NULL, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE);
CREATE TABLE artifacts(id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), project TEXT NOT NULL, bytes INTEGER NOT NULL);
CREATE TABLE artifact_uses(artifact TEXT NOT NULL REFERENCES artifacts(id), run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE, PRIMARY KEY(artifact,run_id));
PRAGMA user_version=2; COMMIT;`)
	if err == nil {
		_, err = db.Exec("INSERT INTO runs(id,project,data,definition) VALUES(?,?,?,?)", r.ID, r.ProjectID, state, definition)
	}
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(root, normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err := s.Load(t.Context())
	if err != nil || len(loaded) != 1 || !reflect.DeepEqual(loaded[0], r) {
		t.Fatal("legacy version-two history changed", err)
	}
	var afterState, afterDefinition []byte
	if err = s.db.QueryRow("SELECT data,definition FROM runs WHERE id=?", r.ID).Scan(&afterState, &afterDefinition); err != nil || string(afterState) != string(state) || string(afterDefinition) != string(definition) {
		t.Fatal("opening schema three rewrote legacy bytes", err)
	}
}

func TestNormalizedAttemptSavePreservesHistoryAndAtomicity(t *testing.T) {
	for _, fault := range []string{"oversize-attempt", "oversize-state", "oversize-definition", "shrink", "reorder", "duplicate", "downgrade", "artifact-failure", "too-many"} {
		t.Run(fault, func(t *testing.T) {
			original := normalizedRun(2)
			s := openNormalized(t, original)
			r := runner.Clone(original)
			switch fault {
			case "oversize-attempt":
				r.Attempts[1].Reason = strings.Repeat("x", 40<<10)
			case "oversize-state":
				r.Reason = strings.Repeat("x", 40<<10)
			case "oversize-definition":
				r.Roles["owner"] = runner.Role{ID: "owner", Purpose: strings.Repeat("x", 40<<10)}
			case "shrink":
				r.Attempts = r.Attempts[:1]
			case "reorder":
				r.Attempts[0], r.Attempts[1] = r.Attempts[1], r.Attempts[0]
			case "duplicate":
				r.Attempts[1].ID = r.Attempts[0].ID
			case "downgrade":
				r.RecordFormat = 0
			case "artifact-failure":
				r.Reason = "must roll back with attempt update"
				r.Attempts[1].End.Result.Outputs["missing"] = runner.Value{ArtifactID: "missing/report"}
			case "too-many":
				r = normalizedRun(129)
			}
			err := s.Save(t.Context(), r, nil, nil)
			if err == nil {
				t.Fatal("invalid save accepted")
			}
			if strings.HasPrefix(fault, "oversize") || fault == "too-many" {
				if !errors.Is(err, runner.ErrCapacity) {
					t.Fatal("capacity failure lost classification", err)
				}
			}
			loaded, err := s.Load(t.Context())
			if err != nil || len(loaded) != 1 || !reflect.DeepEqual(loaded[0], original) {
				t.Fatal("failed save changed retained state/history", err)
			}
		})
	}
}

func TestNormalizedAttemptLoadRejectsIncompleteOrChangedRows(t *testing.T) {
	for _, fault := range []string{"missing", "gap", "identity", "oversize", "count", "inline", "extra", "definition"} {
		t.Run(fault, func(t *testing.T) {
			r := normalizedRun(2)
			s := openNormalized(t, r)
			var err error
			switch fault {
			case "missing":
				_, err = s.db.Exec("DELETE FROM attempts WHERE run_id=? AND position=1", r.ID)
			case "gap":
				_, err = s.db.Exec("UPDATE attempts SET position=3 WHERE run_id=? AND position=1", r.ID)
			case "identity":
				_, err = s.db.Exec("UPDATE attempts SET data=? WHERE run_id=? AND position=1", []byte(`{"id":"other"}`), r.ID)
			case "oversize":
				_, err = s.db.Exec("UPDATE attempts SET data=? WHERE run_id=? AND position=1", []byte(strings.Repeat("x", 40<<10+1)), r.ID)
			case "count", "inline":
				state, _, _ := runner.MarshalRunRecord(r)
				var fields map[string]json.RawMessage
				json.Unmarshal(state, &fields)
				if fault == "count" {
					fields["attempt_count"] = json.RawMessage(`129`)
				} else {
					fields["attempts"] = json.RawMessage(`[]`)
				}
				state, _ = json.Marshal(fields)
				_, err = s.db.Exec("UPDATE runs SET data=? WHERE id=?", state, r.ID)
			case "extra":
				_, err = s.db.Exec("INSERT INTO attempts(run_id,position,id,data) VALUES(?,?,?,?)", r.ID, 2, "extra", []byte(`{"id":"extra"}`))
			case "definition":
				_, err = s.db.Exec("UPDATE runs SET definition=NULL WHERE id=?", r.ID)
			}
			if err != nil {
				t.Fatal("fault fixture failed", err)
			}
			if _, err = s.Load(t.Context()); err == nil {
				t.Fatal("changed attempt history accepted")
			}
		})
	}
}

func TestNormalizedAttemptIdentityCannotMoveBetweenRuns(t *testing.T) {
	original := normalizedRun(1)
	s := openNormalized(t, original)
	other := runner.Clone(original)
	other.ID = "other"
	if err := s.Save(t.Context(), other, nil, nil); err == nil {
		t.Fatal("an existing attempt identity was assigned to another run")
	}
	loaded, err := s.Load(t.Context())
	if err != nil || len(loaded) != 1 || !reflect.DeepEqual(loaded[0], original) {
		t.Fatal("identity conflict partially saved another run", err)
	}
}

func TestTaskGroupArtifactReferencesRetainProducerHistory(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state"), normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	producer := normalizedRun(1)
	patch := producer.Attempts[0].Source.PatchID
	if err = s.Save(t.Context(), producer, []runner.Artifact{{ID: patch, RunID: producer.ID, ProjectID: producer.ProjectID, Bytes: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	consumer := normalizedRun(0)
	consumer.ID = "consumer"
	consumer.TaskGroups = map[string]*runner.TaskGroupState{"cards": {StepID: "cards", RootPatchID: patch, Children: []runner.TaskChild{{RunID: producer.ID, ItemID: "one", Candidate: &runner.TaskCandidate{PatchID: patch}}}}}
	if err = s.Save(t.Context(), consumer, nil, nil); err != nil {
		t.Fatal(err)
	}
	if used, err := s.HasConsumers(t.Context(), producer.ID); err != nil || !used {
		t.Fatal("retained task source did not pin its producer", err, used)
	}
	if err = s.Delete(t.Context(), producer.ID); err == nil {
		t.Fatal("producer was deleted beneath retained task source")
	}
	if err = s.Delete(t.Context(), consumer.ID); err != nil {
		t.Fatal(err)
	}
	if used, err := s.HasConsumers(t.Context(), producer.ID); err != nil || used {
		t.Fatal("explicit consumer deletion did not release its pin", err, used)
	}
}
