# Process core: code walkthrough

The [approved release example](../examples/approved-release/README.md) documents
the 3 November credential-bearing, model-free release adapter and its two explicit
owner decisions. `execution/release.go` retains local attempt identity;
`internal/release` journals and reconciles one operator action at a time over the
existing SSH connection. It does not choose stages or approve work. GitHub and
Compose remain the owning services for merge and deployment state.

This describes the clean first slice, not the previous task/approval runtime.
Start with the example configuration, then `types.go`, then follow a run through
`engine.go`. There are six responsibilities:

| Location | Owns |
| --- | --- |
| `cmd/cortexium-runner/main.go` | Service lifetime, reload/maintenance timers and thin CLI commands. Wires adapters. |
| `internal/runner/` | `types.go` owns contracts; `engine.go` owns operations, admission, coordination and lifecycle decisions. |
| `internal/runner/config/` | YAML/JSON parsing, reference/type validation and validated file edits. |
| `internal/runner/sqlite/` | Single instance lock, bounded snapshots, request receipts and artifact references. |
| `internal/runner/execution/` | Deterministic jobs, Codex, fixed Go checking and configured commands behind Start/Observe/Stop/Release; owns Git workspaces. |
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
`runner_run` combines the existing start operation with a Go revision wait through
completion/Waiting and normal settled cleanup. `runner_result` presents bounded
outputs and usage without loading artifact evidence or returning inputs/setup.
These live in `control/result.go`; they add no core state or coordination policy.
Client cancellation ends only the wait. Full records remain available via inspect.

The 31 October 2026 control qualification used the real compiled CLI and local MCP
service with deterministic steps: one submission completed both steps and cleanup
in 1.049s, returning 606 bytes. Reusing its request ID kept the same run and two
attempts. Further runs confirmed Failed and Waiting results, client interruption
without run cancellation, and explicit cancellation followed by settled cleanup.
These four runs used zero model turns. The control suite owns compact output bounds,
partial token accounting and the actual wire/rejoin contract. Core recovery proofs
were reused; no native coding/recovery exercise was repeated. Required race/vet
gates passed. This qualifies control behavior, not end-to-end model cost savings.

The subsequent guide replay exposed a command-startup race: the owning process
could observe its launched child before `started.json` existed. The command
adapter now keeps that interval Running using its existing child-wait callback;
after owner restart or child exit, absent settlement evidence remains Unknown.
A deterministic regression reproduces the old false uncertainty and proves both
the startup correction and the exit-without-receipt boundary. No grace sleep or
control-layer suppression of Waiting was added.

The intake permission preflight used the pinned native Codex service's
`config/read`, `thread/start` and `mcpServer/tool/call`, with no `turn/start`.
Only `runner_run` was enabled and preapproved, with the owner token passed through
a private environment variable. The actual MCP invocation completed the two-step
deterministic demo and cleanup. This verifies loaded configuration and native
manual tool invocation; it does not claim a new generated intake or coding replay.

## Follow the Codex adapter

`execution/service.go` routes attempts using their selected harness and durable
native job record. `codex.go` owns setup, native thread/turn correlation, bounded
result parsing and collection. `codex_rpc.go` owns bounded JSON-RPC over the private
Unix WebSocket; it never retries a turn-start request. `codex_process.go` owns the
instance's private service, configuration, account link and process lifetime.
The small platform files provide process groups and kernel socket-peer identity.

Each attempt supplies its frozen task, inputs, model/reasoning, instructions,
output contract and deadline. Input-only profiles accept inline text/JSON and
return text. Before generation that profile verifies read-only native policy, no selected environments or
workspace roots, empty external tool catalogs and no loaded instruction files.
The instance's private Codex home replaces ordinary user configuration; the
harness uses a link to the existing local authentication file.

The adapter saves its thread ID before requesting a turn, then saves the turn ID.
An acknowledged active turn on the same subscribed connection uses completion
events; observation does not require its rollout file to be flushed. Lost
ownership, uncertain dispatch and requested stops still reconcile through native
history before reporting settlement or permitting replacement.
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

