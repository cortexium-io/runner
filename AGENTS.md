# Runner working agreements

## Current authority and scope

This repository owns Cortexium Runner. Use explicit user direction, this file,
then the [process-core design](docs/process-core-design.md), implemented contracts
and tests. [Architecture](docs/architecture.md) explains the actual first slice.
Earlier ADRs, redesign plans and qualification records describe the previous
runtime; they are historical evidence, not current implementation requirements.
Do not create a parallel task ledger.

The owner authorized a clean implementation on `codex/runner-process-core` after
agreeing the design. The first slice is one local Go service, YAML/JSON
configuration, SQLite operational state, MCP/CLI control and a deterministic
executor. Finish the complete two-step journey and its recovery/storage proofs.
No installed service, other project, release publication or cutover is authorized.
Real harnesses, roles/skills, benchmarks, Git workspaces and reporting integrations
belong to later explicitly chosen slices. Do not copy old runtime features back.

The owner subsequently authorized the first Codex adapter under the
[agreed ownership, assignment and qualification contract](docs/process-core-design.md#agreed-first-codex-adapter-ownership-and-launch).
It supports text-only input/output and `input-only` access through an instance-owned
`codex app-server`, with no selected task environment or external tools. Workspace
coding, skills/tool catalogs, deployment and installed-service changes remain
outside this slice. The initial three native task executions have been used;
further native generation needs renewed authorization. Ordinary tests skip native
qualification. Protocol/readiness probes that submit no turn use no model call.

The owner subsequently authorized the
[first writable fixture and qualification](docs/process-core-design.md#agreed-first-writable-fixture-and-qualification):
one isolated Go source workspace, retained patch, and a fixed check step. Use one
implementation owner. The renewed native allowance is exactly one coding turn,
Sol / xhigh, at most five minutes within fifteen minutes of qualification; no
automatic repair or model substitution. That one native turn is now consumed;
the writable journey completed. Further native generation needs a renewed
allowance. Normal tests remain model-free. No installation or other-project changes.

The owner subsequently authorized one bounded writable recovery exercise:
abruptly stop the Runner owner process while a native command is active, reopen
the same state, reconcile the existing native attempt, and cancel through normal
controls while retaining its edits. Allow at most one new Sol/xhigh coding turn,
five minutes per attempt and fifteen minutes for qualification. Use the durable
one-turn receipt; no automatic repair turn or replacement task is authorized.
`RUNNER_NATIVE_RECOVERY=1` opts into this exercise; normal tests skip it. The
one-turn allowance was consumed without injecting the crash: code-mode shell
output lacked the notification expected by the first test synchronizer. The
synchronizer now observes the explicit readiness file. The owner explicitly
approved one additional Sol/xhigh coding turn, capped at five minutes, and that
corrected exercise passed: abrupt owner exit, same native attempt reattached,
replacement rejected, cancellation settled, edits retained and cleanup completed.
Both recovery allowances are consumed; further native generation requires renewed
authorization. Preserve both results and their durable no-repeat receipts.

The owner subsequently authorized taking over techba.se and Jack's Break through
fresh work in the new Runner after coordinating with their existing chats. Do not
import or resume old Runner tasks/state. The Techbase publication is complete;
there is no remaining authorized publication task. Jack's old worker is stopped,
and its completed implementation is committed at `cdff68477086d1d2b70f4dd989868e0b15adcf58`.
The first fresh pilot is one bounded Sol/xhigh source-edit attempt correcting
Futoshiki word-button typography, with a five-minute attempt limit and retained
patch. Reuse the existing sandboxed source-edit capability; no Node installation
or permission expansion in the worker. Coordinator-owned focused frontend checks
remain outside the engine for this pilot, and must be reported separately.
No production merge/deployment or replacement of the user's existing preview is
authorized before the agreed local review. The source prompt names a bounded
source task; language/framework instructions belong to the selected role/task.
That pilot allowance is consumed. The attempt timed out, and its partial Vue
edit was recovered without another turn. Its focused component tests/rendered
checks pass, but the Runner result remains Failed. A new coding turn requires a
new task/allowance; do not retry the pilot silently. The pilot also exposed and
fixed client-side MCP error reporting and collection of unchanged large assets.

The owner subsequently authorized diagnosing and fixing the stalled pilot on
30 October 2026. Verify the scoped correction with one fresh Sol/xhigh coding
attempt, at most five minutes, without an automatic retry or model substitution.
Preserve a bounded diagnostic summary before native cleanup; do not retain a
second transcript or change the old pilot's Failed result. This allowance is
separate from the consumed pilot and recovery allowances above.
The fresh attempt completed in 42.318 seconds with 34,731 known native tokens;
its patch passed the existing focused tests and rendered typography checks.
That allowance is now consumed. The guidance correction has one successful live
result; the original stall's exact cause remains unestablished.

The owner subsequently authorized the bounded configurable command/check step
and using it to finish Jack's Break through the local review handoff. Use
read-only source and owned scratch; commands are
configured argv, not model-selected shell strings. Preserve only bounded command
facts and declared artifacts. Existing evidence remains valid while its inputs
are unchanged. The exact new serving executable needs its required Nonogram
qualification once. Updating the existing local preview with its saved progress
preserved is authorized; production merge/deployment and installed-service cutover
are not. No additional model generation is needed for this command-only work.
The connection-scoped native command API lost settlement evidence during this
exercise. The new command harness therefore uses a durable Go worker around
`codex sandbox`; it is an internal execution mode, not another scheduler. The
existing fixed Go-check adapter is unchanged. Preserve the earlier uncertain
results rather than treating service shutdown as command settlement.

## Engineering

Use minimum sufficient complexity and idiomatic Go. Keep one coordinator and one
private transactional store. Core contracts live next to their consumers;
adapters depend on the core, and main wires them. Configuration files own policy;
SQLite stores resolved run settings and bounded operational facts. Do not add
another scheduler, event store, public plugin framework, whole-task checksum
gates or generic abstraction without a concrete need.

Write long composite literals across multiple lines, with one field or element
per line, and use `gofmt` for alignment. Keep short, simple literals compact.
Avoid dense single-line initializers that obscure the structure of a value.

Do not introduce or store Python source, packages, environments or caches.
Preserve unrelated work. A replacement removes obsolete paths and updates their
consumers; it need not preserve behavior from the old pre-stable runtime.

Persist dispatch identity before execution and results before downstream work or
Release. Reconcile uncertainty before replacement. Cancellation requires observed
settlement. Admission limits must leave room to finish/recover admitted work.
Only delete resources whose ownership and lifecycle are known. Never treat an
unqualified adapter as safe for production, arbitrary external processes or
hostile input merely because its interface compiles.

## Verification

Use the smallest reliable evidence for changed behavior. Extend the owning suite,
check duplication and placement, and state the defect or contract a test proves.
Keep each runnable example in `examples/<name>/`. Keep test fixtures in the
repository-root `testdata/`, grouped by purpose and separate from examples. Test only relevant
contracts; keep suites concise and coherent, with no duplicated proof.
Table-driven tests suit related cases; they are not a requirement for unrelated
scenarios. Avoid arbitrary synchronization sleeps and repeated passing checks.
The deterministic core suite owns lifecycle/recovery/storage; config tests own
file/edit semantics; MCP tests own the wire/access boundary. Do not repeat the
whole failure matrix per transport or adapter.

Required implementation gates:

```sh
go test -race -timeout 15m ./...
go vet ./...
```

Run broader platform or real harness qualification only when relevant. Review the
complete diff before finishing and report actual verification and limitations.
Use one implementation owner for this bounded slice; do not add agents without
explicit authorization. Publication and installation need separate authorization.
