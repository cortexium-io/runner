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

## Engineering

Use minimum sufficient complexity and idiomatic Go. Keep one coordinator and one
private transactional store. Core contracts live next to their consumers;
adapters depend on the core, and main wires them. Configuration files own policy;
SQLite stores resolved run settings and bounded operational facts. Do not add
another scheduler, event store, public plugin framework, whole-task checksum
gates or generic abstraction without a concrete need.

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
