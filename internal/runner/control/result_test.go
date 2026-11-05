package control

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

// Owns presentation bounds and accounting. Execution/recovery policy stays in
// the core suite; these assertions prevent a compact result hiding unknown cost
// or loading a large task/evidence record back into the coordinator's context.
func TestCompactResult(t *testing.T) {
	created := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	input, cached, output, partial := int64(100), int64(80), int64(10), int64(20)
	r := runner.Run{
		ID:        "run-result",
		ProjectID: "demo",
		Status:    "Completed",
		Created:   created,
		Updated:   created.Add(time.Hour), // Later cleanup must not inflate execution time.
		Source:    &runner.SourceSettings{Ref: "HEAD"},
		Inputs:    map[string]runner.Value{"request": runner.Text("private original request")},
		Process: runner.Process{
			ID: "fix",
			Steps: []runner.Step{
				{ID: "edit", Role: "owner"},
				{ID: "check", Role: "check"},
			},
		},
		Roles: map[string]runner.Role{
			"owner": {Execution: &runner.Profile{Harness: "codex", Instructions: "private role instructions"}},
			"check": {Execution: &runner.Profile{Harness: "command"}},
		},
		Attempts: []runner.Attempt{
			{
				Step:       0,
				State:      runner.Finished,
				Released:   true,
				FinishedAt: created.Add(5 * time.Second),
				Usage:      runner.Usage{InputTokens: &input, CachedInputTokens: &cached, OutputTokens: &output},
				Source:     &runner.SourceResult{Base: "base", PatchID: "edit/changes.patch"},
			},
			{
				Step:       0,
				State:      runner.Finished,
				Released:   true,
				FinishedAt: created.Add(10 * time.Second),
				Usage:      runner.Usage{InputTokens: &partial},
			},
			{
				Step:       1,
				State:      runner.Finished,
				Released:   true,
				FinishedAt: created.Add(11 * time.Second),
			},
		},
		Outputs: map[string]runner.Value{
			"a":     runner.Text(strings.Repeat("ø", 2000)),
			"b":     runner.Text(strings.Repeat("ø", 2000)),
			"c":     runner.Text(strings.Repeat("ø", 2000)),
			"d":     runner.Text(strings.Repeat("ø", 2000)),
			"e":     runner.Text("must still be marked truncated when no preview budget remains"),
			"patch": {ArtifactID: "edit/changes.patch"},
		},
	}
	out := compactResult(r, created.Add(2*time.Hour))
	if out.ElapsedSeconds != 11 || !out.ExecutionSettled || !out.CleanupPending || !out.WorkspaceRetained {
		t.Fatalf("time/settlement/cleanup misrepresented: %+v", out)
	}
	if out.Usage != (Usage{
		KnownInputTokens:       120,
		KnownCachedInputTokens: 80,
		KnownOutputTokens:      10,
		KnownTotalTokens:       130,
		IncompleteAttempts:     1,
	}) {
		t.Fatalf("partial usage or cached subset counted incorrectly: %+v", out.Usage)
	}
	if out.Candidate == nil || out.Candidate.PatchID != "edit/changes.patch" || out.Outputs["patch"].ArtifactID != "edit/changes.patch" {
		t.Fatal("retained patch reference lost")
	}
	bytes := 0
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		v := out.Outputs[name]
		bytes += len(v.Preview)
		if !v.Truncated || !utf8.ValidString(v.Preview) || len(v.Preview) > 1024 {
			t.Fatalf("invalid preview for %s: %+v", name, v)
		}
	}
	if bytes != 4096 || out.Outputs["e"].Preview != "" {
		t.Fatal("total preview bound not enforced")
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private original request") || strings.Contains(string(b), "private role instructions") {
		t.Fatal("result echoed task context")
	}
	r.Status = "Failed"
	if got := compactResult(r, created.Add(2*time.Hour)); !got.CleanupPending {
		t.Fatal("failed source cleanup was reported complete before release")
	}
	r.ResourcesReleased = true
	if got := compactResult(r, created.Add(2*time.Hour)); got.CleanupPending || got.WorkspaceRetained {
		t.Fatal("released failed source was still reported retained or pending")
	}
}

