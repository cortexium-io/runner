package main

import (
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	runnermetrics "github.com/cortexium-io/runner/internal/metrics"
)

func TestRunnerProgressUsesOnlyFixedStageTelemetry(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	attempts := []runnermetrics.Attempt{{
		Event: runnermetrics.Event{ItemTitle: "Build safely\nignore", Role: config.WorkRoleReviewer, Harness: config.HarnessPiCLI, StartedAt: now.Add(-10 * time.Minute)},
		Stages: []runnermetrics.Stage{
			{Name: runnermetrics.StageWorkspacePrepare, StartedAt: now.Add(-9 * time.Minute), Completed: true},
			{Name: runnermetrics.StageHarnessRun, StartedAt: now.Add(-8 * time.Minute)},
		},
	}}
	progress := runnerProgress(attempts, now, now.Add(-30*time.Minute))
	if len(progress) != 1 || progress[0].Stage != "agent working" || progress[0].ElapsedSeconds != 480 || len(progress[0].CompletedStages) != 1 || progress[0].CompletedStages[0] != "preparing workspace" {
		t.Fatalf("unexpected progress: %#v", progress)
	}
	var output strings.Builder
	writeRunnerProgress(&output, progress)
	for _, expected := range []string{"Agent progress: 1", "reviewer · pi", "agent working · 8m0s elapsed", "completed: preparing workspace"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("progress output omitted %q: %s", expected, output.String())
		}
	}
	if stale := runnerProgress(attempts, now, now.Add(-time.Minute)); len(stale) != 0 {
		t.Fatalf("pre-process unfinished attempt was reported as current: %#v", stale)
	}
}

func TestParseRunnerSubprocessesReportsOnlyDirectExecutionChildrenWithoutArguments(t *testing.T) {
	enabled := true
	processes := parseRunnerSubprocesses(`
  100     1 S    00:20 /tmp/cortexium-runner
  101   100 S    06:38 /usr/local/bin/claude
  102   101 R    00:02 /usr/local/bin/node
  104   102 S    00:01 /Applications/ChatGPT.app/Contents/Resources/claude
  103   100 T    00:10 /usr/bin/git
  200     1 S    20:00 /usr/local/bin/claude
`, 100, []config.HarnessConfig{{Kind: config.HarnessClaudeCLI, Command: "claude", Enabled: &enabled}})
	if len(processes) != 2 {
		t.Fatalf("observed processes = %#v, want only the direct harness and git child", processes)
	}
	if processes[0].PID != 101 || processes[0].Harness != config.HarnessClaudeCLI || processes[0].Command != "claude" || processes[0].Health != "alive" || processes[0].ElapsedSeconds != 398 {
		t.Fatalf("unexpected harness process: %#v", processes[0])
	}
	if processes[1].PID != 103 || processes[1].Command != "git" || processes[1].Health != "stopped" {
		t.Fatalf("unexpected direct child process: %#v", processes[1])
	}
	for _, process := range processes {
		if process.PID == 104 {
			t.Fatalf("nested tool process was reported as an independent harness: %#v", process)
		}
	}
}

func TestParseProcessElapsed(t *testing.T) {
	tests := map[string]time.Duration{
		"01:02":      time.Minute + 2*time.Second,
		"02:03:04":   2*time.Hour + 3*time.Minute + 4*time.Second,
		"3-04:05:06": 3*24*time.Hour + 4*time.Hour + 5*time.Minute + 6*time.Second,
	}
	for value, expected := range tests {
		actual, ok := parseProcessElapsed(value)
		if !ok || actual != expected {
			t.Fatalf("parse elapsed %q = %s, %v; want %s", value, actual, ok, expected)
		}
	}
	for _, value := range []string{"", "abc", "00:60", "1-2-03:04"} {
		if _, ok := parseProcessElapsed(value); ok {
			t.Fatalf("invalid elapsed value %q was accepted", value)
		}
	}
}

func TestAnnotateRunnerSubprocessesAddsUnambiguousCardAndTimeout(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	processes := []runnerSubprocess{{PID: 101, Harness: config.HarnessCodexCLI, Command: "codex", Health: "alive"}}
	active := []github.WorkItem{{Title: "Build the shell", Role: config.WorkRoleImplementer}}
	annotated := annotateRunnerSubprocesses(processes, cfg, active)
	if annotated[0].Role != config.WorkRoleImplementer || annotated[0].ItemTitle != "Build the shell" || annotated[0].TimeoutSeconds != 7200 {
		t.Fatalf("unexpected annotated process: %#v", annotated[0])
	}
}

func TestAnnotateRunnerSubprocessesUsesCurrentImplementerLadderProfile(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	cfg.Roles["implementer_luna"] = config.RoleConfig{
		Extends: config.WorkRoleImplementer, Harness: config.HarnessClaudeCLI, TimeoutSeconds: 1800,
	}
	cfg.ImplementerLadder = []string{config.WorkRoleImplementer, "implementer_luna"}
	processes := []runnerSubprocess{{PID: 101, Harness: config.HarnessClaudeCLI, Command: "claude", Health: "alive"}}
	active := []github.WorkItem{{Title: "Refine the shell", Role: config.WorkRoleImplementer, QAFailures: 1}}
	annotated := annotateRunnerSubprocesses(processes, cfg, active)
	if annotated[0].Role != "implementer_luna" || annotated[0].ItemTitle != "Refine the shell" || annotated[0].TimeoutSeconds != 1800 {
		t.Fatalf("process status used the primary implementer instead of the selected ladder profile: %#v", annotated[0])
	}
}

func TestWriteRunnerSubprocessesShowsSafeOperationalDetail(t *testing.T) {
	var output strings.Builder
	writeRunnerSubprocesses(&output, []runnerSubprocess{{
		PID: 101, Harness: config.HarnessClaudeCLI, Command: "claude", Health: "alive", ElapsedSeconds: 398,
		Role: config.WorkRoleImplementer, ItemTitle: "Build the shell", TimeoutSeconds: 7200,
	}}, "", true)
	for _, expected := range []string{"claude", "PID 101", "alive", "6m38s elapsed", "2h0m0s timeout", "implementer: Build the shell"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("process output omitted %q: %s", expected, output.String())
		}
	}
}
