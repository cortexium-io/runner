# Process-core operator guide

Use a new private state directory for the general role-task path. Do not point
it at an installed Runner database, migrate old approvals or replace a service.
The executable supports `serve`, `run`, `result`, `start`, `status`, `inspect`,
`wait`, `cancel`, `retry`, `intervene` and `config-set`. Each command accepts `--help`.

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
runtime schema. Each file is limited to 16KiB, one document, with at most 128 files
and 16 projects per instance.

An instance sets `projects`, `concurrency`, `reconcile_interval` and `storage`.
`concurrency` limits active root families. Optional `max_active_attempts` is a
shared ceiling on all admitted unfinished attempts, including pending/unknown
effects. It defaults to `concurrency` when general role tasks are configured;
legacy-only configurations retain zero (no additional ceiling). A full ceiling
queues new work without spending attempts or asking for approval. Status reports
the effective ceiling and actual global active count. Legacy `task_concurrency`
still limits isolated source-card actors; `tasks.max_concurrent` limits each
general root within the shared ceiling.
A project sets `id`, `name`, `workspace`, `processes`, `default_process` and role
bindings (`file`, optional `profile`). Optional `source: {ref: HEAD}` enables
a local Git source baseline. Native profiles can instead use the explicitly
configured project workspace directly. A role sets `id`, optional `purpose`, named `inputs`/
`outputs` and either `execution` or named `profiles` with a `default_profile`.

The `harness: deterministic` modes are `echo`, `file`, `fail`,
`needs_help` and `log`; setup fields are `input`, `output`, optional `text`,
`delay` and `log_bytes`. Echo/log return one text output; file returns one artifact.
A selected input must be declared text or artifact. These are controlled test
operations, not commands, tools, skills or real model profiles.

A process declares inputs, an ordered `steps` list, named final output bindings,
`max_attempts`, `max_attempts_per_step` and `attempt_timeout`. A step binds role
inputs to `{from_input: name}` or `{from_step: earlier, output: name}`. Types are
`text`, `json` or `artifact`; the current deterministic transformations consume
text/artifact only. There are at most 64 steps and eight named values per contract.
Legacy processes permit up to 128 configured total attempts. General role-task
processes permit up to 32,768, shared across the whole root family; the configured
allowance should match the job. Automatic execution retries per visit remain
bounded at four. Internal task reservation and status updates do not spend attempts.

`attempt_timeout` limits each admitted attempt, including executor setup and
execution. A step may override the process default with its own `attempt_timeout`
(positive, at most one hour). Five minutes was the small pilot's chosen value,
not a universal Runner limit. Expiry requests a stop; confirming settlement, securing edits and
cleanup can continue afterward. It neither abandons uncertain execution nor
guarantees that the whole run has finished by that time.

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

## General role tasks

A process opts into role-assigned work with a `tasks` policy. These fields select
existing project roles and resource bounds; they do not define executable tools.
It has one root role step; compose the work through its child task graph.
General tasks use native, deterministic or explicit `command` / `shared` profiles.
Legacy source, command mode `""` and release adapters keep their own workspace contracts:

```yaml
tasks:
  roles: [lead, planner, developer, reviewer]
  max_tasks: 40
  max_depth: 6
  max_revisions: 6
  max_concurrent: 3
```

An ordinary step can return a `task_action` instead of final outputs. `delegate`
reserves child identities and dependencies; `revise` names affected settled child
IDs in `replace` and supplies their replacements. A change cannot leave a retained
dependent using superseded inputs. Unrelated accepted work and all old attempts
remain retained. `ask_human` saves a concrete question without inventing an answer.
Each child chooses an allowed `role`, a local `id`, a `brief`, typed `inputs`,
`acceptance`, optional sibling `depends_on` and literal relative `write_paths`.
The same contract supports specialist planners delegating further work.

Runner resumes parents with child/dependency results and the frozen original
goal. The task context includes remaining family attempts/tasks and configured
limits. Pending ancestors and admitted children reserve the work needed to return
their results; expansion cannot consume the allowance already needed to finish.
Execution retries, plan revision and a question for the owner are distinct actions.

Native source tasks share the configured project workspace. A workspace writer
must declare its owned paths; `.` claims the whole project. Overlapping writers
serialize, as do source readers against active writers. Disjoint writers may run
concurrently. Claims coordinate trusted roles; the native sandbox boundary is the
project, not each claimed path. Use one exclusive Genny initializer/regenerator
before disjoint feature work. Writing and research tasks need no Git checkout.

