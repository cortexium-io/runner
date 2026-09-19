package execution

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type usageEventRunner struct{ subprocess.OSRunner }

func (r usageEventRunner) RunBoundedHeadTailInput(ctx context.Context, _ string, args []string, dir string, timeout time.Duration, input io.Reader, limit int, marker string) (subprocess.Result, error) {
	return r.OSRunner.RunBoundedHeadTailInput(ctx, "sh", []string{"-c", "cat; exit 23"}, dir, timeout, input, limit, marker)
}

type ownershipArgumentRunner struct {
	subprocess.OSRunner
	t *testing.T
}

func (r ownershipArgumentRunner) RunBoundedHeadTailInput(ctx context.Context, _ string, args []string, dir string, timeout time.Duration, input io.Reader, limit int, marker string) (subprocess.Result, error) {
	r.t.Helper()
	if len(args) != 4 || !slices.Equal(args[:2], []string{"-c", `shell_environment_policy.inherit="none"`}) || args[2] != "-c" {
		r.t.Fatalf("replaced the configured shell policy: %v", args)
	}
	encoded, found := strings.CutPrefix(args[3], "shell_environment_policy.set."+subprocess.OwnershipEnvironmentVariable+"=")
	owner, err := strconv.Unquote(encoded)
	if !found || err != nil || owner == "" {
		r.t.Fatalf("missing marker policy: %q", args[3])
	}
	return r.OSRunner.RunBoundedHeadTailInput(ctx, "sh", []string{"-c", `test "$CORTEXIUM_RUNNER_PROCESS_OWNER" = "$1"`, "fixture", owner}, dir, timeout, input, limit, marker)
}

func TestCodexOwnershipForwardingPreservesEnvironmentPolicy(t *testing.T) {
	_, _, err := runCodexWithUsage(t.Context(), ownershipArgumentRunner{t: t}, "fixture", []string{"-c", `shell_environment_policy.inherit="none"`}, "", time.Minute, nil)
	if err != nil {
		t.Fatalf("shell policy and process marker differ: %v", err)
	}
}

func TestCodexFailedUsageSurvivesDiagnosticTruncation(t *testing.T) {
	filler := strings.Repeat("progress without usage\n", 60000)
	reported := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":41,"cached_input_tokens":17,"output_tokens":5}}}}`
	var events []metrics.Event
	trace := metrics.NewAttemptTrace(func(event metrics.Event) error { events = append(events, event); return nil }, metrics.Event{AttemptID: "failure"})
	ctx := metrics.WithAttemptTrace(t.Context(), trace)
	result, usage, err := runCodexWithUsage(ctx, usageEventRunner{}, "fixture", nil, "", time.Minute, strings.NewReader(filler+reported+"\n"+filler))
	if err == nil || result.ExitCode != 23 {
		t.Fatalf("lost failed exit: %v, %d", err, result.ExitCode)
	}
	if strings.Contains(result.Stdout, "token_count") {
		t.Fatal("fixture did not discard middle usage event")
	}
	if !usage.Available || usage.Coverage != metrics.UsagePartial || usage.InputTokens != 41 || usage.CacheReadInputTokens != 17 || usage.OutputTokens != 5 {
		t.Fatalf("lost partial usage: %+v", usage)
	}
	if len(result.Stdout) > maxHarnessDiagnosticBytes {
		t.Fatal("diagnostic cap increased")
	}
	if len(events) != 2 || events[0].Stage != metrics.StageHarnessCleanup || events[1].Outcome != metrics.StageOutcomeSucceeded {
		t.Fatalf("missing cleanup interval: %+v", events)
	}
}

func TestUsageStreamCoverageAndResynchronization(t *testing.T) {
	complete := `{"type":"turn.completed","usage":{"input_tokens":25,"cached_input_tokens":9,"output_tokens":7}}`
	for _, test := range []struct {
		name, stream, coverage string
		runErr                 error
		tokens                 int64
	}{
		{"complete", complete, metrics.UsageComplete, nil, 25},
		{"timeout after counters", complete, metrics.UsagePartial, context.DeadlineExceeded, 25},
		{"canceled", complete, metrics.UsagePartial, context.Canceled, 25},
		{"no usage", `{"type":"turn.failed"}`, metrics.UsageUnavailable, errors.New("exit"), 0},
		{"next turn started", complete + "\n" + `{"type":"turn.started"}`, metrics.UsagePartial, nil, 25},
		{"duplicate cumulative", complete + "\n" + complete, metrics.UsageComplete, nil, 25},
		{"oversized then usage", strings.Repeat("x", maxHarnessResultBytes+1) + "\n" + complete, metrics.UsageComplete, nil, 25},
		{"invalid event", `{"type":"item.completed","usage":{"input_tokens":900}}`, metrics.UsageUnavailable, nil, 0},
		{"negative counters", `{"type":"turn.completed","usage":{"input_tokens":-1}}`, metrics.UsageUnavailable, nil, 0},
		{"invalid later snapshot", complete + "\n" + `{"type":"turn.completed","usage":{"input_tokens":-1}}`, metrics.UsagePartial, nil, 25},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &usageStream{kind: config.HarnessCodexCLI}
			// Exercise split JSON records rather than only newline-sized writes.
			for offset := 0; offset < len(test.stream); offset += 7919 {
				_, _ = stream.Write([]byte(test.stream[offset:min(offset+7919, len(test.stream))]))
			}
			usage := stream.finish(complete, test.runErr)
			if usage.Coverage != test.coverage || usage.InputTokens != test.tokens || usage.Available != (test.coverage != metrics.UsageUnavailable) {
				t.Fatalf("usage: %+v", usage)
			}
			if usage.ReportedCostUSD != nil {
				t.Fatal("invented cost")
			}
		})
	}
}

