package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
)

func TestActivityUsesObservedToolIntervalsWithoutPayloads(t *testing.T) {
	start := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	var s activityStream
	s.consume(config.HarnessCodexCLI, []byte(`{"type":"item.started","item":{"id":"shell","type":"command_execution","command":"secret-command"}}`), start)
	s.consume(config.HarnessCodexCLI, []byte(`{"type":"item.started","item":{"id":"shell","type":"command_execution"}}`), start.Add(time.Second))
	s.consume(config.HarnessCodexCLI, []byte(`{"type":"item.completed","item":{"id":"shell","type":"command_execution","aggregated_output":"secret-output"}}`), start.Add(5*time.Second))
	s.consume(config.HarnessCodexCLI, []byte(`{"type":"item.started","item":{"id":"private-tool-id","type":"mcp_tool_call","arguments":{"password":"secret"}}}`), start.Add(6*time.Second))
	got := s.finish()
	if got.ToolsStarted != 2 || got.ToolsCompleted != 1 || got.ToolMilliseconds != 5000 || got.ActiveTools != 1 || !got.OldestActiveAt.Equal(start.Add(6*time.Second)) || got.LastEventKind != "tool_started" {
		t.Fatalf("wrong activity: %+v", got)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "private-tool-id") {
		t.Fatalf("payload retained: %s", encoded)
	}
}

func TestActivityCapsInflightTrackingAndSupportsPi(t *testing.T) {
	var s activityStream
	start := time.Now()
	for i := 0; i < 300; i++ {
		s.consume(config.HarnessPiCLI, []byte(fmt.Sprintf(`{"type":"tool_execution_start","toolCallId":"%d","toolName":"private"}`, i)), start)
	}
	s.consume(config.HarnessPiCLI, []byte(`{"type":"tool_execution_end","toolCallId":"0"}`), start.Add(time.Second))
	got := s.finish()
	if got.Coverage != "partial" || got.ActiveTools != 255 || got.ToolsCompleted != 1 || got.ToolMilliseconds != 1000 {
		t.Fatalf("unbounded or incorrect activity: %+v", got)
	}
	var claude activityStream
	claude.consume(config.HarnessClaudeCLI, []byte(`{"type":"result","usage":{}}`), start)
	if claude.finish().Coverage != "unavailable" {
		t.Fatal("invented tool activity for non-streaming harness")
	}
}

func TestBufferedFallbackDoesNotInventActivityTiming(t *testing.T) {
	_, stream := observeHarness(t.Context(), config.HarnessCodexCLI)
	stream.finish(`{"type":"item.started","item":{"id":"shell","type":"command_execution"}}`, nil)
	if got := stream.activity.finish(); got.Coverage != "unavailable" || got.Events != 0 || !got.LastEventAt.IsZero() {
		t.Fatalf("invented timing from buffered output: %+v", got)
	}
}

func TestTimedOutHarnessActivitySurvivesCleanupAndHistory(t *testing.T) {
	store := metrics.NewStore(filepath.Join(t.TempDir(), "metrics", "history.jsonl"))
	base := metrics.Event{AttemptID: "attempt", Kind: metrics.EventStarted}
	if err := store.Append(base); err != nil {
		t.Fatal(err)
	}
	ctx := metrics.WithAttemptTrace(t.Context(), metrics.NewAttemptTrace(store.Append, base))
	finish := metrics.StartStage(ctx, metrics.StageHarnessRun)
	_, stream := observeHarness(ctx, config.HarnessCodexCLI)
	_, _ = stream.Write([]byte(`{"type":"item.started","item":{"id":"running","type":"command_execution","command":"secret"}}` + "\n"))
	metrics.StartStage(ctx, metrics.StageHarnessCleanup)(metrics.StageOutcomeFailed, "cleanup_unresolved", "manual", metrics.Usage{})
	usage := stream.finish("", context.DeadlineExceeded)
	finish(metrics.StageOutcomeFailed, "cleanup_unresolved", "manual", usage)
	history, err := store.Read()
	if err != nil || history.MalformedRecords != 0 || len(history.Attempts) != 1 {
		t.Fatalf("history: %+v %v", history, err)
	}
	var got *metrics.HarnessActivity
	for _, stage := range history.Attempts[0].Stages {
		if stage.Name == metrics.StageHarnessRun {
			got = stage.HarnessActivity
		} else if stage.HarnessActivity != nil {
			t.Fatal("activity attached to cleanup")
		}
	}
	if got == nil || got.ActiveTools != 1 || got.LastEventKind != "shell_started" || usage.Coverage != metrics.UsageUnavailable {
		t.Fatalf("lost timeout evidence: %+v %+v", got, usage)
	}
}
