package execution

import "testing"

func TestClaudeEnvelopeReportsUsageAndCost(t *testing.T) {
	stdout := `{"result":"{\"outcome\":\"succeeded\"}","duration_api_ms":1234,"num_turns":3,"total_cost_usd":0.42,"usage":{"input_tokens":100,"cache_creation_input_tokens":20,"cache_read_input_tokens":300,"output_tokens":40},"modelUsage":{"claude-opus":{"inputTokens":100,"outputTokens":40,"cacheReadInputTokens":300,"cacheCreationInputTokens":20,"costUSD":0.42}}}`
	message, usage, err := extractHarnessResultAndUsage("claude", stdout, "", false, false)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if message == "" || !usage.Available || usage.InputTokens != 100 || usage.CacheReadInputTokens != 300 || usage.CacheWriteInputTokens != 20 || usage.OutputTokens != 40 || usage.Turns != 3 {
		t.Fatalf("unexpected usage: %#v", usage)
	}
	if usage.ReportedCostUSD == nil || *usage.ReportedCostUSD != 0.42 || usage.Models["claude-opus"].InputTokens != 100 {
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
