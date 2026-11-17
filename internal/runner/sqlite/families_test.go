package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func familyFixture(t *testing.T) (*Store, []string) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state"), normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	family := []string{"root", "child", "leaf"}
	for _, id := range append(append([]string{}, family...), "outside") {
		r := normalizedRun(1)
		r.ID = id
		r.Attempts[0].ID = id + "-attempt"
		r.Attempts[0].Source = nil
		r.Outputs = map[string]runner.Value{"owned": {ArtifactID: "artifact_" + id}}
		artifact := runner.Artifact{ID: "artifact_" + id, RunID: id, ProjectID: r.ProjectID, Bytes: 1}
		receipt := runner.Receipt{Key: id + "-request", Kind: "start", Args: []byte(`{}`), RunID: id}
		if err = s.Save(t.Context(), r, []runner.Artifact{artifact}, &receipt); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		switch r.ID {
		case "root":
			r.Inputs = map[string]runner.Value{"child": {ArtifactID: "artifact_child"}, "outside": {ArtifactID: "artifact_outside"}}
		case "child":
			r.Inputs = map[string]runner.Value{"root": {ArtifactID: "artifact_root"}, "leaf": {ArtifactID: "artifact_leaf"}}
		case "leaf":
			r.Inputs = map[string]runner.Value{"root": {ArtifactID: "artifact_root"}}
		}
		if err = s.Save(t.Context(), r, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return s, family
}

func familyRows(t *testing.T, s *Store) map[string][][]any {
	t.Helper()
	out := map[string][][]any{}
	for _, query := range []string{
		"SELECT id,project,data,definition FROM runs ORDER BY id",
		"SELECT key,kind,args,run_id FROM requests ORDER BY key",
		"SELECT id,run_id,project,bytes FROM artifacts ORDER BY id",
		"SELECT artifact,run_id FROM artifact_uses ORDER BY artifact,run_id",
		"SELECT run_id,position,id,data FROM attempts ORDER BY run_id,position",
		"SELECT parent_run_id,position,child_run_id,data,result_position FROM role_task_links ORDER BY parent_run_id,position",
	} {
		out[query] = nil
		rows, err := s.db.QueryContext(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for i, value := range values {
				if data, ok := value.([]byte); ok {
					values[i] = append([]byte(nil), data...)
				}
			}
			out[query] = append(out[query], values)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestDeleteFamilyAllowsInternalArtifactCyclesAndPreservesOutsideRows(t *testing.T) {
	s, ids := familyFixture(t)
	// Exercise real parent-owned relation cascades as well as artifacts and
	// attempts; the unrelated legacy outside run owns no role-task relation.
	family, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range family {
		if r.ID == "outside" {
			continue
		}
		r.RecordFormat = 9
		r.TaskGraph = &runner.TaskGraphState{RootRunID: "root", TaskID: r.ID, Brief: "Retain exact family evidence"}
		switch r.ID {
		case "root":
			r.TaskGraph.Children = []runner.RoleTaskLink{{Spec: runner.RoleTask{ID: "child", Role: "owner", Brief: "Retain child evidence"}, RunID: "child"}}
		case "child":
			r.TaskGraph.ParentRunID = "root"
			r.TaskGraph.Role = "owner"
			r.TaskGraph.Children = []runner.RoleTaskLink{{Spec: runner.RoleTask{ID: "leaf", Role: "owner", Brief: "Retain leaf evidence"}, RunID: "leaf"}}
		case "leaf":
			r.TaskGraph.ParentRunID = "child"
			r.TaskGraph.Role = "owner"
		}
		if err := s.Save(t.Context(), r, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	var relations int
	if err := s.db.QueryRow("SELECT count(*) FROM role_task_links").Scan(&relations); err != nil || relations != 2 {
		t.Fatal("fixture lacks family-owned relationships", relations, err)
	}
	if used, err := s.HasConsumers(t.Context(), "root"); err != nil || !used {
		t.Fatal("fixture lacks an individual-run retention cycle", used, err)
	}
	if used, err := s.HasExternalConsumers(t.Context(), ids); err != nil || used {
		t.Fatal("family-internal consumer counted as external", used, err)
	}
	before := familyRows(t, s)
	// IDs remain bound parameters even when they contain SQL metacharacters.
	if err := s.DeleteFamily(t.Context(), []string{"missing' OR 1=1--"}); err != nil || !reflect.DeepEqual(before, familyRows(t, s)) {
		t.Fatal("family selection touched rows outside its explicit IDs", err)
	}
	if err := s.Delete(t.Context(), "root"); err == nil || !reflect.DeepEqual(before, familyRows(t, s)) {
		t.Fatal("individual deletion compatibility or rollback changed", err)
	}
	if err := s.DeleteFamily(t.Context(), ids); err != nil {
		t.Fatal("internal artifact cycle prevented atomic retention", err)
	}
	if err := s.DeleteFamily(t.Context(), ids); err != nil {
		t.Fatal("lost acknowledgement could not rejoin family deletion", err)
	}
	runs, err := s.Load(t.Context())
	if err != nil || len(runs) != 1 || runs[0].ID != "outside" || len(runs[0].Attempts) != 1 {
		t.Fatal("outside history was deleted or family history survived", err, len(runs))
	}
	for _, id := range ids {
		if _, err = s.Artifact(t.Context(), "artifact_"+id); !errors.Is(err, runner.ErrNotFound) {
			t.Fatal("family artifact survived", id, err)
		}
		if _, err = s.FindReceipt(t.Context(), id+"-request"); !errors.Is(err, runner.ErrNotFound) {
			t.Fatal("family request survived", id, err)
		}
	}
	after := familyRows(t, s)
	for query, rows := range after {
		ownerColumn := 0
		switch {
		case strings.Contains(query, "FROM requests"):
			ownerColumn = 3
		case strings.Contains(query, "FROM artifacts"), strings.Contains(query, "FROM artifact_uses"):
			ownerColumn = 1
		}
		var expected [][]any
		for _, original := range before[query] {
			if original[ownerColumn] == "outside" {
				expected = append(expected, original)
			}
		}
		if !reflect.DeepEqual(rows, expected) {
			t.Fatal("family-owned rows survived or exact outside rows changed", query)
		}
	}
}

func TestDeleteFamilyBlocksExternalConsumersAndPartialMembership(t *testing.T) {
	s, ids := familyFixture(t)
	if used, err := s.HasExternalConsumers(t.Context(), ids); err != nil || used {
		t.Fatal("initial family could not be collected", used, err)
	}
	if used, err := s.HasExternalConsumers(t.Context(), []string{"root", "child"}); err != nil || !used {
		t.Fatal("omitted child consumer was not external to selected family", used, err)
	}
	runs, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.ID == "outside" {
			r.Inputs = map[string]runner.Value{"external": {ArtifactID: "artifact_child"}}
			if err = s.Save(t.Context(), r, nil, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := familyRows(t, s)
	if used, err := s.HasExternalConsumers(t.Context(), ids); err != nil || !used {
		t.Fatal("external retained artifact consumer was missed", used, err)
	}
	if err = s.DeleteFamily(t.Context(), ids); err == nil || !reflect.DeepEqual(before, familyRows(t, s)) {
		t.Fatal("externally referenced family was deleted or partially mutated", err)
	}
}

func TestDeleteFamilyRollsBackErrorsAndRejectsInvalidSets(t *testing.T) {
	s, ids := familyFixture(t)
	before := familyRows(t, s)
	for _, invalid := range [][]string{nil, {}, {""}, {"root", "root"}, make([]string, s.maxRuns+1)} {
		if _, err := s.HasExternalConsumers(t.Context(), invalid); err == nil {
			t.Fatal("invalid family selection accepted")
		}
		if err := s.DeleteFamily(t.Context(), invalid); err == nil {
			t.Fatal("invalid family deletion accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.DeleteFamily(ctx, ids); err == nil || !reflect.DeepEqual(before, familyRows(t, s)) {
		t.Fatal("interrupted family deletion changed stored records", err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER block_family_retention BEFORE DELETE ON runs
WHEN OLD.id='child' BEGIN SELECT RAISE(ABORT,'synthetic retained row'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFamily(t.Context(), ids); err == nil || !reflect.DeepEqual(before, familyRows(t, s)) {
		t.Fatal("failure after artifact/use removal did not roll back all family records", err)
	}
}
