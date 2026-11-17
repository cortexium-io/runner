package runner

import (
	"testing"
	"time"
)

func TestTaskMaterializationUsesCanonicalGraphBuilderAndRefusesAuthorityChanges(t *testing.T) {
	root := retainedTaskSpecification()
	root.ProjectID, root.Repository, root.Status = "demo", "/owned/repository", "Waiting"
	root.Process.Tasks = &TaskPolicy{Roles: []string{"developer"}}
	root.Roles = map[string]Role{"developer": {ID: "developer", Purpose: "Own assigned source", Inputs: map[string]string{}, Outputs: map[string]string{}, Execution: &Profile{Harness: "native", Access: "workspace-write", Model: "frozen-model"}}}
	root.Profiles = map[string]string{"developer": "default"}
	root.TaskGraph.Phase = TaskWaitingChildren
	link := root.TaskGraph.Children[0]
	initial := graphChild(root, root, link)
	initial.Revision, initial.Updated = 1, time.Now().UTC()
	if !TaskMaterialization(root, root, initial) {
		t.Fatal("actual builder did not consume its reservation")
	}
	for _, fault := range []string{"source", "model", "access", "profile", "inputs", "scope", "budget", "attempt", "result-only", "retired", "foreign-parent"} {
		t.Run(fault, func(t *testing.T) {
			parent, child := Clone(root), Clone(initial)
			switch fault {
			case "source":
				child.Repository = "/other/repository"
			case "model":
				child.Roles["developer"].Execution.Model = "different-model"
			case "access":
				child.Roles["developer"].Execution.Access = "input-only"
			case "profile":
				child.Profiles["developer"] = "other"
			case "inputs":
				child.Inputs = map[string]Value{"ungranted": Text("input")}
			case "scope":
				child.TaskGraph.WritePaths = []string{"."}
			case "budget":
				child.Process.MaxAttempts++
			case "attempt":
				child.Attempts = []Attempt{{ID: "already-started", State: Running}}
			case "result-only":
				parent.TaskGraph.Results = []TaskResultRef{{ID: link.Spec.ID, RunID: link.RunID}}
				parent.TaskGraph.Children = nil
			case "retired":
				parent.TaskGraph.Children[0].Retired = true
			case "foreign-parent":
				child.TaskGraph.ParentRunID = "foreign"
			}
			if TaskMaterialization(parent, parent, child) {
				t.Fatal("unrelated or altered authority consumed an allocation")
			}
		})
	}
	root.CancelRequested = true
	root.TaskGraph.Children[0].Retired = true
	cancelled := cancelledGraphChild(root, root, root.TaskGraph.Children[0])
	cancelled.Revision, cancelled.Updated = 1, time.Now().UTC()
	if !TaskMaterialization(root, root, cancelled) {
		t.Fatal("retired allocation cancellation was stranded")
	}
	if TaskMaterialization(root, root, initial) {
		t.Fatal("cancellation waived queued retired reactivation")
	}
}

func TestTaskMaterializationUsesCanonicalIsolatedGroupBuilder(t *testing.T) {
	_, root := familyBudgetFixture()
	root.Status = "Waiting"
	group := root.TaskGroups["cards"]
	group.Started = time.Now().UTC()
	child := childForItem(root, group, group.Items[0], group.Children[0].RunID)
	child.Revision, child.Updated = 1, time.Now().UTC()
	if !TaskMaterialization(root, root, child) {
		t.Fatal("owned isolated child did not consume its existing reservation")
	}
	for _, fault := range []string{"source", "scope", "group", "identity", "input", "role", "done", "mode"} {
		t.Run(fault, func(t *testing.T) {
			parent, bad := Clone(root), Clone(child)
			switch fault {
			case "source":
				bad.Source.Ref = "different-source"
			case "scope":
				bad.Repository = "/different/repository"
			case "group":
				bad.Task.Identity.GroupStep = "foreign"
			case "identity":
				bad.Task.Identity.ItemID = "two"
			case "input":
				bad.Inputs["goal"] = Text("different goal")
			case "role":
				role := bad.Roles["owner"]
				role.Purpose = "different-role"
				bad.Roles["owner"] = role
			case "done":
				parent.TaskGroups["cards"].Done = true
			case "mode":
				parent.Process.Steps[parent.Step].ForEach.Mode = "serial"
			}
			if TaskMaterialization(parent, parent, bad) {
				t.Fatal("isolated child authority changed")
			}
		})
	}
	root.CancelRequested = true
	identity := childForItem(root, group, group.Items[0], group.Children[0].RunID)
	if identity.CancelRequested || identity.Status != "Waiting" {
		t.Fatal("current cancellation changed retained child identity reconstruction")
	}
	cancelled := initialTaskChild(root, group, group.Items[0], group.Children[0].RunID)
	cancelled.Revision, cancelled.Updated = 1, time.Now().UTC()
	if !TaskMaterialization(root, root, cancelled) || cancelled.Status != "Cancelled" {
		t.Fatal("isolated cancellation did not consume its owned allocation")
	}
	root.CancelRequested, group.Failure = false, "group is stopping after a child failure"
	identity = childForItem(root, group, group.Items[0], group.Children[0].RunID)
	if identity.CancelRequested || identity.Status != "Waiting" {
		t.Fatal("settled group failure changed retained child identity reconstruction")
	}
	cancelled = initialTaskChild(root, group, group.Items[0], group.Children[0].RunID)
	cancelled.Revision, cancelled.Updated = 1, time.Now().UTC()
	if !TaskMaterialization(root, root, cancelled) || cancelled.Status != "Cancelled" {
		t.Fatal("failed group cancellation lost its existing allocation")
	}
}
