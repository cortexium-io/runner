# Cortexium Runner

Runner is a configurable process engine. Ordinary Go code coordinates work;
roles, skills and tools supply domain judgment. Projects have independent
processes and runs, subject to explicit shared resource limits.

This branch contains the **local process core and complete development workflow**: a runnable local
service with file configuration, SQLite state, MCP/CLI control, a controlled
deterministic executor, input-only Codex execution and a bounded writable Go
workflow with model-free checks, bounded correction/measurement routes, read-only
review, saved approval decisions and retained candidate/delivery artifacts.
Planned task collections expand fixed templates into isolated linked runs, enforce
dependency/write-scope ordering and join accepted source before combined review.
Configured synchronous commands support checks, benchmarks, builds and project
delivery without another model coordinator.
General role tasks add nested planning, configured native harness profiles,
dependency-aware parallel work, selective revision and scoped human intervention.
They share one owned project workspace instead of copying source per task; simple
writing can use one ordinary role without a task graph.
It does not import legacy Runner state. Qualified pilot installation remains an
explicit operator action.

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

The [connected Jack's Break process](examples/jacks-break/README.md) adds product
packaging, runtime/browser qualification and approved release, with bounded
correction/measurement and configured recovery. Merge, production and database
restoration remain separate saved owner decisions.

## Qualified boundary

- YAML and JSON files with explicit project, process and role references.
  Valid edits reload automatically; invalid edits retain the last valid setup.
  Active runs retain their selected definitions. Storage changes require restart.
- Ordered steps and named outcome routes with typed inputs/outputs, retained
  artifacts, bounded correction/benchmark repetition, execution retries,
  saved decisions, profile escalation, cancellation and restart reconciliation.
- Legacy fixed task templates expand dependency-aware collections. Their former
  eight-card and 64-expanded-step ceilings apply only when work limits are enabled.
  Isolated actors retain their separately configured concurrency and write scopes.
- Aggregate attempt and total child-task ceilings are optional instance policy.
  `enforce_work_limits: false` (the default) disables both for all projects and
  retained runs without discarding history or changing frozen definitions.
  `true` enables their configured finite values. Local retries, route repetition,
  concurrency, per-execution timeouts, storage checks and owner decisions remain
  enforced. Disabled limits do not automatically retry failed or completed work.
- One instance owner, concurrent independent runs, bounded records/history/logs,
  controlled storage admission, retained-output references and repeatable cleanup.
- Regular source files with literal source/capture ownership, including the
  supported immutable root text-attribute subset described in the
  [operator guide](docs/operator-reference.md). Arbitrary Git transformations,
  submodules and source symlinks are not accepted by this workspace capability.
- Local owner-only MCP over loopback HTTP; the CLI is a client of that service.
  The Jack's Break pilot has a qualified macOS user-service installation and an
  explicitly configured remote release operator. This does not qualify remote
  multi-user control or general installation on every host.

The Codex adapter has qualified the small text-summary fixture on CLI 0.164.0,
using Sol / Extra High: success, lost launch acknowledgement/reconnection and
cancellation followed by a graceful Runner restart. It uses one private native
service per instance and a fresh thread per coding attempt. The
[writable qualification fixture](testdata/seats/README.md) additionally qualifies one
source change, retained patch and fixed Go check. That legacy restricted-adapter
qualification does not cover native project tools or skills. A separate
[writable recovery exercise](docs/architecture.md#writable-recovery-exercise-29-october-2026)
qualified abrupt Runner owner-process exit, reattachment to the original native
work, cancellation and preservation of edits on macOS. The
[complete development qualification](docs/architecture.md#complete-development-workflow-3-november-2026)
adds real coding, two benchmark rounds, review, approval/restart and a packaged
website verified headlessly. The subsequent
[planned workflow qualification](docs/architecture.md#planned-workflows-67-november-2026)
demonstrates real overlapping Go cards and independently reviewed research.
The general role-task path is separate from those legacy source-card limits.
[Its examples](examples/role-tasks/README.md) exercise native writing, model
deliberation and a Genny SaaS; the current implementation and acceptance criteria
are in the [design record](docs/process-core-design.md#general-role-tasks-authorized-15-november-2026).
The [real qualification record](docs/architecture.md#real-qualification-evidence)
includes successful writing, parallel Genny implementation and a final fixed
22-API/seven-UI check, together with earlier failed runtime and model-led
verification attempts and measured context costs. It does not establish unattended
reliability for arbitrary projects or authorize replacing an installed pilot.
Attention/archive operations and dashboards remain outside this implementation.
Benchmarks use project commands and ordinary roles.

The [release-closure plan](docs/process-core-design.md#release-closure-authorized-8-november-2026)
records the bounded pilot's operational acceptance. On 11 November, Jack's Break
passed real native/browser qualification and switched its frontend default to
`deliver-planned-frontend`. A fresh restricted native session verified exact
request replay and same-run result rejoin without a model turn or new product work.
See the [qualified cutover](docs/architecture.md#qualified-planned-frontend-default-11-november-2026)
for identities, evidence and limits.
Merge, production and database restoration retain separate owner decisions.

## Writable Go example

[examples/workspace](examples/workspace/README.md) shows a coding → check process
accepting a per-run request for your repository. All standalone examples are listed
in [examples/README.md](examples/README.md); test fixtures live in the root
`testdata/` directory.
See the [qualification record](docs/architecture.md#writable-go-qualification-29-october-2026)
for measured time, usage and remaining limits.

## Input-only Codex example

The [Codex configuration](examples/codex/instance.yaml) uses your existing local
Codex account. It selects `gpt-6.2-sol`, `xhigh` reasoning and no task environment,
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
make check PACKAGES=./cmd/runner-frontend-check TESTS='^TestSelections'
```

Select the packages and regressions affected by the change. Routine checks
exclude the `TestWorkflow` integration journeys. After related edits and focused
checks pass, run `make check-workflows` once if workflow behavior or its shared
fixtures changed. `make check-full` is the explicit final whole-repository gate
and already covers those workflows; do not also repeat the separate gate for
unchanged inputs or run the full suite after each intermediate fix. Trusted model-free fixtures share
normal Go compiler caches while still executing their checks with `-count=1`.
Real worker cache isolation and native qualification remain unchanged.

There are no model calls in these checks. CI runs complementary fast and workflow
stages on Linux and macOS, preserving the ordinary full suite. Release and
installer automation from the previous runtime is retired on this branch;
distribution needs its own qualification and authorization.

CI selects changed command packages for helper-only changes and omits workflow
qualification for them. Shared runtime, fixture, process or test-infrastructure
changes select the broader coverage; a manual CI run selects the full suite.
Documentation-only changes do not launch Go tests.
