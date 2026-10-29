# Process core: code walkthrough

This describes the clean first slice, not the previous task/approval runtime.
Start with the example configuration, then `types.go`, then follow a run through
`engine.go`. There are six responsibilities:

| Location | Owns |
| --- | --- |
| `cmd/cortexium-runner/main.go` | Service lifetime, reload/maintenance timers and thin CLI commands. Wires adapters. |
| `internal/runner/` | `types.go` owns contracts; `engine.go` owns operations, admission, coordination and lifecycle decisions. |
| `internal/runner/config/` | YAML/JSON parsing, reference/type validation and validated file edits. |
| `internal/runner/sqlite/` | Single instance lock, bounded snapshots, request receipts and artifact references. |
| `internal/runner/execution/` | Deterministic jobs, Codex and fixed Go checking behind Start/Observe/Stop/Release; owns Git workspaces. |
| `internal/runner/control/` | MCP translation and the CLI's MCP client. |

`disk.go` and platform free-space helpers belong to the core's controlled resource
handling. They do not form a second scheduling or storage service. The core
imports no MCP SDK, configuration parser, SQLite driver or execution adapter.

## Follow the demo

1. `config.Load` resolves instance → project → roles/process. It rejects unknown
   fields, unsupported executors, invalid bindings and mismatched contracts.
2. `Core.Start` validates values and availability, freezes the selected process
   and roles, then commits the run and request receipt before acknowledging it.
3. The coordinator admits a saved attempt under concurrency and storage bounds.
   Executor calls run outside its mutex with bounded contexts. Different eligible
   runs can progress while a call or another run is uncertain.
4. `Start` creates a durable controlled job before its side effects. `Observe`
   supplies facts; it does not decide process policy. The demo's first job writes
   `artifacts/<attempt>/result` and returns that reference.
5. The core validates the declared result and owned artifact, then commits both.
   Only then can it dispatch the dependent step and release the old job/work
   directory. Outputs live outside disposable work directories.
6. The second step consumes the retained file. The core resolves the final output
   binding and marks the run Completed. Cleanup can finish independently without
   turning completed work into a new execution failure.

The wake channel makes state changes prompt. Known completion/deadline timers
avoid waiting for the fallback reconciliation interval (default five minutes).
There is one coordinator, no agent polling loop and no model-authored task ledger.
`runner_wait` blocks in Go on a revision signal; the CLI repeats waits in Go.

## Follow the Codex adapter

`execution/service.go` routes attempts using their selected harness and durable
native job record. `codex.go` owns setup, native thread/turn correlation, bounded
result parsing and collection. `codex_rpc.go` owns bounded JSON-RPC over the private
Unix WebSocket; it never retries a turn-start request. `codex_process.go` owns the
instance's private service, configuration, account link and process lifetime.
The small platform files provide process groups and kernel socket-peer identity.

Each attempt supplies its frozen task, inputs, model/reasoning, instructions,
output contract and deadline. The first profile supports inline text only. Before
generation it verifies read-only native policy, no selected environments or
workspace roots, empty external tool catalogs and no loaded instruction files.
The instance's private Codex home replaces ordinary user configuration; the
harness uses a link to the existing local authentication file.

The adapter saves its thread ID before requesting a turn, then saves the turn ID.
After a lost reply, it reads that thread's legacy history. Reattaching event
notifications may call `thread/resume`; this starts no turn and adds no prompt.
The explicit legacy mode is needed because the installed CLI's default history
read reported an unsupported operation during probing.

Native completion/connection events call `Core.ExecutionChanged(attemptID)`,
bringing observation forward from the five-minute fallback. An event arriving
during another executor call remains pending until that call settles. The adapter
records terminal facts before the core collects them. After the core's SQLite
commit, Release deletes the owned native thread. A qualified native "no rollout
found" deletion response makes interrupted cleanup repeatable.

## Follow the writable Go process

The [workspace example](../examples/workspace/README.md) shows the same core and
control operations with a per-run request. The fixed native qualification input
lives in `testdata/seats/`. Optional source descriptors
are data in the core; Git and Go operations belong to the execution adapter.

1. Core freezes the repository/ref, role profiles and bindings with the run.
   `workspace.go` resolves the ref once, records the base and owned resource,
   protects the base with a run ref, and prepares one detached run worktree.
2. `workspace_native.go` creates named native access profiles. The adapter checks
   their effective filesystem/network policy and runs zero-turn command probes.
   Coding explicitly selects the local environment and that workspace's roots;
   input-only execution continues to select no environment or roots. Shared
   native generation/workspace readiness is reused while those facts are stable.
3. The fresh coding thread receives the original brief and returns bounded text.
   `service.go` collects source after native settlement, including failed or
   cancelled partial work. A temporary Git index/object store captures literal
   regular-file bytes without collection hooks/filters or shared loose objects.
   The retained binary patch excludes scratch; its metadata records base/tree,
   producing attempt and prior candidate. Git tree identity describes saved
   content; it is not a task approval checksum.
