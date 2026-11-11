package execution

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

func TestBrowserRecoveryBindsLatestSameRunPublication(t *testing.T) {
	receipt := release.Receipt{
		Action: "publish",
		RunID:  "retained-root",
		Key:    "latest-publication",
		Head:   strings.Repeat("a", 40),
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	run := runner.Run{
		ID: receipt.RunID,
		Process: runner.Process{Steps: []runner.Step{
			{ID: "owner"}, {ID: "publish"}, {ID: "qualify"},
		}},
		Attempts: []runner.Attempt{{
			ID:         receipt.Key,
			Step:       1,
			State:      runner.Finished,
			Released:   true,
			Superseded: true, // The ordinary source backedge invalidates acceptance.
			End: &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{
				Outcome: "ready",
				Outputs: map[string]runner.Value{"receipt": {Inline: data}},
			}},
		}},
	}
	binding := publicationBinding{Latest: receipt, LastAttempt: receipt.Key}
	if err := verifyBrowserRecoveryPublication(run, binding, receipt.Head); err != nil {
		t.Fatal("exact saved publication rejected", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*runner.Run, *publicationBinding)
	}{
		{"changed head", func(_ *runner.Run, b *publicationBinding) { b.Latest.Head = strings.Repeat("b", 40) }},
		{"different root", func(_ *runner.Run, b *publicationBinding) { b.Latest.RunID = "another-root" }},
		{"authority only", func(_ *runner.Run, b *publicationBinding) { b.AuthorityOnly = true }},
		{"old adopted receipt", func(_ *runner.Run, b *publicationBinding) { b.Latest.Action = "adopt" }},
		{"different journal attempt", func(_ *runner.Run, b *publicationBinding) { b.LastAttempt = "old-publication" }},
		{"changed saved receipt", func(r *runner.Run, _ *publicationBinding) {
			r.Attempts[0].End.Result.Outputs["receipt"] = runner.Value{Inline: json.RawMessage(`{}`)}
		}},
		{"unsettled", func(r *runner.Run, _ *publicationBinding) { r.Attempts[0].Released = false }},
		{"newer failed publication", func(r *runner.Run, _ *publicationBinding) {
			r.Attempts = append(r.Attempts, runner.Attempt{ID: "newer", Step: 1, State: runner.Finished, Released: true, End: &runner.ExecutionEnd{Kind: "failed"}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, b := runner.Clone(run), runner.Clone(binding)
			tc.change(&r, &b)
			if err := verifyBrowserRecoveryPublication(r, b, receipt.Head); err == nil {
				t.Fatal("unowned or stale publication accepted")
			}
		})
	}
}
