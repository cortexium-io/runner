package sqlite

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func storedTaskContinuation(t *testing.T) (*Store, runner.Run, runner.Run, []runner.Run, *runner.Receipt) {
	t.Helper()
	original := normalizedRun(33)
	original.ID, original.Status, original.Revision, original.ResourcesReleased = "source-root", "Failed", 17, true
	original.Repository, original.Source = "/owned/repository", &runner.SourceSettings{Ref: strings.Repeat("a", 40)}
	original.Process = runner.Process{
		ID: "deliver-planned-frontend", MaxAttempts: 64,
		Steps: []runner.Step{{ID: "owner"}, {ID: "check"}, {ID: "review"}},
	}
	original.Step, original.Visit = 2, 2
	for i := range original.Attempts {
		original.Attempts[i].Superseded = true
	}
	source := &original.Attempts[30]
	source.Step, source.Visit, source.Superseded = 0, 0, false
	source.End.Result.Outputs = map[string]runner.Value{"patch": {ArtifactID: source.Source.PatchID}}
	check := &original.Attempts[31]
	check.Step, check.Visit, check.Superseded = 1, 1, false
	check.Source.InputPatchID, check.Source.PatchID = source.Source.PatchID, ""
	check.End.Result.Outputs = map[string]runner.Value{"artifact": {ArtifactID: check.ID + "/result"}}
	review := &original.Attempts[32]
	review.Step, review.Visit, review.Superseded, review.Source = 2, 2, false, nil
	review.End.Result.Outcome = "blocked"
	original.FrontendCheckRecovery = &runner.FrontendCheckRecovery{
		RequestID: "serial-approved", Serial: true, CheckStep: "check", CheckVisit: 1,
		PatchID: source.Source.PatchID, PatchSHA256: strings.Repeat("a", 64), HelperSHA256: strings.Repeat("c", 64),
	}
	child := normalizedRun(3)
	child.ID, child.Revision, child.ResourcesReleased = "source-child", 5, true
	child.Repository, child.Source = original.Repository, runner.Clone(original.Source)
	child.Task = &runner.TaskRunLink{Identity: runner.TaskIdentity{RootRunID: original.ID, GroupStep: "cards", ItemID: "board"}}
	for i := range child.Attempts {
		child.Attempts[i].ID = "child_" + child.Attempts[i].ID
	}
	original.TaskGroups = map[string]*runner.TaskGroupState{"cards": {
		StepID: "cards", Repository: original.Repository, Base: original.Source.Ref, Done: true,
		Items: []runner.TaskItem{{ID: "board"}}, Children: []runner.TaskChild{{ItemID: "board", RunID: child.ID}},
	}}
	s, err := Open(filepath.Join(t.TempDir(), "state"), normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	artifacts := []runner.Artifact{
		{ID: source.Source.PatchID, RunID: original.ID, ProjectID: original.ProjectID, Bytes: 30},
		{ID: check.ID + "/result", RunID: original.ID, ProjectID: original.ProjectID, Bytes: 40},
	}
	// Register the retained canonical outputs before linking a new consumer.
	if err := s.Save(t.Context(), original, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), child, nil, nil); err != nil {
		t.Fatal(err)
	}
	lineage := runner.TaskContinuationLineage{
		OriginRunID: original.ID, OriginProjectID: original.ProjectID, OriginRevision: original.Revision,
		Source: *source.Source, PatchSHA256: original.FrontendCheckRecovery.PatchSHA256,
		CheckAttemptID: check.ID, CheckID: check.ID + "/result", CheckSHA256: strings.Repeat("b", 64),
		SerialGrantRequestID: original.FrontendCheckRecovery.RequestID, HelperSHA256: original.FrontendCheckRecovery.HelperSHA256,
		PriorAttempts: 36, MaxAttempts: 64,
	}
	lineage.Source.WorkDir = ""
	target := runner.Run{
		ID: "continued-root", ProjectID: "preview", RecordFormat: 9, Revision: 1, Status: "Queued",
		Repository: "/owned/preview", Created: time.Now().UTC(),
		Inputs: map[string]runner.Value{"patch": {ArtifactID: lineage.Source.PatchID}, "checks": {ArtifactID: lineage.CheckID}},
		Process: runner.Process{
			ID: "continue-preview", MaxAttempts: 64, Inputs: map[string]string{"patch": "artifact", "checks": "artifact"},
			Tasks: &runner.TaskPolicy{Roles: []string{"lead"}, MaxTasks: 4, MaxDepth: 2, MaxRevisions: 1, MaxConcurrent: 2},
		},
		TaskGraph: &runner.TaskGraphState{RootRunID: "continued-root", TaskID: "root", Lineage: &lineage},
	}
	goal, _ := json.Marshal(target.Inputs)
	target.TaskGraph.Brief = string(goal)
	request := runner.ContinueTaskRequest{
		RequestID: "owner-continuation", RunID: original.ID, Revision: original.Revision + child.Revision,
		Project: target.ProjectID, Process: target.Process.ID, SourceAttemptID: source.ID, PatchSHA256: lineage.PatchSHA256,
		CheckAttemptID: check.ID, CheckSHA256: lineage.CheckSHA256, Inputs: runner.Clone(target.Inputs),
	}
	args, _ := json.Marshal(request)
	continued := runner.Clone(original)
	continued.Revision++
	continued.Updated = time.Now().UTC()
	continued.ContinuedBy = &runner.TaskContinuationAudit{RequestID: request.RequestID, RunID: target.ID, GrantedAt: continued.Updated, Lineage: lineage}
	return s, continued, target, []runner.Run{original, child}, &runner.Receipt{Key: request.RequestID, Kind: "continue_task", Args: args, RunID: target.ID}
}