Use `inspect` for the full task definition, retained outputs, attempts and selected
roles. `result` exposes bounded coordination facts, including the current question,
phase, child status, family usage and cleanup. `result --wait` joins automatic child
progress and returns for an actual human question or pause.

### Fixed checks on shared source

`harness: command`, `mode: shared` selects configured argv on the canonical
read-only project without copying a Git checkout or invoking a model. It requires
general task context. Executable, arguments, environment and optional runtime
write roots come from configuration; task output cannot invent them. Substitutions
are limited to the configured source/workspace and owned scratch. The output
contract is `check: text` and, when configured, `artifact: artifact`.

On macOS this capability permits the browser IPC needed by configured headless
Chrome while restricting filesystem writes to scratch/output and explicit runtime
roots. Native AI shell permissions remain unchanged. Browser-specific temporary
and Core Foundation home overrides stay owned by the attempt; the account HOME
is preserved. It uses the same durable worker, process ownership and provisional
settlement rules. Linux has a separate sandbox path; compilation/model-free
coverage is distinct from actual native Linux qualification.

An expected nonzero checker exit may be mapped to a completed checking task so
its failed report/artifact survives. That is not product acceptance: an assessment
role must read the actual report and reject a failed check. The maintained SaaS
example explicitly routes accepted/rejected assessment outcomes. A verification
continuation references existing source, credentials and binary identity; it does
not rewrite a frozen earlier run or authorize new coding. Pure mechanical
verification needs no model assessment loop: the maintained verification-only
process runs the fixed checking role once and maps exit 1 directly to failure,
retaining the report. Full delivery still evaluates the original project goal.

### Native harness profiles

`harness: native` opts into the installed account and ordinary harness discovery.
It requires a purpose, explicit model/access, bounded input/output and a `native`
policy. It does not change the existing restricted `harness: codex` adapter:

```yaml
execution:
  harness: native
  model: gpt-6.1-sol
  reasoning: high
  access: workspace-write
  max_input: 64KiB
  max_output: 16KiB
  native:
    harness: codex
    tools: {mode: native}
    skills: {mode: native}
    network: true
```

Use named role profiles to permit alternate configured models/harnesses. Codex
native tool inheritance supports its enforced filesystem/network sandbox. Claude
supports an enforced selected read-only tool set (`Read`, `Glob`, `Grep`, `WebFetch`,
`WebSearch`) or no tools; its print adapter does not support workspace writes/full
command tools. Web tools need explicit network permission. Skill selection is
`native` or `none`; an unenforceable selected-skill allowlist is rejected. Access
to a tool does not authorize publication, deployment or another protected action.

For a Codex role that needs ordinary local coding tools without inherited
catalogs, explicitly select `native.context: lean` and `native.skills: {mode: none}`.
Empty context or `native` preserves normal native discovery. Lean keeps the
normal account, configuration/provider and project instructions. It disables
app/plugin/automatic-skill discovery, enumerates configured MCP servers with a
model-free CLI read, explicitly disables each name and verifies zero enabled
servers before launch. Unsupported settings or failed discovery reject readiness;
they do not fall back to a larger catalog. The current CLI has no global MCP deny
switch, so a configuration change after readiness can introduce a new server.
The manifest records the overrides, names, verification and probe time.

All Codex native profiles also disable native agent and direct-question handlers
with `agents.enabled=false` and
`tools.experimental_request_user_input.enabled=false`. Feature flags alone do not
disable agents when model metadata selects the newer agent implementation.
Delegation and human requests use Runner's task protocol; the corresponding native
tool descriptions are removed as well. Provenance records these common controls
under `context.runner_overrides`, including for full native discovery.

`skills: none` disables the harness's skill discovery/catalog; permitted file tools
can still read specific guidance named in the role instructions. The lean Genny
roles use that explicit path for the small relevant skill rather than advertising
every installed skill. Full native profiles remain available for research/design
roles that need connected capabilities.

`native.runtime_write_paths` grants up to sixteen explicit absolute runtime/cache
directories in addition to the task's source, scratch and output roots. Use
observed narrow tool cache locations, including required project-lock caches;
the adapter rejects source/coordinator-state overlap. These directories remain
shared runtime resources with their own maintenance policy. They are recorded
permissions, not model-selected paths or copies made per task.

`max_input` bounds the request supplied by Runner, including its task context.
It does not cap context injected by the native harness or each internal provider
call. An attempt is one executor visit; that visit can make several model/tool
calls before returning. Known token usage and native timing describe the actual
visit, with missing telemetry marked incomplete. Choose tools, model and effort
for the role; inheriting a complete catalog has a measured context cost.

