package sqlite

import (
	"os"
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestCorrectionHistoryAndBothContextsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	r := runner.Run{ID: "run_corrections", ProjectID: "demo", Status: "Failed", CorrectionRecovery: &runner.CorrectionRecovery{
		RequestID: "second", CurrentRoute: true, MaxUses: 5, PreviousUses: 4,
		Context:  &runner.CorrectionContext{ID: "second-context", Bytes: 2, SHA256: "second-sha", Roles: []string{"owner"}},
		Previous: &runner.CorrectionRecovery{RequestID: "first", CurrentRoute: true, MaxUses: 4, PreviousUses: 3, Context: &runner.CorrectionContext{ID: "first-context", Bytes: 2, SHA256: "first-sha", Roles: []string{"owner"}}},
	}}
	artifacts := []runner.Artifact{{ID: "first-context", RunID: r.ID, ProjectID: r.ProjectID, Bytes: 2}, {ID: "second-context", RunID: r.ID, ProjectID: r.ProjectID, Bytes: 2}}
	if err := s.Save(t.Context(), r, artifacts, nil); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.Load(t.Context())
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0].CorrectionRecovery, r.CorrectionRecovery) {
		t.Fatal("restart lost prior correction grant", err)
	}
	if err := s.Save(t.Context(), rows[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, a := range artifacts {
		var uses int
		if err := s.db.QueryRow("SELECT count(*) FROM artifact_uses WHERE artifact=? AND run_id=?", a.ID, r.ID).Scan(&uses); err != nil || uses != 1 {
			t.Fatal("ordinary save orphaned correction context", a.ID, uses, err)
		}
	}
}