4. Core persists source references before downstream work or Release. The fixed
   `go-check` profile consumes the exact patch identity and runs `go test ./...`
   through the native standalone command API. It creates no model turn. Source is
   read-only; caches/output are attempt-owned, with network and downloads disabled.
5. Core's optional `RunResources` lifecycle releases successful/cancelled run
   worktrees after attempts/results are secured. Failed worktrees remain for
   authorized retry until retained-history cleanup. Attempt caches are released
   separately. History expiry releases the base pin before deleting artifacts.

A source collection/SQLite failure holds recovery state rather than releasing
unsaved code. Command termination acknowledgement is not completion: only the
final command response establishes settlement. A lost command connection or changed
native service with unresolved children keeps execution Unknown and retains the
workspace/capacity. Killing the server is not treated as proof that children exited.
The first workspace/check configuration requires concurrency one; arbitrary
background commands, crash survival and broader project layouts are unqualified.

## State and recovery

Run states are Queued, Running, Waiting, Completed, Failed and Cancelled, with a
reason and revision. Step position is zero-based in the API. Attempts separately
record execution state, selected profile, deadline, terminal result, stop/retry
allowances and cleanup. Waiting does not imply that execution has stopped.

A saved dispatch may have reached its executor even if the response was lost.
An uncertain acknowledgement becomes Unknown; it holds capacity and is observed,
not replaced. Restart reconciles all unsettled saved attempts first. Only the
deterministic adapter can interpret an absent job as definitely NotStarted,
because that job is its entire execution. Future adapters must establish their
own positive evidence; an absent CLI session listing is not enough.

Cancellation requests Stop but settles only after confirmed terminal/absent
execution. Stop errors have a bounded allowance; observations continue after it
is exhausted. Retry requires settled failed/waiting work, keeps attempt history,
and needs explicit extra allowance when consumed. Escalation changes only a
permitted profile and consumes the same process attempt allowance.

Configuration edits affect future runs. Valid global concurrency changes affect
admission immediately; a lower limit does not kill existing attempts. Storage
policy is fixed for a running service. Invalid reloads leave all prior settings
active and appear in status. There is no task-content checksum approval gate.

## Bounded storage

SQLite holds one capped snapshot per run plus small request/artifact records;
there are no transcripts or append-only observation payloads. Replaying a request
returns the existing run. Retention deletes its request receipts, so replay is
only guaranteed while that run is retained. At most 17 receipts are retained per
run. The effective run count may be below the configured maximum to reserve
update space. Database main pages use at most one third of the DB allowance;
DELETE journaling leaves the rest for rollback and metadata overhead.

The controlled file writer checks per-file limits, managed content bytes and
minimum free space before writes, including temporary replacement content. New
attempt admission reserves room for controlled job/result/log writes. Logs have a
bounded retained tail and a dropped-byte count. Release removes settled jobs and
work; retention removes expired logs and terminal runs, including their remaining
logs/artifacts. A retained consumer pins its producer's artifacts.

These are file-content and database bounds, not a filesystem quota. Directory
allocation, filesystem metadata, unrelated files and arbitrary external processes
are not accounted as exact physical bytes. The deterministic adapter creates a
bounded number of owned files; minimum free space preserves headroom. A real
executor must qualify its own external output/resource controls before use.
There is no claim of power-loss durability across every filesystem: atomic rename
and SQLite FULL synchronization cover the tested process-restart boundary.

## Test ownership

| Suite | Purpose |
| --- | --- |
| `internal/runner/engine_test.go` | Real SQLite/file integration: outcomes, shared allowances, escalation, idempotency, uncertain acknowledgements, independent progress, restart, stop/cancel, unusable metadata, frozen settings, record/log/history bounds and consumer-safe retention. |
| `internal/runner/config/config_test.go` | Mixed formats, reference/schema errors, change/repair detection, comment-preserving YAML edits and validated atomic changes. |
| `internal/runner/control/mcp_test.go` | Real SDK/HTTP round trip, wire replay/inspect/wait/config forwarding, token and Host/Origin boundaries. |

Adapters are exercised by the owning integration suites rather than repeating
all scenarios in separate adapter suites. Tests use revision notifications and
controlled outcomes instead of arbitrary sleeps. `execution/codex_test.go` owns
native protocol translation, lost acknowledgement, settings checks, setup
cancellation, saved-result recovery and repeatable cleanup. `codex_rpc_test.go`
owns the actual Unix/WebSocket framing and unsolicited-request refusal. The
opt-in native suite is separate; normal tests submit no model tasks. Broader
arbitrary workspace/tool execution, remote control and installation remain unverified.

Test fixtures live under the repository-root `testdata/`: configuration mutation
fixtures under `testdata/config/`, and the disposable Go qualification project
under `testdata/seats/`. Published examples are independent configurations;
one table-driven configuration test checks that they load without executing work.

The workspace collector suite owns base pinning, user checkout preservation,
new/deleted/binary/mode changes, bounded capture, artifact reuse and restoration.
Writable protocol tests add lost-reply/unknown/cancel/restart source assertions.
`command_test.go` owns terminate-versus-settlement and disconnected command
behavior. The core suite owns a source result's failed SQLite commit; the config
suite owns writable/check profile restrictions. These do not spend model turns.