func TestTaskContinuationTransactionPreservesBytesAndFreezesOldFamily(t *testing.T) {
	s, origin, target, family, receipt := storedTaskContinuation(t)
	// Keep one unrelated row and both old normalized documents unchanged.
	unrelated := normalizedRun(1)
	unrelated.ID, unrelated.Attempts[0].ID = "unrelated", "attempt_unrelated"
	if err := s.Save(t.Context(), unrelated, nil, nil); err != nil {
		t.Fatal(err)
	}
	query := "SELECT runs.definition,attempts.data FROM runs JOIN attempts ON attempts.run_id=runs.id WHERE runs.id=? AND attempts.position=0"
	var beforeDefinition, beforeAttempt []byte
	if err := s.db.QueryRow(query, origin.ID).Scan(&beforeDefinition, &beforeAttempt); err != nil {
		t.Fatal(err)
	}
	var unrelatedState []byte
	if err := s.db.QueryRow("SELECT data FROM runs WHERE id=?", unrelated.ID).Scan(&unrelatedState); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), origin, nil, nil); err == nil {
		t.Fatal("ordinary Save introduced an unaudited continuation")
	}
	if err := s.Save(t.Context(), target, nil, receipt); err == nil {
		t.Fatal("ordinary Save introduced an independent continuation debit")
	}
	broken := runner.Clone(target)
	broken.Reason = strings.Repeat("overflow", int(s.policy.Database.MaxRecord))
	if err := s.SaveTaskContinuation(t.Context(), origin, broken, family, receipt); err == nil {
		t.Fatal("oversized new root bypassed rollback")
	}
	loaded, err := s.Load(t.Context())
	if err != nil || len(loaded) != 3 {
		t.Fatal("failed transaction committed a partial root", err)
	}
	for _, r := range loaded {
		if r.ID == origin.ID && r.ContinuedBy != nil {
			t.Fatal("failed transaction froze origin without its replacement")
		}
	}
	if _, err := s.FindReceipt(t.Context(), receipt.Key); err == nil {
		t.Fatal("failed transaction retained a receipt")
	}
	if err = s.SaveTaskContinuation(t.Context(), origin, target, family, receipt); err != nil {
		t.Fatal(err)
	}
	var afterDefinition, afterAttempt, afterUnrelated []byte
	if err := s.db.QueryRow(query, origin.ID).Scan(&afterDefinition, &afterAttempt); err != nil || string(beforeDefinition) != string(afterDefinition) || string(beforeAttempt) != string(afterAttempt) {
		t.Fatal("continuation rewrote old definition/attempt bytes", err)
	}
	if err := s.db.QueryRow("SELECT data FROM runs WHERE id=?", unrelated.ID).Scan(&afterUnrelated); err != nil || string(unrelatedState) != string(afterUnrelated) {
		t.Fatal("continuation rewrote unrelated history", err)
	}
	for _, r := range []runner.Run{origin, family[1]} {
		if err := s.Save(t.Context(), r, nil, nil); err == nil {
			t.Fatal("ordinary Save mutated frozen old family", r.ID)
		}
	}
	child := runner.Clone(family[1])
	child.Task = nil
	if err := s.Save(t.Context(), child, nil, nil); err == nil {
		t.Fatal("discarded child ownership bypassed continuation freeze")
	}
	target.Status = "Failed"
	if err := s.Save(t.Context(), target, nil, nil); err != nil {
		t.Fatal(err)
	}
	var uses int
	if err := s.db.QueryRow("SELECT count(*) FROM artifact_uses WHERE run_id=?", target.ID).Scan(&uses); err != nil || uses != 2 {
		t.Fatal("ordinary future saves lost selected canonical artifact uses", uses, err)
	}
	if used, err := s.HasExternalConsumers(t.Context(), []string{origin.ID, family[1].ID}); err != nil || !used {
		t.Fatal("continued root failed to pin its canonical old evidence", err)
	}
	if used, err := s.HasExternalConsumers(t.Context(), []string{target.ID, origin.ID, family[1].ID}); err != nil || used {
		t.Fatal("combined continuation family cannot expire normally", err)
	}
	for _, kind := range []string{"debit", "cap", "remove"} {
		bad := runner.Clone(target)
		switch kind {
		case "debit":
			bad.TaskGraph.Lineage.PriorAttempts--
		case "cap":
			bad.Process.MaxAttempts++
		case "remove":
			bad.TaskGraph.Lineage = nil
		}
		if err := s.Save(t.Context(), bad, nil, nil); err == nil {
			t.Fatal("ordinary Save changed immutable continuation allowance", kind)
		}
	}
	if err := s.SaveTaskContinuation(t.Context(), origin, target, family, receipt); err == nil {
		t.Fatal("stored continuation could repeat after its first commit")
	}
	gotReceipt, err := s.FindReceipt(t.Context(), receipt.Key)
	if err != nil || !reflect.DeepEqual(gotReceipt, *receipt) {
		t.Fatal("atomic continuation lost its original idempotency receipt", err)
	}
	for _, id := range []string{origin.ID, family[1].ID, target.ID} {
		if err := s.Delete(t.Context(), id); err == nil {
			t.Fatal("ordinary retention separated linked continuation history", id)
		}
	}
	if err := s.DeleteFamily(t.Context(), []string{target.ID, origin.ID}); err == nil {
		t.Fatal("continued retention omitted an original child")
	}
	if err := s.DeleteFamily(t.Context(), []string{target.ID, origin.ID, family[1].ID}); err != nil {
		t.Fatal("linked continuation cannot expire as one family", err)
	}
	remaining, err := s.Load(t.Context())
	if err != nil || len(remaining) != 1 || remaining[0].ID != unrelated.ID {
		t.Fatal("combined retention deleted unrelated history or left partial lineage", remaining, err)
	}
}

