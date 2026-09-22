package metrics

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
)

func TestNormalizeUsagePreservesProviderBasisCoverageAndModels(t *testing.T) {
	for _, test := range []struct {
		harness      string
		input, total int64
	}{
		{"codex", 100, 120}, {"claude", 145, 165}, {"pi", 145, 165},
	} {
		t.Run(test.harness, func(t *testing.T) {
			cost := .25
			raw := Usage{Available: true, Coverage: UsagePartial, InputTokens: 100, CacheReadInputTokens: 40,
				CacheWriteInputTokens: 5, OutputTokens: 20, ReasoningOutputTokens: 7, ReportedCostUSD: &cost,
				Models: map[string]ModelUsage{"model": {InputTokens: 100, CacheReadInputTokens: 40, CacheWriteInputTokens: 5, OutputTokens: 20, ReportedCostUSD: &cost}}}
			before, _ := json.Marshal(raw)
			u, err := NormalizeUsage(raw, test.harness)
			if err != nil || u.TokenAccounting != InclusiveInputV1 || u.Coverage != UsagePartial || u.InputTokens != test.input || u.Models["model"].InputTokens != test.input || *u.ReportedCostUSD != cost {
				t.Fatalf("normalized=%+v error=%v", u, err)
			}
			if total, ok := ReportedTokens(u); !ok || total != test.total {
				t.Fatalf("total=%d known=%t", total, ok)
			}
			again, err := NormalizeUsage(u, "different current config")
			if err != nil || !reflect.DeepEqual(u, again) {
				t.Fatal("normalization was not idempotent")
			}
			u.Models["new"] = ModelUsage{}
			*u.ReportedCostUSD = 7
			model := u.Models["model"]
			*model.ReportedCostUSD = 8
			after, _ := json.Marshal(raw)
			if !bytes.Equal(before, after) {
				t.Fatal("normalization aliased provider input")
			}
		})
	}
}

func TestUnknownAndMixedLegacyUsageCannotAcquireInventedBasis(t *testing.T) {
	raw := Usage{Available: true, Coverage: UsageComplete, InputTokens: 12} // Even with zero cache.
	unknown, err := NormalizeUsage(raw, "")
	if err != nil || unknown.TokenAccounting != AccountingUnresolved {
		t.Fatalf("unknown=%+v err=%v", unknown, err)
	}
	canonical, _ := NormalizeUsage(raw, "codex")
	for _, u := range []Usage{unknown, raw.Add(raw), canonical.Add(raw), raw.Add(canonical), Usage{}, {Coverage: UsageUnavailable}} {
		if _, ok := ReportedTokens(u); ok {
			t.Fatalf("invented total for %+v", u)
		}
		if u.Available {
			again, err := NormalizeUsage(u, "codex")
			if err != nil || again.TokenAccounting != AccountingUnresolved {
				t.Fatalf("guessed lost provenance: %+v %v", again, err)
			}
		}
	}
}

func TestSummaryJSONDistinguishesUnavailableTotalFromReportedZero(t *testing.T) {
	for _, test := range []struct {
		name  string
		usage Usage
		want  string
	}{
		{"missing", Usage{}, `"reported_tokens":null`},
		{"unknown basis", Usage{Available: true, Coverage: UsageComplete}, `"reported_tokens":null`},
		{"reported zero", Usage{Available: true, Coverage: UsageComplete, TokenAccounting: InclusiveInputV1}, `"reported_tokens":0`},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(Summarize([]Attempt{{Completed: true, Event: Event{Usage: test.usage}}}))
			if err != nil || !bytes.Contains(data, []byte(test.want)) {
				t.Fatalf("JSON=%s err=%v", data, err)
			}
		})
	}
}

