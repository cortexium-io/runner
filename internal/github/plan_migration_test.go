package github

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func legacyMigrationBatch(sourced bool) []WorkItem {
	parent := WorkItem{ID: "legacy-parent", Title: "Historical planning", Body: "Approved historical request", Status: "Done", Repository: "owner/repo"}
	children := []WorkItem{
		{ID: "legacy-one", Title: "First", Body: "First historical contract", Status: "Done", Repository: "owner/repo", Phase: "agent_qa", Approval: "stale-approval", Branch: "runner/first", PullRequest: "https://github.com/owner/repo/pull/1", QACommit: strings.Repeat("a", 40)},
		{ID: "legacy-two", Title: "Second", Body: "Second historical contract", Status: "Done", Repository: "owner/repo", Dependencies: []string{"legacy-one"}},
	}
	if !sourced {
		// The observed old CLI batch had nine Done members and no parent.
		for len(children) < 9 {
			id := fmt.Sprintf("legacy-%d", len(children)+1)
			children = append(children, WorkItem{ID: id, Title: id, Body: "Historical contract " + id, Status: "Done", Repository: "owner/repo"})
		}
	}
	for i := range children {
		child := &children[i]
		child.PlanningSourceLane, child.PlanningSourceFingerprint, child.PlanningDestination = "local_plan", "v1:original-request", "Ready"
		child.PlanningBatchFingerprint, child.PlanningBatchSize, child.PlanningItemIndex = "v1:legacy-batch", len(children), i+1
		if sourced {
			child.PlanningSourceID = parent.ID
			child.PlanningSourceFingerprint = PlanningSourceFingerprint(parent)
		}
	}
	if sourced {
		return append(children, parent)
	}
	return children
}

func legacyMigrationProject() *Project {
	return NewProject(config.ProjectConfig{GitHubProjectConfig: config.GitHubProjectConfig{Owner: "owner", Number: 1},
		DoneStatus: "Done", RunningStatus: "Implementing", AgentStatuses: []string{"Ready", "Agent QA"},
		LaneStatuses: map[string]string{"ready": "Ready", "agent_qa": "Agent QA", "done": "Done"}}, nil)
}

func TestDeliveryMigrationPreservesLegacyDoneWithoutGrantingAuthority(t *testing.T) {
	for _, sourced := range []bool{false, true} {
		name := "direct"
		if sourced {
			name = "retained parent"
		}
		t.Run(name, func(t *testing.T) {
			p, items := legacyMigrationProject(), legacyMigrationBatch(sourced)
			before := append([]WorkItem(nil), items...)
			if preserved, err := p.requireCompletedPreRolloutWork(items); err != nil || preserved != len(items) {
				t.Fatalf("inert historical batch prevented migration: %v", err)
			}
			if !reflect.DeepEqual(items, before) {
				t.Fatal("historical approval, phase, proof or status changed")
			}
			if p.hasSuccessfulOutcome(items[0], items) || p.dependenciesSatisfied(WorkItem{ID: "external", Dependencies: []string{items[0].ID}}, items) {
				t.Fatal("administrative preservation granted execution/dependency success")
			}
			if sourced && p.planningSourceWorkCompleted(items[2], items) {
				t.Fatal("historical planning was relabeled authenticated delivery")
			}
		})
	}
}

func TestDeliveryMigrationRefusesIncompleteLegacyTopology(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]WorkItem) []WorkItem
	}{
		{"nonterminal child", func(items []WorkItem) []WorkItem { items[0].Status = "Agent QA"; return items }},
		{"transition locked", func(items []WorkItem) []WorkItem { items[0].Transition = "in-flight"; return items }},
		{"nonterminal parent", func(items []WorkItem) []WorkItem { items[2].Status = "Plan"; return items }},
		{"missing parent", func(items []WorkItem) []WorkItem { return items[:2] }},
		{"missing member", func(items []WorkItem) []WorkItem { return items[1:] }},
		{"missing entire released batch", func(items []WorkItem) []WorkItem {
			items[2].Approval = "batch-v1:retained-stale-release"
			return items[2:]
		}},
		{"duplicate index", func(items []WorkItem) []WorkItem { items[1].PlanningItemIndex = 1; return items }},
		{"duplicate identity", func(items []WorkItem) []WorkItem { items[1].ID = items[0].ID; return items }},
		{"changed cardinality", func(items []WorkItem) []WorkItem { items[1].PlanningBatchSize = 3; return items }},
		{"changed batch", func(items []WorkItem) []WorkItem { items[1].PlanningBatchFingerprint = "v1:different"; return items }},
		{"changed source", func(items []WorkItem) []WorkItem { items[1].PlanningSourceFingerprint = "v1:different"; return items }},
		{"changed parent", func(items []WorkItem) []WorkItem { items[2].Body = "Different request"; return items }},
		{"malformed metadata", func(items []WorkItem) []WorkItem { items[1].PlanningMetadataInvalid = true; return items }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := legacyMigrationProject().requireCompletedPreRolloutWork(tc.change(legacyMigrationBatch(true))); err == nil {
				t.Fatal("incomplete or active historical batch accepted")
			}
		})
	}
}

func TestDeliveryMigrationNeverDowngradesNewPlanAuthority(t *testing.T) {
	for _, released := range []bool{false, true} {
		p, parent, children := deliveryFixture(t)
		parent.Status, parent.Approval = "Done", "stale-approval"
		var items []WorkItem
		if !released {
			parent.PlanRelease = ""
			// An ordinary valid Done signature is not a released delivery
			// contract and cannot excuse missing manifest members.
			parent = signDeliveryFixture(t, p, parent, "reviewer", "done")
			items = []WorkItem{parent}
		} else {
			for i := range children {
				children[i].Status = "Done"
			}
			items = append(children, parent)
		}
		if _, err := p.requireCompletedPreRolloutWork(items); err == nil {
			t.Fatal("new delivery contract treated as inert legacy history")
		}
	}
}
