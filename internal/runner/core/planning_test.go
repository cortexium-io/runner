package core

import (
	"strings"
	"testing"
)

func TestProposalRequiresCompleteCardsAndAClosedAcyclicGraph(t *testing.T) {
	body := "## Outcome\nGreeting\n## Scope\ngreet.go\n## Acceptance\nReturn hello\n## Verification\nExtend the existing greeting test; empty input returns hello.\n"
	for _, change := range []string{"valid", "missing key", "duplicate key", "cycle", "duplicate dependency", "missing verification"} {
		t.Run(change, func(t *testing.T) {
			p := PlanningResult{Body: body, Cards: []PlannedCard{{Key: "base", Body: body}, {Key: "follow-up", Body: body, Dependencies: []string{"base"}}}}
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
	parent := Task{ID: "plan", Approval: &Approval{}, Release: &PlanRelease{Children: []string{"child"}}, Stage: Ready}
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

func TestIncludedRequiresExactOwnedReceiptAndLiveAuthority(t *testing.T) {
	for _, change := range []string{"valid", "missing receipt", "rejected write", "changed review", "changed candidate", "replacement", "revoked parent", "revoked child"} {
		t.Run(change, func(t *testing.T) {
			parent := Task{ID: "plan", Generation: 1, Stage: Ready, Approval: &Approval{Generation: 1}, Release: &PlanRelease{Preview: "release", Children: []string{"child"}}}
			c := Candidate{Commit: "accepted", Tree: "tree"}
			child := Task{ID: "child", ParentID: parent.ID, Generation: 1, Stage: AwaitingDelivery, Approval: &Approval{Generation: 1}, Versions: []Version{{Number: 1, Quality: "quality"}}, Candidate: &c, Review: []Check{{ID: "change", Status: "pass", Evidence: "verified"}}}
			child.Inclusion = &PlanInclusion{WriteID: "write", Release: "release", Version: 1, Generation: 1, Previous: "base", Candidate: c}
			child.RefWrites = []RefWrite{{ID: "write", Kind: "inclusion", State: "confirmed", Version: 1, Generation: 1, Release: "release", Old: "base", Commit: c.Commit, Proof: &BaseProof{Base: "base", Quality: "quality", Candidate: c, ReviewDigest: Digest(child.Review)}}}
			switch change {
			case "missing receipt":
				child.RefWrites = nil
			case "rejected write":
				child.RefWrites[0].State = "rejected"
			case "changed review":
				child.Review[0].Status = "gap"
			case "changed candidate":
				child.Candidate.Tree = "other"
			case "replacement":
				child.Versions[0].Number = 2
			case "revoked parent":
				parent.Generation++
			case "revoked child":
				child.Generation++
			}
			if Included(child, parent) != (change == "valid") {
				t.Fatal("incorrect inclusion", change)
			}
		})
	}
}
