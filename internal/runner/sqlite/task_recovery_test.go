package sqlite

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func storedTaskRecovery(t *testing.T) (*Store, []runner.Run, []runner.Run, *runner.Receipt) {
	t.Helper()
	s, origin, root, oldFamily, continuation := storedTaskContinuation(t)
	root.Process.Tasks.Roles = []string{"owner"}
	root.Process.Steps = []runner.Step{{ID: "work", Role: "owner"}}
	root.Roles = map[string]runner.Role{"owner": {ID: "owner", Inputs: map[string]string{}, Outputs: map[string]string{"report": "text"}, Execution: &runner.Profile{Harness: "deterministic"}}}
	root.TaskGraph.Role = "owner"
	if err := s.SaveTaskContinuation(t.Context(), origin, root, oldFamily, continuation); err != nil {
		t.Fatal(err)
	}
	root.Status, root.ResourcesReleased, root.Revision = "Failed", true, 4
	root.Outputs = map[string]runner.Value{"report": runner.Text("failed parent assessment")}
	root.Attempts = []runner.Attempt{{ID: "recovery-parent-attempt", State: runner.Finished, Released: true,
		End: &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: "rejected", Outputs: runner.Clone(root.Outputs)}}}}
	family := []runner.Run{root}
	for i, id := range []string{"import", "baseline", "accepted"} {
		child := runner.Run{
			ID: string(rune('a'+i)) + "-" + id, ProjectID: root.ProjectID, Repository: root.Repository,
			RecordFormat: 9, Revision: 1, Status: "Failed", ResourcesReleased: true,
			Process: runner.Clone(root.Process), Roles: runner.Clone(root.Roles),
			TaskGraph: &runner.TaskGraphState{RootRunID: root.ID, ParentRunID: root.ID, TaskID: id, Role: "owner"},
		}
		spec := runner.RoleTask{ID: id, Role: "owner", Brief: id}
		switch id {
		case "import":
			for j := range 4 {
				child.Attempts = append(child.Attempts, runner.Attempt{ID: "recovery-import-" + string(rune('a'+j)), State: runner.Finished, Released: true,
					End: &runner.ExecutionEnd{Kind: "failed", Reason: "fixed helper maintenance failure"}})
			}
		case "baseline":
			spec.DependsOn = []string{"import"}
			child.TaskGraph.DependsOn = []string{family[1].ID}
			child.Reason = "declared task dependency did not complete: import"
		case "accepted":
			child.Status = "Completed"
			child.Outputs = map[string]runner.Value{"report": runner.Text("accepted independent result")}
			child.Attempts = []runner.Attempt{{ID: "recovery-accepted", State: runner.Finished, Released: true,
				End: &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: "ready", Outputs: runner.Clone(child.Outputs)}}}}
		}
		root.TaskGraph.Children = append(root.TaskGraph.Children, runner.RoleTaskLink{Spec: spec, RunID: child.ID})
		root.TaskGraph.Results = append(root.TaskGraph.Results, runner.TaskResultRef{ID: id, RunID: child.ID})
		family = append(family, child)
		if err := s.Save(t.Context(), child, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	family[0] = root
	if err := s.Save(t.Context(), root, nil, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	after := []runner.Run{runner.Clone(family[1]), runner.Clone(family[2]), runner.Clone(root)}
	ids := []string{after[0].ID, after[1].ID, root.ID}
	sort.Strings(ids)
	for i := range after {
		after[i].Revision++
		after[i].Updated, after[i].ResourcesReleased, after[i].Outputs = now, false, nil
		if after[i].ID == root.ID {
			after[i].Status, after[i].Reason = "Waiting", "waiting for maintenance-recovered role tasks"
			after[i].TaskGraph.Phase = runner.TaskWaitingChildren
			after[i].TaskGraph.Results = []runner.TaskResultRef{{ID: "accepted", RunID: family[3].ID}}
			after[i].TaskGraph.Recoveries = []runner.TaskRecoveryAudit{{RequestID: "recover-generation", OwnerID: root.ID, TaskIDs: []string{"import"}, Rearmed: ids, Spent: 42, Reason: "fixed runtime repaired", GrantedAt: now}}
		} else {
			after[i].Status, after[i].Reason = "Queued", "owner-authorized maintenance recovery of the same task generation"
			if after[i].ID == family[1].ID {
				after[i].Visit++
				after[i].TaskGraph.Results = []runner.TaskResultRef{}
			}
		}
	}
	req := runner.RecoverTasksRequest{RequestID: "recover-generation", RunID: root.ID, Revision: root.Revision + 3, TaskIDs: []string{"import"}, Reason: "fixed runtime repaired"}
	args, _ := json.Marshal(req)
	return s, runner.Clone(family), after, &runner.Receipt{Key: req.RequestID, Kind: "recover_tasks", Args: args, RunID: root.ID}
}

func TestTaskRecoveryAtomicRollbackRestartAndImmutableEvidence(t *testing.T) {
	s, before, after, receipt := storedTaskRecovery(t)
	var oldAccepted, oldImport, dbPath string
	if err := s.db.QueryRow("SELECT data FROM runs WHERE id=?", before[3].ID).Scan(&oldAccepted); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT data FROM attempts WHERE run_id=? AND position=0", before[1].ID).Scan(&oldImport); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER fail_task_recovery BEFORE UPDATE ON runs WHEN NEW.id='continued-root' BEGIN SELECT RAISE(ABORT,'injected recovery persistence failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTaskRecovery(t.Context(), before, after, receipt); err == nil {
		t.Fatal("injected later row failure did not abort the atomic operation")
	}
	loaded, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range loaded {
		for _, old := range before {
			if r.ID == old.ID && !reflect.DeepEqual(r, old) {
				t.Fatal("partial recovery survived rollback", r.ID)
			}
		}
	}
	if _, err := s.FindReceipt(t.Context(), receipt.Key); err == nil {
		t.Fatal("rolled-back recovery retained its receipt")
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_task_recovery"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTaskRecovery(t.Context(), before, after, receipt); err != nil {
		t.Fatal(err)
	}
	var accepted, imported string
	if err := s.db.QueryRow("SELECT data FROM runs WHERE id=?", before[3].ID).Scan(&accepted); err != nil || accepted != oldAccepted {
		t.Fatal("accepted state changed during recovery", err)
	}
	if err := s.db.QueryRow("SELECT data FROM attempts WHERE run_id=? AND position=0", before[1].ID).Scan(&imported); err != nil || imported != oldImport {
		t.Fatal("old failed execution evidence changed", err)
	}
	if err := s.SaveTaskRecovery(t.Context(), before, after, receipt); err == nil {
		t.Fatal("stale generation committed again")
	}
	root := runner.Clone(after[2])
	root.TaskGraph.Recoveries[0].Spent = 0
	if err := s.Save(t.Context(), root, nil, nil); err == nil {
		t.Fatal("ordinary save discarded recovery accounting audit")
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
	if _, err = reopened.Load(t.Context()); err != nil {
		t.Fatal("recovery could not reopen after commit", err)
	}
	if got, err := reopened.FindReceipt(t.Context(), receipt.Key); err != nil || !reflect.DeepEqual(got, *receipt) {
		t.Fatal("restart lost exact recovery receipt", err)
	}
}
