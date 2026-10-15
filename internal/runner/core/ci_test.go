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
