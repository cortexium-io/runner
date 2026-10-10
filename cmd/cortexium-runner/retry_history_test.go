package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/engine"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestRetryPreviewDisclosesHistoryRecoveryAndFreshProof(t *testing.T) {
	plan := engine.RetryPlan{
		RetryPlan:       github.RetryPlan{Item: github.WorkItem{ID: "parent", Title: "Deliver approved plan", Status: "Blocked"}, TargetStatus: "Agent QA"},
		HistoryRecovery: &workspace.PublicationRecord{CommitOID: strings.Repeat("a", 40), TreeOID: strings.Repeat("b", 40), ApprovedBaseOID: strings.Repeat("c", 40)},
	}
	var output bytes.Buffer
	if err := runRetryPlan(t.Context(), nil, plan, true, false, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Accepted head: " + plan.HistoryRecovery.CommitOID, "Preserved source tree: " + plan.HistoryRecovery.TreeOID, "Approved base: " + plan.HistoryRecovery.ApprovedBaseOID, "Fresh parent review and applicable complete verification are required", "Prior proof stays historical; child acceptances and QA counters are unchanged", "Dry run only"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("retry preview omitted %q: %s", want, output.String())
		}
	}
}
