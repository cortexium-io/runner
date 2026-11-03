# Cortexium Runner

Runner is a configurable process engine. Ordinary Go code coordinates work;
roles, skills and tools supply domain judgment. Projects have independent
processes and runs, subject to explicit shared resource limits.

This branch contains the **local process core and complete development workflow**: a runnable local
service with file configuration, SQLite state, MCP/CLI control, a controlled
deterministic executor, input-only Codex execution and a bounded writable Go
workflow with model-free checks, bounded correction/measurement routes, read-only
review, saved approval decisions and retained candidate/delivery artifacts.
Dependency-ordered task collections expand fixed templates inside the same run.
Configured synchronous commands support checks, benchmarks, builds and project
delivery without another model coordinator.
It does not replace an installed Runner service or import its state.

## Try the two-step process

Use the Go version in `go.mod`. From this checkout:

```sh
go build -o ./cortexium-runner ./cmd/cortexium-runner
./cortexium-runner serve --config examples/demo/instance.yaml --data /absolute/new/private-state
```

In another terminal:

```sh
./cortexium-runner run --data /absolute/new/private-state --project demo --inputs '{"name":"Hello, Dan"}' --request-id first-demo
```

The first step retains a file. The second consumes that retained artifact and
returns `Hello, Dan`. No model, GitHub, credentials for a harness or network
service is needed. The local control endpoint uses a private owner token.

`run` submits once, waits in Go through the configured steps and normal cleanup,
then returns a compact result. It also returns when work reaches Waiting or has a
cleanup error. No model coordinates or polls the steps. The result includes
bounded output previews, artifact references, elapsed time, known token usage and
settlement/cleanup state, without echoing the request or role configuration.
Use `result --run RUN_ID` for a compact snapshot, or add `--wait` to rejoin work.
`inspect` remains available for detailed diagnosis. The MCP equivalents are
`runner_run` and `runner_result`; existing start/wait operations remain supported.

Read the [operator guide](docs/operator-reference.md) for configuration, retry,
cancellation and storage. Read the [code walkthrough](docs/architecture.md) to
understand the six Go responsibilities and the proof owned by each test suite.
The [design record](docs/process-core-design.md) preserves agreed direction and
separates this implementation from later capabilities.

The [reviewed Go process](examples/reviewed-go/README.md) keeps implementation,
focused verification, read-only review and local candidate integration inside
Runner. [External coordinators](docs/coordinator.md) submit work and report results
or decisions; they do not perform missing stages themselves. This process stops
at a checked, reviewed local branch and does not merge, push or deploy.

## Qualified boundary

- YAML and JSON files with explicit project, process and role references.
  Valid edits reload automatically; invalid edits retain the last valid setup.
  Active runs retain their selected definitions. Storage changes require restart.
- Ordered steps and named outcome routes with typed inputs/outputs, retained
  artifacts, bounded correction/benchmark repetition, execution retries,
  saved decisions, profile escalation, cancellation and restart reconciliation.
- Fixed task templates expanded from bounded dependency-aware collections.
  Items are serial in one owned workspace; independent runs can progress concurrently.
- One instance owner, concurrent independent runs, bounded records/history/logs,
  controlled storage admission, retained-output references and repeatable cleanup.
- Local owner-only MCP over loopback HTTP; the CLI is a client of that service.
  Remote control, authentication for multiple users and service installation
  have not been qualified.

The Codex adapter has qualified the small text-summary fixture on CLI 0.164.0,
using Sol / Extra High: success, lost launch acknowledgement/reconnection and
cancellation followed by a graceful Runner restart. It uses one private native
service per instance and a fresh thread per coding attempt. The
[writable qualification fixture](testdata/seats/README.md) additionally qualifies one
source change, retained patch and fixed Go check. Arbitrary project execution,
skills and tool catalogs remain outside this qualification. A separate
[writable recovery exercise](docs/architecture.md#writable-recovery-exercise-29-october-2026)
qualified abrupt Runner owner-process exit, reattachment to the original native
work, cancellation and preservation of edits on macOS. The
[complete development qualification](docs/architecture.md#complete-development-workflow-3-november-2026)
adds real coding, two benchmark rounds, review, approval/restart and a packaged
website verified headlessly. Task-local parallel execution, nested/dynamically
amended plans, attention/archive operations and dashboards remain outside this
qualified implementation. Benchmarks use project commands and ordinary roles.

## Writable Go example

[examples/workspace](examples/workspace/README.md) shows a coding → check process
accepting a per-run request for your repository. All standalone examples are listed
in [examples/README.md](examples/README.md); test fixtures live in the root
`testdata/` directory.
See the [qualification record](docs/architecture.md#writable-go-qualification-29-october-2026)
for measured time, usage and remaining limits.

## Input-only Codex example

The [Codex configuration](examples/codex/instance.yaml) uses your existing local
Codex account. It selects `gpt-6.1-sol`, `xhigh` reasoning and no task environment,
MCP servers or external tools. Model/reasoning and effective native restrictions
are checked before a turn is submitted. There is no automatic model fallback.

```sh
./cortexium-runner serve --config examples/codex/instance.yaml --data /absolute/new/codex-state
```

Starting the service submits no model task. In another terminal, an explicit
start spends model usage:

```sh
./cortexium-runner start --data /absolute/new/codex-state --project demo --inputs '{"document":"Booking Desk opens Tuesday at noon. Every reservation requires email confirmation."}' --request-id summary-1
```

The first native qualification used its three-task allowance. Ordinary tests skip
it. See the [measured results and limits](docs/architecture.md#codex-adapter-qualification-29-october-2026)
and [operator instructions](docs/operator-reference.md#input-only-codex-execution).

## Verify

```sh
go test -race -timeout 15m ./...
go vet ./...
go build ./cmd/cortexium-runner
```

There are no model calls in these checks. CI runs the same Go gates on Linux and
macOS. Release and installer automation from the previous runtime is retired on
this branch; distribution needs its own qualification and authorization.
