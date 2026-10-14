package core

import "testing"

func TestVersionAndPreviewBindDifferentInputs(t *testing.T) {
	body := "## Outcome\nBehavior\n## Scope\nOne file\n## Acceptance\nCorrect result\n## Verification\nExisting suite, expected result, public boundary\n"
	v, err := NewVersion(1, body, "repo", "policy", "quality")
	if err != nil {
		t.Fatal(err)
	}
	task := Task{ID: "task", Versions: []Version{v}}
	first := Preview(task, "repo", "policy", "quality", "base")
	for name, p := range map[string]string{"repository": Preview(task, "other", "policy", "quality", "base"), "quality": Preview(task, "repo", "policy", "new", "base"), "base": Preview(task, "repo", "policy", "quality", "new")} {
		if p == first {
			t.Fatalf("%s omitted from approval", name)
		}
	}
	if v.Digest == first {
		t.Fatal("content and approval digest conflated")
	}
	if _, err = NewVersion(2, "## Outcome\nOnly outcome", "repo", "policy", "quality"); err == nil {
		t.Fatal("incomplete task accepted")
	}
	if _, err = NewVersion(2, body+"## Dependencies\nother", "repo", "policy", "quality"); err == nil {
		t.Fatal("unsupported dependencies accepted")
	}
}
func TestProofCompletenessAndClassification(t *testing.T) {
	proofs := []Proof{{ID: "change"}, {ID: "verification"}}
	good := Result{Outcome: "succeeded", Summary: "Evidence inspected", Checks: []Check{{ID: "change", Status: "pass", Evidence: "specific evidence"}, {ID: "verification", Status: "pass", Evidence: "command passed"}}}
	if err := ValidateResult(good, proofs); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"pass", "gap", "defect", "needs_input"} {
		checks := []Check{{Status: "gap"}, {Status: status}}
		expected := map[string]string{"pass": "gap", "gap": "gap", "defect": "defect", "needs_input": "needs_input"}[status]
		if got := ReviewOutcome(checks); got != expected {
			t.Fatalf("%s: %s", status, got)
		}
	}
	for _, checks := range [][]Check{good.Checks[:1], {good.Checks[0], good.Checks[0]}, {{ID: "change", Status: "pass", Evidence: ""}, good.Checks[1]}, {{ID: "change", Status: "accepted", Evidence: "claim"}, good.Checks[1]}} {
		bad := good
		bad.Checks = checks
		if ValidateResult(bad, proofs) == nil {
			t.Fatal("incomplete or malformed proof accepted")
		}
	}
}
