package runner

import (
	"encoding/json"
	"strings"
	"testing"
)

func familyBudgetFixture() (*Core, Run) {
	root := Run{
		ID: "root", ProjectID: "demo", RecordFormat: 3,
		Repository: "/owned/repository", Inputs: map[string]Value{},
		Process: Process{ID: "planned", Inputs: map[string]string{}, MaxAttempts: 64, MaxAttemptsPerStep: 1, AttemptTimeout: "20m", Steps: []Step{{ID: "cards", ForEach: &TaskGroup{
			Mode: "isolated", Inputs: map[string]string{"goal": "text"}, Steps: []Step{{ID: "owner", Role: "owner"}},
		}}}},
		Roles: map[string]Role{"owner": {ID: "owner"}}, Profiles: map[string]string{},
		Attempts: make([]Attempt, 2),
	}
	group := &TaskGroupState{
		StepID: "cards", AttemptID: "group-attempt", Repository: root.Repository, Base: strings.Repeat("a", 40),
		Items:    []TaskItem{{ID: "one", Inputs: map[string]json.RawMessage{"goal": json.RawMessage(`"first"`)}}, {ID: "two", Inputs: map[string]json.RawMessage{"goal": json.RawMessage(`"second"`)}}},
		Children: []TaskChild{{ItemID: "one", RunID: "child-one"}, {ItemID: "two", RunID: "child-two"}},
	}
	root.TaskGroups = map[string]*TaskGroupState{"cards": group}
	one := childForItem(root, group, group.Items[0], "child-one")
	two := childForItem(root, group, group.Items[1], "child-two")
	one.Attempts, two.Attempts = make([]Attempt, 3), make([]Attempt, 5)
	return &Core{runs: map[string]Run{root.ID: root, one.ID: one, two.ID: two}}, root
}

func TestFamilyBudgetSharesOneRootCeilingWithoutMutation(t *testing.T) {
	c, root := familyBudgetFixture()
	before, _ := json.Marshal(c.runs)
	for _, r := range []Run{root, c.runs["child-one"], c.runs["child-two"]} {
		if used, remaining := c.familyAttempts(r), c.familyRemaining(r); used != 10 || remaining != 54 {
			t.Fatal("task got a separate budget", r.ID, used, remaining)
		}
		if err := c.familyReserve(r, 1, 53); err != nil {
			t.Fatal("exact aggregate reserve rejected", err)
		}
		if err := c.familyReserve(r, 1, 54); err == nil {
			t.Fatal("child dispatch consumed reserved root completion")
		}
	}
	child := Clone(c.runs["child-one"])
	child.Attempts = append(child.Attempts, Attempt{})
	if used := c.familyAttempts(child); used != 11 {
		t.Fatal("new child attempt was absent before persistence", used)
	}
	root.Attempts = append(root.Attempts, Attempt{})
	if used := c.familyAttempts(root); used != 11 {
		t.Fatal("new root attempt was absent before persistence", used)
	}
	after, _ := json.Marshal(c.runs)
	if string(before) != string(after) {
		t.Fatal("budget evaluation changed frozen run state")
	}
}

func TestFamilyBudgetPreservesLegacyAndEffectiveAllowances(t *testing.T) {
	r := Run{ID: "legacy", Process: Process{MaxAttempts: 35}, Attempts: make([]Attempt, 28)}
	c := &Core{}
	if c.familyAttempts(r) != 28 || c.familyRemaining(r) != 7 || c.familyReserve(r, 1, 6) != nil || c.familyReserve(r, 1, 7) == nil {
		t.Fatal("legacy35-attempt allowance changed")
	}
	r.Process.MaxAttempts = 39 // An existing separately authorized Retry grant.
	if c.familyRemaining(r) != 11 || c.familyReserve(r, 1, 10) != nil {
		t.Fatal("effective authorized total was ignored")
	}
	for _, costs := range [][2]int{{-1, 0}, {0, -1}, {12, 0}} {
		if c.familyReserve(r, costs[0], costs[1]) == nil {
			t.Fatal("invalid reserve accepted", costs)
		}
	}
}

func TestFamilyBudgetFailsClosedOnInvalidOwnership(t *testing.T) {
	for _, fault := range []string{"parent", "project", "root-link", "item-link", "source", "duplicate", "orphan", "group", "undurable"} {
		t.Run(fault, func(t *testing.T) {
			c, root := familyBudgetFixture()
			child := Clone(c.runs["child-one"])
			switch fault {
			case "parent":
				delete(c.runs, root.ID)
			case "project":
				child.ProjectID = "foreign"
			case "root-link":
				child.Task.Identity.RootRunID = "foreign"
			case "item-link":
				child.Task.Identity.ItemID = "undeclared"
			case "source":
				child.Source.Ref = strings.Repeat("b", 40)
			case "duplicate":
				root.TaskGroups["cards"].Children = append(root.TaskGroups["cards"].Children, root.TaskGroups["cards"].Children[0])
			case "orphan":
				orphan := Clone(child)
				orphan.ID = "orphan"
				c.runs[orphan.ID] = orphan
			case "group":
				root.TaskGroups["cards"].StepID = "foreign"
			case "undurable":
				root = Clone(root)
				root.TaskGroups["cards"].Children = append(root.TaskGroups["cards"].Children, TaskChild{ItemID: "third", RunID: "not-reserved"})
			}
			if fault == "parent" {
				root = child
			} else {
				c.runs[child.ID] = child
			}
			if used := c.familyAttempts(root); used != invalidFamilyAttempts || c.familyRemaining(root) != 0 || c.familyReserve(root, 0, 0) == nil {
				t.Fatal("invalid membership granted budget", used)
			}
		})
	}
}

func TestFamilyBudgetAcceptsOnlyUnfinishedDurableChildReservations(t *testing.T) {
	c, root := familyBudgetFixture()
	delete(c.runs, "child-two")
	if used := c.familyAttempts(root); used != 5 {
		t.Fatal("undispatched durable reservation was treated as execution", used)
	}
	root.TaskGroups["cards"].Children[1].Candidate = &TaskCandidate{RunID: "child-two"}
	if c.familyReserve(root, 0, 0) == nil {
		t.Fatal("missing already-pinned child lost its consumed allowance")
	}
	root.TaskGroups["cards"].Children[1].Candidate = nil
	root.TaskGroups["cards"].Done = true
	if c.familyReserve(root, 0, 0) == nil {
		t.Fatal("completed group silently lost child history")
	}
}

func TestFamilyBudgetRejectsReservedChildWithoutItsParentLink(t *testing.T) {
	c, _ := familyBudgetFixture()
	child := Clone(c.runs["child-one"])
	child.Task = nil
	c.runs[child.ID] = child
	if c.familyAttempts(child) != invalidFamilyAttempts || c.familyRemaining(child) != 0 || c.familyReserve(child, 1, 0) == nil {
		t.Fatal("reserved child obtained a new standalone allowance")
	}
}
