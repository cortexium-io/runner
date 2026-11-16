package sqlite

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func storedTaskRoleAmendment(t *testing.T, historical int) (*Store, runner.Run, runner.Run, runner.Run, *runner.Receipt, runner.Artifact) {
	t.Helper()
	s, before, _, _, _ := storedTaskDefinition(t)
	before.Status, before.Reason, before.ResourcesReleased = "Waiting", "owner paused this goal", false
	before.TaskGraph.Paused, before.TaskGraph.Phase = true, runner.TaskWaitingChildren
	before.Revision++
	for i := range historical {
		before.Attempts = append(before.Attempts, runner.Attempt{
			ID:       fmt.Sprintf("unlimited-history-%d", i),
			State:    runner.Finished,
			Released: true,
		})
	}
	child := normalizedRun(1)
	child.ID, child.ProjectID, child.Repository = "old-role-child", before.ProjectID, before.Repository
	child.RecordFormat, child.Revision, child.ResourcesReleased = 9, 3, true
	child.Process, child.Roles = runner.Clone(before.Process), runner.Clone(before.Roles)
	child.Process.Tasks = nil
	child.Attempts[0].ID = "old-role-child-attempt"
	child.TaskGraph = &runner.TaskGraphState{
		RootRunID:   before.ID,
		ParentRunID: before.ID,
		TaskID:      "old-role-task",
		Role:        "lead",
		Brief:       "Retained completed work",
	}
	before.TaskGraph.Children = []runner.RoleTaskLink{{
		Spec: runner.RoleTask{
			ID:    child.TaskGraph.TaskID,
			Role:  child.TaskGraph.Role,
			Brief: child.TaskGraph.Brief,
		},
		RunID: child.ID,
	}}
	if err := s.Save(t.Context(), child, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), before, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, definition, err := runner.MarshalRunRecord(before)
	if err != nil {
		t.Fatal(err)
	}
	digest := func(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }
	req := runner.TaskRoleAmendmentRequest{
		RequestID:        "owner-role-amendment",
		RunID:            before.ID,
		Revision:         before.Revision + child.Revision,
		Reason:           "Owner authorized the existing role to complete the retained goal",
		RoleInstructions: map[string]string{"lead": "Use the approved source boundaries and require focused passing evidence before review"},
	}
	after := runner.Clone(before)
	after.Revision++
	after.Updated = time.Now().UTC()
	after.Roles["lead"].Execution.Instructions = req.RoleInstructions["lead"]
	proof := &runner.CardRecoveryArtifact{
		ID:     before.ID + "/definition-" + digest(definition) + ".json",
		RunID:  before.ID,
		Bytes:  int64(len(definition)),
		SHA256: digest(definition),
	}
	after.TaskGraph.AllowanceGrants = append(after.TaskGraph.AllowanceGrants, runner.TaskAllowanceGrant{
		RequestID:          req.RequestID,
		PreviousLimit:      before.Process.MaxAttempts,
		Limit:              before.Process.MaxAttempts,
		Spent:              before.TaskGraph.Lineage.PriorAttempts + len(before.Attempts) + len(child.Attempts),
		Reason:             req.Reason,
		GrantedAt:          after.Updated,
		PreviousDefinition: proof,
		InstructionChanges: []runner.TaskInstructionChange{{
			RoleID:         "lead",
			PreviousSHA256: digest([]byte(before.Roles["lead"].Execution.Instructions)),
			SHA256:         digest([]byte(req.RoleInstructions["lead"])),
		}},
	})
	args, _ := json.Marshal(req)
	receipt := &runner.Receipt{Key: req.RequestID, Kind: "task_role_amendment", Args: args, RunID: before.ID}
	artifact := runner.Artifact{ID: proof.ID, RunID: before.ID, ProjectID: before.ProjectID, Bytes: proof.Bytes}
	return s, before, child, after, receipt, artifact
}