Provenance separately records `runner_prompt_bytes`, `output_schema_bytes` and
the CLI's cumulative usage scope. Cumulative input can include repeated internal
calls and cached context; it is not the initial prompt size. Exact duplicate
goal/brief/input text is supplied once with explicit wire references. If complete
child results exceed the inline request or assignment bound, a file-reading role
receives one bounded, hash-bound read-only `results.json` with their complete
contents. Compact inline facts retain child identities and exact artifact IDs;
the canonical child outputs are not truncated. The private result file is bounded
by `max_artifact`, reserved by its actual JSON byte count and removed on release.
A Claude role without `Read` cannot use this overflow path.

SQLite operational schema seven prevents older services from misreading native
output recovery and primary completion, in addition to the schema-six
allocation/release and schema-five native-policy boundaries.
Opening supported earlier state preserves its saved
run/definition/attempt bytes; older binaries refuse the upgraded operational marker.
Use new private qualification state, leaving an installed pilot's cutover explicit.

The small native job record owns an attempt before any assignment/result files
are staged. An interrupted allocation settles as a pre-dispatch failure. Once
dispatch identity is persisted, missing inputs cannot authorize replacement or
cleanup. Release records completion of assignment-dependent cleanup before deleting
those inputs, retains its terminal ownership record until all deletion finishes,
and resumes safely after interruption. A failed parent resumption can be retried
within its existing completion reserve, retaining accepted children; only an
explicit root grant changes the family's attempt allowance.

Native cleanup positively identifies owned processes using inherited markers and
supported process identity/ancestry. It checks identities before stopping helpers,
preserves unrelated sessions, and keeps bounded provisional output/usage during
uncertain settlement so the same execution can resume without another model call.
The reported assurance is `cooperative-known-processes`, not arbitrary process
containment. On macOS, a fast detached descendant with hidden/cleared environment
and an unseen intermediate parent can be unobservable.

New attempts use a private attempt-owned FIFO and an exclusive output reader
lease. A terminal checkpoint preserves the result/usage before observer handoff;
normal reconciliation automatically recovers that same execution, including
after service restart. It requires verified original-stream EOF and successful
process settlement before collecting output or removing scratch. Recovery spends
no new attempt and does not rerun the model or command. Primary completion time
keeps cleanup beyond the deadline from failing work that finished on time.
The canonical provenance records recovered EOF; raw model streams are not saved.

An already-expired legacy anonymous pipe, or loss before a complete terminal
checkpoint, remains Unknown when the evidence cannot establish the result and
closure. An empty process inventory does not establish stream EOF. Shared command
diagnostics are bounded tails; a crash between a late read and its checkpoint
write can lose those late diagnostic bytes. The committed exit outcome remains
authoritative. The configured free-space
reserve is monitored during execution as a soft stop, including the source volume;
it is not an OS quota on third-party tools and shared caches.

Attempt provenance distinguishes permitted policy, observed availability and
observed calls, with machine/harness/model/context fingerprints and bounded result
decisions. Missing harness telemetry remains an explicit gap. A capability listed
in configuration is not proof that the model used it. Reports and provenance stay
with the accepted attempt; transient prompts and scratch belong to its cleanup.

### Human intervention

The owner can intervene on an exact inspected task revision. The `request_id`
makes replay idempotent. For example:

```sh
./cortexium-runner intervene --data /absolute/new/state --run RUN_ID --revision 12 --action pause --request-id pause-1
./cortexium-runner intervene --data /absolute/new/state --run RUN_ID --revision 13 --action steer --text 'Use a shorter technical article aimed at experienced developers.' --request-id steer-1
./cortexium-runner intervene --data /absolute/new/state --run RUN_ID --revision 14 --action resume --request-id resume-1
```

`pause` stops new subtree dispatch while active effects settle; it does not claim
to undo them. `steer` updates future work and `answer` resolves the saved question.
`reassign --profile NAME` selects a permitted named profile for a settled future
visit. Incompatible active changes are rejected until effects settle. `cancel`
propagates through the subtree and preserves uncertainty and cleanup obligations.
Interventions preserve attempts, usage, old decisions and unaffected work.