The 1 November 2026 reviewed Go process moves focused checking, independent
read-only review and local candidate integration behind the same run. The Codex
adapter reuses `runner-check` for review, materializes the retained patch in
scratch and keeps stable configured context before access-specific guidance.
The model-free `git-candidate` adapter secures its intent before reconstructing
the patch with a private index and creating an owned local commit/ref. It never
changes the user checkout, merges, pushes or deploys. Its same-intent replay,
blocked review, branch conflict, cancellation and retention boundaries are owned
by `candidate_test.go`; the existing collector owns patch fidelity.

The four-stage process and rejection path passed the scripted native/core
integration tests. Real pinned Codex permission probes established read-only
review setup and source/scratch/private/Git/network restrictions without a model
turn. A separate native intake configuration exposed only run/result/status and
successfully invoked status. Required race/vet gates passed. The idle Jack's
Break pilot was switched to the maintained build and four-stage configuration,
preserving all 29 historical runs and the existing project checkpoint. This
qualifies wiring and permissions, not model review quality, savings or a new
end-to-end product result. Workspace concurrency remains one; unsupported
frontend/browser/release work must surface a capability gap. See the
[coordinator contract](coordinator.md) and [example](../examples/reviewed-go/README.md).

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

SQLite holds separately capped settings and operational history documents for
each run, atomically in one row, plus small request/artifact records. Format 2
adds the settings column while retaining existing format-1 history unchanged;
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

## Writable recovery exercise, 29 October 2026

On macOS arm64, CLI 0.164.0 and Go 1.27.1, the corrected recovery exercise passed
with one `gpt-6.1-sol` / `xhigh` turn. After the worker wrote `seats.go` and reached
a live blocked command, the test sent SIGKILL to the subprocess hosting Runner's
real core, adapter and SQLite store. No graceful shutdown ran in that subprocess.
Reopening the same state reattached to the original native service and thread.

- Run, attempt, thread, turn, source base and workspace identities were preserved.
- Reattachment took 25 ms; the original command was still live afterward.
- Retry was rejected while that execution remained active. No replacement turn
  or check-step attempt was submitted.
- Normal Cancel settled the native turn and command. The run became Cancelled,
  with its attempt and workspace resources released and no cleanup error.
- The 530-byte patch restored exactly the source bytes present before the crash.
  The fixture, private native service, workspace and caches were cleaned up. Only
  result/start/no-repeat receipts remain (545 bytes of file content).

The corrected exercise took 23.02 seconds and reported native input 25,374 and
output 714 tokens (26,088 known native tokens). Cached input 18,560 is included in
input. This measures the bounded fixture, not arbitrary-project recovery or
efficiency against baseline Codex. Native service/host loss and native Linux
execution remain unqualified; their uncertain settlement must not be bypassed.

Checkpoint `66a09bd` preserves the writable implementation and `0e01ea3` the
corrected test. The runtime needed no recovery change. Race tests and vet passed
for that code and were reused; only documentation changed after the live pass.

The first exercise failed before fault injection because code-mode nested shell
output did not emit the commandExecution output notification expected by its
synchronizer. Its barrier expired and the ordinary coding/check process completed.
That failed exercise took 300.26 seconds and reported native input 56,001 and
output 1,042 tokens (57,043 known native tokens), including 42,368 cached input.
The correction observes the explicit readiness file and live process instead of
tool-output notification shape. Failed evidence and no-repeat receipts were
preserved; its disposable resources were cleaned up. The owner approved exactly
one additional turn for the corrected exercise. Both allowances are now consumed.

The two exercises reported 83,131 native tokens in total. Coordination usage is
unknown, so this is a known native subtotal rather than complete resource cost.

## First real-project pilot, 29 October 2026

Dan authorized taking over techba.se and Jack's Break, coordinating their chats,
and starting fresh work through this Runner. No old Runner tasks, approval gates
or execution state were imported. Techba.se's five-article publication was already
complete; no new publication work was invented. Jack's old worker was confirmed
stopped before the fresh run, with its completed source committed at
`cdff68477086d1d2b70f4dd989868e0b15adcf58`.

