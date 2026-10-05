package github

import (
	"reflect"
	"strings"
	"testing"
)

func TestUnstartedDeliveryMemberReauthorizationBindsExactRelease(t *testing.T) {
	for _, change := range []string{"valid", "body", "profile", "sibling authority", "parent authority", "missing parent", "cancelled", "review history", "published", "wrong phase", "closed", "draft", "standalone"} {
		t.Run(change, func(t *testing.T) {
			p, parent, children := deliveryFixture(t)
			p.cfg.AssessmentStatus = "Needs assessment"
			p.cfg.IntakeLabel = "needs-assessment"
			item := children[1]
			item.Status, item.Approval, item.Phase = "Needs assessment", "", "ready"
			item.URL, item.IssueState = "https://github.com/owner/repo/issues/999", "OPEN"
			item.Activity, item.Labels = "Amended — affected work requires acceptance", []string{"needs-assessment", "keep-label"}
			switch change {
			case "body":
				item.Body += "unapproved scope"
			case "profile":
				item.ImplementationProfile = "other"
			case "sibling authority":
				children[0].Approval = ""
			case "parent authority":
				parent.Approval = ""
			case "missing parent":
				parent.ID = "other"
			case "cancelled":
				parent.Phase = PlanCancelledPhase
				parent = signDeliveryFixture(t, p, parent, "planner", "backlog")
			case "review history":
				item.QAFailures = 1
			case "published":
				item.PullRequest = "https://github.com/owner/repo/pull/3"
			case "wrong phase":
				item.Phase = "agent_qa"
			case "closed":
				item.IssueState = "CLOSED"
			case "draft":
				item.DraftContentID = "draft"
			case "standalone":
				item.PlanningSourceID = ""
			}
			items := []WorkItem{parent, children[0], item}
			before := append([]WorkItem(nil), items...)
			plan, err := p.planReauthorization(item, items)
			if change != "valid" {
				if err == nil {
					t.Fatal("invalid or changed member acquired new authority")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Unstarted || !plan.RemoveIntakeLabel || plan.PlanRevision != PlanRevision(parent.Body) || plan.next.Item.Status != p.readyStatus() || !reflect.DeepEqual(plan.next.Item.Labels, []string{"keep-label"}) {
				t.Fatal("preview lost exact release, unstarted boundary or label consumption")
			}
			if !reflect.DeepEqual(items, before) {
				t.Fatal("preview mutated existing state")
			}
			if _, err := p.validateAction(plan.next.Item); err != nil {
				t.Fatal(err)
			}
			if _, err := p.ValidatePlanDelivery(parent, []WorkItem{parent, children[0], plan.next.Item}); err != nil {
				t.Fatal(err)
			}
			if plan.next.Item.Branch != "" || plan.next.Item.QACommit != "" || plan.next.Item.QAFailures != 0 || !strings.Contains(plan.Result, "unstarted") {
				t.Fatal("recovery fabricated implementation or review evidence")
			}
		})
	}
}