Explicit `steer` can also reopen a failed general task after its affected work
and cleanup have settled. It requires a valid current step and remaining original
family allowance; the directive and reopening are one idempotent operation.
It does not extend the allowance or alter profiles. Completed and cancelled tasks
stay closed. This supports a justified recheck after an observed runtime repair
without racing a retry against a separate steering request or creating a new goal.

## Runs, retries and cancellation

When composing role instructions, put stable general guidance first, shared
project knowledge next, then role-specific guidance. Supply the changing task
last through run inputs. Keep reusable blocks byte-identical and avoid volatile
IDs, paths or timestamps before them. The current adapter sends role instructions
as base context and the request afterward; it does not expose separate tier fields.
Measure startup and cumulative cached input separately. Correct ordering alone
does not prove cross-role cache reuse, and later within-session hits must not be
reported as evidence of it. Do not pad context merely to increase cache counts.

For a specified task, use one invocation with the project's existing process:

```sh
./cortexium-runner run --data /absolute/new/state --project demo --inputs '{"name":"Hello"}' --request-id request-1
./cortexium-runner result --data /absolute/new/state --run RUN_ID
./cortexium-runner result --data /absolute/new/state --run RUN_ID --wait
```

`run` uses the same start receipt and coordinator as `start`. It waits in Go for
completion and normal settled cleanup, or returns at Waiting or a terminal
cleanup error. Configured retry/escalation limits still apply; this operation
adds no allowance or repair agent. `result` reads the same compact presentation;
`--wait` rejoins without starting work. Cancelling/disconnecting the client stops
its wait, not the run. Reuse the same request ID and arguments after an uncertain
submission; do not invent a replacement ID. Explicit cancellation still uses
`cancel` and requires observed settlement.

For agents, `runner_run` takes the same arguments as `runner_start` and returns
the compact result in one MCP call. `runner_result` takes `run_id` and optional
`wait: true`. The caller's transport timeout can end a long call; use CLI `run`
where the tool host cannot keep an MCP call open, or rejoin using the same receipt.
There is no model polling loop or new scheduler in either path.

For a dedicated, short Codex intake, configure only the selected Runner tool and
preapprove that tool for the user's explicitly authorized submission. With a
private Codex home, the relevant configuration is:

```toml
[mcp_servers.runner]
url = "http://127.0.0.1:8787/mcp"
bearer_token_env_var = "RUNNER_OWNER_TOKEN"
enabled_tools = ["runner_run"]
tool_timeout_sec = 900

[mcp_servers.runner.tools.runner_run]
approval_mode = "approve"
```

Supply the private owner token through that intake process's environment, never
in its prompt or tracked files. This does not authorize arbitrary future work;
the intake still receives the actual user-authorized project, process and brief.
With `approval_policy = "never"`, leaving a write tool's approval mode at its
default can reject the call before Runner receives it. Check the effective
configuration and invoke a deterministic demo through the native manual MCP call
API before spending model turns; creating a thread alone submits no model turn.

Future comparisons must execute both complete arms in fresh, short sessions,
including intake, coordination, verification and reporting. This historical chat
must not coordinate the intervening steps. Reuse the prepared configuration and
measure the full boundary; keep preparation/repair costs visible and do not assign
half a shared experiment's costs as though they were a measured direct baseline.

The compact result exposes:

- Status/reason, revision and attempt count. Waiting can have unsettled execution;
  check `execution_settled` before interpreting it as stopped.
- Declared final output previews, at most 1KiB each and 4KiB in total, with an
  explicit `truncated` flag. Artifacts remain references. A retained source
  candidate's base and patch ID remain visible even if a later check fails.
- `elapsed_seconds`: creation through the latest settled attempt for terminal
  work, or through the snapshot time for ongoing work. Queue time is included;
  later cleanup does not inflate execution time. A terminal run with no attempts
  uses its saved update time.
- Known input/output/cached token subtotals across every attempt, including
  retries. `known_total_tokens` is input plus output; cached input is a subset.
  `incomplete_attempts` identifies model attempts with missing usage. Deterministic
  and command steps consume no model tokens. These figures exclude upstream
  intake/coordinator models, which must be measured separately in comparisons.
- `cleanup_pending`, `cleanup_error` and `workspace_retained`. Failed source
  checkouts can be released after their retained patch is verified; incomplete
  proof or uncaptured edits preserve the checkout and expose a cleanup error.
  A Completed result does not mean publication or deployment occurred.

Keep the run input to the actual task, relevant file/line references and completion
conditions. Stable role guidance belongs in the selected role configuration.
Do not copy coordinator conversations, historical plans or tool transcripts into
the assignment. Ordinary checks belong in configured command steps. Inspect a
detailed record only for a concrete diagnostic need.