The fresh task corrected one observed Futoshiki typography inconsistency: Erase
inherited a 25px numeric-key style, while Notes used the shared 16px word-label
style. One source-edit step used Sol/xhigh, a five-minute attempt deadline, no
automatic retry and a retained patch. Focused frontend verification belonged to
the coordinator; the built-in Go check was not presented as frontend support.

The first admission on Mega Mini was correctly rejected below its 5GiB free-space
reserve (about 4.4GiB available). No run or model turn was admitted there. Temporary
tools were removed. The actual run used Black Magic, with about 168GiB available,
2GiB managed-content allowance and the same 5GiB reserve. The source repository
contains about 1.1GiB of tracked documentation/evidence, which makes whole-tree
checkout a concrete resource cost even for this small edit.

Run `run_ANLYOQ2SUF5T3DKSC5CVFBCBO5` used one attempt. The model made the edit but
did not return a structured completion before its deadline. The result is
**Failed**, not a successful automated workflow. Runner stopped the turn and
preserved its workspace. The collector then incorrectly rejected unchanged large
baseline assets under the patch limit. After the collector fix and restart, the
same settled attempt produced a 7,255-byte patch; no replacement turn ran.

The recovered patch changes one Vue file by two insertions/two deletions. Its
gameplay script is byte-identical. Coordinator checks on Node24.21.0 passed all
17 existing Futoshiki component tests (2.09s). An isolated Vite fixture mounted
the actual modified component against a copied public Go fixture. At 1280×1000
and 390×844, word labels measured 16px, digits 25px, targets at least 48px, with
no horizontal overflow. Browser input exercised entry, Erase and Notes on/off.
This does not qualify a new embedded app or the full seven-player handoff. The
existing port4320, operator storage and production were unchanged.

Reported native usage was 66,127 input and 3,329 output tokens: **69,456 known
native tokens**, including 35,968 cached input. Coordination usage is unavailable.
The worker attempted unavailable Node and Perl commands before using the edit
tool. The cause of the missing timely completion is unestablished. No efficiency
improvement is demonstrated; this cost is excessive for the change.

Two actual integration defects were fixed in the owning suites:

- MCP tool errors are read from wire content. The SDK's `GetError` is server-only
  and always nil on clients; using it hid the admission error behind an empty-JSON
  error. The round-trip test now proves rejection preserves its domain reason.
- Collection discovers changed/new paths using the workspace index and retains
  their literal bytes in the separate collection index. Unchanged baseline assets
  are preserved without rehashing or applying the patch-size cap to them. Existing
  coverage now includes an unchanged asset larger than the patch allowance;
  modified-file limits, deletion, staging, modes and restoration remain covered.

Race tests and vet passed after these fixes. The source prompt is language-neutral;
the profile retains its existing Go/system-command permissions. Node tooling in
the worker, general frontend execution and automatic frontend verification have
not been added or qualified.

## Bounded completion follow-up, 30 October 2026

The first pilot's native transcript had already expired. Saved coordinator
observations show failed Node and Perl edit attempts, followed by a successful
patch and readback. Usage was unchanged between the 14:49:19 UTC observation and
deadline interruption at 14:52:27. These facts identify avoidable tool selection
and a missing final result; they do not establish why final generation stalled.

The workspace instructions now direct edits through `apply_patch`, describe the
current Go/system-command environment, discourage alternate interpreters and
require the declared result immediately after the requested edit and readback.
The pilot role also explicitly names available tools and the completion boundary.
No execution policy, timeout, permission or model setting was relaxed.

One fresh run used the same original source base, task, Sol/xhigh setting and
five-minute attempt timeout. It completed normally in 42.318 seconds, returned
its structured result and retained a 7,249-byte patch. Native usage was 32,520
input and 2,211 output tokens: **34,731 known native tokens**, including 13,312
cached input. Coordination tokens remain unavailable. The native diagnostic
summary records two tool calls and a final answer; no transcript was retained.
This single successful follow-up demonstrates completion with the corrected
guidance, not repeatable savings or isolation of the earlier stall's cause.

