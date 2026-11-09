package execution

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func deadlinePayloadAssignment() runner.Assignment {
	return runner.Assignment{
		Task: "Implement the scoped correction and hand it to the configured checks.",
		Setup: runner.ExecutionSetup{Profile: runner.Profile{
			Harness:   "codex",
			Access:    "workspace-write",
			Model:     "gpt-6.1-sol",
			Reasoning: "xhigh",
			MaxInput:  4096,
			MaxOutput: 512,
		}},
		Inputs:      map[string]runner.Value{"request": runner.Text("Fix the retained candidate.")},
		OutputTypes: map[string]string{"summary": "text"},
		Outcomes:    []string{"blocked"},
	}
}

func TestAssignmentAdmissionDoesNotReadFutureRecoveryArtifact(t *testing.T) {
	a := deadlinePayloadAssignment()
	a.AttemptID = "attempt_collection_repair"
	a.Inputs["measurement"] = runner.Value{ArtifactID: "attempt_failed/collection-failure.json"}
	// Recovery validates admission before registering its diagnostic. Only normal
	// preparation may read the artifact; admission still enforces the input cap.
	s := &Service{}
	if err := s.ValidateAssignment(a); err != nil {
		t.Fatal("admission tried to materialize an unregistered recovery artifact", err)
	}
	a.Setup.Profile.MaxInput = 128
	if err := s.ValidateAssignment(a); err == nil {
		t.Fatal("deferred artifact reading bypassed the existing input allowance")
	}
}

func TestNativePayloadIncludesAuthoritativeDeadlineSnapshot(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 500_000_000, time.UTC)
	future := now.Add(61*time.Second + 250*time.Millisecond).In(time.FixedZone("fixture", 2*60*60))
	for _, tc := range []struct {
		name            string
		deadline, built time.Time
		utc             string
		remaining       int64
	}{
		{"legacy zero", time.Time{}, now, "", 0},
		{"UTC exact cutoff", future, now, "2026-11-05T12:01:01.75Z", 61},
		{"setup consumed time", future, now.Add(2 * time.Second), "2026-11-05T12:01:01.75Z", 59},
		{"less than a second", now.Add(750 * time.Millisecond), now, "2026-11-05T12:00:01.25Z", 0},
		{"deadline reached", now, now, "2026-11-05T12:00:00.5Z", 0},
		{"deadline passed", now.Add(-time.Second), now, "2026-11-05T11:59:59.5Z", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := deadlinePayloadAssignment()
			a.Deadline = tc.deadline
			// Task inputs remain task data even when they use similar names.
			a.Inputs["deadline_utc"] = runner.Text("2099-01-01T00:00:00Z")
			a.Inputs["remaining_seconds"] = runner.Text("unlimited")
			payload, _, err := nativePayloadAt(a, tc.built)
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Deadline  string            `json:"deadline_utc"`
				Remaining *int64            `json:"remaining_seconds"`
				Inputs    map[string]string `json:"inputs"`
			}
			if err = json.Unmarshal([]byte(payload), &out); err != nil {
				t.Fatal(err)
			}
			if tc.deadline.IsZero() {
				if out.Deadline != "" || out.Remaining != nil {
					t.Fatal("legacy assignment invented a cutoff", payload)
				}
			} else if out.Deadline != tc.utc || out.Remaining == nil || *out.Remaining != tc.remaining {
				t.Fatal("deadline changed, grew after setup or lost zero remaining time", payload)
			}
			if out.Inputs["deadline_utc"] != "2099-01-01T00:00:00Z" || out.Inputs["remaining_seconds"] != "unlimited" || !a.Deadline.Equal(tc.deadline) {
				t.Fatal("deadline metadata changed task data or the authoritative assignment")
			}
		})
	}
}

func TestNativeDeadlineMetadataUsesExistingInputAllowance(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	a := deadlinePayloadAssignment()
	payload, schema, err := nativePayloadAt(a, now)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	baseBytes := len(workspaceBase) + len(a.Setup.Profile.Instructions) + len(encoded)
	a.Setup.Profile.MaxInput = runner.Bytes(baseBytes + len(payload))
	if _, _, err = nativePayloadAt(a, now); err != nil {
		t.Fatal("exact original allowance rejected", err)
	}
	a.Deadline = now.Add(20 * time.Minute)
	if _, _, err = nativePayloadAt(a, now); err == nil || !strings.Contains(err.Error(), "exceed max_input") {
		t.Fatal("deadline metadata escaped the existing input bound", err)
	}
	a.Setup.Profile.MaxInput = 4096
	payload, _, err = nativePayloadAt(a, now)
	if err != nil {
		t.Fatal(err)
	}
	a.Setup.Profile.MaxInput = runner.Bytes(baseBytes + len(payload))
	if _, _, err = nativePayloadAt(a, now); err != nil {
		t.Fatal("exact deadline-aware allowance rejected", err)
	}
	a.Setup.Profile.MaxInput--
	if _, _, err = nativePayloadAt(a, now); err == nil {
		t.Fatal("deadline-aware payload exceeded its allowance")
	}
}
