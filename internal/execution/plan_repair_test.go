package execution

import (
	"encoding/json"
	"strings"
	"testing"
)

func repairContent(t *testing.T, targets any) string {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal([]byte(failingReviewerContent()), &data); err != nil {
		t.Fatal(err)
	}
	data["repair_targets"] = targets
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPlanRepairTargetsValidateOwnershipAndFailedStageKeys(t *testing.T) {
	valid := PlanRepairTarget{CheckKey: "P1", ItemID: "child-a", Finding: "Restore the approved ready behavior", InScope: true}
	for _, tc := range []struct {
		name       string
		targets    []PlanRepairTarget
		standalone bool
		valid      bool
	}{
		{"valid", []PlanRepairTarget{valid}, false, true},
		{"no routing", nil, false, true},
		{"advisory veto", []PlanRepairTarget{{CheckKey: "P1", ItemID: "child-a", Finding: "Needs a new scope decision", InScope: false}}, false, true},
		{"multiple owners", []PlanRepairTarget{valid, {CheckKey: "P1", ItemID: "child-b", Finding: "Repair integrated dependent use", InScope: true}}, false, true},
		{"foreign", []PlanRepairTarget{{CheckKey: "P1", ItemID: "foreign", Finding: "Repair", InScope: true}}, false, false},
		{"parent", []PlanRepairTarget{{CheckKey: "P1", ItemID: "parent", Finding: "Repair", InScope: true}}, false, false},
		{"passed", []PlanRepairTarget{{CheckKey: "P2", ItemID: "child-a", Finding: "Repair", InScope: true}}, false, false},
		{"unknown", []PlanRepairTarget{{CheckKey: "P30", ItemID: "child-a", Finding: "Repair", InScope: true}}, false, false},
		{"empty finding", []PlanRepairTarget{{CheckKey: "P1", ItemID: "child-a", InScope: true}}, false, false},
		{"duplicate", []PlanRepairTarget{valid, valid}, false, false},
		{"standalone injection", []PlanRepairTarget{valid}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := deliveryReviewAssignment()
			if tc.standalone {
				a = reviewerAssignment()
			}
			// Empty routing must be an array, not JSON null.
			targets := append([]PlanRepairTarget{}, tc.targets...)
			result, err := assembleReviewerContent(a, repairContent(t, targets))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if err == nil && (result.ReviewAssessment.Verdict != "needs_changes" || len(result.ReviewAssessment.RepairTargets) != len(targets)) {
				t.Fatal("defect assessment or routing lost")
			}
		})
	}
}

func TestPlanRepairTargetRequiresExplicitBoolean(t *testing.T) {
	for _, raw := range []string{`{"check_key":"P1","item_id":"child-a","finding":"Repair"}`, `{"check_key":"P1","item_id":"child-a","finding":"Repair","in_scope":null}`, `{"check_key":"P1","item_id":"child-a","finding":"Repair","in_scope":"true"}`} {
		var target PlanRepairTarget
		if json.Unmarshal([]byte(raw), &target) == nil {
			t.Fatalf("accepted ambiguous scope %s", raw)
		}
	}
}

func TestPlanRepairRoutingSurvivesFocusedResolutionAndBaseline(t *testing.T) {
	a := deliveryReviewAssignment()
	content := repairContent(t, []PlanRepairTarget{{CheckKey: "P1", ItemID: "child-a", Finding: "Restore ready", InScope: true}})
	content = strings.Replace(content, `"P2":{"evidence":["git diff --check returned no findings."],"status":"passed"`, `"P2":{"evidence":["git diff --check returned no findings."],"status":"check_required"`, 1)
	audit, err := decodeReviewerAuditContent(a, content)
	if err != nil {
		t.Fatal(err)
	}
	// Build the explicit unresolved check rather than relying on JSON map order.
	audit.Criteria["P2"] = reviewerContentCheck{Status: "check_required", Summary: "Does the changed source pass?", Evidence: []string{"Not yet executed"}}
	unresolved := reviewerUnresolvedChecks(a, audit)
	resolution, err := decodeReviewerResolutionContent(unresolved, `{"checks":{"P2":{"status":"failed","summary":"Changed source fails.","evidence":["Focused fixture demonstrated the failure."]}},"summary":"One defect.","repair_targets":[{"check_key":"P2","item_id":"child-b","finding":"Fix the dependent check","in_scope":true}]}`, a.Spec)
	if err != nil {
		t.Fatal(err)
	}
	merged := mergeReviewerResolution(audit, resolution)
	b, _ := json.Marshal(merged)
	final, err := assembleReviewerContent(a, string(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(final.ReviewAssessment.RepairTargets) != 2 {
		t.Fatal("stage merge lost audit or focused owner")
	}
	baseline := &ReviewBaseline{Assessment: *final.ReviewAssessment, CommentContext: []string{}}
	if err := ValidateReviewBaseline(a.Spec, baseline); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeReviewerResolutionContent(unresolved, `{"checks":{"P2":{"status":"passed","summary":"Pass","evidence":["Passed"]}},"summary":"Pass","repair_targets":[{"check_key":"P1","item_id":"child-a","finding":"Hijack audit","in_scope":true}]}`, a.Spec); err == nil {
		t.Fatal("focused stage changed resolved audit ownership")
	}
}
