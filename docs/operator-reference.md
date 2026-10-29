# Process-core operator guide

This branch is a clean first slice. Use a new private state directory. Do not point
it at an installed Runner database, migrate old approvals or replace a service.
The executable supports `serve`, `start`, `status`, `inspect`, `wait`, `cancel`,
`retry` and `config-set`. Each command accepts `--help`.

## Service and control

```sh
go build -o ./cortexium-runner ./cmd/cortexium-runner
./cortexium-runner serve --config examples/demo/instance.yaml --data /absolute/new/state
```

The default endpoint is `http://127.0.0.1:8787/mcp`. Change the server address with
`--listen 127.0.0.1:PORT` and give clients the matching `--endpoint .../mcp`.
Only numeric loopback listeners are accepted. There is one service per state
directory, enforced with an OS lock. Startup refuses non-private directories,
foreign database formats and unexpected state-root contents.

`owner.token` is private local authentication; clients read it through `--data`.
An MCP client can connect using Streamable HTTP and `Authorization: Bearer TOKEN`.
Do not expose the token or endpoint through a public proxy. TLS/OAuth, SSH/stdio
forwarding and multi-machine discovery are not part of this qualification.

SIGINT/SIGTERM stops the service. It does not cancel runs. Restart with the same
configuration/state to reconcile saved deterministic jobs and collect results.
Do not launch a second service to recover a live instance.

## Configuration files

[The demo](../examples/demo/instance.yaml) selects a project, which selects role
and process files. References resolve relative to the containing file. Either
YAML or JSON is accepted with the same strict fields. The demo deliberately uses
both. Unrecognized fields fail validation; later design examples are not yet the
runtime schema. Each file is limited to 16KiB, one document, with at most 64 files
and 16 projects per instance.

An instance sets `projects`, `concurrency`, `reconcile_interval` and `storage`.
A project sets `id`, `name`, `workspace`, `processes`, `default_process` and role
bindings (`file`, optional `profile`). Workspace is descriptive in this slice;
no code is executed there. A role sets `id`, optional `purpose`, named `inputs`/
`outputs` and either `execution` or named `profiles` with a `default_profile`.

Only `harness: deterministic` is supported. Its modes are `echo`, `file`, `fail`,
`needs_help` and `log`; setup fields are `input`, `output`, optional `text`,
`delay` and `log_bytes`. Echo/log return one text output; file returns one artifact.
A selected input must be declared text or artifact. These are controlled test
operations, not commands, tools, skills or real model profiles.

A process declares inputs, an ordered `steps` list, named final output bindings,
`max_attempts`, `max_attempts_per_step` and `attempt_timeout`. A step binds role
inputs to `{from_input: name}` or `{from_step: earlier, output: name}`. Types are
`text`, `json` or `artifact`; the current deterministic transformations consume
text/artifact only. Up to eight steps, eight named values per contract and sixteen
total attempts are supported. Automatic per-step attempts are bounded at four.

A step may declare escalation, using profiles already defined on its role:

```yaml
escalation:
  on: needs_help
  profile: stronger
  max_times: 1
  exhausted: wait
```

This changes the execution profile while preserving contracts/history. It does
not reset the run budget or authorize another project, tool or capability.

Human edits are detected by a 250ms file-stat check plus a five-minute fallback.
A valid complete configuration reloads automatically. Invalid edits leave the
last valid setup active and appear as `config_error` in status. Fix the file to
clear the error. Active runs keep their selected definitions; global concurrency
controls new admission. Storage changes require a restart and sufficient existing
capacity. MCP rejects a live storage edit before changing the file.

```sh
./cortexium-runner config-set --data /absolute/new/state --file instance.yaml --field concurrency --value 3
```

Control editing is restricted to selected files. The field path uses dots and
zero-based sequence indexes. YAML comments are preserved where possible; JSON
stays JSON. The full configuration is validated before atomic replacement. There
is no separate apply action, task checksum or automatic modification of active runs.

## Runs, retries and cancellation

```sh
./cortexium-runner start --data /absolute/new/state --project demo --inputs '{"name":"Hello"}' --request-id request-1
./cortexium-runner inspect --data /absolute/new/state --run RUN_ID
./cortexium-runner wait --data /absolute/new/state --run RUN_ID
./cortexium-runner cancel --data /absolute/new/state --run RUN_ID
./cortexium-runner retry --data /absolute/new/state --run RUN_ID --request-id retry-1 --extra-attempts 1
```

