package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/core"
)

func TestOperatorAdviceDistinguishesAttentionWorkAndWaits(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name, group, action string
		change              func(*View)
		running             bool
	}{
		{"draft", "Needs you", "task approve task --version 1 --preview approval", func(v *View) { v.Task.Stage = core.Draft }, true},
		{"proposal", "Needs you", "task release task --version 1 --preview release", func(v *View) { v.Task.Stage = core.AwaitingApproval; v.ReleasePreview = "release" }, true},
		{"decision", "Needs you", "CLARIFIED_TASK.md", func(v *View) {
			v.Task.Attention = &core.Attention{Kind: core.Decision, Detail: "Which greeting?", Attempt: "attempt"}
		}, true},
		{"interrupted review", "Needs you", "task resume task --attempt attempt", func(v *View) { v.Task.Attention = &core.Attention{Kind: core.Interrupted, Attempt: "attempt"} }, true},
		{"saved review", "Needs you", "task resume task --attempt attempt", func(v *View) { v.Task.Attention = &core.Attention{Kind: "saved_result", Attempt: "attempt"} }, true},
		{"ownership", "Needs you", "do not replay", func(v *View) {
			v.Task.Attention = &core.Attention{Kind: "ownership", Attempt: "attempt"}
			v.Attempts[0].Occupying = true
		}, true},
		{"active", "Working", "wait for the recorded attempt", func(v *View) { v.Attempts[0].Occupying = true }, true},
		{"absent with reservation", "Needs you", "reconcile ownership", func(v *View) { v.Attempts[0].Occupying = true }, false},
		{"absent without reservation", "Needs you", "run to reconcile", func(v *View) {}, false},
		{"dependency", "Waiting", "exact completed version", func(v *View) {
			v.Eligibility.WaitKind = "dependency"
			v.Eligibility.WaitingFor = "dependency prior v2: Awaiting delivery"
		}, true},
		{"capacity", "Waiting", "wait", func(v *View) {
			v.Eligibility.WaitKind = "capacity"
			v.Eligibility.WaitingFor = "global capacity: 1/1 occupied"
		}, true},
		{"retry limit", "Needs you", "config edit", func(v *View) {
			v.Eligibility.WaitKind = "retry_limit"
			v.Eligibility.WaitingFor = "transient retry limit"
		}, true},
		{"backoff", "Waiting", "wait", func(v *View) {
			v.Eligibility.WaitKind = "backoff"
			v.Eligibility.WaitingFor = "retry backoff until deadline"
		}, true},
		{"reviewed", "Retained", "integration is still pending", func(v *View) { v.Task.Stage = core.AwaitingDelivery }, true},
		{"draft destination", "Needs you", "task approve", func(v *View) { v.Task.Stage = core.Draft; v.DeliveryDestination = "owner/repo" }, true},
		{"publishing", "Working", "wait for remote reconciliation", func(v *View) {
			v.Publications = []core.Publication{{Occupying: true, Intent: core.PublicationIntent{Repository: "owner/repo"}}}
		}, true},
		{"interrupted publication", "Needs you", "uncertain PR creation cannot repeat", func(v *View) { v.Publications = []core.Publication{{Occupying: true}} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := View{Task: core.Task{ID: "task", Stage: core.Reviewing, Versions: []core.Version{{Number: 1, Body: "## Outcome\nGreeting\n"}}}, Preview: "approval", Attempts: []core.Attempt{{ID: "attempt", Version: 1, Role: "audit", State: "running", StartedAt: now.Add(-5 * time.Second), Profile: core.Profile{Model: "actual", Reasoning: "high"}}}}
			tc.change(&v)
			describeView(&v, tc.running, now)
			if v.Group != tc.group || !strings.Contains(v.NextAction, tc.action) {
				t.Fatalf("wrong advice: %+v", v)
			}
			if v.FocusAttempt.Profile.Model != "actual" || *v.ElapsedSeconds != 5 {
				t.Fatal("actual attempt facts lost")
			}
			if tc.name == "saved review" && v.Recovery.Repeats != "integrity checks and deterministic continuation; no model call" {
				t.Fatal("saved recovery advertised another call")
			}
			if tc.name == "interrupted review" && (v.Recovery.Role != "audit" || !strings.Contains(v.Recovery.Repeats, "--allow-new-invocation")) {
				t.Fatal("recovery lost the role or authorization boundary")
			}
		})
	}
}
