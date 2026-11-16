//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestHarnessSettlementRequiresSuccessfulInventories(t *testing.T) {
	failure := errors.New("inventory unavailable")
	for _, failAt := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			calls := 0
			err := settleNativeInventory(t.Context(), func() ([]nativeChild, error) {
				calls++
				if calls == failAt {
					return nil, failure
				}
				return nil, nil
			}, func(nativeChild) error {
				t.Fatal("empty inventory must not terminate a process")
				return nil
			})
			if failAt == 0 {
				if err != nil || calls != 2 {
					t.Fatal("settlement needs two successful empty observations", calls, err)
				}
			} else if !errors.Is(err, failure) || calls != failAt {
				t.Fatal("unavailable inventory was accepted as settlement", calls, err)
			}
		})
	}
}

func TestHarnessFinalSelectionPreservesTerminalFailures(t *testing.T) {
	final := `{"outcome":"ready","outputs":{"article":"Complete"},"reason":"Done"}`
	oversized := strings.Repeat("progress ", 32)
	for _, tc := range []struct {
		name     string
		messages []string
		terminal bool
		accepted bool
	}{
		{name: "earlier oversized message", messages: []string{oversized, final}, accepted: true},
		{name: "oversized final", messages: []string{final, oversized}},
		{name: "terminal error", messages: []string{final}, terminal: true},
		{name: "no final"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := harnessEvents{Limit: 128}
			if tc.terminal {
				events.accept([]byte(`{"type":"error","message":"terminal harness failure"}`))
			}
			for _, text := range tc.messages {
				data, _ := json.Marshal(map[string]any{
					"type": "item.completed",
					"item": map[string]string{"type": "agent_message", "text": text},
				})
				events.accept(data)
			}
			receipt := completedHarnessReceipt(runner.Assignment{}, events, false, nil, "")
			accepted := false
			if receipt.Kind == "returned" {
				a := harnessAssignment(t)
				a.Setup.Profile.MaxOutput = 128
				end, err := harnessResult(a, receipt.Final, t.TempDir(), nil)
				accepted = err == nil && end.Kind == "returned"
			}
			if accepted != tc.accepted || receipt.OutputFinalized != tc.terminal || events.FinalOverflow && len(events.Final) != 0 || tc.terminal && events.Failed == "" {
				t.Fatalf("wrong final-result authority: accepted=%v receipt=%+v", accepted, receipt)
			}
		})
	}
}

func TestHarnessTerminalCheckpointIgnoresLateEvents(t *testing.T) {
	for _, terminal := range []string{
		`{"type":"turn.completed","usage":{"input_tokens":12,"output_tokens":3}}`,
		`{"type":"result","result":"saved","usage":{"input_tokens":12,"output_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`,
	} {
		events := harnessEvents{Limit: 128, Final: []byte("saved")}
		events.accept([]byte(terminal))
		checkpoint := runner.Clone(events)
		for _, late := range []string{
			`{"type":"item.completed","item":{"type":"agent_message","text":"replacement"}}`,
			`{"type":"turn.completed","usage":{"input_tokens":999,"output_tokens":999}}`,
			`{"type":"error","message":"late descendant error"}`,
		} {
			events.accept([]byte(late))
		}
		if !events.Terminal || string(events.Final) != string(checkpoint.Final) || events.Failed != checkpoint.Failed || *events.Usage.InputTokens != *checkpoint.Usage.InputTokens || *events.Usage.OutputTokens != *checkpoint.Usage.OutputTokens {
			t.Fatal("late stream bytes replaced terminal result or usage", events)
		}
	}
}