Start/retry print their request ID before the operation. Reuse the same ID and
arguments after an uncertain response; a different operation under that ID is
rejected. Receipts expire with retained runs. `--artifacts '{"name":"ID"}'` supplies
retained artifact inputs separately from inline `--inputs`. Artifacts must belong
to the same project. Retry optionally selects an existing `--profile`; explicit
extra allowance is at most four, with the total still bounded at sixteen.

Status returns at most the 100 most recently updated summaries plus `total_runs`,
configuration/storage errors and content-byte accounting. Inspect returns one
bounded record with resolved setup, attempts, outputs, usage and cleanup state.
Unknown usage is omitted, not recorded as zero. No model tokens are consumed by
the deterministic executor. Wait uses Go revision signals and returns at terminal
or Waiting status; Waiting requires inspection of its reason and active attempt.

A cancellation request is not confirmation. Uncertain execution is observed before
replacement and continues to hold capacity. If stop retries are exhausted, inspect
execution and repeat Cancel to explicitly authorize another bounded stop allowance.
Never retry an active/unknown execution merely to clear its status.

## Storage and retention

The demo uses small allowances. Defaults are 10GiB managed content, 5GiB minimum
free space, 256MiB database reservation, 16KiB records, up to 1000 retained runs,
16MiB artifacts and 2MiB logs per attempt. Log retention is 48 hours; terminal run
retention is seven days. Byte values accept integers or B/KiB/MiB/GiB strings.
The database reserves recovery headroom and can admit fewer runs than max_runs.

Oversized inline data must be reduced or passed as a declared retained artifact.
A run definition initially uses at most half its record allowance so attempts can
finish. History capacity or disk pressure can leave work Waiting/Queued with a
reason. Release failures appear separately from successful process completion.

Maintenance runs each minute. Only settled, released, expired terminal runs are
removed; retained consumers pin producer artifacts. Deleting a run also deletes
its receipts and remaining owned logs. Active/uncertain resources are retained.
File-content accounting is not an OS disk quota; see the precise
[storage boundary](architecture.md#bounded-storage). Do not manually edit the
SQLite database or delete live job/artifact files to make space.

## Input-only Codex execution

Use [examples/codex](../examples/codex/instance.yaml) with a new state directory and
an existing Codex CLI account. The instance's `codex` block selects `binary`,
optional `auth_file` (default `~/.codex/auth.json`) and `storage_limit`. The native
home is private to this instance and links to the existing account file. No login,
installation or normal user configuration is changed by Runner. Native managed
authentication may refresh its credentials as ordinary Codex does.

The role needs an explicit `purpose` and an execution profile containing `harness:
codex`, `model`, `reasoning`, `access: input-only`, `max_input`, `max_output` and
optional `instructions`. Supported reasoning selections are low/medium/high/xhigh;
the native model catalog must advertise the exact selected pair. Both byte limits
must be explicit, from 128B to 8KiB. They bound Runner-assembled input and the final
native result, respectively. Native harness context is additional: these fields
are not total model-context/token caps. There is no hard token/cost cap in this
adapter; supported observations are reported, with missing usage kept unknown.

Only declared inline text inputs/outputs are supported. No environment or workspace
roots are selected. MCP/apps/plugins/hooks, shell/subagent tools, web search and
automatic skill/instruction discovery are disabled. Required effective settings
are checked before requesting a turn. Wider workspace access and role skills/tools
need their own adapter/profile qualification; they are rejected by this schema.

`inspect` includes the native thread/turn handle, confirmed effective setup and
reported usage. `status` reports shared native-store bytes. The recorded native
peak is sampled, instance-scoped data, not disk usage attributable to one attempt;
do not sum it across attempts. The native socket path must fit within 100 bytes;
choose a short private state-directory path.

Instance Codex settings require restart; role/profile edits affect future runs.
On normal service shutdown, Runner requests interruption, retains native facts
and stops its private app-server within a five-second grace. Startup collects
saved results and reconciles unfinished work. This does not promise that active
work survives a service/host crash. An unknown execution stays Waiting until
resolved. Use explicit Cancel/Retry under the normal retained allowance rules.

Native storage is measured every 250ms. `storage_limit` is a stop threshold, not
an OS quota, and can overshoot between observations. Session cleanup follows
SQLite result commit. Reusable native cache is removed on clean service exit when
all native jobs have been released. Uncertain/uncommitted state is preserved.

The initial native qualification used its three-task allowance. The ordinary Go
suite skips the opt-in test. Do not enable `RUNNER_NATIVE_QUALIFY=1` without explicit
authorization for a new bounded set of model executions.