## First-slice verification, 28 October 2026

On macOS arm64 with Go 1.27.1, `go test -race -timeout 15m ./...` and
`go vet ./...` passed. The built binary completed the two-step MCP/CLI journey,
returned the expected text, released both workspaces, replayed the same start,
preserved an attempt across restart and confirmed active cancellation. A deadline
expired across the restart exercise and settled as Failed without replacement.
Valid control edits applied; invalid control edits were rejected before writing.
The file watcher exposed an invalid manual edit and cleared its error after repair.
Linux amd64 compiled with CGO disabled; native Linux execution and hosted CI have
not been run. Owned smoke services and temporary proof data were removed afterward.

## Codex adapter qualification, 29 October 2026

CLI 0.164.0 on macOS arm64, Go 1.27.1; all tasks selected `gpt-6.1-sol` / `xhigh`.
Exactly three native turns were submitted. Initial connection/readiness failures
submitted zero turns; the fix allowed Codex's private socket alias without
following it during storage accounting.

| Native case | Result | Core wall time | Reported input/output tokens |
| --- | --- | --- | --- |
| Summary and deterministic result consumer | Completed; owned thread released | 6.49s | 4623 / 117 |
| Lost launch reply, closed connection and reattachment | Completed with one submitted turn | 11.97s | 4623 / 199 |
| Cancel, close Runner and reopen saved state | Cancelled; one retained attempt, cleanup completed | 0.72s | Unknown |

The two completed calls reported 9,562 tokens combined, including native harness
input. Both reported zero cached input tokens. Usage for the interrupted call is
unknown; 9,562 is a known subtotal, not total account cost. The fixture suite took
19.23s. This is lifecycle qualification, not a comparison against baseline Codex
or proof of better model quality/cache efficiency. The tiny task still consumed
4,623 native input tokens per completed call, which leaves context cost as a
concrete future measurement target.

Observed native content was roughly 3.2–3.6MiB, excluding core SQLite and filesystem
allocation overhead. Native writes are rescanned every 250ms; `storage_limit` is
a monitored stop threshold that can overshoot, not an OS quota. Admission reserves
native growth/recovery room. Session data is deleted after result commit; reusable
metadata/cache remains bounded and is cleared on clean service exit when no native
job remains. Unknown/uncommitted resources stay retained. Links are counted without
following them and are never accepted as result artifacts. The private socket alias
and native account/runtime links are supported; other managed buckets reject links.

The qualification exercised graceful Runner restart and a real transport break.
Abrupt host loss, forced native-process death, upgrades, writable workspaces,
arbitrary skills/tools and native Linux execution remain unqualified. The three-task
allowance is consumed; further generation requires renewed authorization. Offline
race/vet checks and Linux cross-compilation are recorded with the final handoff.

## Writable Go qualification, 29 October 2026

CLI 0.164.0 on macOS arm64, Go 1.27.1, `gpt-6.1-sol` / `xhigh`. One new
coding turn was submitted. It added an idiomatic `ParseSeats` implementation
using `strconv.Atoi` and a positive-count guard. The supplied six-case Go test
passed through the configured model-free check. After original worktree removal,
the retained patch restored the same Git tree; disposable repositories, caches,
state and native service were removed. Only small source/result/receipt evidence
remains in the private qualification directory.

| Measurement | Observed |
| --- | --- |
| Successful qualification retry | 33.04 seconds |
| Coding attempt, including its readiness | 26.135 seconds |
| Whole qualification window, including setup debugging | 784.274 seconds |
| Native reported input/output | 31,094 / 602 tokens |
| Cached input, included in input above | 23,680 tokens |
| Observed managed content peak | 149,720,417 bytes; SQLite capacity reserved separately at 4 MiB |
| Observed native content peak | 4,647,014 bytes; included in managed content |
| Retained patch | 531 bytes |

31,696 is the known native token subtotal, not the complete implementation or
coordination cost. Coordination tokens and a reliable worker command history were
unavailable. The configured check executed once; the command-history gap prevents
independent confirmation that the worker never ran an additional check. This is
lifecycle qualification, not a quality/efficiency comparison or proof of savings.

Zero-turn probes established edit/check source and scratch access, protected module
and test files, denied private/Git reads, symlink escape denial and network denial.
An empty input-only thread confirmed its restrictions in the workspace-enabled
instance. Initial writable setup incorrectly retained input-only's empty environment
list, which disabled local execution. The setup guard rejected it before any turn.
The adapter now explicitly selects/verifies the local environment. An empty-thread
qualification-audit cleanup error was also fixed without model calls. The window
above includes that failed readiness attempt and debugging; the one-turn receipt
was retained across retries, and the fifteen-minute deadline was not reset.

The renewed one-turn allowance is consumed. Normal tests skip both opt-in native
suites. Race tests and vet passed; native cancellation/abrupt child-process loss,
Linux runtime enforcement, arbitrary projects/tools and production installation
remain outside this live qualification. Uncertain settlement fails closed.
