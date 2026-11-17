package sqlite

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func storedModelAmendment(t *testing.T, fixed bool) (*Store, runner.Run, runner.Run, *runner.Receipt, runner.Artifact) {
	t.Helper()
	if !fixed {
		s, before, _, after, receipt, artifact := storedTaskRoleAmendment(t, 8)
		after.Roles["lead"].Execution.Instructions = before.Roles["lead"].Execution.Instructions
		after.Roles["lead"].Execution.Model = "new-model"
		grant := &after.TaskGraph.AllowanceGrants[len(after.TaskGraph.AllowanceGrants)-1]
		grant.InstructionChanges = nil
		grant.ModelChanges = []runner.TaskModelChange{{RoleID: "lead", PreviousModel: before.Roles["lead"].Execution.Model, Model: "new-model"}}
		var req runner.TaskRoleAmendmentRequest
		if err := json.Unmarshal(receipt.Args, &req); err != nil {
			t.Fatal(err)
		}
		req.RoleInstructions, req.RoleModels = nil, map[string]string{"lead": "new-model"}
		receipt.Args, _ = json.Marshal(req)
		return s, before, after, receipt, artifact
	}
	before := normalizedRun(2)
	before.Status, before.ResourcesReleased, before.Revision = "Failed", true, 1
	before.Process.Steps = []runner.Step{{ID: "owner", Role: "owner"}}
	before.Roles["owner"] = runner.Role{ID: "owner", Execution: &runner.Profile{Harness: "codex", Model: "old-model", Reasoning: "xhigh", Access: "workspace-read"}}
	for i := range before.Attempts {
		before.Attempts[i].Step = 0
		before.Attempts[i].Effective = &runner.ExecutionFacts{Model: "old-model", Reasoning: "xhigh"}
	}
	s := openNormalized(t, before)
	_, definition, err := runner.MarshalRunRecord(before)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(definition))
	proof := &runner.CardRecoveryArtifact{ID: before.ID + "/definition-" + digest + ".json", RunID: before.ID, Bytes: int64(len(definition)), SHA256: digest}
	req := runner.TaskRoleAmendmentRequest{RequestID: "owner-model-amendment", RunID: before.ID, Revision: before.Revision, Reason: "Owner selected a new future model", RoleModels: map[string]string{"owner": "new-model"}}
	after := runner.Clone(before)
	after.Revision++
	after.Updated = time.Now().UTC()
	after.Roles["owner"].Execution.Model = "new-model"
	after.RoleAmendments = []runner.TaskAllowanceGrant{{
		RequestID: req.RequestID, PreviousLimit: before.Process.MaxAttempts, Limit: before.Process.MaxAttempts,
		Spent: len(before.Attempts), Reason: req.Reason, GrantedAt: after.Updated, PreviousDefinition: proof,
		ModelChanges: []runner.TaskModelChange{{RoleID: "owner", PreviousModel: "old-model", Model: "new-model"}},
	}}
	args, _ := json.Marshal(req)
	return s, before, after, &runner.Receipt{Key: req.RequestID, Kind: "task_role_amendment", RunID: before.ID, Args: args}, runner.Artifact{ID: proof.ID, RunID: before.ID, ProjectID: before.ProjectID, Bytes: proof.Bytes}
}

func TestModelAmendmentAtomicProofAndHistoricalExecutionRetention(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(fmt.Sprint("fixed_", fixed), func(t *testing.T) {
			s, before, after, receipt, artifact := storedModelAmendment(t, fixed)
			history := allowanceAttemptBytes(t, s)
			rows, err := s.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("CREATE TRIGGER fail_model_receipt BEFORE INSERT ON requests BEGIN SELECT RAISE(ABORT,'injected model receipt failure'); END"); err != nil {
				t.Fatal(err)
			}
			if err := s.Save(t.Context(), after, []runner.Artifact{artifact}, receipt); err == nil {
				t.Fatal("late model receipt failure did not roll back")
			}
			unchanged, err := s.Load(t.Context())
			if err != nil || !reflect.DeepEqual(rows, unchanged) || allowanceAttemptBytes(t, s) != history {
				t.Fatal("failed model transaction changed history", err)
			}
			if _, err := s.db.Exec("DROP TRIGGER fail_model_receipt"); err != nil {
				t.Fatal(err)
			}
			for _, fault := range []string{"missing-receipt", "reasoning", "past-model", "proof"} {
				forged := runner.Clone(after)
				authority := receipt
				switch fault {
				case "missing-receipt":
					authority = nil
				case "reasoning":
					for _, role := range forged.Roles {
						role.Execution.Reasoning = "different"
					}
				case "past-model":
					forged.Attempts[0].Effective = &runner.ExecutionFacts{Model: "invented-model"}
				case "proof":
					runner.RoleDefinitionGrants(forged)[len(runner.RoleDefinitionGrants(forged))-1].PreviousDefinition.Bytes--
				}
				if err := s.Save(t.Context(), forged, []runner.Artifact{artifact}, authority); err == nil {
					t.Fatal("forged model amendment accepted", fault)
				}
			}
			if err := s.Save(t.Context(), after, []runner.Artifact{artifact}, receipt); err != nil {
				t.Fatal("valid model amendment refused", err)
			}
			if allowanceAttemptBytes(t, s) != history || !reflect.DeepEqual(before.Attempts, after.Attempts) {
				t.Fatal("model amendment rewrote historical execution models")
			}
			var uses int
			if err := s.db.QueryRow("SELECT count(*) FROM artifact_uses WHERE artifact=? AND run_id=?", artifact.ID, after.ID).Scan(&uses); err != nil || uses != 1 {
				t.Fatal("model proof lost its retention reference", uses, err)
			}
			if err := s.Save(t.Context(), after, nil, nil); err != nil {
				t.Fatal("ordinary save lost model audit", err)
			}
			loaded, err := s.Load(t.Context())
			if err != nil {
				t.Fatal("persisted model audit could not load", err)
			}
			found := false
			for _, row := range loaded {
				if row.ID == after.ID {
					found = reflect.DeepEqual(row, after)
				}
			}
			if !found {
				t.Fatal("persisted model audit differs")
			}
		})
	}
}
