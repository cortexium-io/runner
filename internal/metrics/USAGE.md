# Reported-token accounting

New provider reports carry `token_accounting: inclusive_input_v1`. `input_tokens`
includes cache-read/cache-write tokens, and `output_tokens` includes reasoning
tokens. Those fields remain useful breakdowns but are not added again. The
single checked `ReportedTokens` function supplies admission and summary totals;
comparison ingestion uses these same production paths. A summary's null
`reported_tokens` is unavailable/unresolved, not zero. Coverage (`complete`,
`partial`, `unknown`, `unavailable`) remains a separate property: a partial count
is only a known lower bound. This is not an estimated bill or provider quota.

Provider basis:

- Codex preserves inclusive input. Pinned [Codex 0.155.1 protocol, `non_cached_input`](https://github.com/openai/codex/blob/rust-v0.155.1/codex-rs/protocol/src/protocol.rs#L2252-L2254)
  defines non-cached input as input minus cached input. Cumulative reports replace
  earlier snapshots; they are never summed as independent calls.
- Claude input excludes cache reads and writes, per the provider's
  [prompt-caching contract](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#tracking-cache-performance).
  Normalize both envelope counters and model breakdowns by adding those disjoint
  categories once; retain reported costs unchanged.
- Pi exposes disjoint input/cache categories. Its pinned [0.57.1 Responses adapter, lines 424–436](https://github.com/badlogic/pi-mono/blob/v0.57.1/packages/ai/src/providers/openai-responses-shared.ts#L424-L436)
  subtracts cached input from OpenAI input before reporting `input`; its
  [Anthropic adapter, lines 231–241](https://github.com/badlogic/pi-mono/blob/v0.57.1/packages/ai/src/providers/anthropic.ts#L231-L241)
  reports the provider's separate categories. Normalize each message before
  addition; streaming collection still deduplicates terminal/message events.

Historical JSONL is never rewritten. On read, a leaf's saved harness determines
its unmarked units; coverage is not upgraded. Known homogeneous attempt totals
use that same harness basis. Per-model counters normalize at the same boundary.
Unrecognized/missing harnesses and explicitly unresolved aggregates keep
`unresolved` accounting, including when their cache counters happen to be zero.
Adding unnormalized reports marks the result unresolved rather than guessing
that their units agree; a later harness cannot recover lost provenance.

Signed overflow, negative counters, impossible canonical cache/reasoning
subsets and invalid costs fail validation. `Usage.Add` retains an `invalid`
marker after an arithmetic failure so further additions cannot wrap a bad total
back into a plausible positive one. It copies maps and cost pointers. Parsers
preserve earlier valid partial observations when later events cannot be used.
Unknown token units block a token-total budget, not an independent attempt,
harness-time or complete cost budget. Invalid history retains its existing
fail-closed behavior.

## Observed coordination time

The bounded stages `authority_validation`, `evidence_capture`,
`snapshot_validation`, and `plan_integration` measure Runner-owned operations.
They can nest within one another and existing stages. Summary
`uncovered_duration_milliseconds` counts completed-attempt time outside the
union of completed observed stage intervals; it does not add overlapping
durations or infer what an unobserved interval contained. Missing historical
stages remain uncovered, not reconstructed from today's implementation. Neither
stage totals nor reported token counts alone establish wasted work or billing.
