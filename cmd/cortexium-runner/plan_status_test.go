package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/engine"
	"github.com/cortexium-io/runner/internal/github"
)

func TestStatusLabelsIntegrationAndHistoricalPlanningWithoutClaimingDelivery(t *testing.T) {
	var out bytes.Buffer
	writePlanProgress(&out, engine.WorkStatus{
		IntegratedUndelivered: []github.WorkItem{{ID: "child", Title: "Accepted child", Status: "Backlog"}},
		PlanningCompleted:     []github.WorkItem{{ID: "old", Title: "Old proposal", Status: "Done"}},
		CancelledPlans:        []github.WorkItem{{ID: "cancelled", Title: "Retained plan", Status: "Backlog"}},
	}, "runner.json")
	for _, want := range []string{"Integrated into plan — not delivered: 1", "Accepted child", "Historical planning completed — not a delivery claim: 1", "Old proposal", "Cancelled plans — retained work, no admission: 1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status omitted %q: %s", want, out.String())
		}
	}
}
