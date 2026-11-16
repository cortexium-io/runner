package sqlite

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func storedTaskFollowup(t *testing.T) (*Store, runner.Run, runner.Run, *runner.Receipt) {
	t.Helper()
	s, origin, parent, family, receipt := storedTaskContinuation(t)
	parent.Roles = map[string]runner.Role{
		"lead": {ID: "lead", Purpose: "Complete the assigned goal", Inputs: runner.Clone(parent.Process.Inputs), Outputs: map[string]string{"result": "text"}},
	}
	parent.Process.Steps = []runner.Step{{ID: "lead", Role: "lead"}}
	parent.TaskGraph.Role = "lead"
	if err := s.SaveTaskContinuation(t.Context(), origin, parent, family, receipt); err != nil {
		t.Fatal(err)
	}
	parent.Status, parent.ResourcesReleased, parent.Step = "Completed", true, 1
	parent.Revision++
	parent.Updated = time.Now().UTC()
	if err := s.Save(t.Context(), parent, nil, nil); err != nil {
		t.Fatal(err)
	}
	target := runner.Clone(parent)
	target.ID, target.Status, target.ResourcesReleased = "followup-root", "Queued", false
	target.Process.ID = "followup-preview"
	target.Step, target.Visit, target.Revision = 0, 0, 1
	target.Created, target.Updated = time.Now().UTC(), time.Now().UTC()
	target.TaskGraph = &runner.TaskGraphState{
		RootRunID: target.ID, TaskID: "root", Role: "lead", Brief: parent.TaskGraph.Brief,
	}
	target.Followup = &runner.TaskFollowupLineage{RunID: parent.ID, Revision: parent.Revision, RequestID: "owner-followup"}
	request := runner.StartRequest{
		RequestID: target.Followup.RequestID, Project: target.ProjectID, Process: target.Process.ID,
		Followup: &runner.TaskFollowupRequest{RunID: parent.ID, Revision: parent.Revision}, Inputs: runner.Clone(target.Inputs),
	}
	args, _ := json.Marshal(request)
	return s, parent, target, &runner.Receipt{Key: request.RequestID, Kind: "start", Args: args, RunID: target.ID}
}

func TestTaskFollowupTransactionPreservesParentRollbackRestartAndRetention(t *testing.T) {
	s, parent, target, receipt := storedTaskFollowup(t)
	before, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	history := allowanceAttemptBytes(t, s)
	var parentState, parentDefinition []byte
	if err := s.db.QueryRow("SELECT data,definition FROM runs WHERE id=?", parent.ID).Scan(&parentState, &parentDefinition); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), target, nil, receipt); err == nil {
		t.Fatal("ordinary save introduced unverified follow-up lineage")
	}
	if _, err := s.db.Exec("CREATE TRIGGER fail_followup_receipt BEFORE INSERT ON requests WHEN NEW.key='owner-followup' BEGIN SELECT RAISE(ABORT,'injected follow-up receipt failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTaskFollowup(t.Context(), target, []runner.Run{parent}, receipt); err == nil {
		t.Fatal("receipt failure committed a partial follow-up")
	}
	loaded, err := s.Load(t.Context())
	if err != nil || !reflect.DeepEqual(before, loaded) || history != allowanceAttemptBytes(t, s) {
		t.Fatal("failed admission changed source history", err)
	}
	if _, err := s.FindReceipt(t.Context(), receipt.Key); err == nil {
		t.Fatal("failed admission kept an idempotency receipt")
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_followup_receipt"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTaskFollowup(t.Context(), target, []runner.Run{parent}, receipt); err != nil {
		t.Fatal(err)
	}
	var gotState, gotDefinition []byte
	if err := s.db.QueryRow("SELECT data,definition FROM runs WHERE id=?", parent.ID).Scan(&gotState, &gotDefinition); err != nil || string(gotState) != string(parentState) || string(gotDefinition) != string(parentDefinition) || history != allowanceAttemptBytes(t, s) {
		t.Fatal("follow-up rewrote completed source records or attempts", err)
	}
	if used, err := s.HasConsumers(t.Context(), parent.ID); err != nil || !used {
		t.Fatal("parent without owned output bytes lost its logical follow-up consumer", used, err)
	}
	ids := make([]string, 0, len(before))
	for _, r := range before {
		ids = append(ids, r.ID)
	}
	if used, err := s.HasExternalConsumers(t.Context(), ids); err != nil || !used {
		t.Fatal("family retention ignored the completed-parent reference", used, err)
	}
	if err := s.DeleteFamily(t.Context(), ids); err == nil {
		t.Fatal("parent family expired before its linked follow-up")
	}
	if err := s.Delete(t.Context(), parent.ID); err == nil {
		t.Fatal("individual deletion detached the follow-up parent")
	}
	var file string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&file); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(filepath.Dir(file), normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err = s.Load(t.Context())
	if err != nil || len(loaded) != len(before)+1 {
		t.Fatal("restart lost a linked family", err)
	}
	for _, r := range loaded {
		if r.ID == target.ID && !reflect.DeepEqual(r, target) {
			t.Fatal("restart changed follow-up state or lineage")
		}
	}
	if err := s.DeleteFamily(t.Context(), []string{target.ID}); err != nil {
		t.Fatal("independently expired follow-up could not release its parent reference", err)
	}
	if used, err := s.HasConsumers(t.Context(), parent.ID); err != nil || used {
		t.Fatal("deleted follow-up leaked its parent reference", used, err)
	}
	if err := s.DeleteFamily(t.Context(), ids); err != nil {
		t.Fatal("released parent family could no longer expire", err)
	}
}

