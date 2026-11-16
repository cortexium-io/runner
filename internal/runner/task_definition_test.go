package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type taskDefinitionStore struct {
	*continuationStore
	runs      map[string]Run
	fail      bool
	committed bool
}

func (s *taskDefinitionStore) Load(context.Context) ([]Run, error) {
	var ids []string
	for id := range s.runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var runs []Run
	for _, id := range ids {
		runs = append(runs, Clone(s.runs[id]))
	}
	return runs, nil
}

func (s *taskDefinitionStore) Save(ctx context.Context, r Run, artifacts []Artifact, receipt *Receipt) error {
	if s.fail && !s.committed {
		return errors.New("injected transaction refusal")
	}
	if err := s.frontendRecheckStore.Save(ctx, r, artifacts, receipt); err != nil {
		return err
	}
	s.runs[r.ID] = Clone(r)
	for _, artifact := range artifacts {
		s.artifacts[artifact.ID] = artifact
	}
	if s.fail {
		return errors.New("injected lost commit acknowledgment")
	}
	return nil
}

func taskDefinitionFixture(t *testing.T) (*Core, *taskDefinitionStore, Run, RetryRequest) {
	t.Helper()
	c, oldStore, _, continuation := taskContinuationFixture(t)
	root, err := c.ContinueTask(continuation)
	if err != nil {
		t.Fatal(err)
	}
	root = c.runs[root.ID]
	for id, role := range root.Roles {
		role.Execution = &Profile{
			Harness: "native", Model: "fixture-model", Access: "workspace-read", MaxInput: 128 << 10, MaxOutput: 4 << 10,
			Instructions: "Original instructions for " + id,
			Native:       &NativeProfile{Harness: "codex", Tools: CapabilitySelection{Mode: "native"}, Skills: CapabilitySelection{Mode: "none"}},
		}
		root.Roles[id] = role
	}
	root.Process.MaxAttempts, root.Process.Tasks.MaxTasks = 72, 20
	root.Status, root.ResourcesReleased, root.TaskGraph.Paused = "Failed", true, true
	root.TaskGraph.AllowanceGrants = []TaskAllowanceGrant{{
		RequestID: "previous-budget-grant", PreviousLimit: 64, Limit: 72, Spent: 63,
		Reason: taskAllowanceReason, GrantedAt: time.Now().UTC(),
	}}
	for i := range 33 {
		root.Attempts = append(root.Attempts, Attempt{ID: fmt.Sprintf("retained-native-%d", i), Visit: i + 1, State: Finished, Released: true, Superseded: i != 32})
	}
	root.Visit = 33
	link := RoleTaskLink{Spec: RoleTask{ID: "accepted", Role: "reader", Brief: "Accepted independent report"}, RunID: "accepted-reader"}
	child := c.graphChild(root, root, link)
	child.Status, child.ResourcesReleased, child.Revision = "Completed", true, 3
	child = Clone(child)
	root.TaskGraph.Children = []RoleTaskLink{link}
	c.runs[root.ID], c.runs[child.ID] = root, child
	s := &taskDefinitionStore{continuationStore: oldStore, runs: Clone(c.runs)}
	c.store = s
	return c, s, Clone(root), RetryRequest{
		RequestID: "owner-definition-amendment", RunID: root.ID, Revision: c.outputRun(root).Revision,
		ExtraAttempts: 56, MaxTasks: 64, Reason: "Owner approved completing the retained goal with evidence-ready reviews",
		RoleInstructions: map[string]string{"lead": "Plan focused development and checks before requesting independent reviews", "reader": "Review only an evidence-ready candidate against the original goal"},
	}
}

func TestTaskDefinitionAmendmentPreservesHistoryAndFutureRoles(t *testing.T) {
	c, store, before, req := taskDefinitionFixture(t)
	oldChild := Clone(c.runs[before.TaskGraph.Children[0].RunID])
	_, previousDefinition, _ := MarshalRunRecord(before)
	after, err := c.Retry(req)
	if err != nil {
		t.Fatal(err)
	}
	if after.Process.MaxAttempts != 128 || after.Process.Tasks.MaxTasks != 64 || after.TaskBudget.Spent != 69 || after.TaskBudget.Remaining != 59 || after.VisitExtraAttempts != before.VisitExtraAttempts || !reflect.DeepEqual(after.Attempts, before.Attempts) || !reflect.DeepEqual(c.runs[oldChild.ID], oldChild) {
		t.Fatalf("definition amendment reset work: limit=%d tasks=%d budget=%+v visit=%d/%d attempts_equal=%t child_equal=%t", after.Process.MaxAttempts, after.Process.Tasks.MaxTasks, after.TaskBudget, after.VisitExtraAttempts, before.VisitExtraAttempts, reflect.DeepEqual(after.Attempts, before.Attempts), reflect.DeepEqual(c.runs[oldChild.ID], oldChild))
	}
	grant := after.TaskGraph.AllowanceGrants[1]
	if grant.PreviousMaxTasks != 20 || grant.MaxTasks != 64 || len(grant.InstructionChanges) != 2 || grant.PreviousDefinition == nil {
		t.Fatal("definition amendment lost its bounded audit", grant)
	}
	proof, err := c.disk.Read("artifacts/"+grant.PreviousDefinition.ID, c.disk.Policy.MaxArtifact)
	if err != nil || !bytes.Equal(proof, previousDefinition) {
		t.Fatal("prior complete definition was not retained exactly", err)
	}
	if err := c.readTaskDefinitionProofs(c.runs[after.ID]); err != nil {
		t.Fatal("amended definition cannot authenticate its prior proof", err)
	}
	for id, instructions := range req.RoleInstructions {
		oldRole, amended := before.Roles[id], Clone(after.Roles[id])
		amended.Execution.Instructions = oldRole.Execution.Instructions
		if !reflect.DeepEqual(amended, oldRole) {
			t.Fatal("instruction amendment changed execution authority", id)
		}
		if c.runs[after.ID].Roles[id].Execution.Instructions != instructions {
			t.Fatal("future root role did not receive owner guidance", id)
		}
	}
	future := c.graphChild(c.runs[after.ID], c.runs[after.ID], RoleTaskLink{Spec: RoleTask{ID: "future-review", Role: "reader"}, RunID: "future-reader"})
	if future.Roles["reader"].Execution.Instructions != req.RoleInstructions["reader"] || oldChild.Roles["reader"].Execution.Instructions == req.RoleInstructions["reader"] {
		t.Fatal("future and historical child guidance did not retain separate ownership")
	}
	saves := store.saves
	if replay, err := c.Retry(req); err != nil || replay.Process.MaxAttempts != 128 || store.saves != saves {
		t.Fatal("replay repeated the amendment", err)
	}
}

