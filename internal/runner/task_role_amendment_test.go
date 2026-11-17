package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type roleAmendmentReadFailureStore struct {
	*taskDefinitionStore
	reads int
}

func (s *roleAmendmentReadFailureStore) FindReceipt(ctx context.Context, key string) (Receipt, error) {
	if key == "owner-roles-only" {
		s.reads++
		if s.reads == 2 {
			return Receipt{}, errors.New("injected first committed-receipt reconciliation read failure")
		}
	}
	return s.taskDefinitionStore.FindReceipt(ctx, key)
}

func roleAmendmentFixture(t *testing.T, enforce bool) (*Core, *taskDefinitionStore, Run, TaskRoleAmendmentRequest) {
	t.Helper()
	c, store, root, old := taskDefinitionFixture(t)
	c.snapshot.Instance.EnforceWorkLimits = enforce
	root.Status, root.ResourcesReleased = "Waiting", false
	root.TaskGraph.Phase, root.TaskGraph.Paused = TaskWaitingChildren, true
	root.VisitExtraAttempts = 2
	if !enforce {
		for i := range 8 {
			root.Attempts = append(root.Attempts, Attempt{ID: fmt.Sprintf("past_dormant_%d", i), Visit: i + 40, State: Finished, Released: true})
		}
	}
	c.runs[root.ID], store.runs[root.ID] = Clone(root), Clone(root)
	return c, store, Clone(root), TaskRoleAmendmentRequest{
		RequestID: "owner-roles-only", RunID: root.ID, Revision: c.outputRun(root).Revision,
		Reason:           "Owner corrected the scope of the existing goal without granting capacity",
		RoleInstructions: old.RoleInstructions,
	}
}

func TestTaskRoleAmendmentPreservesPausedWorkAndOnlyFutureInstructions(t *testing.T) {
	for _, enforce := range []bool{false, true} {
		t.Run(fmt.Sprint("work_limits_", enforce), func(t *testing.T) {
			c, store, before, req := roleAmendmentFixture(t, enforce)
			oldChild := Clone(c.runs[before.TaskGraph.Children[0].RunID])
			budget := c.familyAttemptBudget(before)
			_, definition, err := MarshalRunRecord(before)
			if err != nil {
				t.Fatal(err)
			}
			after, err := c.AmendTaskRoles(req)
			if err != nil {
				t.Fatal(err)
			}
			saved := c.runs[after.ID]
			grant := saved.TaskGraph.AllowanceGrants[len(before.TaskGraph.AllowanceGrants)]
			if !roleOnlyAmendment(grant) || grant.Limit != before.Process.MaxAttempts || grant.Spent != budget.Spent || !reflect.DeepEqual(after.TaskBudget, budget) {
				t.Fatal("role-only amendment minted capacity or lost spending", grant, after.TaskBudget)
			}
			expected := Clone(before)
			for id, text := range req.RoleInstructions {
				expected.Roles[id].Execution.Instructions = text
			}
			expected.TaskGraph.AllowanceGrants = Clone(saved.TaskGraph.AllowanceGrants)
			expected.Revision, expected.Updated = saved.Revision, saved.Updated
			if !reflect.DeepEqual(expected, saved) || !reflect.DeepEqual(oldChild, c.runs[oldChild.ID]) {
				t.Fatal("amendment changed work, status, pause, source, history, authority or local retries")
			}
			proof, err := c.disk.Read("artifacts/"+grant.PreviousDefinition.ID, c.disk.Policy.MaxArtifact)
			if err != nil || !bytes.Equal(proof, definition) || c.readTaskDefinitionProofs(saved) != nil {
				t.Fatal("amendment did not authenticate its exact previous definition", err)
			}
			future := graphChild(saved, saved, RoleTaskLink{Spec: RoleTask{ID: "new-scoped-work", Role: "reader", WritePaths: []string{"internal/catalogue.go"}}, RunID: "future-scoped-work"})
			if future.Roles["reader"].Execution.Instructions != req.RoleInstructions["reader"] || oldChild.Roles["reader"].Execution.Instructions == req.RoleInstructions["reader"] {
				t.Fatal("new and historical child snapshots did not retain independent definitions")
			}
			saves := store.saves
			if replay, err := c.AmendTaskRoles(req); err != nil || replay.ID != after.ID || store.saves != saves {
				t.Fatal("same owner request repeated the amendment", err)
			}
			if !c.driveGraph(saved) || !reflect.DeepEqual(c.runs[saved.ID], saved) {
				t.Fatal("paused amendment dispatched or evaluated children without an explicit resume")
			}
			resumed, err := c.InterveneTask(TaskInterventionRequest{RequestID: "explicit-resume", RunID: saved.ID, Revision: c.outputRun(saved).Revision, Action: "resume"})
			if err != nil || resumed.TaskGraph.Paused || len(resumed.TaskGraph.AllowanceGrants) != len(saved.TaskGraph.AllowanceGrants) {
				t.Fatal("normal explicit resume could not retain the authenticated amendment", err)
			}
		})
	}
}