func TestTaskFollowupImmutableLineageAndInputs(t *testing.T) {
	s, parent, target, receipt := storedTaskFollowup(t)
	if err := s.SaveTaskFollowup(t.Context(), target, []runner.Run{parent}, receipt); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Load(t.Context())
	for _, change := range []func(*runner.Run){
		func(r *runner.Run) { r.Followup = nil },
		func(r *runner.Run) { r.Followup.Revision++ },
		func(r *runner.Run) { r.Followup.RequestID = "different" },
		func(r *runner.Run) { r.ProjectID = "different" },
		func(r *runner.Run) { r.Repository = "/different/workspace" },
		func(r *runner.Run) { r.Inputs["patch"] = r.Inputs["checks"] },
		func(r *runner.Run) { r.TaskGraph.Brief = "A goal that the owner did not submit" },
	} {
		bad := runner.Clone(target)
		change(&bad)
		if err := s.Save(t.Context(), bad, nil, nil); err == nil {
			t.Fatal("ordinary save mutated follow-up ownership or input authority")
		}
	}
	loaded, err := s.Load(t.Context())
	if err != nil || !reflect.DeepEqual(before, loaded) {
		t.Fatal("rejected lineage mutations changed persisted rows", err)
	}
}

func TestTaskFollowupTransactionRejectsParentDriftAndUnselectedImports(t *testing.T) {
	for _, fault := range []string{"stale-parent", "unsettled-cleanup", "omitted-child", "wrong-receipt", "different-workspace", "unselected-import"} {
		t.Run(fault, func(t *testing.T) {
			s, parent, target, receipt := storedTaskFollowup(t)
			expected := []runner.Run{runner.Clone(parent)}
			switch fault {
			case "stale-parent":
				expected[0].Revision++
			case "unsettled-cleanup":
				parent.ResourcesReleased = false
				if err := s.Save(t.Context(), parent, nil, nil); err != nil {
					t.Fatal(err)
				}
				expected[0] = parent
			case "omitted-child":
				orphan := runner.Clone(target)
				orphan.ID, orphan.Followup = "unreserved-child", nil
				orphan.TaskGraph.RootRunID, orphan.TaskGraph.ParentRunID = parent.ID, parent.ID
				if err := s.Save(t.Context(), orphan, nil, nil); err != nil {
					t.Fatal(err)
				}
			case "wrong-receipt":
				receipt.Args = []byte(`{"request_id":"other"}`)
			case "different-workspace":
				target.Repository = "/different/workspace"
			case "unselected-import":
				foreign := normalizedRun(0)
				foreign.ID, foreign.ProjectID = "foreign", "foreign"
				artifact := runner.Artifact{ID: "foreign/report", RunID: foreign.ID, ProjectID: foreign.ProjectID, Bytes: 4}
				if err := s.Save(t.Context(), foreign, []runner.Artifact{artifact}, nil); err != nil {
					t.Fatal(err)
				}
				target.Inputs["patch"] = runner.Value{ArtifactID: artifact.ID}
				var request runner.StartRequest
				json.Unmarshal(receipt.Args, &request)
				request.Inputs = runner.Clone(target.Inputs)
				receipt.Args, _ = json.Marshal(request)
			}
			before, _ := s.Load(t.Context())
			if err := s.SaveTaskFollowup(t.Context(), target, expected, receipt); err == nil {
				t.Fatal("changed parent or unselected import admitted a follow-up")
			}
			loaded, err := s.Load(t.Context())
			if err != nil || !reflect.DeepEqual(before, loaded) {
				t.Fatal("rejected follow-up changed retained records", err)
			}
		})
	}
}
