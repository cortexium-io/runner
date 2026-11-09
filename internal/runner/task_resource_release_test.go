package runner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type taskResourceArtifactStore struct {
	Store
	artifact Artifact
	err      error
}

func (s *taskResourceArtifactStore) Artifact(_ context.Context, id string) (Artifact, error) {
	if s.err == nil && s.artifact.ID != id {
		return Artifact{}, errors.New("unknown task artifact")
	}
	return s.artifact, s.err
}

func acceptedTaskResourceFixture(t *testing.T) (*Core, Run, *taskResourceArtifactStore) {
	t.Helper()
	c, root := familyBudgetFixture()
	c.ctx = t.Context()
	root.Status = "Running"
	child := c.runs["child-one"]
	child.Status = "Completed"
	for i := range child.Attempts {
		child.Attempts[i] = Attempt{
			ID:       "attempt_" + string(rune('a'+i)),
			State:    Finished,
			Released: true,
			End:      &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready"}},
		}
	}
	candidate := TaskCandidate{
		Identity:   child.Task.Identity,
		RunID:      child.ID,
		Repository: child.Repository,
		Base:       child.Source.Ref,
		Commit:     strings.Repeat("b", 40),
		Tree:       strings.Repeat("c", 40),
		PatchID:    "attempt_b/changes.patch",
	}
	data, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	child.Outputs = map[string]Value{"result": {Inline: data}}
	root.TaskGroups["cards"].Children[0].Candidate = &candidate
	c.runs[root.ID], c.runs[child.ID] = root, child
	store := &taskResourceArtifactStore{artifact: Artifact{ID: candidate.PatchID, RunID: child.ID, ProjectID: child.ProjectID}}
	c.store = store
	return c, child, store
}

func TestTaskResourcesReleaseAcceptedCompletedChildBeforeParentTerminates(t *testing.T) {
	for _, status := range []string{"Queued", "Running", "Waiting"} {
		t.Run(status, func(t *testing.T) {
			c, child, _ := acceptedTaskResourceFixture(t)
			root := c.runs[child.Task.Identity.RootRunID]
			root.Status = status
			if status == "Waiting" {
				root.Decision = &Decision{ID: "merge-decision", Step: "prepare", Next: "merge", Outcome: "ready"}
			}
			c.runs[root.ID] = root
			before, err := json.Marshal(c.runs)
			if err != nil {
				t.Fatal(err)
			}
			if c.holdTaskResources(child) {
				t.Fatal("accepted completed child retained its full checkout until the parent terminated", status)
			}
			after, err := json.Marshal(c.runs)
			if err != nil || string(before) != string(after) || child.ResourcesReleased || c.runs[root.ID].ResourcesReleased {
				t.Fatal("release eligibility changed attempts, acceptance, history or resource state", err)
			}
		})
	}
}

func TestTaskResourcesHoldChildWithoutSettledAuthenticatedAcceptance(t *testing.T) {
	for _, fault := range []string{
		"not accepted", "parent missing", "group missing", "ambiguous reservation", "undeclared item", "source differs", "parent unknown",
		"failed", "cancelled", "waiting", "cancellation requested", "unreleased", "unknown execution", "malformed result", "artifact result",
		"different commit", "different tree", "different patch", "different identity", "invalid commit", "unknown artifact", "foreign artifact run", "foreign artifact project",
	} {
		t.Run(fault, func(t *testing.T) {
			c, child, store := acceptedTaskResourceFixture(t)
			root := c.runs[child.Task.Identity.RootRunID]
			group := root.TaskGroups["cards"]
			switch fault {
			case "not accepted":
				group.Children[0].Candidate = nil
			case "parent missing":
				delete(c.runs, root.ID)
			case "group missing":
				delete(root.TaskGroups, "cards")
			case "ambiguous reservation":
				group.Children = append(group.Children, group.Children[0])
			case "undeclared item":
				group.Items = group.Items[1:]
			case "source differs":
				child.Source.Ref = strings.Repeat("d", 40)
			case "parent unknown":
				root.Status = "Unknown"
			case "failed":
				child.Status = "Failed"
			case "cancelled":
				child.Status = "Cancelled"
			case "waiting":
				child.Status = "Waiting"
			case "cancellation requested":
				child.CancelRequested = true
			case "unreleased":
				child.Attempts[0].Released = false
			case "unknown execution":
				child.Attempts[0].State = Unknown
			case "malformed result":
				child.Outputs["result"] = Value{Inline: json.RawMessage(`{`)}
			case "artifact result":
				child.Outputs["result"] = Value{ArtifactID: "attempt_b/result"}
			case "different commit":
				group.Children[0].Candidate.Commit = strings.Repeat("d", 40)
			case "different tree":
				group.Children[0].Candidate.Tree = strings.Repeat("d", 40)
			case "different patch":
				group.Children[0].Candidate.PatchID = "attempt_other/changes.patch"
			case "different identity":
				group.Children[0].Candidate.Identity.ItemID = "two"
			case "invalid commit":
				candidate := *group.Children[0].Candidate
				candidate.Commit = strings.Repeat("z", 40)
				raw, err := json.Marshal(candidate)
				if err != nil {
					t.Fatal(err)
				}
				child.Outputs["result"] = Value{Inline: raw}
				group.Children[0].Candidate = &candidate
			case "unknown artifact":
				store.err = errors.New("artifact ownership unavailable")
			case "foreign artifact run":
				store.artifact.RunID = "foreign"
			case "foreign artifact project":
				store.artifact.ProjectID = "foreign"
			}
			if fault != "parent missing" {
				c.runs[root.ID] = root
			}
			c.runs[child.ID] = child
			if !c.holdTaskResources(child) {
				t.Fatal("ambiguous or unfinished child obtained early source cleanup", fault)
			}
		})
	}
}

func TestTaskResourcesRequirePersistedParentPinAndPreserveTerminalCleanup(t *testing.T) {
	c, child, _ := acceptedTaskResourceFixture(t)
	root := c.runs[child.Task.Identity.RootRunID]
	staged := Clone(root)
	root.TaskGroups["cards"].Children[0].Candidate = nil
	c.runs[root.ID] = root
	if staged.TaskGroups["cards"].Children[0].Candidate == nil || !c.holdTaskResources(child) {
		t.Fatal("unpersisted parent acceptance authorized child cleanup")
	}
	// Failed/unaccepted children still use ordinary family cleanup once the
	// owning parent has terminated; the early-release exception cannot leak them.
	root.Status, child.Status = "Failed", "Failed"
	c.runs[root.ID] = root
	if c.holdTaskResources(child) {
		t.Fatal("terminal parent no longer releases settled failed child resources")
	}
	root.ResourcesReleased = false
	root.TaskGroups["cards"].Children = root.TaskGroups["cards"].Children[:1]
	c.runs[child.ID] = child
	if !c.holdTaskResources(root) {
		t.Fatal("parent cleanup no longer waits for child source release")
	}
	child.ResourcesReleased = true
	c.runs[child.ID] = child
	if c.holdTaskResources(root) {
		t.Fatal("released child prevented ordinary parent cleanup")
	}
}