func TestPiUsageDoesNotDoubleCountMessagesAndTerminalSnapshot(t *testing.T) {
	message := `{"role":"assistant","timestamp":123,"usage":{"input":12,"output":5,"cost":{"total":0.2}}}`
	line := `{"type":"message_end","message":` + message + `}` + "\n"
	partial := &usageStream{kind: config.HarnessPiCLI}
	_, _ = io.WriteString(partial, line+line)
	usage := partial.finish("", context.Canceled)
	if usage.InputTokens != 12 || usage.Coverage != metrics.UsagePartial || usage.ReportedCostUSD == nil || *usage.ReportedCostUSD != .2 {
		t.Fatalf("partial message counters: %+v", usage)
	}
	complete := &usageStream{kind: config.HarnessPiCLI}
	end := `{"type":"agent_end","messages":[` + message + `]}` + "\n"
	_, _ = io.WriteString(complete, line+line+end+end)
	usage = complete.finish("", nil)
	if usage.InputTokens != 12 || usage.OutputTokens != 5 || usage.Coverage != metrics.UsageComplete || *usage.ReportedCostUSD != .2 {
		t.Fatalf("double-counted terminal snapshot: %+v", usage)
	}
}

func TestClaudeUsageSurvivesFailure(t *testing.T) {
	stream := &usageStream{kind: config.HarnessClaudeCLI}
	usage := stream.finish("{\n\"usage\":{\"input_tokens\":12,\"output_tokens\":5},\n\"total_cost_usd\":0.2\n}", errors.New("failed"))
	if !usage.Available || usage.Coverage != metrics.UsagePartial || usage.InputTokens != 12 || usage.ReportedCostUSD == nil || *usage.ReportedCostUSD != .2 {
		t.Fatalf("lost Claude usage: %+v", usage)
	}
}

func TestPiUsageIncludesSeparateAgentLoops(t *testing.T) {
	first := `{"role":"assistant","timestamp":123,"usage":{"input":12,"output":5}}`
	second := `{"role":"assistant","timestamp":456,"usage":{"input":20,"output":3}}`
	stream := &usageStream{kind: config.HarnessPiCLI}
	for _, message := range []string{first, second} {
		_, _ = io.WriteString(stream, `{"type":"agent_start"}`+"\n"+
			`{"type":"message_end","message":`+message+"}\n"+
			`{"type":"agent_end","messages":[`+message+"]}\n")
	}
	usage := stream.finish("", nil)
	if usage.InputTokens != 32 || usage.OutputTokens != 8 || usage.Turns != 2 || usage.Coverage != metrics.UsageComplete {
		t.Fatalf("lost an agent loop or counted messages twice: %+v", usage)
	}
}

func TestPiMissingMessageCountersRemainPartial(t *testing.T) {
	stream := &usageStream{kind: config.HarnessPiCLI}
	usage := stream.finish(`{"type":"agent_end","messages":[{"role":"assistant","timestamp":123,"usage":{"input":12}},{"role":"assistant","timestamp":456}]}`, nil)
	if usage.InputTokens != 12 || usage.Coverage != metrics.UsagePartial {
		t.Fatalf("missing message counters claimed complete: %+v", usage)
	}
}

func TestClaudeCostOnlyReportIsPreservedWithoutInventingTokens(t *testing.T) {
	for _, runErr := range []error{nil, context.Canceled} {
		stream := &usageStream{kind: config.HarnessClaudeCLI}
		usage := stream.finish(`{"total_cost_usd":0.2}`, runErr)
		want := metrics.UsageComplete
		if runErr != nil {
			want = metrics.UsagePartial
		}
		if usage.Available || usage.InputTokens != 0 || usage.Coverage != want || usage.ReportedCostUSD == nil || *usage.ReportedCostUSD != .2 || metrics.ValidateUsage(usage) != nil {
			t.Fatalf("cost-only report lost or invented tokens: %+v", usage)
		}
	}
}

func TestCleanupFailureOverridesProviderRetry(t *testing.T) {
	err := errors.Join(context.DeadlineExceeded, &subprocess.CleanupError{Err: errors.New("surviving child")})
	output, known := classifyHarnessFailure(err, HarnessFailureEvidence{FailureClass: FailureCapacityExhausted, RetryDisposition: RetryAutomatic})
	if !known || output.FailureClass != FailureCleanupUnresolved || output.RetryDisposition != RetryManual {
		t.Fatalf("cleanup failure automatically retried: %+v", output)
	}
}
