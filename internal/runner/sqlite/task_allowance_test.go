package sqlite

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func storedTaskAllowance(t *testing.T, roles ...runner.Role) (*Store, runner.Run, runner.Run, *runner.Receipt) {
	t.Helper()
	s, origin, root, family, continuation := storedTaskContinuation(t)
	root.Process.MaxAttemptsPerStep = 4
	if len(roles) != 0 {
		root.Roles = map[string]runner.Role{}
		for _, role := range roles {
			root.Roles[role.ID] = role
		}
	}
	if err := s.SaveTaskContinuation(t.Context(), origin, root, family, continuation); err != nil {
		t.Fatal(err)
	}
	root.Status, root.ResourcesReleased, root.Revision = "Failed", true, 8
	root.VisitExtraAttempts = 1
	root.TaskGraph.Directive = "Previous owner guidance"
	root.Attempts = normalizedRun(27).Attempts
	for i := range root.Attempts {
		root.Attempts[i].ID = "continued_" + root.Attempts[i].ID
	}
	if err := s.Save(t.Context(), root, nil, nil); err != nil {
		t.Fatal(err)
	}
	req := runner.RetryRequest{
		RequestID: "owner-grant-eight", RunID: root.ID, ExtraAttempts: 8,
		Reason: "Owner approved the remaining repair and review cycle", Directive: "Complete the retained goal with the repaired verifier",
	}
	args, _ := json.Marshal(req)
	after := runner.Clone(root)
	after.Status, after.Reason, after.ResourcesReleased = "Queued", "authorized task retry", false
	after.Process.MaxAttempts = 72
	after.TaskGraph.Directive = req.Directive
	after.Revision++
	after.Updated = time.Now().UTC()
	after.TaskGraph.AllowanceGrants = []runner.TaskAllowanceGrant{{
		RequestID: req.RequestID, PreviousLimit: 64, Limit: 72, Spent: 63,
		Reason: req.Reason, GrantedAt: after.Updated,
	}}
	return s, root, after, &runner.Receipt{Key: req.RequestID, Kind: "retry", Args: args, RunID: root.ID}
}

