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
