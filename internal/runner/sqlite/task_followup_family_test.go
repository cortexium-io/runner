package sqlite

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestTaskFollowupProtectsArtifactFreeParentChildrenAndWholeGraphs(t *testing.T) {
	parent := normalizedRun(0)
	parent.ID, parent.RecordFormat, parent.ProjectID = "plain-parent", 9, "demo"
	parent.Revision, parent.ResourcesReleased, parent.Repository = 5, true, "/owned/workspace"
	parent.Process.Tasks = &runner.TaskPolicy{Roles: []string{"lead"}, MaxTasks: 4, MaxDepth: 2, MaxConcurrent: 1}
	parent.Process.Steps = []runner.Step{{ID: "lead", Role: "lead"}}
	parent.Roles = map[string]runner.Role{"lead": {ID: "lead", Purpose: "Deliver the requested goal"}}
	parent.TaskGraph = &runner.TaskGraphState{
		RootRunID: parent.ID, TaskID: "root", Role: "lead", Brief: "Accepted original work",
		Children: []runner.RoleTaskLink{{
			Spec: runner.RoleTask{ID: "child", Role: "lead", Brief: "Complete the original child"}, RunID: "plain-child",
		}},
	}
	child := runner.Clone(parent)
	child.ID, child.Revision = "plain-child", 2
	child.Process.Tasks = nil
	child.TaskGraph = &runner.TaskGraphState{RootRunID: parent.ID, ParentRunID: parent.ID, TaskID: "child", Role: "lead", Brief: "Complete the original child"}
	s := openNormalized(t, parent)
	if err := s.Save(t.Context(), child, nil, nil); err != nil {
		t.Fatal(err)
	}
	target := runner.Clone(parent)
	target.ID, target.Status, target.ResourcesReleased, target.Revision = "plain-followup", "Queued", false, 1
	target.Process.ID = "followup"
	target.Inputs = map[string]runner.Value{"request": runner.Text("New owner feedback")}
	target.Process.Inputs = map[string]string{"request": "text"}
	target.Created, target.Updated = time.Now().UTC(), time.Now().UTC()
	target.TaskGraph = &runner.TaskGraphState{RootRunID: target.ID, TaskID: "root", Role: "lead", Brief: "New owner feedback"}
	target.Followup = &runner.TaskFollowupLineage{RunID: parent.ID, Revision: parent.Revision + child.Revision, RequestID: "plain-followup-start"}
	req := runner.StartRequest{RequestID: target.Followup.RequestID, Project: target.ProjectID, Process: target.Process.ID, Followup: &runner.TaskFollowupRequest{RunID: parent.ID, Revision: target.Followup.Revision}, Inputs: runner.Clone(target.Inputs)}
	args, _ := json.Marshal(req)
	receipt := &runner.Receipt{Key: req.RequestID, Kind: "start", Args: args, RunID: target.ID}
	if err := s.SaveTaskFollowup(t.Context(), target, []runner.Run{parent, child}, receipt); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Load(t.Context())
	for _, id := range []string{parent.ID, child.ID} {
		if used, err := s.HasConsumers(t.Context(), id); err != nil || !used {
			t.Fatal("artifact-free followed family member lost its consumer", id, used, err)
		}
		if used, err := s.HasExternalConsumers(t.Context(), []string{id}); err != nil || !used {
			t.Fatal("family cleanup could remove a followed child before SQL deletion", id, used, err)
		}
		if err := s.Delete(t.Context(), id); err == nil {
			t.Fatal("individual deletion detached followed history", id)
		}
	}
	for _, ids := range [][]string{
		{child.ID}, {parent.ID, child.ID}, {parent.ID, target.ID}, {child.ID, target.ID},
	} {
		if err := s.DeleteFamily(t.Context(), ids); err == nil {
			t.Fatal("partial source-family deletion broke a retained graph", ids)
		}
	}
	loaded, err := s.Load(t.Context())
	if err != nil || !reflect.DeepEqual(loaded, before) {
		t.Fatal("rejected partial deletion changed retained work", err)
	}
	if err := s.DeleteFamily(t.Context(), []string{parent.ID, child.ID, target.ID}); err != nil {
		t.Fatal("complete joined retention could not expire its internal references", err)
	}
}
