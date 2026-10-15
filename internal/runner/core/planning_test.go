package core

import (
	"strings"
	"testing"
)

func TestProposalRequiresCompleteCardsAndAClosedAcyclicGraph(t *testing.T) {
	body := "## Outcome\nGreeting\n## Scope\ngreet.go\n## Acceptance\nReturn hello\n## Verification\nExtend the existing greeting test; empty input returns hello.\n"
	for _, change := range []string{"valid", "missing key", "duplicate key", "cycle", "duplicate dependency", "missing verification"} {
		t.Run(change, func(t *testing.T) {
			p := PlanningResult{Cards: []PlannedCard{{Key: "base", Body: body}, {Key: "follow-up", Body: body, Dependencies: []string{"base"}}}}
			switch change {
			case "missing key":
				p.Cards[1].Dependencies = []string{"outside"}
			case "duplicate key":
				p.Cards[1].Key = "base"
			case "cycle":
				p.Cards[0].Dependencies = []string{"follow-up"}
			case "duplicate dependency":
				p.Cards[1].Dependencies = []string{"base", "base"}
			case "missing verification":
				p.Cards[0].Body = strings.Split(body, "## Verification")[0]
			}
			if err := ValidateProposal(p); (err == nil) != (change == "valid") {
				t.Fatalf("%s: %v", change, err)
			}
		})
	}
}

func TestPrerequisitesRequireExactIntegratedVersionAndParentAuthority(t *testing.T) {
	parent := Task{ID: "plan", Release: &PlanRelease{Children: []string{"child"}}, Stage: Ready}
	child := Task{ID: "child", ParentID: "plan", Versions: []Version{{Number: 1, Dependencies: []Dependency{{TaskID: "base", Version: 1}}}}}
	for _, state := range []Stage{AwaitingDelivery, Stopped, Completed} {
		base := Task{ID: "base", Stage: state, Versions: []Version{{Number: 1}}}
		if wait := PrerequisiteWait(child, []Task{parent, base}); (wait == "") != (state == Completed) {
			t.Fatalf("%s: %s", state, wait)
		}
		base.Versions[0].Number = 2
		if PrerequisiteWait(child, []Task{parent, base}) == "" {
			t.Fatal("replacement satisfied old prerequisite")
		}
	}
	child.Versions[0].Dependencies = nil
	parent.Stage = Stopped
	if PrerequisiteWait(child, []Task{parent}) == "" {
		t.Fatal("revoked parent admitted child")
	}
}

func TestPreparationCannotReturnReplacementCardsOrEmptyProof(t *testing.T) {
	for _, p := range []PlanningResult{{}, {Cards: []PlannedCard{{Key: "new-task"}}, Verification: "proof"}} {
		if ValidateRoleResult("prepare", Result{Outcome: "succeeded", Summary: "prepared", Planning: &p}, nil) == nil {
			t.Fatal("invalid preparation accepted")
		}
	}
	if ValidateRoleResult("plan", Result{Outcome: "needs_input", Summary: "question", Planning: &PlanningResult{}}, nil) == nil {
		t.Fatal("missing decision without a question accepted")
	}
}
