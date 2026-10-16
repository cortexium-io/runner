# Model and role evaluation

The shipped execution adapter owns role instructions and structured results.
Normal Go tests validate deterministic contracts without calling a model.
A live evaluation is an explicit, paid Codex invocation, using the selected model
and reasoning; it is not part of routine packaging.

From the checkout, choose one existing suite and a new private evidence directory:

```sh
sh scripts/test-agent-behavior.sh ready gpt-6.1-sol high /absolute/evidence/ready
```

| Suite | Behavioral boundary |
| --- | --- |
| `ready` | Paired review cases: weak assertions versus sufficient consolidated proof, fabricated versus supplied evidence, real defects versus speculative concerns, scoped baseline exceptions versus new defects. |
| `plan` | Plan decomposition and missing Ready verification briefs under the approved quality agreement. |
| `ci-repair` | Repair of an observed pinned CI failure within the approved obligation. |
| `combined` | Combined Plan outcome: reject a missing consumer behavior despite passing unit checks; accept the correct control. |
| `conflict-repair` | Supported text conflict resolution with candidate and base content preserved. |

The wrapper requires an unchanged clean checkout and records its commit, model/reasoning,
test output and retained per-case inputs/results. Each invocation has the existing
three-minute case timeout; the wrapper bounds the selected suite at forty minutes.
Capability failure or a failing behavioral case fails the run. Use a new absolute evidence path outside the checkout and run from the exact
candidate being assessed. A changed checkout invalidates the run.
Inspect each result against the case expectation, including unnecessary changes
and redundant tests. A green process exit alone is not behavioral adequacy.

Native Codex deterministic-check sandbox qualification runs separately and does
not select a model:

```sh
RUNNER_LOCAL_LIVE_CHECKS=1 go test -count=1 -timeout 5m ./internal/runner/execution -run '^TestLiveQualityCheckBoundary$'
```

Record actual calls, elapsed time, interventions, repeated checks and reasons.
Retain provider-reported usage when available; do not infer tokens or money from
elapsed time. Existing qualification records in the
[redesign plan](runner-redesign-proposal.md) remain evidence for unchanged prompts.
R4 retires the old multi-harness evaluation launcher and its Project-runtime suite;
the historical results do not authorize unsupported harnesses in this release.

## Local harness benchmark learnings

The owner's [Assess OpenCode 2.0 for local models chat](codex://threads/01a13e6c-3d8a-76d2-b509-6e295a1781b4)
contains isolated experiments, not qualification of Runner adapters. The successful
Magnitude repair trials used Pi 0.99.1 and OpenCode 2.0.29 with Qwen3.8-27b GGUF
Q6 and requested low reasoning, with thinking observed. Both eventually passed six
independent probes, race/vet and four no-CGO builds. Pi needed no corrective code
feedback; OpenCode needed one round after its change accepted views as tables.
Their context and backend conditions differed. The fresh matched OpenCode rerun
is pending; these results do not establish a speed/quality ranking.

- Validate terminal protocol completion and the required structured result;
  process exit zero alone is insufficient. Pi exited zero after a failed stream
  without a final report, then completed from its retained compacted session.
- Separate harness capabilities from model and inference-backend capabilities.
  Tool readiness, namespaced tool schemas, structured output and streaming must
  work on the actual selected path. Never silently substitute a provider/model.
- Bound time, output, context and invocations. Preserve counters and evidence
  across resume; compaction is not a fresh budget. The smaller trial used a
  49,152-token context, about 24,576-token compaction threshold, 8,192 retained
  recent tokens and 16,384 output tokens. These are observations, not universal
  defaults or a new public context-management framework.
- Keep thinking enabled at low/medium for these local Qwen trials. Requested
  reasoning and observed thinking are evidence; the backend's effective setting
  was not independently exposed. This does not replace the selected Codex high
  profiles or prove a hard thinking-token limit.
- Backend memory/stream failures are infrastructure evidence. They do not justify
  rewriting source, inventing a baseline exception or consuming defect repair.
- Repository policy and the author define meaningful proof. Independent probes
  exposed invalid-schema acceptance and failed-COMMIT cleanup defects despite
  worker-written tests and green full gates. Map those guarantees to the current
  store before adding coverage; the benchmark's different store implementation
  and patches are not production fixes to cherry-pick.

Private evidence is indexed in
`~/.local/share/cortexium-runner/benchmarks/20261016-pi-magnitude-repair/summary.json`
and `20261016-magnitude-repair-retry-1/summary.json`; README files record source
hashes, observations, interventions and limitations. The matched rerun has a
separate `20261016-opencode-magnitude-matched` evidence directory. Historical
multi-harness tests and benchmark settings do not admit new production adapters.
