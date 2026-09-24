package github

import (
	"reflect"
	"strings"
	"testing"
)

func completedDeliveryFixture(t *testing.T) (*Project, []WorkItem) {
	t.Helper()
	p, parent, children := deliveryFixture(t)
	children[1].Status, children[1].Phase = "Backlog", PlanIntegratedPhase
	children[1].Branch, children[1].QACommit = "runner/b", strings.Repeat("b", 40)
	children[1] = signDeliveryFixture(t, p, children[1], "reviewer", "backlog")
	parent.Status, parent.Phase = "Done", ""
	parent.PullRequest, parent.QACommit = "https://github.com/owner/repo/pull/10", strings.Repeat("c", 40)
	parent = signDeliveryFixture(t, p, parent, "reviewer", "done")
	return p, append([]WorkItem{parent}, children...)
}

func TestCompletedPlanSurvivesIssueClosureAndProfileEvolution(t *testing.T) {
	for _, tc := range []struct {
		name            string
		doneChildren    int
		changedProfiles bool
	}{
		{"original policy", 0, false},
		{"partial issue closure", 1, false},
		{"issue closure moves children to Done", 2, false},
		{"removed implementation profile", 0, true},
		{"closed issues and new policy", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, items := completedDeliveryFixture(t)
			for i := 1; i <= tc.doneChildren; i++ {
				// GitHub automation changes only the visible lane, not Runner's
				// signed integration record. No signature is renewed here.
				items[i].Status = "Done"
			}
			if tc.changedProfiles {
				p.cfg.PlanProfileDigests = map[string]string{"replacement": "v1:" + strings.Repeat("f", 64)}
			}
			before := append([]WorkItem(nil), items...)
			if !p.planningSourceWorkCompleted(items[0], items) {
				t.Fatal("authenticated completed plan lost delivery proof")
			}
			for _, item := range items {
				if !p.hasSuccessfulOutcome(item, items) || !p.dependenciesSatisfied(WorkItem{ID: "external", Dependencies: []string{item.ID}}, items) {
					t.Fatalf("completed outcome %s no longer satisfies external dependencies", item.ID)
				}
			}
			if _, err := p.requireCompletedPreRolloutWork(items); err != nil {
				t.Fatalf("completed plan blocks rollout: %v", err)
			}
			if got := p.PlanningProgress(items); len(got.IntegratedUndelivered) != 0 || len(got.PlanningCompleted) != 0 {
				t.Fatal("delivered work reported as unfinished or historical planning")
			}
			if !reflect.DeepEqual(items, before) {
				t.Fatal("completion inspection rewrote retained authority or history")
			}
			if tc.doneChildren > 0 || tc.changedProfiles {
				if _, err := p.ValidatePlanDelivery(items[0], items); err == nil {
					t.Fatal("historical completion relaxed current execution authority")
				}
			}
		})
	}
}

func TestCompletedPlanRefusesChangedAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Project, []WorkItem) []WorkItem
	}{
		{"parent not merged", func(p *Project, items []WorkItem) []WorkItem {
			items[0].Status = "Agent QA"
			items[0] = signDeliveryFixture(t, p, items[0], "reviewer", "agent_qa")
			return items
		}},
		{"hand-moved parent Done", func(p *Project, items []WorkItem) []WorkItem {
			items[0] = signDeliveryFixture(t, p, items[0], "reviewer", "agent_qa")
			return items
		}},
		{"signed incomplete child", func(p *Project, items []WorkItem) []WorkItem {
			items[1].Status, items[1].Phase = "Ready", ""
			items[1] = signDeliveryFixture(t, p, items[1], "implementer", "ready")
			items[1].Status = "Done"
			return items
		}},
		{"child signed Done instead of integrated", func(p *Project, items []WorkItem) []WorkItem {
			items[1] = signDeliveryFixture(t, p, items[1], "reviewer", "done")
			return items
		}},
		{"parent PR from another repository", func(p *Project, items []WorkItem) []WorkItem {
			items[0].PullRequest = "https://github.com/owner/other/pull/10"
			items[0] = signDeliveryFixture(t, p, items[0], "reviewer", "done")
			return items
		}},
		{"nonterminal parent phase", func(p *Project, items []WorkItem) []WorkItem {
			items[0].Phase = PlanRepairingPhase
			items[0] = signDeliveryFixture(t, p, items[0], "reviewer", "done")
			return items
		}},
		{"missing parent", func(_ *Project, items []WorkItem) []WorkItem { return items[1:] }},
		{"duplicate parent", func(_ *Project, items []WorkItem) []WorkItem { return append(items, items[0]) }},
		{"missing child", func(_ *Project, items []WorkItem) []WorkItem { return items[:2] }},
		{"duplicate child", func(_ *Project, items []WorkItem) []WorkItem { return append(items, items[1]) }},
		{"new shared contract with fresh lifecycle signature", func(p *Project, items []WorkItem) []WorkItem {
			items[0].Body = strings.Replace(items[0].Body, "One complete outcome", "Changed outcome", 1)
			items[0] = signDeliveryFixture(t, p, items[0], "reviewer", "done")
			return items
		}},
		{"revoked release", func(_ *Project, items []WorkItem) []WorkItem { items[0].PlanRelease = ""; return items }},
		{"revoked parent", func(_ *Project, items []WorkItem) []WorkItem { items[0].Approval = ""; return items }},
		{"changed parent PR", func(_ *Project, items []WorkItem) []WorkItem { items[0].PullRequest += "1"; return items }},
		{"changed parent candidate", func(_ *Project, items []WorkItem) []WorkItem {
			items[0].QACommit = strings.Repeat("d", 40)
			return items
		}},
		{"locked parent", func(_ *Project, items []WorkItem) []WorkItem { items[0].Transition = "pending"; return items }},
		{"revoked child", func(_ *Project, items []WorkItem) []WorkItem { items[1].Approval = ""; return items }},
		{"child content", func(_ *Project, items []WorkItem) []WorkItem { items[1].Body += " Changed"; return items }},
		{"child profile", func(_ *Project, items []WorkItem) []WorkItem {
			items[1].ImplementationProfile = "replacement"
			return items
		}},
		{"child dependency", func(_ *Project, items []WorkItem) []WorkItem { items[2].Dependencies = nil; return items }},
		{"child candidate", func(_ *Project, items []WorkItem) []WorkItem {
			items[1].QACommit = strings.Repeat("d", 40)
			return items
		}},
		{"child result", func(_ *Project, items []WorkItem) []WorkItem { items[1].Result = "Changed"; return items }},
		{"child failures", func(_ *Project, items []WorkItem) []WorkItem { items[1].QAFailures++; return items }},
		{"child branch", func(_ *Project, items []WorkItem) []WorkItem { items[1].Branch += "-changed"; return items }},
		{"child phase", func(_ *Project, items []WorkItem) []WorkItem { items[1].Phase = "agent_qa"; return items }},
		{"child reopened", func(_ *Project, items []WorkItem) []WorkItem { items[1].Status = "Ready"; return items }},
		{"child transition", func(_ *Project, items []WorkItem) []WorkItem { items[1].Transition = "pending"; return items }},
		{"child metadata", func(_ *Project, items []WorkItem) []WorkItem { items[1].PlanningMetadataInvalid = true; return items }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, items := completedDeliveryFixture(t)
			items[1].Status, items[2].Status = "Done", "Done"
			items = tc.change(p, items)
			p.cfg.PlanProfileDigests = nil
			if _, err := p.validateCompletedPlanDelivery(items[0], items); err == nil {
				t.Fatal("changed or incomplete authority retained terminal proof")
			}
			if _, err := p.requireCompletedPreRolloutWork(items); err == nil {
				t.Fatal("changed or incomplete plan allowed rollout")
			}
			for _, item := range items {
				if p.hasSuccessfulOutcome(item, items) {
					t.Fatalf("invalid plan granted dependency success to %s", item.ID)
				}
			}
		})
	}
}

func TestCompletedProofCannotRenewExecutionPolicy(t *testing.T) {
	for _, change := range []string{"profile changed", "profile removed", "gate changed", "disabled"} {
		t.Run(change, func(t *testing.T) {
			p, items := completedDeliveryFixture(t)
			switch change {
			case "profile changed":
				p.cfg.PlanProfileDigests["implementer"] = "v1:" + strings.Repeat("f", 64)
			case "profile removed":
				p.cfg.PlanProfileDigests = nil
			case "gate changed":
				p.cfg.PlanVerificationDigest = "v1:" + strings.Repeat("f", 64)
			case "disabled":
				p.cfg.PlanDelivery = false
			}
			if _, err := p.validateCompletedPlanDelivery(items[0], items); err != nil {
				t.Fatalf("current policy invalidated intact historical completion: %v", err)
			}
			if _, err := p.ValidatePlanDelivery(items[0], items); err == nil {
				t.Fatal("historical proof admitted execution under changed policy")
			}
			if _, err := p.validatePlanningBatch(items[0].PlanRelease, items[0], items[1:], batchReleasedState); err == nil {
				t.Fatal("ordinary batch validation bypassed current policy")
			}
			if _, err := p.signPlanningBatch(items[0], items[1:], batchReleasedState, "new"); err == nil {
				t.Fatal("historical proof allowed a new release under changed policy")
			}
		})
	}
}