func TestHarnessStreamOverflowCannotSelectStaleFinal(t *testing.T) {
	event := func(text string) string {
		data, _ := json.Marshal(map[string]any{
			"type": "item.completed",
			"item": map[string]string{"type": "agent_message", "text": text},
		})
		return string(data)
	}
	finalText := `{"outcome":"ready","outputs":{"article":"Complete"},"reason":"Done"}`
	final := event(finalText)
	largeFinalText := `{"outcome":"ready","outputs":{"article":"` + strings.Repeat("x", 5<<10) + `"},"reason":""}`
	oversized := event(strings.Repeat("x", 300<<10))
	for _, tc := range []struct {
		name, kind, reason string
		events             []string
		finalBytes         int
		finalKnown         bool
		oversizedEvents    int
	}{
		{
			name:            "oversized last event",
			events:          []string{final, oversized},
			kind:            "failed",
			reason:          "native stream event exceeded its 262144-byte bound",
			oversizedEvents: 1,
		},
		{
			name:            "bounded final after overflow",
			events:          []string{oversized, final},
			kind:            "returned",
			finalKnown:      true,
			finalBytes:      len(finalText),
			oversizedEvents: 1,
		},
		{
			name:       "oversized final JSON",
			events:     []string{final, event(largeFinalText)},
			kind:       "failed",
			reason:     "max_output_bytes=4096",
			finalKnown: true,
			finalBytes: len(largeFinalText),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disk := workspaceDisk(t)
			a := harnessAssignment(t)
			a.Setup.Profile.MaxOutput = 4 << 10
			binary := filepath.Join(t.TempDir(), "fake-codex")
			script := `#!/bin/sh
if [ "$1" = --version ]; then printf 'fixture 1\n'; exit 0; fi
cat >/dev/null
cat <<'NATIVE_EVENTS'
` + strings.Join(tc.events, "\n") + "\nNATIVE_EVENTS\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			a.Setup.Profile.Native.Binary = binary
			native := NewNative(disk, nil, nil, nil)
			defer native.Close()
			done := make(chan error, 1)
			native.launchWorker = func(file string, finished func()) error {
				go func() { done <- RunNativeWorker(context.Background(), file); finished() }()
				return nil
			}
			if _, err := native.Start(t.Context(), a); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			o, err := native.Observe(t.Context(), a.AttemptID)
			if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != tc.kind {
				t.Fatal("stream overflow selected the wrong final result", o, err)
			}
			if tc.reason != "" && !strings.Contains(o.End.Reason, tc.reason) {
				t.Fatal("retry reason conflated final JSON size with transport event loss", o.End.Reason)
			}
			data, err := os.ReadFile(filepath.Join(disk.Root, "artifacts", a.AttemptID, "provenance.json"))
			var proof runner.NativeProvenance
			if err != nil || json.Unmarshal(data, &proof) != nil || (proof.FinalResultBytes != nil) != tc.finalKnown || proof.OversizedEvents != tc.oversizedEvents || proof.MaxOutputBytes != 4<<10 {
				t.Fatal("durable native output facts lost final authority or the configured bound", proof, err)
			}
			if proof.FinalResultBytes != nil && *proof.FinalResultBytes != tc.finalBytes || tc.oversizedEvents > 0 && proof.StreamEventLimit != 256<<10 {
				t.Fatal("durable final-byte count or stream event bound is inaccurate", proof)
			}
			if err := native.Release(t.Context(), a.AttemptID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHarnessAllocationFailureRetainsOwnershipThroughRecovery(t *testing.T) {
	disk := workspaceDisk(t)
	disk.Policy.MinFree = 0
	a := harnessAssignment(t)
	a.OutputDir = filepath.Join(disk.Root, "artifacts", a.AttemptID)
	a.Setup.Profile.MaxInput = 8 << 10
	a.TaskContext = &runner.TaskContext{
		Goal: "Evaluate complete reports",
		Results: []runner.RoleTaskResult{{
			ID: "child", Outputs: map[string]runner.Value{"report": runner.Text(strings.Repeat("x", 16<<10))},
		}},
	}
	_, results, data, err := planHarnessAssignment(a, filepath.Join(disk.Root, harnessControl(a.AttemptID)), a.Setup.Profile.MaxInput+disk.Policy.Database.MaxRecord)
	if err != nil || results == nil {
		t.Fatal("fixture needs a separately staged result file", err)
	}
	capacity := disk.Policy.MaxManaged
	// Ownership and results fit; the subsequent assignment publication fails.
	disk.Policy.MaxManaged = disk.Policy.Database.MaxSize + runner.Bytes(4096+len(data)+1024)
	native := NewNative(disk, nil, nil, nil)
	defer native.Close()
	native.launchWorker = func(string, func()) error {
		t.Error("failed allocation dispatched a worker")
		return errors.New("unexpected dispatch")
	}
	service := &Service{harnesses: native}
	_, _ = service.Start(t.Context(), a)
	if !native.Owns(a.AttemptID) {
		t.Fatal("allocation failure lost durable ownership of staged inputs")
	}
	if _, err := os.Stat(results.Path); err != nil {
		t.Fatal("fixture did not reach the partial allocation boundary", err)
	}
	native.Close()
	disk.Policy.MaxManaged = capacity
	reopened := NewNative(disk, nil, nil, nil)
	defer reopened.Close()
	service.harnesses = reopened
	o, err := service.Observe(t.Context(), a.AttemptID)
	if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "failed" {
		t.Fatal("known pre-dispatch failure became unknown after restart", o, err)
	}
	if replay, err := service.Start(t.Context(), a); err != nil || replay.End == nil || replay.End.Kind != "failed" {
		t.Fatal("same dispatch did not retain its terminal allocation outcome", replay, err)
	}
	if err := service.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if reopened.Owns(a.AttemptID) {
		t.Fatal("released allocation still owns a job")
	}
	if _, err := os.Stat(filepath.Join(disk.Root, "work", a.AttemptID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("staged inputs survived release", err)
	}
}

func TestHarnessAllocationWithoutOwnershipHasNoStagedFiles(t *testing.T) {
	disk := workspaceDisk(t)
	disk.Policy.MinFree = 0
	disk.Policy.MaxManaged = disk.Policy.Database.MaxSize + 4096
	native := NewNative(disk, nil, nil, nil)
	defer native.Close()
	a := harnessAssignment(t)
	o, err := (&Service{harnesses: native}).Start(t.Context(), a)
	if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != "failed" || o.NativeHandle != "" || native.Owns(a.AttemptID) {
		t.Fatal("known allocation refusal became uncertain execution", o, err)
	}
	if _, err := os.Stat(filepath.Join(disk.Root, "work", a.AttemptID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("input staged before ownership", err)
	}
}

func TestHarnessReleaseResumesWithoutDisposableAssignment(t *testing.T) {
	disk := workspaceDisk(t)
	a := harnessAssignment(t)
	native := NewNative(disk, nil, nil, nil)
	defer native.Close()
	job := harnessJob{
		Assignment: a,
		Stage:      "releasing",
		Workspace:  &workspaceRecord{WorkDir: t.TempDir()},
		End:        &runner.ExecutionEnd{Kind: "failed", Reason: "settled fixture"},
	}
	if err := native.save(job); err != nil {
		t.Fatal(err)
	}
	// Assignment-dependent cleanup was completed before the releasing marker.
	// Reproduce interruption between deleting disposable inputs and the job.
	if err := disk.Remove("work/" + a.AttemptID); err != nil {
		t.Fatal(err)
	}
	native.Close()
	reopened := NewNative(disk, nil, nil, nil)
	defer reopened.Close()
	service := &Service{harnesses: reopened}
	if o, err := service.Observe(t.Context(), a.AttemptID); err != nil || o.State != runner.Finished || o.Source == nil || o.Source.AttemptID != a.AttemptID {
		t.Fatal("released inputs became a missing job or retriggered source capture", o, err)
	}
	if err := service.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if reopened.Owns(a.AttemptID) {
		t.Fatal("interrupted release left a stale job")
	}
	if err := reopened.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal("completed release was not idempotent", err)
	}
}

func TestHarnessMissingDispatchedInputCannotReleaseOrReplaceWork(t *testing.T) {
	disk := workspaceDisk(t)
	a := harnessAssignment(t)
	native := NewNative(disk, nil, nil, nil)
	defer native.Close()
	if err := native.save(harnessJob{Assignment: a, Stage: "dispatching"}); err != nil {
		t.Fatal(err)
	}
	if err := disk.Remove(harnessControl(a.AttemptID) + "/assignment.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := native.Observe(t.Context(), a.AttemptID); err == nil {
		t.Fatal("lost dispatched input was treated as a known prelaunch failure")
	}
	if o, err := native.Start(t.Context(), a); err != nil || o.NativeHandle != "harness/"+a.AttemptID || o.End != nil {
		t.Fatal("missing input replaced the original dispatch", o, err)
	}
	if err := native.Release(t.Context(), a.AttemptID); err == nil || !native.Owns(a.AttemptID) {
		t.Fatal("missing dispatched input permitted unsettled cleanup", err)
	}
}

func TestHarnessLegacyInlineJobSurvivesControlUpdates(t *testing.T) {
	disk := workspaceDisk(t)
	a := harnessAssignment(t)
	data, _ := json.Marshal(harnessJob{Assignment: a, Stage: "allocated"})
	if err := disk.Write(harnessName(a.AttemptID), data, disk.Policy.Database.MaxRecord); err != nil {
		t.Fatal(err)
	}
	native := NewNative(disk, nil, nil, nil)
	defer native.Close()
	if err := native.Stop(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if job, err := native.load(a.AttemptID); err != nil || job.Assignment.Task != a.Task || !job.StopRequested {
		t.Fatal("control update discarded the legacy inline assignment", job, err)
	}
	if o, err := native.Observe(t.Context(), a.AttemptID); err != nil || o.State != runner.Finished {
		t.Fatal("legacy allocation did not settle", o, err)
	}
	if err := native.Release(context.Background(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
}