func TestTaskRoleAmendmentAtomicRollbackRestartAndRetainedHistory(t *testing.T) {
	for _, historical := range []int{0, 8} {
		t.Run(fmt.Sprint(historical), func(t *testing.T) {
			s, before, child, after, receipt, artifact := storedTaskRoleAmendment(t, historical)
			original, err := s.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			history := allowanceAttemptBytes(t, s)
			var childState, childDefinition []byte
			if err := s.db.QueryRow("SELECT data,definition FROM runs WHERE id=?", child.ID).Scan(&childState, &childDefinition); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("CREATE TRIGGER fail_role_receipt BEFORE INSERT ON requests WHEN NEW.key='owner-role-amendment' BEGIN SELECT RAISE(ABORT,'injected role amendment persistence failure'); END"); err != nil {
				t.Fatal(err)
			}
			if err := s.Save(t.Context(), after, []runner.Artifact{artifact}, receipt); err == nil {
				t.Fatal("late receipt failure did not roll back the role amendment")
			}
			loaded, err := s.Load(t.Context())
			if err != nil || !reflect.DeepEqual(loaded, original) || allowanceAttemptBytes(t, s) != history {
				t.Fatal("failed amendment changed retained state or attempt evidence", err)
			}
			if _, err := s.Artifact(t.Context(), artifact.ID); err == nil {
				t.Fatal("rolled-back amendment retained its proof registration")
			}
			if _, err := s.FindReceipt(t.Context(), receipt.Key); err == nil {
				t.Fatal("rolled-back amendment retained its owner receipt")
			}
			if _, err := s.db.Exec("DROP TRIGGER fail_role_receipt"); err != nil {
				t.Fatal(err)
			}
			if err := s.Save(t.Context(), after, []runner.Artifact{artifact}, receipt); err != nil {
				t.Fatal(err)
			}
			if allowanceAttemptBytes(t, s) != history || !reflect.DeepEqual(before.Process, after.Process) || before.VisitExtraAttempts != after.VisitExtraAttempts || after.Status != before.Status || !after.TaskGraph.Paused || after.ResourcesReleased {
				t.Fatal("role amendment changed work, limits, local retry allowance or paused source ownership")
			}
			var gotChildState, gotChildDefinition []byte
			if err := s.db.QueryRow("SELECT data,definition FROM runs WHERE id=?", child.ID).Scan(&gotChildState, &gotChildDefinition); err != nil || string(childState) != string(gotChildState) || string(childDefinition) != string(gotChildDefinition) {
				t.Fatal("role amendment rewrote an old child's role snapshot or state", err)
			}
			if err := s.Save(t.Context(), after, nil, receipt); err == nil {
				t.Fatal("the same receipt committed twice")
			}
			var path string
			if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(filepath.Dir(path), normalizedPolicy())
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			loaded, err = reopened.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for i := range original {
				if original[i].ID == before.ID {
					original[i] = after
				}
			}
			if !reflect.DeepEqual(loaded, original) || allowanceAttemptBytes(t, reopened) != history {
				t.Fatal("restart changed the amended root or retained family")
			}
			if got, err := reopened.FindReceipt(t.Context(), receipt.Key); err != nil || !reflect.DeepEqual(got, *receipt) {
				t.Fatal("restart lost the exact role-only owner receipt", err)
			}
			if err := reopened.Save(t.Context(), after, nil, nil); err != nil {
				t.Fatal("ordinary post-restart save rejected the role amendment chain", err)
			}
			var uses int
			if err := reopened.db.QueryRow("SELECT count(*) FROM artifact_uses WHERE artifact=? AND run_id=?", artifact.ID, after.ID).Scan(&uses); err != nil || uses != 1 {
				t.Fatal("ordinary save orphaned the exact prior role definition", uses, err)
			}
		})
	}
}

func TestTaskRoleAmendmentRejectsForgedProofAndChangedFamily(t *testing.T) {
	for _, fault := range []string{"no-receipt", "retry-receipt", "unknown-request-field", "no-proof-artifact", "foreign-proof-artifact", "registered-foreign-proof", "proof-bytes", "same-size-wrong-definition", "stale-child-revision", "active-child", "unreleased-attempt", "unreleased-terminal-child", "allowance-change", "old-evidence"} {
		t.Run(fault, func(t *testing.T) {
			s, before, child, after, receipt, artifact := storedTaskRoleAmendment(t, 0)
			artifacts := []runner.Artifact{artifact}
			switch fault {
			case "no-receipt":
				receipt = nil
			case "retry-receipt":
				receipt.Kind = "retry"
			case "unknown-request-field":
				var args map[string]json.RawMessage
				_ = json.Unmarshal(receipt.Args, &args)
				args["profile"] = json.RawMessage(`"different-profile"`)
				receipt.Args, _ = json.Marshal(args)
			case "no-proof-artifact":
				artifacts = nil
			case "foreign-proof-artifact":
				artifacts[0].RunID = child.ID
			case "registered-foreign-proof":
				if _, err := s.db.Exec("INSERT INTO artifacts(id,run_id,project,bytes) VALUES(?,?,?,?)", artifact.ID, child.ID, child.ProjectID, artifact.Bytes); err != nil {
					t.Fatal(err)
				}
			case "proof-bytes":
				after.TaskGraph.AllowanceGrants[1].PreviousDefinition.Bytes--
			case "same-size-wrong-definition":
				var definition []byte
				if err := s.db.QueryRow("SELECT definition FROM runs WHERE id=?", before.ID).Scan(&definition); err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(definition, &fields); err != nil {
					t.Fatal(err)
				}
				reordered, err := json.Marshal(fields)
				if err != nil || len(reordered) != len(definition) || string(reordered) == string(definition) {
					t.Fatal("fixture needs semantically equal definition bytes of the same length", err)
				}
				if _, err := s.db.Exec("UPDATE runs SET definition=? WHERE id=?", reordered, before.ID); err != nil {
					t.Fatal(err)
				}
			case "stale-child-revision", "active-child", "unreleased-attempt", "unreleased-terminal-child":
				switch fault {
				case "stale-child-revision":
					child.Revision++
				case "active-child":
					child.Status, child.Attempts[0].State = "Running", runner.Running
				case "unreleased-attempt":
					child.Attempts[0].Released = false
				case "unreleased-terminal-child":
					child.ResourcesReleased = false
				}
				if err := s.Save(t.Context(), child, nil, nil); err != nil {
					t.Fatal(err)
				}
			case "allowance-change":
				after.Process.MaxAttempts++
				after.TaskGraph.AllowanceGrants[1].Limit++
			case "old-evidence":
				after.Attempts[0].End.Reason = "rewritten historical evidence"
			}
			var state, definition []byte
			if err := s.db.QueryRow("SELECT data,definition FROM runs WHERE id=?", before.ID).Scan(&state, &definition); err != nil {
				t.Fatal(err)
			}
			history := allowanceAttemptBytes(t, s)
			if err := s.Save(t.Context(), after, artifacts, receipt); err == nil {
				t.Fatal("forged or stale amendment crossed the transactional boundary")
			}
			var gotState, gotDefinition []byte
			if err := s.db.QueryRow("SELECT data,definition FROM runs WHERE id=?", before.ID).Scan(&gotState, &gotDefinition); err != nil || string(gotState) != string(state) || string(gotDefinition) != string(definition) || allowanceAttemptBytes(t, s) != history {
				t.Fatal("rejected amendment changed persisted source, definition or attempts", err)
			}
		})
	}
}