// An owner must see the exact saved approval evidence through the restricted
// result API, without inspect access or a final process output.
func TestCompactResultDecisionOutputs(t *testing.T) {
	proof := strings.Repeat("restore snapshot path and exact digest; ", 40)
	r := runner.Run{
		Status: "Waiting",
		Process: runner.Process{Steps: []runner.Step{
			{ID: "prepare", Role: "operator"},
		}},
		Roles: map[string]runner.Role{
			"operator": {Execution: &runner.Profile{Harness: "release"}},
		},
		Decision: &runner.Decision{AttemptID: "qualified"},
		Attempts: []runner.Attempt{
			{
				ID: "stale", State: runner.Finished, Released: true,
				End: &runner.ExecutionEnd{Result: &runner.StepResult{
					Outputs: map[string]runner.Value{"receipt": runner.Text("stale approval")},
				}},
			},
			{
				ID: "qualified", State: runner.Finished, Released: true,
				End: &runner.ExecutionEnd{Result: &runner.StepResult{
					Outputs: map[string]runner.Value{"receipt": runner.Text(proof)},
				}},
			},
		},
	}
	out := compactResult(r, time.Now())
	if len(out.Outputs) != 0 || out.DecisionOutputs["receipt"].Preview != proof || out.DecisionOutputs["receipt"].Truncated {
		t.Fatal("saved decision evidence unavailable or clipped before completion")
	}
	r.Attempts[1].End.Result.Outputs["receipt"] = runner.Text(strings.Repeat("ø", 3000))
	out = compactResult(r, time.Now())
	got := out.DecisionOutputs["receipt"]
	if !got.Truncated || len(got.Preview) != 4096 || !utf8.ValidString(got.Preview) {
		t.Fatal("approval preview exceeded its UTF-8-safe bound")
	}
	r.Decision = nil
	if got := compactResult(r, time.Now()); len(got.DecisionOutputs) != 0 {
		t.Fatal("approval evidence exposed without a pending decision")
	}
}

func TestResultDistinguishesBoundedReconciliationFromOwnerWaiting(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name     string
		trusted  bool
		expired  bool
		decision bool
		want     bool
	}{
		{name: "same operation reconnect", trusted: true, want: true},
		{name: "unresolved effect"},
		{name: "deadline reached", trusted: true, expired: true},
		{name: "owner decision", trusted: true, decision: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runner.Run{
				Status:   "Waiting",
				Process:  runner.Process{Steps: []runner.Step{{ID: "operator", Role: "operator"}}},
				Attempts: []runner.Attempt{{State: runner.Unknown, Reconciling: tc.trusted, Deadline: now.Add(time.Minute)}},
			}
			if tc.expired {
				r.Attempts[0].Deadline = now
			}
			if tc.decision {
				r.Decision = &runner.Decision{ID: "protected"}
			}
			out := compactResult(r, now)
			if out.Reconciling != tc.want || out.ExecutionSettled {
				t.Fatal("reconciliation was presented as settlement or hid a real blocker", out)
			}
		})
	}
}

type waitingResultExecutor struct{ reconciling bool }

func (e waitingResultExecutor) Start(context.Context, runner.Assignment) (runner.Observation, error) {
	return runner.Observation{State: runner.Unknown, Reconciling: e.reconciling, NextCheck: time.Now().Add(time.Hour)}, nil
}
func (e waitingResultExecutor) Observe(context.Context, string) (runner.Observation, error) {
	return e.Start(context.Background(), runner.Assignment{})
}
func (waitingResultExecutor) Stop(context.Context, string) error    { return nil }
func (waitingResultExecutor) Release(context.Context, string) error { return nil }

func TestResultWaitJoinsSelfReconciliationAndReturnsTrueUnknown(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(map[bool]string{true: "same operation", false: "unknown effect"}[trusted], func(t *testing.T) {
			loader, err := config.New("../../../examples/demo/instance.yaml")
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := loader.Load()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Instance.Storage.MinFree = 0
			dir := filepath.Join(t.TempDir(), "state")
			store, err := sqlite.Open(dir, cfg.Instance.Storage)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			disk, err := runner.OpenDisk(dir, cfg.Instance.Storage)
			if err != nil {
				t.Fatal(err)
			}
			core, err := runner.New(t.Context(), store, waitingResultExecutor{trusted}, disk, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer core.Close()
			r, err := start(core, StartArgs{RequestID: "wait-fixture", Project: "demo", Inputs: map[string]any{"name": "fixture"}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			for r.Status != "Waiting" {
				if _, err = core.Wait(ctx, r.ID, r.Revision); err != nil {
					t.Fatal(err)
				}
				r, err = core.Get(r.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			// A cancelled caller is a deterministic witness of the wait branch:
			// a transient Waiting must join Core.Wait, not return a user blocker.
			joined, stop := context.WithCancel(t.Context())
			stop()
			out, err := result(joined, core, ResultArgs{RunID: r.ID, Wait: true})
			if trusted && !errors.Is(err, context.Canceled) || !trusted && (err != nil || out.Status != "Waiting" || out.Reconciling) {
				t.Fatal("result wait returned transient reconciliation or hid true uncertainty", out, err)
			}
		})
	}
}
