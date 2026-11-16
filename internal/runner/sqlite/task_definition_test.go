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

func storedTaskDefinition(t *testing.T) (*Store, runner.Run, runner.Run, *runner.Receipt, runner.Artifact) {
	t.Helper()
	role := runner.Role{
		ID: "lead", Purpose: "Complete this retained goal", Inputs: map[string]string{}, Outputs: map[string]string{"result": "text"},
		Execution: &runner.Profile{
			Harness: "native", Model: "fixture-model", Access: "workspace-read", MaxInput: 128 << 10, MaxOutput: 4 << 10,
			Instructions: "Original role instructions",
			Native:       &runner.NativeProfile{Harness: "codex", Tools: runner.CapabilitySelection{Mode: "native"}, Skills: runner.CapabilitySelection{Mode: "none"}},
		},
	}
	s, _, first, firstReceipt := storedTaskAllowance(t, role)
	if err := s.Save(t.Context(), first, nil, firstReceipt); err != nil {
		t.Fatal(err)
	}
	before := runner.Clone(first)
	before.Status, before.ResourcesReleased = "Failed", true
	before.Revision++
	for i := range 6 {
		before.Attempts = append(before.Attempts, runner.Attempt{ID: fmt.Sprintf("later-attempt-%d", i), State: runner.Finished, Released: true})
	}
	if err := s.Save(t.Context(), before, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, definition, err := runner.MarshalRunRecord(before)
	if err != nil {
		t.Fatal(err)
	}
	digest := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
	req := runner.RetryRequest{
		RequestID: "owner-definition-change", RunID: before.ID, Revision: before.Revision,
		ExtraAttempts: 56, MaxTasks: 64, RoleInstructions: map[string]string{"lead": "Require focused passing evidence before independent review"},
		Reason: "Owner authorized completing the retained goal",
	}
	after := runner.Clone(before)
	after.Revision++
	after.Updated = time.Now().UTC()
	after.Status, after.Reason, after.ResourcesReleased = "Queued", "authorized task retry", false
	after.Process.MaxAttempts, after.Process.Tasks.MaxTasks = 128, req.MaxTasks
	after.Roles["lead"].Execution.Instructions = req.RoleInstructions["lead"]
	proof := &runner.CardRecoveryArtifact{ID: before.ID + "/definition-" + digest(string(definition)) + ".json", RunID: before.ID, Bytes: int64(len(definition)), SHA256: digest(string(definition))}
	after.TaskGraph.AllowanceGrants = append(after.TaskGraph.AllowanceGrants, runner.TaskAllowanceGrant{
		RequestID: req.RequestID, PreviousLimit: 72, Limit: 128, Spent: 69, Reason: req.Reason, GrantedAt: after.Updated,
		PreviousDefinition: proof, PreviousMaxTasks: before.Process.Tasks.MaxTasks, MaxTasks: req.MaxTasks,
		InstructionChanges: []runner.TaskInstructionChange{{RoleID: "lead", PreviousSHA256: digest(before.Roles["lead"].Execution.Instructions), SHA256: digest(req.RoleInstructions["lead"])}},
	})
	args, _ := json.Marshal(req)
	return s, before, after, &runner.Receipt{Key: req.RequestID, Kind: "retry", Args: args, RunID: before.ID}, runner.Artifact{ID: proof.ID, RunID: before.ID, ProjectID: before.ProjectID, Bytes: proof.Bytes}
}

func TestTaskDefinitionAtomicRollbackRestartAndArtifactRetention(t *testing.T) {
	s, before, after, receipt, artifact := storedTaskDefinition(t)
	history := allowanceAttemptBytes(t, s)
	beforeRows, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER fail_definition_receipt BEFORE INSERT ON requests WHEN NEW.key='owner-definition-change' BEGIN SELECT RAISE(ABORT,'injected definition persistence failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), after, []runner.Artifact{artifact}, receipt); err == nil {
		t.Fatal("injected late receipt failure did not roll back the definition")
	}
	rows, err := s.Load(t.Context())
	if err != nil || !reflect.DeepEqual(rows, beforeRows) || allowanceAttemptBytes(t, s) != history {
		t.Fatal("failed definition amendment changed retained state or evidence", err)
	}
	if _, err := s.Artifact(t.Context(), artifact.ID); err == nil {
		t.Fatal("rolled-back definition registered a proof artifact")
	}
	if _, err := s.FindReceipt(t.Context(), receipt.Key); err == nil {
		t.Fatal("rolled-back definition retained an owner receipt")
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_definition_receipt"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), after, []runner.Artifact{artifact}, receipt); err != nil {
		t.Fatal(err)
	}
	if allowanceAttemptBytes(t, s) != history || !reflect.DeepEqual(after.Attempts, before.Attempts) {
		t.Fatal("definition amendment rewrote prior execution evidence")
	}
	var uses int
	if err := s.db.QueryRow("SELECT count(*) FROM artifact_uses WHERE artifact=? AND run_id=?", artifact.ID, after.ID).Scan(&uses); err != nil || uses != 1 {
		t.Fatal("prior definition proof is not pinned by its owning run", uses, err)
	}
	if err := s.Save(t.Context(), after, nil, nil); err != nil {
		t.Fatal("ordinary future save discarded the immutable definition audit", err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM artifact_uses WHERE artifact=? AND run_id=?", artifact.ID, after.ID).Scan(&uses); err != nil || uses != 1 {
		t.Fatal("ordinary future save orphaned the prior definition", err)
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
	rows, err = reopened.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.ID == after.ID {
			found = true
			if !reflect.DeepEqual(r, after) {
				t.Fatal("restart lost the exact amended definition and audit")
			}
		}
	}
	if got, err := reopened.FindReceipt(t.Context(), receipt.Key); err != nil || !reflect.DeepEqual(got, *receipt) || !found {
		t.Fatal("restart lost its exact owner amendment receipt", err)
	}
}

func TestTaskDefinitionRejectsForgedOrUnauditedChanges(t *testing.T) {
	for _, fault := range []string{"no-receipt", "no-artifact", "foreign-artifact", "proof-bytes", "instruction-digest", "stale-revision", "cap-only-role-change", "model", "tools", "profile", "task-concurrency", "old-attempt"} {
		t.Run(fault, func(t *testing.T) {
			s, before, after, receipt, artifact := storedTaskDefinition(t)
			artifacts := []runner.Artifact{artifact}
			switch fault {
			case "no-receipt":
				receipt = nil
			case "no-artifact":
				artifacts = nil
			case "foreign-artifact":
				artifacts[0].RunID = "foreign-run"
			case "proof-bytes":
				after.TaskGraph.AllowanceGrants[1].PreviousDefinition.Bytes--
			case "instruction-digest":
				after.TaskGraph.AllowanceGrants[1].InstructionChanges[0].PreviousSHA256 = after.TaskGraph.AllowanceGrants[1].InstructionChanges[0].SHA256
			case "stale-revision", "cap-only-role-change":
				var req runner.RetryRequest
				_ = json.Unmarshal(receipt.Args, &req)
				if fault == "stale-revision" {
					req.Revision--
				} else {
					req.RoleInstructions = nil
				}
				receipt.Args, _ = json.Marshal(req)
			case "model":
				after.Roles["lead"].Execution.Model = "different-model"
			case "tools":
				after.Roles["lead"].Execution.Native.Network = true
			case "profile":
				after.Profiles = map[string]string{"lead": "different-profile"}
			case "task-concurrency":
				after.Process.Tasks.MaxConcurrent++
			case "old-attempt":
				after.Attempts[0].Released = false
			}
			if err := s.Save(t.Context(), after, artifacts, receipt); err == nil {
				t.Fatal("forged definition amendment crossed the transactional boundary")
			}
			rows, err := s.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				if r.ID == before.ID && !reflect.DeepEqual(r, before) {
					t.Fatal("rejected amendment changed the retained root")
				}
			}
		})
	}
	s, before, _, _, _ := storedTaskDefinition(t)
	for _, fault := range []string{"role", "process", "profile"} {
		bad := runner.Clone(before)
		switch fault {
		case "role":
			bad.Roles["lead"].Execution.Instructions = "Unaudited new instructions"
		case "process":
			bad.Process.Tasks.MaxTasks++
		case "profile":
			bad.Profiles = map[string]string{"lead": "unaudited-selection"}
		}
		if err := s.Save(t.Context(), bad, nil, nil); err == nil {
			t.Fatal("ordinary Save changed the frozen task definition", fault)
		}
	}
}