func allowanceAttemptBytes(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.Query("SELECT run_id,position,data FROM attempts ORDER BY run_id,position")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var all string
	for rows.Next() {
		var id, data string
		var position int
		if err := rows.Scan(&id, &position, &data); err != nil {
			t.Fatal(err)
		}
		all += fmt.Sprintf("%s/%d/%s\n", id, position, data)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return all
}

func TestTaskAllowanceAtomicRollbackRestartAndRetainedHistory(t *testing.T) {
	s, before, after, receipt := storedTaskAllowance(t)
	history := allowanceAttemptBytes(t, s)
	var originState, originDefinition, dbPath string
	if err := s.db.QueryRow("SELECT data,definition FROM runs WHERE id=?", before.TaskGraph.Lineage.OriginRunID).Scan(&originState, &originDefinition); err != nil {
		t.Fatal(err)
	}
	loadedBefore, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER fail_allowance_receipt BEFORE INSERT ON requests WHEN NEW.key='owner-grant-eight' BEGIN SELECT RAISE(ABORT,'injected owner receipt failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), after, nil, receipt); err == nil {
		t.Fatal("receipt failure did not roll back the allowance and definition")
	}
	loaded, err := s.Load(t.Context())
	if err != nil || !reflect.DeepEqual(loaded, loadedBefore) || history != allowanceAttemptBytes(t, s) {
		t.Fatal("failed grant changed retained runs or attempt bytes", err)
	}
	if _, err := s.FindReceipt(t.Context(), receipt.Key); err == nil {
		t.Fatal("failed grant retained an idempotency receipt")
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_allowance_receipt"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), after, nil, receipt); err != nil {
		t.Fatal(err)
	}
	if history != allowanceAttemptBytes(t, s) {
		t.Fatal("grant rewrote retained attempt evidence")
	}
	var state, definition string
	if err := s.db.QueryRow("SELECT data,definition FROM runs WHERE id=?", before.TaskGraph.Lineage.OriginRunID).Scan(&state, &definition); err != nil || state != originState || definition != originDefinition {
		t.Fatal("grant changed original frozen family authority", err)
	}
	if err := s.Save(t.Context(), after, nil, receipt); err == nil {
		t.Fatal("same receipt committed the grant again")
	}
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(filepath.Dir(dbPath), normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err = reopened.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range loaded {
		if r.ID == after.ID {
			found = true
			if !reflect.DeepEqual(r, after) || r.Process.MaxAttempts != 72 || r.TaskGraph.Lineage.MaxAttempts != 64 || r.TaskGraph.Lineage.PriorAttempts != 36 || len(r.Attempts) != 27 || r.VisitExtraAttempts != before.VisitExtraAttempts {
				t.Fatal("restart changed the effective ceiling or historical debit")
			}
		}
		if r.ContinuedBy != nil {
			if err := reopened.Save(t.Context(), r, nil, nil); err == nil {
				t.Fatal("owner grant unfroze source-family mutation")
			}
		}
	}
	if got, err := reopened.FindReceipt(t.Context(), receipt.Key); err != nil || !reflect.DeepEqual(got, *receipt) || !found {
		t.Fatal("restart lost the exact owner receipt or amended root", err)
	}
	if err := reopened.Save(t.Context(), after, nil, nil); err != nil {
		t.Fatal("ordinary post-restart save lost audited capacity", err)
	}
}

func TestTaskAllowanceRejectsForgedAuthorityAndHistory(t *testing.T) {
	for _, fault := range []string{"no-receipt", "wrong-receipt", "different-reason", "different-directive", "private-debit", "history", "roles", "inputs", "lineage", "visit-limit", "visit-extra", "child-grant", "unaudited-limit", "fresh-root"} {
		t.Run(fault, func(t *testing.T) {
			s, before, after, receipt := storedTaskAllowance(t)
			switch fault {
			case "no-receipt":
				receipt = nil
			case "wrong-receipt":
				receipt.Key = "another-owner-request"
			case "different-reason":
				after.TaskGraph.AllowanceGrants[0].Reason = "different authority"
			case "different-directive":
				after.TaskGraph.Directive = "unapproved direction"
			case "private-debit":
				after.TaskGraph.AllowanceGrants[0].Spent--
			case "history":
				after.Attempts[0].End.Reason = "silently changed the old result"
			case "roles":
				after.Roles = map[string]runner.Role{"new": {ID: "new"}}
			case "inputs":
				after.Inputs["request"] = runner.Text("new goal")
			case "lineage":
				after.TaskGraph.Lineage.PriorAttempts--
			case "visit-limit":
				after.Process.MaxAttemptsPerStep++
			case "visit-extra":
				after.VisitExtraAttempts++
			case "child-grant":
				after.TaskGraph.ParentRunID = "different-root"
			case "unaudited-limit":
				after.TaskGraph.AllowanceGrants = nil
			case "fresh-root":
				after.ID, after.TaskGraph.RootRunID = "unlinked-new-root", "unlinked-new-root"
				after.TaskGraph.Lineage = nil
				receipt.RunID = after.ID
			}
			if err := s.Save(t.Context(), after, nil, receipt); err == nil {
				t.Fatal("forged allowance or work alteration crossed the store boundary")
			}
			loaded, err := s.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range loaded {
				if r.ID == before.ID && !reflect.DeepEqual(r, before) {
					t.Fatal("rejected allowance mutated retained root")
				}
			}
		})
	}
	s, _, after, receipt := storedTaskAllowance(t)
	if err := s.Save(t.Context(), after, nil, receipt); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"rewrite", "discard", "lower-limit"} {
		bad := runner.Clone(after)
		switch fault {
		case "rewrite":
			bad.TaskGraph.AllowanceGrants[0].Spent--
		case "discard":
			bad.TaskGraph.AllowanceGrants = nil
		case "lower-limit":
			bad.Process.MaxAttempts = 64
		}
		if err := s.Save(t.Context(), bad, nil, nil); err == nil {
			t.Fatal("ordinary save rewrote or discarded owner authority", fault)
		}
	}
}
