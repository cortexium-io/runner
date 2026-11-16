package runner

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type followupFixtureStore struct {
	*continuationStore
	followups int
	failed    bool
}

func (s *followupFixtureStore) SaveTaskFollowup(_ context.Context, target Run, family []Run, receipt *Receipt) error {
	if err := ValidateTaskFollowup(target, family, receipt); err != nil {
		return err
	}
	if s.failed {
		return errors.New("injected follow-up transaction failure")
	}
	s.followups++
	s.receipts[receipt.Key] = Clone(*receipt)
	return nil
}

func taskFollowupFixture(t *testing.T) (*Core, *followupFixtureStore, Run, StartRequest) {
	t.Helper()
	c, retained, _, request := taskContinuationFixture(t)
	continued, err := c.ContinueTask(request)
	if err != nil {
		t.Fatal(err)
	}
	parent := c.runs[continued.ID]
	parent.Status, parent.ResourcesReleased, parent.Step = "Completed", true, len(parent.Process.Steps)
	parent.Revision++
	c.runs[parent.ID] = parent
	store := &followupFixtureStore{continuationStore: retained}
	c.store = store
	return c, store, parent, StartRequest{
		RequestID: "owner-follow-up", Project: parent.ProjectID, Process: parent.Process.ID,
		Followup: &TaskFollowupRequest{RunID: parent.ID, Revision: c.outputRun(parent).Revision},
		Inputs: map[string]Value{
			"request": Text("Refine the accepted candidate using the owner's new feedback"),
			"patch":   parent.Inputs["patch"],
			"checks":  parent.Inputs["checks"],
		},
	}
}

func TestTaskFollowupPreservesCompletedParentAndInheritedEvidence(t *testing.T) {
	c, store, parent, req := taskFollowupFixture(t)
	origin := Clone(c.runs[parent.TaskGraph.Lineage.OriginRunID])
	started, err := c.Start(req)
	if err != nil {
		t.Fatal(err)
	}
	if started.ID == parent.ID || started.Followup == nil || started.Followup.RunID != parent.ID || started.Followup.Revision != req.Followup.Revision || started.Followup.RequestID != req.RequestID || started.TaskGraph.Lineage != nil || len(started.Attempts) != 0 || len(started.TaskGraph.Children) != 0 || started.TaskBudget == nil || started.TaskBudget.PriorAttempts != 0 || started.TaskBudget.Spent != 0 || store.followups != 1 {
		t.Fatal("follow-up reused accepted work, history or original allowance", started.Followup, started.TaskBudget)
	}
	if !reflect.DeepEqual(c.runs[parent.ID], parent) || !reflect.DeepEqual(c.runs[origin.ID], origin) {
		t.Fatal("follow-up mutated the completed parent or unfroze the legacy source family")
	}
	ctx, err := c.graphContext(c.runs[started.ID])
	if err != nil || ctx.Followup == nil || *ctx.Followup != *started.Followup || ctx.Lineage != nil || len(ctx.Results) != 0 || ctx.Goal == parent.TaskGraph.Brief {
		t.Fatal("follow-up context copied old goal/tasks or lost explicit lineage", ctx, err)
	}
	for _, name := range []string{"patch", "checks"} {
		if !c.graphArtifactAllowed(c.runs[started.ID], req.Inputs[name].ArtifactID) {
			t.Fatal("linked follow-up lost the parent's exact authenticated input", name)
		}
	}
	if err := c.validateTaskFollowupLinks(); err != nil {
		t.Fatal("restart validation rejected a normal accepted-parent follow-up", err)
	}
	replay, err := c.Start(req)
	if err != nil || replay.ID != started.ID || store.followups != 1 {
		t.Fatal("start receipt replay admitted another follow-up", err)
	}
	ordinary := req
	ordinary.RequestID, ordinary.Followup = "unlinked-import", nil
	if _, err := c.Start(ordinary); err == nil {
		t.Fatal("ordinary start acquired the parent's cross-project artifact grant")
	}
}

func TestTaskFollowupRejectsChangedAuthorityWithoutEffects(t *testing.T) {
	for name, change := range map[string]func(*Core, *followupFixtureStore, Run, *StartRequest){
		"stale revision": func(_ *Core, _ *followupFixtureStore, _ Run, r *StartRequest) { r.Followup.Revision-- },
		"failed result": func(c *Core, _ *followupFixtureStore, p Run, _ *StartRequest) {
			p.Status = "Failed"
			c.runs[p.ID] = p
		},
		"unreleased cleanup": func(c *Core, _ *followupFixtureStore, p Run, _ *StartRequest) {
			p.ResourcesReleased = false
			c.runs[p.ID] = p
		},
		"unknown execution": func(c *Core, _ *followupFixtureStore, p Run, _ *StartRequest) {
			p.Attempts = []Attempt{{ID: "unsettled", State: Unknown}}
			c.runs[p.ID] = p
		},
		"unmaterialized child": func(c *Core, _ *followupFixtureStore, p Run, _ *StartRequest) {
			p.TaskGraph.Children = []RoleTaskLink{{RunID: "reserved", Spec: RoleTask{ID: "reserved"}}}
			c.runs[p.ID] = p
		},
		"different project": func(_ *Core, _ *followupFixtureStore, _ Run, r *StartRequest) { r.Project = "other" },
		"implicit process":  func(_ *Core, _ *followupFixtureStore, _ Run, r *StartRequest) { r.Process = "" },
		"source ref":        func(_ *Core, _ *followupFixtureStore, _ Run, r *StartRequest) { r.SourceRef = "different" },
		"different workspace": func(c *Core, _ *followupFixtureStore, p Run, _ *StartRequest) {
			project := c.snapshot.Projects[p.ProjectID]
			project.Workspace = t.TempDir()
			c.snapshot.Projects[p.ProjectID] = project
		},
		"foreign unselected artifact": func(c *Core, s *followupFixtureStore, p Run, r *StartRequest) {
			id := "unselected/patch"
			s.artifacts[id] = Artifact{ID: id, ProjectID: "another", RunID: "another", Bytes: 4}
			r.Inputs["patch"] = Value{ArtifactID: id}
		},
		"pending collection": func(c *Core, _ *followupFixtureStore, p Run, _ *StartRequest) {
			if err := c.disk.Write(familyCollectionDirectory+"/"+p.ID+".json", []byte("{}"), c.disk.Policy.Database.MaxRecord); err != nil {
				t.Fatal(err)
			}
		},
		"transaction failure": func(_ *Core, s *followupFixtureStore, _ Run, _ *StartRequest) { s.failed = true },
	} {
		t.Run(name, func(t *testing.T) {
			c, store, parent, req := taskFollowupFixture(t)
			change(c, store, parent, &req)
			before := Clone(c.runs)
			if _, err := c.Start(req); err == nil || store.followups != 0 || !reflect.DeepEqual(c.runs, before) {
				t.Fatal("rejected follow-up changed retained work or admitted execution", err)
			}
			if _, err := store.FindReceipt(c.ctx, req.RequestID); err == nil {
				t.Fatal("rejected follow-up retained an admission receipt")
			}
		})
	}
}