The patch changes only the same Vue file by two insertions/two deletions. Its
script is unchanged, and its only difference from the previously checked
candidate is `.value-controls .number-key` instead of
`.value-controls button.number-key`. All 17 existing Futoshiki component tests
passed on the new file in 2.28 seconds. The isolated component fixture again
measured 16px word labels, 25px digits, targets at least 48px and no horizontal
overflow at 1280×1000 and 390×844. These are coordinator checks, not an automatic
frontend check step or a full application qualification. Runner's race tests and
vet passed after the instruction change. No product commit, merge or deployment
was made by this follow-up, and no additional coding attempt is authorized.

## Configured commands and local review, 30 October 2026

`configured_command.go` owns setup, observation, collection and release.
`command_worker.go` is a private mode of the same executable. It claims dispatch
once, runs configured argv through `codex sandbox`, bounds its log and writes a
durable result outside the child's sandbox. This removes the new harness's
dependence on an app-server connection for command settlement. Core scheduling,
retry policy and workspace/artifact lifecycle remain unchanged. The old fixed
`go-check` adapter retains its previous native API and limitations.

The adapter suite owns worker-owner separation, duplicate-dispatch rejection,
completion/cancellation, artifact collection and uncertain-worker retention.
The disk suite owns disposable dependency-link accounting and controlled IO
escape rejection. The existing core/config suites cover per-step deadlines and
command contracts; there is no duplicated transport failure matrix.

A real zero-model exercise used Node under the actual sandbox, confirming source
read, denied source writes, denied Git reads and scratch artifact handoff.
After a second command reached an explicit FIFO barrier, SIGKILL stopped the
Runner owner. Restart recovered the same worker/attempt; normal Cancel settled
the command and released its resources. No replacement execution was submitted.

Jack's Break then used the adapter for its existing Futoshiki tests, embedded app
build and exact executable's Nonogram qualification:

| Evidence | Result |
| --- | --- |
| Focused tests | Existing 17 tests passed; reused after a scratch-copy helper fix because frontend inputs were unchanged. |
| Build | Typecheck, client/SSR/prerender and pinned Go embed passed, 20.502s. |
| Qualification | 120 development + 480 held-out starts, exact replay; same four enabled choices, 40.196s. |
| Final build → qualification run | Completed in 61.472s, two attempts, resources released. |
| Model usage for these command steps | Zero. Coordination usage is unavailable; this is not a total-cost benchmark. |

The compiled app passed layout checks for all seven players at desktop/phone
widths. Coordinator browser review and updating the existing local preview were
outside Runner; this is not an automatic end-to-end release workflow. The
preview kept its SQLite file and browser origin. No production merge/deployment
or installed-service cutover occurred.

The first native-command prototypes hit ordinary dependency symlinks during
storage accounting. Their connection-scoped exits were lost, so those runs remain
uncertain; they are not rewritten as successes. A subsequent durable-command build
failed normally because its scratch copy omitted TypeScript JSON fixtures. That
helper was corrected without rerunning the passing frontend tests. Managed work
now counts symlink bytes without following them; retained artifacts and controlled
IO still reject link traversal.

Live qualification is macOS arm64 with Codex 0.164.0. Only trusted synchronous
commands that retain their process group are supported. Host/worker loss, escaped
background children, Linux sandbox enforcement and arbitrary hostile programs
remain outside this proof. Missing settlement evidence retains resources. A
monitored storage limit may overshoot and is not a filesystem quota.

Final `go test -race -timeout 15m ./...` and `go vet ./...` passed. Linux amd64
compiled with CGO disabled; this is compile evidence, not Linux sandbox proof.

## Complete development workflow, 3 November 2026

The existing core now routes declared domain outcomes, retaining per-route
repetition counts independently of retries within a step visit. Returning to an
owner freezes its feedback, supersedes downstream proof and creates another
visit. A default ready result continues forward. Unknown worker outcomes cannot
invent stages or waive proof. `workflow.go` owns transitions and exact saved
decisions; `tasks.go` expands fixed templates from validated bounded task data.
The same SQLite run retains the resolved steps, attempts, artifacts and usage.
No scheduler, task ledger or model polling layer was added.