func TestTaskContinuationTransactionRejectsStaleFamilyAndForgedDebit(t *testing.T) {
	for _, fault := range []string{"child-revision", "child-attempt", "debit", "artifact-owner", "omitted-child", "receipt"} {
		t.Run(fault, func(t *testing.T) {
			s, origin, target, family, receipt := storedTaskContinuation(t)
			switch fault {
			case "child-revision":
				family[1].Revision++
			case "child-attempt":
				family[1].Attempts[0].Released = false
			case "debit":
				target.TaskGraph.Lineage.PriorAttempts--
				origin.ContinuedBy.Lineage = *target.TaskGraph.Lineage
			case "artifact-owner":
				if _, err := s.db.Exec("UPDATE artifacts SET project='foreign' WHERE id=?", target.TaskGraph.Lineage.CheckID); err != nil {
					t.Fatal(err)
				}
			case "omitted-child":
				family = family[:1]
			case "receipt":
				receipt.Args = []byte(`{}`)
			}
			if err := s.SaveTaskContinuation(t.Context(), origin, target, family, receipt); err == nil {
				t.Fatal("transaction accepted stale or unauthenticated authority", fault)
			}
			var count int
			if err := s.db.QueryRow("SELECT count(*) FROM runs WHERE id=?", target.ID).Scan(&count); err != nil || count != 0 {
				t.Fatal("rejected transfer left a new run", count, err)
			}
		})
	}
}

func TestTaskContinuationSchemaBoundaryPreservesOlderStoredRows(t *testing.T) {
	r := normalizedRun(1)
	s := openNormalized(t, r)
	var before []byte
	if err := s.db.QueryRow("SELECT data FROM runs WHERE id=?", r.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 12 {
		t.Fatal("older binaries can ignore continuation debit/freeze", version, err)
	}
	var after []byte
	if err := s.db.QueryRow("SELECT data FROM runs WHERE id=?", r.ID).Scan(&after); err != nil || fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatal("schema marker changed legacy state", err)
	}
}