func TestUsageRejectsMalformedSubsetsAndNormalizationOverflow(t *testing.T) {
	for _, test := range []struct {
		name, harness string
		usage         Usage
	}{
		{"negative", "codex", Usage{Available: true, InputTokens: -1}},
		{"cache exceeds input", "codex", Usage{Available: true, InputTokens: 2, CacheReadInputTokens: 3}},
		{"cache categories exceed input", "codex", Usage{Available: true, InputTokens: 3, CacheReadInputTokens: 2, CacheWriteInputTokens: 2}},
		{"cache sum overflow", "codex", Usage{Available: true, InputTokens: math.MaxInt64, CacheReadInputTokens: math.MaxInt64, CacheWriteInputTokens: 1}},
		{"reasoning exceeds output", "codex", Usage{Available: true, OutputTokens: 2, ReasoningOutputTokens: 3}},
		{"total overflow", "codex", Usage{Available: true, InputTokens: math.MaxInt64, OutputTokens: 1}},
		{"exclusive normalization overflow", "claude", Usage{Available: true, InputTokens: math.MaxInt64, CacheReadInputTokens: 1}},
		{"model normalization overflow", "pi", Usage{Models: map[string]ModelUsage{"m": {InputTokens: math.MaxInt64, CacheWriteInputTokens: 1}}}},
		{"model subset", "codex", Usage{Models: map[string]ModelUsage{"m": {InputTokens: 1, CacheReadInputTokens: 2}}}},
		{"invalid cost", "claude", Usage{ReportedCostUSD: newFloat(math.Inf(1))}},
	} {
		t.Run(test.name, func(t *testing.T) {
			u, err := NormalizeUsage(test.usage, test.harness)
			if err == nil || ValidateUsage(u) == nil {
				t.Fatalf("malformed usage admitted: %+v %v", u, err)
			}
			for range 3 {
				u = u.Add(Usage{Available: true, TokenAccounting: InclusiveInputV1, InputTokens: math.MaxInt64})
				if _, ok := ReportedTokens(u); ok || ValidateUsage(u) == nil {
					t.Fatal("poisoned usage became plausible")
				}
			}
		})
	}
}

func newFloat(n float64) *float64 { return &n }

func TestUsageAddChecksEveryCounterAndDoesNotAlias(t *testing.T) {
	for _, u := range []Usage{
		{Available: true, TokenAccounting: InclusiveInputV1, InputTokens: math.MaxInt64},
		{Turns: math.MaxInt64}, {APIDurationMilliseconds: math.MaxInt64},
		{ReportedCostUSD: newFloat(math.MaxFloat64)},
		{TokenAccounting: InclusiveInputV1, Models: map[string]ModelUsage{"m": {InputTokens: math.MaxInt64}}},
		{Models: map[string]ModelUsage{"m": {ReportedCostUSD: newFloat(math.MaxFloat64)}}},
	} {
		total := u.Add(u)
		for range 3 {
			if ValidateUsage(total) == nil {
				t.Fatalf("overflow was accepted: %+v", total)
			}
			total = total.Add(u)
		}
	}
	u := Usage{TokenAccounting: InclusiveInputV1, Models: map[string]ModelUsage{"m": {InputTokens: 3, ReportedCostUSD: newFloat(.5)}}, ReportedCostUSD: newFloat(.5)}
	before, _ := json.Marshal(u)
	sum := u.Add(u)
	sum.Models["m"] = ModelUsage{InputTokens: 999}
	*sum.ReportedCostUSD = 99
	after, _ := json.Marshal(u)
	if !bytes.Equal(before, after) {
		t.Fatal("Add mutated source maps or costs")
	}
}

func TestStoreNormalizesHistoricalLeavesWithoutRewritingOrDoubleCountingStages(t *testing.T) {
	store := NewStore(privateMetricsPath(t))
	for _, harness := range []string{"codex", "claude", "pi", "unknown"} {
		raw := Usage{Available: true, InputTokens: 100, CacheReadInputTokens: 40, OutputTokens: 20,
			Models: map[string]ModelUsage{"m": {InputTokens: 100, CacheReadInputTokens: 40, OutputTokens: 20}}}
		event := Event{Kind: EventCompleted, AttemptID: harness, Harness: harness, Usage: raw}
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
		event.Kind, event.StageID, event.Stage, event.Outcome = EventStageCompleted, "harness", StageHarnessRun, StageOutcomeSucceeded
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadFile(store.Path())
	history, err := store.Read()
	if err != nil || history.MalformedRecords != 0 || len(history.Attempts) != 4 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	var known []Attempt
	for _, a := range history.Attempts {
		if a.Harness == "unknown" {
			if a.Usage.TokenAccounting != AccountingUnresolved {
				t.Fatal("unknown harness guessed")
			}
			continue
		}
		want := int64(100)
		if a.Harness != "codex" {
			want = 140
		}
		if a.Usage.InputTokens != want || a.Usage.Models["m"].InputTokens != want || a.Stages[0].Usage.InputTokens != want || a.Usage.CoverageStatus() != UsageUnknown {
			t.Fatalf("lost leaf provenance: %+v", a)
		}
		known = append(known, a)
	}
	summary := Summarize(known)
	if summary.ReportedTokens == nil || *summary.ReportedTokens != 440 || summary.Stages[0].ReportedTokens == nil || *summary.Stages[0].ReportedTokens != 440 {
		t.Fatalf("summary double counted stages or cache: %+v", summary)
	}
	if Summarize(history.Attempts).ReportedTokens != nil {
		t.Fatal("mixed unresolved summary guessed total")
	}
	after, _ := os.ReadFile(store.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("read rewrote historical bytes")
	}
}