Task collections execute in dependency order in the shared owned workspace.
Template roles are selected before execution; task data supplies only inputs and
dependencies. Invalid plans fail before item execution. Joins forward committed
artifact references without changing their producer. Current-candidate bindings
make cumulative source available to final verification/review/integration and a
later correction owner. Offline text-only checks can opt into reuse within a run;
source tree, selected inputs/profile/runtime must match. Reuse is recorded as an
attempt linked to its earlier proof and executes no command or model.

The model-free core suite owns correction, repetition exhaustion, stale proof,
feedback handoff, invalid dependencies and decision persistence/replay. The native
adapter's existing candidate suite additionally verifies two dependent coding
items produce one combined reviewed tree while preserving the user's checkout.
Configuration tests reject unbounded return routes and invalid template inputs;
command tests distinguish expected domain rejection from execution failure.

The isolated development fixture loads ordinary file configuration and uses the
real MCP interface. Its paid qualification used macOS arm64, Go 1.27.1 and pinned
Codex 0.164.0, with exactly three Sol/xhigh turns:

| Stage | Evidence |
| --- | --- |
| First owner | Implemented the Go website greeting and HTML escaping; 25.677s. |
| Fixed verification | Required greeting/HTTP tests passed; 4.441s. |
| Benchmark | Actual Go benchmark retained measurements and requested another round; 4.338s. |
| Second owner | Interpreted the measurements; retained the unchanged correct implementation; 18.973s. |
| Verification reuse | Same captured source tree and check inputs; zero command execution. |
| Second benchmark | Actual new measurement, within the configured return limit; 4.189s. |
| Independent read-only review | Approved the original request, candidate and supplied evidence; 17.533s. |
| Candidate integration | Created one local reviewed commit; 0.105s. |
| Approval/restart | Saved fixture approval survived closing/reopening Runner; explicit decision resumed the same run. |
| Package and delivered-result check | Built a retained executable, then launched that exact artifact and passed headless desktop/mobile greeting and HTML-injection journeys; 5.111s combined. |

One `runner_run` submission executed through the approval wait; one explicit
fixture decision followed by `runner_result(wait=true)` completed the rest. The
maintenance chat did not select, launch or interpret individual process stages.
The final result reported 10 attempts, three model turns, 80.400s and 65,383 known
worker tokens (31,232 cached input), with no incomplete usage and no pending
cleanup. These are process measurements; this maintenance chat is additional,
and no comparative quality, currency cost or optimization win is claimed.

Run: `run_QQOLP5GULXAODQJO6XPIFWBQXI`. Its selected configuration, full attempts,
compact result and artifacts are retained privately under
`~/.codex/rw-live-20261103a`. Three earlier model-free preflights exposed Node
library/OpenSSL and macOS Chromium launch prerequisites; a fourth passed with the
existing qualified standalone Node and headless browser setup. A final probe
also confirmed check reuse. All preflights used scripted judgments and zero model
turns. Their failures were preserved, not reported as model failures or successes.

This qualifies the complete bounded Go website fixture and the generic routing
mechanism. The approval is for a disposable fixture, not production publication.
Live forced review rejection and model quality across projects are not established;
rejection/correction/limits are covered deterministically. Native Linux sandbox,
remote/multi-user operation, intra-run parallel workspaces, arbitrary external
processes and deployment to a production provider remain unqualified. Project
verification, benchmark and release tools still require their real configuration.

Final verification also passed the required full race suite and `go vet`, the
compiled CLI's dependency-ordered three-attempt journey (45.668ms, zero model
usage, settled cleanup), and the maintained portable intake profile's real native
tool inventory/status call (zero model turns). Intake exposed exactly
run/result/status and rejected an unavailable configuration tool. Model-free
comparison-input tests cover separate retained baseline/proposed artifacts;
approval rejection tests confirm that denied delivery never executes.