func TestTaskRoleAmendmentRejectsUnsettledOrUnboundWorkBeforeWriting(t *testing.T) {
	for _, fault := range []string{"not-paused", "terminal", "child-target", "stale-child-revision", "unknown-attempt", "unreleased-attempt", "root-inflight", "child-inflight", "cleanup", "unmaterialized", "no-change"} {
		t.Run(fault, func(t *testing.T) {
			c, _, before, req := roleAmendmentFixture(t, false)
			child := c.runs[before.TaskGraph.Children[0].RunID]
			switch fault {
			case "not-paused":
				before.TaskGraph.Paused = false
			case "terminal":
				before.Status = "Completed"
			case "child-target":
				req.RunID = child.ID
			case "stale-child-revision":
				child.Revision++
				c.runs[child.ID] = child
			case "unknown-attempt":
				before.Attempts[0].State = Unknown
			case "unreleased-attempt":
				before.Attempts[0].Released = false
			case "root-inflight":
				c.inFlight[before.ID] = true
			case "child-inflight":
				c.inFlight[child.ID] = true
			case "cleanup":
				before.CleanupBlocked = true
			case "unmaterialized":
				delete(c.runs, child.ID)
			case "no-change":
				req.RoleInstructions = map[string]string{"lead": before.Roles["lead"].Execution.Instructions}
			}
			c.runs[before.ID] = Clone(before)
			used := c.disk.Used()
			if _, err := c.AmendTaskRoles(req); err == nil {
				t.Fatal("unsafe role amendment reached persistence")
			}
			if used != c.disk.Used() || !reflect.DeepEqual(c.runs[before.ID], before) {
				t.Fatal("rejected amendment changed work or wrote proof bytes")
			}
		})
	}
}

func TestTaskRoleAmendmentReconcilesSaveUncertaintyAndRestart(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint("committed_", committed), func(t *testing.T) {
			c, store, before, req := roleAmendmentFixture(t, false)
			store.fail, store.committed = true, committed
			after, err := c.AmendTaskRoles(req)
			if committed {
				if err != nil || after.ID != before.ID || len(after.TaskGraph.AllowanceGrants) != len(before.TaskGraph.AllowanceGrants)+1 {
					t.Fatal("lost acknowledgement did not reconcile the exact durable paused amendment", err)
				}
			} else {
				if err == nil || !reflect.DeepEqual(c.runs[before.ID], before) {
					t.Fatal("refused save changed the in-memory root")
				}
				if _, err := store.FindReceipt(t.Context(), req.RequestID); !errors.Is(err, ErrNotFound) {
					t.Fatal("refused save retained an owner receipt", err)
				}
			}
			store.fail = false
			after, err = c.AmendTaskRoles(req)
			if err != nil {
				t.Fatal("identical request could not rejoin/reuse proof", err)
			}
			reopened, err := New(t.Context(), store, c.executor, c.disk, c.snapshot)
			if err != nil {
				t.Fatal("authenticated role amendment could not reopen", err)
			}
			defer reopened.Close()
			replayed, err := reopened.AmendTaskRoles(req)
			if err != nil || replayed.Process.MaxAttempts != before.Process.MaxAttempts || replayed.TaskBudget.Spent != after.TaskBudget.Spent || !replayed.TaskGraph.Paused || len(replayed.TaskGraph.AllowanceGrants) != len(after.TaskGraph.AllowanceGrants) {
				t.Fatal("restart replay changed the same paused amendment or work budget", err)
			}
		})
	}
}

func TestTaskRoleAmendmentRequiresEarlierDefinitionProof(t *testing.T) {
	c, _, before, req := roleAmendmentFixture(t, false)
	before.Process.MaxAttempts, before.TaskGraph.AllowanceGrants = 64, nil
	c.runs[before.ID] = before
	first, err := c.AmendTaskRoles(req)
	if err != nil {
		t.Fatal("first role-only audit did not admit", err)
	}
	proof := first.TaskGraph.AllowanceGrants[0].PreviousDefinition
	if err := c.disk.Remove("artifacts/" + proof.ID); err != nil {
		t.Fatal(err)
	}
	req.RequestID, req.Revision, req.RoleInstructions = "second-owner-amendment", first.Revision, map[string]string{"lead": "New instruction text after the first amendment"}
	used, retained := c.disk.Used(), Clone(c.runs[first.ID])
	if _, err := c.AmendTaskRoles(req); err == nil || c.disk.Used() != used || !reflect.DeepEqual(c.runs[first.ID], retained) {
		t.Fatal("missing earlier proof was bypassed or wrote new work", err)
	}
}

func TestTaskRoleAmendmentReplayReconcilesAfterTransientCommittedReadFailure(t *testing.T) {
	c, store, before, req := roleAmendmentFixture(t, false)
	wrapper := &roleAmendmentReadFailureStore{taskDefinitionStore: store}
	c.store = wrapper
	store.fail, store.committed = true, true
	if _, err := c.AmendTaskRoles(req); err == nil || !reflect.DeepEqual(c.runs[before.ID], before) {
		t.Fatal("unavailable commit evidence was reported as reconciled", err)
	}
	store.fail = false
	saves := store.saves
	after, err := c.AmendTaskRoles(req)
	if err != nil || after.ID != before.ID || len(after.TaskGraph.AllowanceGrants) != len(before.TaskGraph.AllowanceGrants)+1 || store.saves != saves || c.runs[before.ID].Roles["lead"].Execution.Instructions != req.RoleInstructions["lead"] {
		t.Fatal("same-process replay returned stale roles or repeated the committed amendment", err)
	}
}
