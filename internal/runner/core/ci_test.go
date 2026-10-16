package core

import "testing"

func TestRequiredCIEvidenceCannotBeInferredFromGreenJobs(t *testing.T) {
	req := CIRequirement{Kind: "check", Name: "test", AppID: 15368}
	pass := RemoteCheck{CIRequirement: req, Status: "COMPLETED", Conclusion: "SUCCESS"}
	for _, tc := range []struct {
		name, want string
		checks     []RemoteCheck
	}{
		{"passed", "passed", []RemoteCheck{pass}},
		{"missing", "missing", nil},
		{"unrelated green", "missing", []RemoteCheck{{CIRequirement: CIRequirement{Kind: "check", Name: "other", AppID: 15368}, Status: "COMPLETED", Conclusion: "SUCCESS"}}},
		{"wrong producer", "missing", []RemoteCheck{{CIRequirement: CIRequirement{Kind: "check", Name: "test", AppID: 42}, Status: "COMPLETED", Conclusion: "SUCCESS"}}},
		{"duplicate", "unavailable", []RemoteCheck{pass, pass}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, results := AssessCI([]CIRequirement{req}, tc.checks)
			if state != tc.want || len(results) != 1 || results[0].State != tc.want {
				t.Fatal(state, results)
			}
		})
	}
	for _, status := range []string{"QUEUED", "IN_PROGRESS", "PENDING", "REQUESTED", "WAITING"} {
		c := pass
		c.Status, c.Conclusion = status, ""
		if state, _ := AssessCI([]CIRequirement{req}, []RemoteCheck{c}); state != "pending" {
			t.Fatal(status, state)
		}
	}
	for _, conclusion := range []string{"SUCCESS", "SKIPPED", "NEUTRAL", "FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STALE", "STARTUP_FAILURE", "NEW_VALUE", ""} {
		want := "failed"
		if conclusion == "SUCCESS" || conclusion == "SKIPPED" || conclusion == "NEUTRAL" {
			want = "passed"
		}
		if conclusion == "NEW_VALUE" || conclusion == "" {
			want = "unavailable"
		}
		c := pass
		c.Conclusion = conclusion
		if state, results := AssessCI([]CIRequirement{req}, []RemoteCheck{c}); state != want || results[0].Conclusion != conclusion {
			t.Fatal(conclusion, state)
		}
	}
	legacy := CIRequirement{Kind: "status", Name: "legacy"}
	for _, tc := range []struct{ status, want string }{{"SUCCESS", "passed"}, {"PENDING", "pending"}, {"FAILURE", "failed"}, {"ERROR", "failed"}, {"NEW_VALUE", "unavailable"}} {
		state, _ := AssessCI([]CIRequirement{req, legacy}, []RemoteCheck{pass, {CIRequirement: legacy, Status: tc.status}})
		if state != tc.want {
			t.Fatal(tc, state)
		}
	}
	if state, _ := AssessCI(nil, []RemoteCheck{pass}); state != "unconfigured" {
		t.Fatal("empty policy passed")
	}
}

func TestCIRepairRequiresCurrentCompleteExplicitFailure(t *testing.T) {
	candidate := Candidate{Commit: "head"}
	task := Task{ID: "task", Versions: []Version{{Number: 1, Quality: "quality"}}, Approval: &Approval{Base: "base"}, Stage: AwaitingDelivery, Candidate: &candidate, Review: []Check{{ID: "proof", Status: "pass", Evidence: "accepted"}}}
	task.BaseProof = &BaseProof{Base: "base", Candidate: candidate, Quality: "quality", ReviewDigest: Digest(task.Review)}
	failed := CIResult{RemoteCheck: RemoteCheck{CIRequirement: CIRequirement{Kind: "check", Name: "test", AppID: 1}, Status: "COMPLETED", Conclusion: "FAILURE"}, State: "failed"}
	for _, scenario := range []string{"failure", "legacy failure", "pending", "missing", "unavailable", "cancelled", "timeout", "startup", "action required", "status error", "new proof", "queued read", "stopped"} {
		t.Run(scenario, func(t *testing.T) {
			current := task
			p := Publication{State: "published", Intent: PublicationIntent{TaskID: "task", Version: 1, ApprovedBase: "base", Candidate: candidate, Quality: "quality", ReviewDigest: Digest(task.Review)}, Observation: &RemotePublication{Number: 1, State: "OPEN", Draft: true, BranchCommit: "head"}, CI: &CIAssessment{Proof: *task.BaseProof, State: "failed", Results: []CIResult{failed}}}
			switch scenario {
			case "legacy failure", "status error":
				p.CI.Results[0].Kind, p.CI.Results[0].Status = "status", "FAILURE"
				if scenario == "status error" {
					p.CI.Results[0].Status = "ERROR"
				}
			case "pending", "missing", "unavailable":
				p.CI.Results = append(p.CI.Results, CIResult{State: scenario})
			case "cancelled", "timeout", "startup", "action required":
				p.CI.Results[0].Conclusion = map[string]string{"cancelled": "CANCELLED", "timeout": "TIMED_OUT", "startup": "STARTUP_FAILURE", "action required": "ACTION_REQUIRED"}[scenario]
			case "new proof":
				p.CI.Proof.Base = "old"
			case "queued read":
				p.CIRequest = task.BaseProof
			case "stopped":
				current.Stage = Stopped
			}
			want := scenario == "failure" || scenario == "legacy failure"
			if p.CanRepairCI(current) != want {
				t.Fatal("wrong repair admission", scenario)
			}
		})
	}
}
