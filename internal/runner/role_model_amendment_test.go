package runner

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func modelAmendmentFixture(t *testing.T, fixed bool) (*Core, *taskDefinitionStore, Run, TaskRoleAmendmentRequest) {
	t.Helper()
	c, store, root, req := roleAmendmentFixture(t, false)
	if fixed {
		root.TaskGraph, root.Process.Tasks = nil, nil
		root.RecordFormat = 3
		root.Status, root.ResourcesReleased = "Failed", true
		c.runs, store.runs = map[string]Run{}, map[string]Run{}
	}
	c.runs[root.ID], store.runs[root.ID] = Clone(root), Clone(root)
	req.RequestID, req.Revision = "owner-model-only", c.outputRun(root).Revision
	req.RoleInstructions = nil
	req.RoleModels = map[string]string{"lead": "new-owner-selected-model", "reader": "new-owner-selected-model"}
	return c, store, root, req
}

func TestRoleModelAmendmentPreservesHistoryAndChangesOnlyFutureSelection(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(fmt.Sprint("fixed_", fixed), func(t *testing.T) {
			c, store, before, req := modelAmendmentFixture(t, fixed)
			children := Clone(c.runs)
			after, err := c.AmendTaskRoles(req)
			if err != nil {
				t.Fatal(err)
			}
			saved := c.runs[after.ID]
			expected := Clone(before)
			for id, model := range req.RoleModels {
				expected.Roles[id].Execution.Model = model
			}
			setRoleDefinitionGrants(&expected, Clone(RoleDefinitionGrants(saved)))
			expected.Revision, expected.Updated = saved.Revision, saved.Updated
			if !reflect.DeepEqual(expected, saved) || c.readTaskDefinitionProofs(saved) != nil {
				t.Fatal("model amendment changed work, prior execution facts, source, reasoning, permissions or allowance")
			}
			for id, child := range children {
				if id != before.ID && !reflect.DeepEqual(child, c.runs[id]) {
					t.Fatal("model amendment rewrote a retained child", id)
				}
			}
			grant := RoleDefinitionGrants(saved)[len(RoleDefinitionGrants(before))]
			if len(grant.ModelChanges) != len(req.RoleModels) || len(grant.InstructionChanges) != 0 || grant.PreviousLimit != grant.Limit || grant.ModelChanges[0].PreviousModel != before.Roles["lead"].Execution.Model {
				t.Fatal("model audit obscured previous selection or granted capacity", grant)
			}
			saves := store.saves
			if _, err := c.AmendTaskRoles(req); err != nil || store.saves != saves {
				t.Fatal("same request repeated the amendment", err)
			}
			if !fixed {
				child := children[before.TaskGraph.Children[0].RunID]
				old := child.Roles["reader"].Execution.Model
				if got := c.amendedVisitModel(child, Attempt{Started: grant.GrantedAt.Add(time.Second)}, "reader", old); got != req.RoleModels["reader"] {
					t.Fatal("future retained-child visit did not use audited selection", got)
				}
				if got := c.amendedVisitModel(child, Attempt{Started: grant.GrantedAt.Add(-time.Second)}, "reader", old); got != old {
					t.Fatal("audit relabelled a past visit", got)
				}
				role := child.Roles["reader"]
				fallback := *role.Execution
				fallback.Model = "distinct-fallback-model"
				role.Profiles = map[string]Profile{"fallback": fallback}
				child.Roles["reader"] = role
				if got := c.amendedVisitModel(child, Attempt{Started: grant.GrantedAt.Add(time.Second), Profile: "fallback"}, "reader", fallback.Model); got != fallback.Model {
					t.Fatal("default amendment overwrote an explicit named profile", got)
				}
				role.DefaultProfile = "fallback"
				child.Roles["reader"] = role
				if got := c.amendedVisitModel(child, Attempt{Started: grant.GrantedAt.Add(time.Second)}, "reader", fallback.Model); got != fallback.Model {
					t.Fatal("default amendment overwrote a named default selector", got)
				}
			}
			reopened, err := New(t.Context(), store, c.executor, c.disk, c.snapshot)
			if err != nil {
				t.Fatal("model audit could not reopen", err)
			}
			defer reopened.Close()
			if _, err := reopened.AmendTaskRoles(req); err != nil || store.saves != saves {
				t.Fatal("restart lost idempotent model audit", err)
			}
		})
	}
}

func TestRoleModelAmendmentRejectsUnsafeOrUnboundChanges(t *testing.T) {
	for _, fault := range []string{"mixed", "unchanged", "unknown-role", "non-model-harness", "invalid-model", "stale", "active", "completed", "reasoning-forgery"} {
		t.Run(fault, func(t *testing.T) {
			c, _, before, req := modelAmendmentFixture(t, true)
			switch fault {
			case "mixed":
				req.RoleInstructions = map[string]string{"lead": "Changed instructions"}
			case "unchanged":
				req.RoleModels["lead"] = before.Roles["lead"].Execution.Model
			case "unknown-role":
				req.RoleModels = map[string]string{"missing": "new-model"}
			case "non-model-harness":
				before.Roles["lead"].Execution.Harness = "command"
			case "invalid-model":
				req.RoleModels["lead"] = "model\n--access=host"
			case "stale":
				req.Revision--
			case "active":
				before.Attempts[len(before.Attempts)-1].Released = false
			case "completed":
				before.Status = "Completed"
			case "reasoning-forgery":
				after, err := c.AmendTaskRoles(req)
				if err != nil {
					t.Fatal(err)
				}
				forged := c.runs[after.ID]
				forged.Roles["lead"].Execution.Reasoning = "changed"
				if ValidateTaskAllowanceChange(before, forged, nil, []Run{before}) == nil {
					t.Fatal("ordinary save changed the amended definition")
				}
				return
			}
			c.runs[before.ID] = Clone(before)
			used := c.disk.Used()
			if _, err := c.AmendTaskRoles(req); err == nil || used != c.disk.Used() || !reflect.DeepEqual(c.runs[before.ID], before) {
				t.Fatal("unsafe model amendment changed state or wrote proof", err)
			}
		})
	}
}

func TestRoleModelAmendmentReconcilesUncertainSaveAndInstructionChain(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint("committed_", committed), func(t *testing.T) {
			c, store, before, req := modelAmendmentFixture(t, false)
			store.fail, store.committed = true, committed
			_, err := c.AmendTaskRoles(req)
			if (err == nil) != committed {
				t.Fatal("uncertain model save was not reconciled from its exact receipt", err)
			}
			store.fail = false
			first, err := c.AmendTaskRoles(req)
			if err != nil {
				t.Fatal(err)
			}
			instruction := TaskRoleAmendmentRequest{
				RequestID: "later-instruction", RunID: before.ID, Revision: first.Revision,
				Reason:           "Owner changed future guidance separately",
				RoleInstructions: map[string]string{"lead": "Later explicit instruction"},
			}
			second, err := c.AmendTaskRoles(instruction)
			if err != nil {
				t.Fatal("model audit broke the existing instruction path", err)
			}
			req.RequestID, req.Revision = "later-model", second.Revision
			req.RoleModels = map[string]string{"lead": "another-owner-model"}
			if _, err := c.AmendTaskRoles(req); err != nil || c.readTaskDefinitionProofs(c.runs[before.ID]) != nil {
				t.Fatal("interleaved authenticated definition proofs do not rejoin", err)
			}
		})
	}
}
