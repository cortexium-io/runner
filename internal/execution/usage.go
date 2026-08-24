package execution

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"

	"github.com/cortexium-io/runner/internal/metrics"
)

type claudeResultEnvelope struct {
	Result            string          `json:"result"`
	StructuredOutput  json.RawMessage `json:"structured_output"`
	DurationAPIMillis int64           `json:"duration_api_ms"`
	Turns             int64           `json:"num_turns"`
	TotalCostUSD      *float64        `json:"total_cost_usd"`
	Usage             *struct {
		InputTokens         int64 `json:"input_tokens"`
		CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadTokens     int64 `json:"cache_read_input_tokens"`
		OutputTokens        int64 `json:"output_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		InputTokens         int64    `json:"inputTokens"`
		OutputTokens        int64    `json:"outputTokens"`
		CacheReadTokens     int64    `json:"cacheReadInputTokens"`
		CacheCreationTokens int64    `json:"cacheCreationInputTokens"`
		CostUSD             *float64 `json:"costUSD"`
	} `json:"modelUsage"`
}

func usageFromClaudeEnvelope(envelope claudeResultEnvelope) metrics.Usage {
	usage := metrics.Usage{
		APIDurationMilliseconds: envelope.DurationAPIMillis,
		Turns:                   envelope.Turns,
		ReportedCostUSD:         envelope.TotalCostUSD,
	}
	if envelope.Usage != nil {
		usage.Available = true
		usage.InputTokens = envelope.Usage.InputTokens
		usage.CacheReadInputTokens = envelope.Usage.CacheReadTokens
		usage.CacheWriteInputTokens = envelope.Usage.CacheCreationTokens
		usage.OutputTokens = envelope.Usage.OutputTokens
	}
	if len(envelope.ModelUsage) > 0 {
		usage.Models = map[string]metrics.ModelUsage{}
		for model, reported := range envelope.ModelUsage {
			usage.Models[model] = metrics.ModelUsage{
				InputTokens: reported.InputTokens, OutputTokens: reported.OutputTokens,
				CacheReadInputTokens: reported.CacheReadTokens, CacheWriteInputTokens: reported.CacheCreationTokens,
				ReportedCostUSD: reported.CostUSD,
			}
		}
	}
	return usage
}

// parseCodexUsage accepts current Codex exec JSONL and the token-count event
// shape used by older Codex builds. The last cumulative usage event wins.
func parseCodexUsage(stdout string) metrics.Usage {
	var latest metrics.Usage
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	scanner.Buffer(make([]byte, 64*1024), maxHarnessDiagnosticBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Usage *struct {
				InputTokens           int64 `json:"input_tokens"`
				CachedInputTokens     int64 `json:"cached_input_tokens"`
				CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
				OutputTokens          int64 `json:"output_tokens"`
				ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
			} `json:"usage"`
			Payload struct {
				Info struct {
					Total *struct {
						InputTokens           int64 `json:"input_tokens"`
						CachedInputTokens     int64 `json:"cached_input_tokens"`
						CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
						OutputTokens          int64 `json:"output_tokens"`
						ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		if event.Usage != nil {
			latest = metrics.Usage{
				Available: true, InputTokens: event.Usage.InputTokens,
				CacheReadInputTokens:  event.Usage.CachedInputTokens,
				CacheWriteInputTokens: event.Usage.CacheWriteInputTokens,
				OutputTokens:          event.Usage.OutputTokens,
				ReasoningOutputTokens: event.Usage.ReasoningOutputTokens,
			}
		}
		if event.Payload.Info.Total != nil {
			total := event.Payload.Info.Total
			latest = metrics.Usage{
				Available: true, InputTokens: total.InputTokens,
				CacheReadInputTokens:  total.CachedInputTokens,
				CacheWriteInputTokens: total.CacheWriteInputTokens,
				OutputTokens:          total.OutputTokens,
				ReasoningOutputTokens: total.ReasoningOutputTokens,
			}
		}
	}
	return latest
}