For the Runner-owned software delivery process, use the
[reviewed Go example](../examples/reviewed-go/README.md) and
[external coordinator contract](coordinator.md). Its four steps own implementation,
focused checking, read-only review and local candidate integration. A failed
check or blocked review stops with no extra attempt. The caller must not perform
missing stages outside Runner or invent a repair dispatch loop.

Codex additionally accepts `workspace-read` access for source review. Retained
artifact inputs are materialized in attempt scratch; `patch` must identify the
run's retained candidate. This also supports measurement evidence. Other inputs
are inline text or JSON; outputs remain text.
The existing `runner-check` permission profile allows source reads and scratch
writes, with Git/private/network restrictions unchanged. Review submits a model
turn only when that step is actually dispatched. The zero-turn preflight verifies
permissions and thread setup; it does not establish model review quality.

`git-candidate` has no model. Its role requires `patch: artifact`, `verdict: text`
and `review: text` inputs and one `candidate: text` output, with
`access: workspace-read`. It creates an exact local candidate commit/ref only
for `APPROVE`; it changes neither the user checkout nor an existing branch. Its
owned unused branch expires with the run; checked-out or advanced branches are
preserved. Merge, push and deployment require
separate configured stages and authorization.

Configured commands can use `{text.NAME}` to pass one declared text input as a
literal argument/environment value. It is not a shell expression, and inserted
text is not recursively expanded. Existing `{input.NAME}` artifact paths remain
unchanged. Select relevant checks explicitly; never let the model construct a
shell command from task text.

The individual operations remain available:

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
extra allowance is at most four, with the total still bounded at 128. A grant
increases the run total and only the current step visit's execution allowance;
advancing to another step or correction visit restores its configured limit.

`recover-evidence-handoff` is a separate owner action for a source owner blocked
by an incomplete compact check handoff. It requires the exact public revision,
active `--phase-request-id`, blocked `--attempt`, accepted `--source-attempt`,
`--patch-sha256`, prior `--check-attempt`/`--check-sha256`, and retained
`--evidence-id`/`--evidence-sha256`/`--evidence-bytes` plus explicit JSON
`--evidence-roles`. The original full archive must belong to this root and its
producer, saved check and frozen declarations must bind the same candidate and
owner reference. The blocked owner's captured tree and patch bytes must equal
the accepted source. All attempts and cleanup must already have settled.

This action appends a receipt/audit to the existing current-route phase and
consumes its next remaining use through the saved correction edge. It does not
increase any ceiling, retry allowance or profile, change frozen inputs, replace
old archive bytes, or convert the blocked result into success. Only named future
source/review roles receive the exact archive in their existing protected input
namespace. The next source owner runs normally and must pass all fresh checks,
reviews and protected gates. An exhausted phase rejects admission. Reuse the
same request ID and exact arguments after disconnect; replay and restart recover
the committed audit without spending again. Ordinary `recover-corrections`
continues to require an exhausted returned source-check rejection and cannot
recover a blocked source owner.

Status returns at most the 100 most recently updated summaries plus `total_runs`,
configuration/storage errors and content-byte accounting. Inspect returns one
bounded record with resolved setup, attempts, outputs, usage and cleanup state.
Unknown usage is omitted, not recorded as zero. No model tokens are consumed by
the deterministic executor. Wait uses Go revision signals and returns at terminal
or Waiting status; Waiting requires inspection of its reason and active attempt.

