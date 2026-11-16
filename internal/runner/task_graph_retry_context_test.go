package runner

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGeneralTaskSavedRetryPreservesOnlyLatestSettledSameVisitFailure(t *testing.T) {
	policy := DefaultStorage()
	policy.MinFree = 0
	policy.Database.MaxRuns = 1000
	disk, err := OpenDisk(t.TempDir(), policy)
	if err != nil {
		t.Fatal(err)
	}
	failure := Attempt{
		ID:       "failed",
		Step:     0,
		Visit:    3,
		State:    Finished,
		Released: true,
		End:      &ExecutionEnd{Kind: "failed", Reason: "native final JSON is 8193 UTF-8 bytes; max_output_bytes=8192", Diagnostic: "latest bounded failure facts"},
	}
	pending := Attempt{
		ID:    "saved-retry",
		Step:  0,
		Visit: 3,
		State: NotStarted,
	}
	oldVisit, otherStep, returned := Clone(failure), Clone(failure), Clone(failure)
	oldVisit.Visit, otherStep.Step = 2, 1
	returned.ID, returned.End = "returned", &ExecutionEnd{Kind: "returned"}
	for _, tc := range []struct {
		name       string
		attempts   []Attempt
		want       string
		diagnostic string
	}{
		{name: "fresh dispatch", attempts: []Attempt{failure}, want: failure.End.Reason, diagnostic: failure.End.Diagnostic},
		{name: "saved NotStarted retry", attempts: []Attempt{failure, pending}, want: failure.End.Reason, diagnostic: failure.End.Diagnostic},
		{name: "different visit", attempts: []Attempt{oldVisit, pending}},
		{name: "different step", attempts: []Attempt{otherStep, pending}},
		{name: "newer settled outcome", attempts: []Attempt{failure, returned, pending}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Run{
				ID:        "root",
				ProjectID: "demo",
				Status:    "Running",
				Step:      0,
				Visit:     3,
				Inputs:    map[string]Value{"request": Text("Complete the original goal")},
				Process: Process{
					ID:          "general",
					MaxAttempts: 64,
					Steps:       []Step{{ID: "work", Role: "worker"}},
					Tasks: &TaskPolicy{
						Roles:         []string{"worker"},
						MaxTasks:      16,
						MaxDepth:      2,
						MaxConcurrent: 1,
					},
				},
				Roles: map[string]Role{"worker": {
					ID:        "worker",
					Purpose:   "Complete the assigned work",
					Inputs:    map[string]string{},
					Outputs:   map[string]string{"report": "text"},
					Execution: &Profile{Harness: "deterministic"},
				}},
				TaskGraph: &TaskGraphState{
					RootRunID: "root",
					TaskID:    "root",
					Brief:     "Complete the original goal",
				},
				Attempts: Clone(tc.attempts),
			}
			// Reconstitute the saved run with its trailing NotStarted attempt,
			// as engine start/restart does before reconstructing the assignment.
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var saved Run
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			before := Clone(saved)
			c := &Core{disk: disk, runs: map[string]Run{saved.ID: saved}}
			ctx, err := c.graphContext(saved)
			if err != nil || ctx.PreviousFailure != tc.want || ctx.PreviousDiagnostic != tc.diagnostic || !reflect.DeepEqual(saved, before) {
				t.Fatal("saved retry lost or invented prior-visit feedback, or rewrote history", ctx, err)
			}
		})
	}
}
