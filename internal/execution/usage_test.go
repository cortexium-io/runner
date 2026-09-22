package execution

import (
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
)

func TestClaudeEnvelopeReportsUsageAndCost(t *testing.T) {
	stdout := `{"result":"{\"outcome\":\"succeeded\"}","duration_api_ms":1234,"num_turns":3,"total_cost_usd":0.42,"usage":{"input_tokens":100,"cache_creation_input_tokens":20,"cache_read_input_tokens":300,"output_tokens":40},"modelUsage":{"claude-opus":{"inputTokens":100,"outputTokens":40,"cacheReadInputTokens":300,"cacheCreationInputTokens":20,"costUSD":0.42}}}`
	message, usage, err := extractHarnessResultAndUsage("claude", stdout, "", false, false)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if message == "" || !usage.Available || usage.InputTokens != 420 || usage.CacheReadInputTokens != 300 || usage.CacheWriteInputTokens != 20 || usage.OutputTokens != 40 || usage.Turns != 3 {
		t.Fatalf("unexpected usage: %#v", usage)
	}
	if usage.ReportedCostUSD == nil || *usage.ReportedCostUSD != 0.42 || usage.Models["claude-opus"].InputTokens != 420 {
		t.Fatalf("cost/model usage lost: %#v", usage)
	}
}

func TestParseCodexUsageUsesLastCumulativeEvent(t *testing.T) {
	stdout := "{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":10,\"cached_input_tokens\":2,\"output_tokens\":3}}\n" +
		"{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":25,\"cached_input_tokens\":9,\"cache_write_input_tokens\":4,\"output_tokens\":7,\"reasoning_output_tokens\":5}}\n"
	usage := parseCodexUsage(stdout)
	if !usage.Available || usage.InputTokens != 25 || usage.CacheReadInputTokens != 9 || usage.CacheWriteInputTokens != 4 || usage.OutputTokens != 7 || usage.ReasoningOutputTokens != 5 {
		t.Fatalf("unexpected usage: %#v", usage)
	}
	if usage.ReportedCostUSD != nil {
		t.Fatalf("Codex cost must remain unreported: %#v", usage)
	}
}

func TestMalformedUsageNeverBecomesACompleteZeroOrDropsEarlierProof(t *testing.T) {
	for _, invalid := range []string{
		`{"input_tokens":9223372036854775807,"cache_read_input_tokens":1}`,
		`{"input_tokens":-1}`, `{"input_tokens":9223372036854775808}`, `{"input_tokens":"12"}`,
	} {
		_, usage, err := extractHarnessResultAndUsage("claude", `{"result":"ok","usage":`+invalid+`}`, "", false, false)
		if err == nil {
			t.Fatalf("invalid Claude report accepted: %+v", usage)
		}
	}
	good := `{"type":"turn.completed","usage":{"input_tokens":25,"cached_input_tokens":9,"output_tokens":7}}`
	for _, bad := range []string{
		`{"type":"turn.completed","usage":{"input_tokens":2,"cached_input_tokens":3}}`,
		`{"type":"turn.completed","usage":{"input_tokens":9223372036854775808}}`,
		`{"type":"turn.completed","usage":{"input_tokens":"12"}}`,
	} {
		s := &usageStream{kind: config.HarnessCodexCLI}
		u := s.finish(good+"\n"+bad, nil)
		if u.Coverage != metrics.UsagePartial || u.InputTokens != 25 || u.TokenAccounting != metrics.InclusiveInputV1 {
			t.Fatalf("lost partial Codex provenance: %+v", u)
		}
	}
}

func TestPiMalformedAndOverflowingMessagesPreserveEarlierPartialUsage(t *testing.T) {
	first := `{"role":"assistant","timestamp":1,"usage":{"input":12,"cacheRead":3,"cacheWrite":2,"output":5}}`
	for _, bad := range []string{
		`{"input":9223372036854775807,"cacheRead":1}`, `{"input":-1}`, `{"input":9223372036854775808}`,
		`{"input":9223372036854775800}`, // Valid alone, but overflows accumulated input.
	} {
		stdout := `{"type":"agent_end","messages":[` + first + `]}` + "\n" +
			`{"type":"agent_end","messages":[{"role":"assistant","timestamp":2,"usage":` + bad + `}]}`
		u, err := usageFromPiEventStream(stdout)
		if err == nil || u.InputTokens != 17 || u.OutputTokens != 5 || u.Coverage != metrics.UsagePartial {
			t.Fatalf("direct parser lost partial proof: %+v %v", u, err)
		}
		s := &usageStream{kind: config.HarnessPiCLI}
		u = s.finish(stdout, nil)
		if u.InputTokens != 17 || u.OutputTokens != 5 || u.Coverage != metrics.UsagePartial || metrics.ValidateUsage(u) != nil {
			t.Fatalf("stream lost partial proof: %+v", u)
		}
	}
}