After an unexpected Runner exit, restart with the same configuration and data
directory, then use `inspect --run RUN_ID`. Runner reconciles saved execution
before admitting a replacement; restarting does not authorize another coding
attempt. If the original Codex service is reachable, the adapter can reattach to
its saved thread/turn. The writable fixture qualified this path on macOS after
SIGKILL of the Runner owner process, with the original native service and command
still running; see the [recovery exercise record](architecture.md#writable-recovery-exercise-29-october-2026).

To stop recovered work, use `cancel --run RUN_ID`, then inspect until cancellation
and cleanup are confirmed. A Waiting/unknown attempt still holds capacity. If the
reason says that original child settlement is unconfirmed, retain the state and
workspace and inspect the original execution; there is no supported force-clear
operation. Do not delete job files, edit SQLite or retry to bypass that state.

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
finish. Format 2 stores settings and operational history as separately capped
documents in the same transaction, within the existing database/run reservation.
Format-1 records remain readable without rewriting their history. Downgrading
requires a matching pre-upgrade database backup. History capacity or disk pressure
can leave work Waiting/Queued with a
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

For workspace-read/write profiles, Runner can keep bulky declared inline inputs
in its existing protected attempt input directory instead of exceeding this
assignment limit. The native payload's trusted outer `input_file_keys` lists
exactly which values are JSON-file descriptors; the worker reads and decodes those
files to obtain the complete original typed values. Other input objects retain
their ordinary meaning. Saved assignments and evidence are unchanged, with the
same artifact, storage and access limits. Selection reserves the widest deadline
field so observation/restart cannot change file ownership; a few bytes near the
limit may therefore use files conservatively. Input-only profiles still reject
oversized input directly. Staging does not limit the total context or tokens
consumed when a model reads source or evidence files.

Declared inline text/JSON inputs and text outputs are supported. No environment or workspace
roots are selected. MCP/apps/plugins/hooks, shell/subagent tools, web search and
automatic skill/instruction discovery are disabled. Required effective settings
are checked before requesting a turn. The separately qualified bounded Go workspace profiles are described below.
Other role skills/tool catalogs remain outside this schema.

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

## Bounded writable Go execution

See [the configuration example](../examples/workspace/README.md). It accepts the
implementation task through the `request` run input. The fixed Go qualification
fixture belongs to `testdata/seats/`.
A source-enabled project names its repository root and explicit `source.ref`.
`start --source-ref REF` / MCP `source_ref` may select another local ref/commit.
Runner freezes that selection with the run and resolves the commit at preparation;
subsequent branch movement/restarts do not change its base. No automatic fetch,
pull, merge or inclusion of uncommitted user edits occurs.

The instance must select `codex.go_binary` and concurrency one. Its optional
`codex.read_only_paths` lists exact relative source paths protected from worker
writes; the example protects `go.mod`; add the test files that must remain unchanged. These are service
settings and require restart. Workspaces/state must use short private paths outside
system temporary directories. The first fixture supports regular source files and
rejects symlinks/submodules/Git attributes in the baseline; it is not a general
qualification of existing repositories or custom Git configurations.

Optional `codex.go_module_cache` selects an existing, prepopulated Go module-cache
directory, resolved relative to the instance file. Workspace workers receive it as
`GOMODCACHE`; coding and review/check profiles can read it but cannot write to it.
The input-only profile has no cache grant or workspace environment. The directory
is checked before native setup and each workspace worker; a missing cache fails
readiness before a model turn. Runner downloads no dependencies and keeps native
workspace networking disabled. The default remains an unselected module cache.

Optional `codex.go_check_binary` selects an existing executable file for the fixed
Go check reporter. Workspace workers receive its path as `RUNNER_GO_CHECK` and a
read-only file grant. Input-only gets no additional grant or environment. The
executable is checked again before workspace execution; Runner does not install it.

A coding role uses `harness: codex` / `access: workspace-write`, declared text
outputs plus `patch: artifact`. Runner produces that patch; the native result
schema requests text only. Coding uses the explicitly selected local environment,
named permissions, no login shell and an explicit command environment. Git/native
account/control files and external capabilities remain inaccessible to the worker.

A workspace coding role or initial configured read-only command may explicitly
accept `patch: artifact` to continue retained source in a fresh run. Runner
validates and restores that seed before dispatch and keeps the caller's accepted
source ref as the base. A later coding role captures seed plus correction
cumulatively. A prepared seed is not a captured or reviewed candidate.
Protected paths, symlinks/submodules, unsafe
paths, malformed patches and size-limit violations are rejected. An empty seed
means the unchanged base. Seed provenance/readiness is durable; only an owned,
pre-model seed preparation may be rebuilt after interruption. Ready workspaces
preserve edits on replay. This retains failed work without accepting or publishing it.
Failed preparation can release an unchanged base or verified retained seed;
uncaptured source changes remain preserved.

Source may include one immutable tracked root `.gitattributes` containing only
unquoted patterns with the built-in `text=auto` and `-whitespace` rules, plus
comments. Runner disables global/system attributes, rejects repository info
overrides and nested/unsupported attribute rules, and makes the root file
read-only for native workers. Literal blob comparisons preserve exact source and
capture bytes even when Git text normalization would consider an edit unchanged.
That comparison reads the tracked files of attribute-bearing workspaces; it is
bounded by the existing workspace limits. Filters, custom drivers, encodings,
macros and submodules remain unsupported.

The check role uses `harness: go-check` / `access: workspace-read`, exactly
`patch: artifact` input and `check: text` output. It has no model settings and
runs only the fixed Go suite command. A candidate mismatch prevents checking.
No automatic repair is configured in the example. Inspect shows source base/tree,
workspace, input/saved patch references, check result and `resources_released`.

Source is secured even for failed/cancelled coding attempts. Successful/cancelled
worktrees are removed after settlement and saved results. Failed worktrees are
also released once all attempts have settled and released, the saved bounded
patch reconstructs its recorded candidate tree against the pinned base, and the
checkout contains no uncaptured source changes. Missing proof or unique edits
preserve the workspace and its history for inspection, including at history expiry.
An explicitly authorized Retry restores the same candidate against its saved base
before execution; interrupted preparation can replay, while ready edits survive.
Cleanup preserves the Failed result, logs, patch and provenance, and grants no
new attempt allowance. Source checkout size and recovery/native headroom are
checked before Git allocates a worktree. Retained patches pin their base commit,
with ordinary retention releasing that ref once no results/consumers require it.
Collection or persistence
failure holds the attempt for recovery. An uncertain child command holds capacity;
server exit alone does not authorize source cleanup or replacement execution.

Native worker/scratch growth is monitored, not an OS quota. The measured live scope
and remaining limitations are in the [qualification record](architecture.md#writable-go-qualification-29-october-2026).
The renewed one-turn allowance has been used; do not opt in to another native run
without a new explicit allowance. Ordinary repository gates submit no model work.

## Configured command execution

[examples/command](../examples/command/README.md) shows a model-free command on
committed source. Use `harness: command`, `access: workspace-read` and an explicit
task purpose. This first implementation shares workspace preparation, Go runtime
settings and concurrency one with the writable adapter. It uses the installed
Codex sandbox, but creates no native thread or model turn and needs no model settings.

`command.args` is argv, with an absolute executable and at most 32 arguments;
there is no implicit shell. `directory` is relative to the source root. Optional
`env` adds/replaces explicit environment entries. Args and env support
`{workspace}`, `{scratch}` and `{input.NAME}` for a retained artifact input.
Text inputs express dependencies; they are not interpolated as commands.
The command starts with a clean environment and attempt-owned Go caches.

Inputs may be text and retained artifacts, within the eight-name role contract.
Baseline/proposed comparisons can receive separate bounded artifacts. Outputs are `check: text` plus
optional `artifact: artifact`. `command.artifact` names that one regular file,
relative to scratch, within `storage.max_artifact`; symlink escapes are rejected.
The check output contains exit status and a short diagnostic. A bounded log tail
is retained separately. Unmapped failed exits do not produce successful outputs;
declared domain outcomes follow the configured routes below.

Source is read-only and only attempt scratch is writable. Networking is disabled
unless the trusted configured command explicitly sets `command.network: true`.
That opt-in permits connections to external hosts; it is not a loopback allowlist.
Verification scripts must bind any test server to `127.0.0.1`. The setting applies
only to configured commands; native coding/review profiles remain offline, and
source, private-file and scratch restrictions are unchanged.
`codex.command_read_paths` grants up to 16 selected runtime/dependency paths for
commands only. It does not expand coding permissions. Paths are resolved relative
to the instance file; filesystem root is forbidden. Runtime settings require a
service restart. Copy needed dependencies into scratch before a build that writes
beside them. Ordinary dependency links in disposable work are counted without
following them; controlled file IO and retained artifacts cannot traverse links.

The same Runner executable starts a small private `command-worker` process after
saving dispatch intent. The worker claims its attempt once and saves its result
outside the child's sandbox. Owner shutdown detaches it; restart reads that same
receipt rather than dispatching again. Cancel writes a private stop signal, and
the worker stops the command's process group before recording settlement. Missing
or invalid settlement evidence remains Unknown and retains resources.

Supported scope is trusted, synchronous commands on macOS/Linux. Do not configure
daemons, detached children or scripts that escape their process group. Owner-crash
recovery and the sandbox were exercised on macOS; host/worker loss and Linux
runtime enforcement remain unqualified. Storage growth is monitored, not an OS
quota. The older `go-check` uses the connection-scoped native API and retains its
documented lost-connection limitation.

## Complete workflows: corrections, measurements and decisions

An ordinary `runner_run` now follows declared outcome routes as well as ordered
steps. `ready` advances to the next step unless explicitly routed. Other outcome
names must appear in that step's `routes`; workers cannot supply next steps.
`$complete` resolves the process's required outputs; `$fail` rejects the result.
Missing proof cannot become successful completion.

```json
"routes": {
  "changes_requested": {"next": "owner", "max_times": 2},
  "ready": {"next": "review"}
}
```

A return path needs a positive `max_times`. Its consumed count is saved in the
run and is independent of execution retries. `max_attempts_per_step` bounds
execution attempts per visit; `max_attempts` bounds all visits and retries in the
run. A rejected benchmark candidate is a normal domain outcome, not a crashed
executor. Exhausting a return allowance stops with its actual reason. No route
increases the configured authority or allowance.

Correction feedback is resolved before downstream results are superseded. Ordinary
bindings then exclude superseded proof. An explicit `previous: true` binding can
carry earlier measurements/findings, and a typed inline `default` supplies the
first visit's initial feedback. Use previous results as feedback, not as proof of
a changed candidate. For example:

```json
"measurement": {
  "from_step": "measure", "output": "check",
  "previous": true, "default": {"inline": ""}
}
```

`{"from_candidate": true}` selects the latest successfully captured source
patch. It supports combined task results and later correction owners; it does
not select a partial failed coding result. Verification, review and integration
can bind this same current candidate. Their outputs are invalidated together
when the process returns to the correction owner.

A command can classify expected nonzero exits with `exit_outcomes`, such as
`{"1":"changes_requested"}` for a test command. Unmapped exits remain execution
failures. Alternatively, `command.result` names a bounded scratch JSON file:
`{"outcome":"continue","reason":"one more comparison is justified"}`.
The file accepts only outcome/reason; it cannot select roles, commands or routes.
Declare actual measurements separately with `command.artifact`. A benchmark
helper performs the fixed measurement in ordinary code; an owner interprets it
only when the configured next stage needs judgment. Keep rules, cohorts and
comparison conditions explicit; retaining a measurement establishes no speedup.

A step may opt into `reuse: true` for an offline model-free read-only check with
text receipts. Within the same run, the selected profile/runtime and inputs must
match. Captured source-tree identity identifies equivalent regenerated patches.
A reused attempt records `reused_from`, zero new model usage and no new executor
job. Changed source or selected inputs require new proof. Hold configured tools
and their dependencies fixed during a run. Review, artifact-producing measurements
and delivery are not reusable checks.

An outcome route with `approval: true` saves a decision tied to that exact
attempt and outcome. `runner_result` exposes its ID and continuation. Approve or
reject explicitly through the owner control interface:

```sh
cortexium-runner decide --data /private/state --run RUN_ID \
  --decision DECISION_ID --choice approve --request-id delivery-decision-1
cortexium-runner result --data /private/state --run RUN_ID --wait
```

The MCP operation is `runner_decide`. Reuse its request ID after an uncertain
response. Stale decision IDs and retry-based approval bypasses are rejected.
Restart preserves the decision and completed work. A fixture's simulated approval
does not authorize a real project's deployment.

## Dependency-ordered task collections

A `for_each` step expands a fixed template from 1–8 task items supplied as inline
JSON or a planner's JSON text. Items contain `id`, optional `depends_on`, and
`inputs`. Those inputs must match the configured item contract. Unknown fields,
duplicate IDs, missing dependencies and cycles fail before item execution. Task
data cannot introduce roles, permissions, executable commands or artifact access.

```json
{
  "id": "tasks",
  "for_each": {
    "items": {"from_step": "plan", "output": "tasks"},
    "inputs": {"request": "text"},
    "steps": [{"id":"work", "role":"implement", "inputs":{
      "request":{"from_item":"request"}
    }}],
    "output": {"from_step":"work", "output":"patch"}
  }
}
```

Items execute serially in stable dependency order in the run's owned workspace.
Predecessor code is present for dependent work. The join returns `results`, a
text JSON manifest of each item's selected output, and `last`, its final selected
output. Shared verification/review/delivery can follow once, using the current
combined source candidate. A template may have bounded local correction routes;
nested expansion and return paths across an entire collection are rejected.
A later combined correction uses a separate owner after the group, preserving
completed task history. There are at most 64 expanded steps and 128 total attempts;
configured record/storage budgets still apply. Expansion that cannot fit its
record waits with an actionable capacity reason before item execution.

The [tasks example](../examples/tasks/README.md) runs without models. The
[reviewed development example](../examples/reviewed-go/README.md) includes linear,
iterative and planner/task variants. These are selected processes, not another
scheduler or a worker-defined workflow language.