func TestTaskDefinitionAmendmentPreservesProofAcrossSaveUncertainty(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			c, store, before, req := taskDefinitionFixture(t)
			store.fail, store.committed = true, committed
			if _, err := c.Retry(req); err == nil {
				t.Fatal("injected storage failure disappeared")
			}
			if !reflect.DeepEqual(c.runs[before.ID], before) {
				t.Fatal("failed acknowledgment changed the in-memory root")
			}
			_, definition, _ := MarshalRunRecord(before)
			proofID := before.ID + "/definition-" + recoveryDigestRawText(string(definition)) + ".json"
			if proof, err := c.disk.Read("artifacts/"+proofID, c.disk.Policy.MaxArtifact); err != nil || !bytes.Equal(proof, definition) {
				t.Fatal("save uncertainty discarded its immutable proof", err)
			}
			store.fail = false
			if !committed {
				if _, err := store.FindReceipt(t.Context(), req.RequestID); !errors.Is(err, ErrNotFound) {
					t.Fatal("refused transaction retained a receipt", err)
				}
				if _, err := c.Retry(req); err != nil {
					t.Fatal("safe retry could not reuse its bounded proof", err)
				}
			}
			reopened, err := New(t.Context(), store, c.executor, c.disk, c.snapshot)
			if err != nil {
				t.Fatal("committed amendment could not reopen", err)
			}
			defer reopened.Close()
			replay, err := reopened.Retry(req)
			if err != nil || replay.Process.MaxAttempts != 128 || len(replay.TaskGraph.AllowanceGrants) != 2 || replay.TaskBudget.Spent != 69 {
				t.Fatal("restart replay lost the same amendment or spent more attempts", err)
			}
		})
	}
}

func TestTaskDefinitionAmendmentRejectsInvalidAuthorityBeforeWriting(t *testing.T) {
	for _, fault := range []string{"stale", "missing-revision", "missing-grant", "unknown-role", "non-native", "noop", "blank", "instruction-size", "request-size", "task-decrease", "task-ceiling", "active", "cleanup", "unreleased-child"} {
		t.Run(fault, func(t *testing.T) {
			c, store, before, req := taskDefinitionFixture(t)
			switch fault {
			case "stale":
				req.Revision--
			case "missing-revision":
				req.Revision = 0
			case "missing-grant":
				req.ExtraAttempts = 0
			case "unknown-role":
				req.RoleInstructions["unknown"] = "New guidance"
			case "non-native":
				role := before.Roles["reader"]
				role.Execution.Harness = "deterministic"
				before.Roles["reader"] = role
				c.runs[before.ID] = before
			case "noop":
				req.RoleInstructions["lead"] = before.Roles["lead"].Execution.Instructions
			case "blank":
				req.RoleInstructions["lead"] = "  "
			case "instruction-size":
				req.RoleInstructions["lead"] = strings.Repeat("x", 33<<10)
			case "request-size":
				req.RoleInstructions["lead"] = strings.Repeat("x", int(c.disk.Policy.Database.MaxRecord))
			case "task-decrease":
				req.MaxTasks = 19
			case "task-ceiling":
				req.MaxTasks = 65
			case "active":
				before.Attempts[len(before.Attempts)-1].State = Running
				c.runs[before.ID] = before
			case "cleanup":
				before.CleanupBlocked = true
				c.runs[before.ID] = before
			case "unreleased-child":
				child := c.runs[before.TaskGraph.Children[0].RunID]
				child.ResourcesReleased = false
				c.runs[child.ID] = child
			}
			used := c.disk.Used()
			if _, err := c.Retry(req); err == nil {
				t.Fatal("invalid owner amendment reached persistence")
			}
			if c.disk.Used() != used || !reflect.DeepEqual(c.runs[before.ID], before) {
				t.Fatal("rejected amendment wrote proof bytes or changed work")
			}
			if _, err := store.FindReceipt(t.Context(), req.RequestID); !errors.Is(err, ErrNotFound) {
				t.Fatal("rejected amendment retained an owner receipt", err)
			}
		})
	}
}
