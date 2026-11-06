# Runner process core design

Updated 6 November 2026. **The owner has authorized completing and installing the
full bounded planner workflow, safe parallelism and routine recovery, verified
across real projects. Merge and production remain owner-approved actions.**

## Current completion plan

This is the active plan for the authorized completion work. Earlier slice limits
below remain historical qualification boundaries until the new proof is recorded.

1. Connect original intake to a source-aware read-only planner, a bounded set of
   cards with completion conditions and dependencies, and the existing complete
   check/review/qualification/release process. Models select work within fixed
   role contracts; they cannot introduce commands, permissions or release policy.
2. Add opt-in isolated task groups managed by the existing Core scheduler. Each
   card uses an internally linked run and its own workspace. Declared overlapping
   write scopes require dependency ordering. Accepted predecessor source seeds
   dependent cards; deterministic integration establishes one combined candidate.
   Combined correction and review do not replan or repeat completed cards.
3. Keep routine source corrections, bounded measurements, known safe infrastructure
   retries and authenticated same-PR continuation in the configured process.
   Preserve unknown effects and integrity failures rather than replaying them.
   One root attempt envelope includes its card attempts and reserves the release
   suffix before more source work. New planned processes default to eight cards,
   two parallel card actors and 64 aggregate attempts; older definitions stay fixed.
4. Store new attempt histories as individually bounded rows in the same SQLite
   store. Keep existing history documents unchanged and retain the current
   database, record, artifact and disk limits. Prove restart, cancellation,
   dependency blocking, scope conflicts, aggregate limits and linked retention.
5. Qualify the complete path using real Go work, the retained Jack's Break mobile
   request and a bounded investigation, plus independent project evidence where
   prerequisites permit. All product execution stays inside Runner. Record exact
   source/configuration identities, task overlap and dependency evidence, quality
   results, recovery events, usage and remaining gaps. Install only the verified
   build, preserving pilot history and all protected approval gates.

Completion requires repeatable request-to-qualified-result behavior with no
external stage selection, product repair, limit increases or gate bypasses.
Routine decisions within the approved request use the configured policy; only
material missing product decisions, uncertain authority/effects, exhausted
envelopes and protected actions reach the owner. A successful fixture alone does
not establish cross-project reliability. Existing planner guidance must match
the actually qualified runtime before the capability is described as ready.

The [README](../README.md), [actual code walkthrough](architecture.md) and
[operator guide](operator-reference.md) describe that slice and its verification
boundary. The subsequent input-only Codex adapter is also implemented and its
three-task qualification is recorded in the code walkthrough. Broader examples
below are not automatically fields accepted by the runtime loader.

This is the concise record for the clean Runner design being developed with Dan,
one piece at a time. It captures the agreed direction and working concepts;
broader schemas, APIs and execution policies remain for later slices.
Earlier Runner code and qualification remain historical references. The new
slice implements the bounded scope below; it does not complete every capability
in this design. Keep this record current rather than creating another plan.

The historical 3 November completion implemented named outcome routes, bounded correction
and experiment repetition separate from execution retries, current-candidate
bindings, opt-in reuse of unchanged check evidence, persisted approval/resume and
dependency-ordered task expansion from fixed templates. One ordinary core owns
all of them. The operator guide specifies the accepted syntax; the architecture
record owns end-to-end evidence. That qualification used serial task items in
one owned workspace. The 6 November completion adds isolated parallel card
groups as described above and in the current code walkthrough. Nested groups,
in-flight plan amendment, remote/multi-user control, arbitrary role tool catalogs
and distribution remain outside the qualified scope.
Production delivery requires its configured project capability and authorization.

## Core contract at a glance

This is the consolidated working direction. The detailed sections retain the
reasoning and examples; their provisional syntax is not a frozen runtime API.

**Purpose.** Runner is a generic configurable process engine written in Go.
Ordinary code coordinates execution; roles, skills and tools provide domain
judgment. Software development is the first use case. GitHub and other external
systems are optional integrations.

**Configuration.** Human-editable YAML/JSON files own configuration. Separate
project, process and role files give each definition one clear home. A role keeps
its purpose, input/result contract and execution setups together. A process selects
roles/capabilities and connects steps. A project supplies explicit selections,
context/tool bindings and permissions. Small bindings may be inline; separate
tool files are equally valid. Validate and reload edits automatically, retaining
the last valid loaded configuration and a clear error for rejected changes.

**Execution.** One local service/coordinator owns an instance; CLI/MCP clients
share its operations. A run selects one process and at most one project context.
Steps receive bounded assignments and produce declared outcomes plus named
outputs. Each actual try is an attempt. Simple step lists are sequential;
branches/parallelism are explicit. SQLite owns operational state/history and the
resolved settings used, while files remain configuration authority.

**Profiles and escalation.** Select one complete setup of harness/model, guidance,
skills, tools and context policy. Shared role requirements and acceptance stay
fixed. Configured escalation can select another permitted profile, route to a
specialist or ask a human. Retries, domain repair and escalation remain distinct,
bounded and charged to the same retained resource accounting.

**Isolation and recovery.** Projects progress independently, subject to explicit
shared capacity/conflicting resources. Persist attempts before dispatch and
results before releasing dependent work. Reconcile uncertain execution before
any potentially duplicating replacement. Retain completed work, waits and partial
effects through restart. Edits do not become blanket checksum/reapproval gates.

**Context and authority.** Give each assignment its selected relevant sources,
original request/authorized changes and required prior outputs. Keep stable
shared/role material first where supported; measure actual cache behavior. Tool
availability does not grant access. Project/instance policy and caller authority
constrain supported execution; profiles cannot widen them.

**Visibility and resources.** Derive Queued, Running, Waiting, Completed, Failed
or Cancelled from recorded facts, with reasons and next actions. Attention and
archiving are separate. Account for all observed work, preserve unknown usage,
and enforce supported allowances without inventing more work or waiving finish
conditions. Bound owned storage, keep only selected results/diagnostics, and clean
up disposable resources under retention/recovery policy.

**First build boundary.** One local instance, a two-step deterministic process,
human-editable configuration, SQLite state, local MCP/CLI control and focused
proof of handoffs, bounded retry/escalation, cancellation, reload, restart and
bounded storage/release of temporary executor data.
Use a second project/run to prove independent progress. Detailed acceptance is
in [the proposed first implementation slice](#proposed-first-implementation-slice).
Real AI roles, harness qualification, remote installation, domain workspaces,
delivery, full cleanup/upgrade qualification and experiments follow in their
own slices. These are retained design requirements, not first-slice claims.

**Before implementation.** Agree that bounded build contract and the concrete
interfaces/schema needed for it. Executor/service/upgrade details below still
contain proposals. The concrete package layout and isolated build boundary below
are proposed for approval; changing Cortexium/Worker ownership is outside a
Runner-only slice.
No implementation, installation or model experiment is authorized by this summary.

## Guiding principle

**Encode settled decisions once, and focus AI reasoning on decisions still open.**

Genny illustrates the principle: opinionated capabilities can remove repeated
implementation choices. Runner should execute selected processes consistently;
roles, skills and tools supply the domain knowledge. An addition earns its cost
by removing repeated work or improving a consequential decision. More agents,
instructions or tests are not success measures by themselves.

## Agreed constraint: proportional safeguards

Ordinary edits must not become fragile workflows because of checksums, locks or
version dependencies. Every blocking safeguard needs a specific failure it
prevents, a clear owner and an understandable recovery action. Use the simplest
mechanism that protects that boundary.

Keep stable task/run identity independent of editable content. Retain the actual
configuration and assignment used for execution as history. References to versions
in this design mean useful provenance and explicit adoption of changes; they do
not require a bespoke checksum/version protocol for every editable definition.

Approval covers a defined scope or action. Display-only edits and progress notes
must not invalidate it. Changes affecting that scope or supporting evidence need
targeted handling, using authorization already granted and retaining unaffected
work. Do not hash an entire task document as a blanket approval gate. Exact
artifact identity may still matter at a boundary such as publishing reviewed code.

Use normal storage transactions for short state changes. Exclusive ownership is
for actual conflicting execution/resources, with visible ownership and recovery;
ordinary editing must not acquire a whole-run or project-wide execution lock.
Retain the agreed reconciliation rule for uncertain external execution. A failed
guard must explain the relevant change and next action rather than expose an
opaque checksum mismatch as the user's repair task.

## Agreed configuration ownership and automatic reload

Human-readable files are authoritative for Runner configuration: project/process/
role/profile definitions and selections, bindings and policy settings. Humans can
edit them directly. CLI and MCP configuration operations update those same owning
files under the caller's authority; they do not maintain a separate editable
database configuration.

Runner detects file changes, re-reads the affected configuration, validates it
and automatically activates valid changes. No separate Apply/Import operation or
service restart is required. Detect edits promptly and reconcile configuration
at startup and through the existing fallback loop so a missed notification does
not leave an edit unnoticed indefinitely. YAML/JSON and the multi-file organization
below are agreed; exact locations and change-detection mechanisms remain
implementation decisions.

An invalid or unreadable edit leaves the last valid loaded configuration active,
with a visible configuration error identifying the file, location where available
and reason. Do not report the edit as applied or partially activate an invalid
set of related definitions. Other projects continue independently. Without a valid
loaded configuration, affected new dispatch waits with an actionable reason;
reconciliation of already-started execution must remain possible.

Activation follows the agreed change rules below: reusable defaults normally
affect future runs; targeted active-run changes have explicit effective points.
Automatic reload does not silently replace an executing assignment or migrate a
run's process structure. Existing permission-revocation rules still apply.

SQLite owns operational state and history, including the resolved configuration
actually used by a run/attempt. Those snapshots explain and recover existing work;
they are not another editable source of configuration. Inspect operations show
effective loaded configuration and any rejected/pending file changes. Source code,
skill/tool implementations and knowledge documents retain their normal repository/
file owners. Secrets stay in the normal credential facilities; configuration holds
references where needed. No two-way file/database synchronization or whole-task
checksum gate is introduced.

### Agreed human-facing file organization

Prefer YAML for human editing and support JSON with the same fields, meaning and
validation. Both formats load into the same configuration model; they are not two
configuration systems. Files can use either format within one configuration set.
This organization is agreed; concrete fields and authoring conventions still need
review and are not implemented APIs.

Use one named role or process per file by default, grouped in discoverable
directories. For example, one project's configuration directory could contain:

```text
runner/
  project.yaml
  roles/
    planner.yaml
    implementer.yaml
    test-author.json
    reviewer.yaml
  processes/
    software-change.yaml
    content.yaml
```

The directory name is illustrative. Project configurations remain independent;
shared definitions are selected explicitly rather than inherited through hidden
directory precedence or automatic merging.

A role file should provide a coherent view of that role: purpose, input/result
contract, instructions, skills, tools, context requirements and default execution
settings. The logical role/profile distinction remains useful, but does not require
separate files. Keep role-specific named execution profiles in the same role file
when useful for local/large-model variations or experiments. Shared profile files
are optional when reuse justifies them. Lengthy instructions can be referenced as
relative Markdown files instead of expanding every role configuration.

A process file describes the steps, role/profile selections, input/output sharing,
routes and process-level retry/repetition/finish policies. It references role IDs
instead of copying their model, tool and instruction settings. Ordinary program
steps can reference capabilities directly without inventing an agent role. Project
configuration selects its processes and supplies project context, concrete tool
bindings and authorized actions.

Resolve named references across the configured files. Duplicate IDs within the
same definition scope and missing references produce clear errors with source
paths; file ordering must not silently select a winner. CLI/MCP edits target the
owning file and retain its format and unrelated content/comments. Automatic reload
uses the existing validation/last-valid-configuration rules across related files.
Exact schema and locations still need review; avoid mandatory inheritance,
templating or configuration layers for this initial organization.

### Working role and process example

Dan accepts this as a plausible working design: cohesive role files, process files
referencing roles, and an ordered step list for simple sequential work. Concrete
field names and richer routing syntax remain provisional rather than a frozen API.

These snippets illustrate authoring, not installed roles or finished role guidance.
The execution settings show placement, not a qualified model/profile selection.

`roles/writer.yaml` keeps purpose, contract and default execution setup together:

```yaml
id: writer
purpose: Turn a supplied brief into a draft.

inputs:
  brief: text
outputs:
  draft: text

execution:
  harness: codex
  model: gpt-6.1-sol
  reasoning: xhigh

instructions: |
  Follow the brief and return the requested draft.
  Stay within the requested scope.

tools: []
```

`processes/draft-post.yaml` selects the role and connects inputs/final outputs:

```yaml
id: draft-post
inputs:
  brief: text

steps:
  - id: write
    role: writer
    inputs:
      brief: {from_input: brief}

outputs:
  draft: {from_step: write, output: draft}
```

Propose an ordered step list for ordinary sequential processes: start at the first,
continue to the next on its sole successful outcome, and complete after the last
succeeds with required final outputs saved. That case needs no explicit start/next/
finish boilerplate. Branches, parallel paths and multiple domain outcomes need
explicit configuration; their syntax remains open. Resolve this shorthand into the
same core process model, not a second execution engine.

Listed inputs/outputs here are required text values. Named bindings identify exact
sources without an expression language. Success requires the role's declared
outputs; execution failures follow the selected recovery policy. Empty `tools`
requests no tools, not every installed harness tool; actual enforcement still
needs qualification. Skills/context/profile selections belong in the role when
needed, without repeating them in processes.

Control starts `draft-post` with a brief. The core resolves `writer`, saves the
run/assignment and dispatches through its executor. After saving the returned
draft, the core completes the run with that named output. Completion means a draft
is ready; publication would be a separate configured authorized action. JSON has
the same fields and behavior. The first build slice still uses deterministic
capabilities to qualify the core; this illustration does not add AI execution to it.

### Working project example

`runner/project.yaml` connects the project's working location and selected
definitions. This Jack's Break example is illustrative, not its actual installed
configuration; paths, bindings and action names remain proposed syntax.

```yaml
id: jacks-break
name: Jack's Break
workspace: ..

processes:
  software-change: processes/software-change.yaml
  new-puzzle: processes/new-puzzle.yaml
default_process: software-change

roles:
  planner:
    file: roles/planner.yaml
  implementer:
    file: roles/implementer.yaml
    profile: local
  test-author:
    file: roles/test-author.yaml
  reviewer:
    file: roles/reviewer.yaml

context:
  overview: ../README.md
  project-rules: ../AGENTS.md

tools:
  tests: tools/project-tests.yaml

permissions:
  allow:
    - workspace.read
    - workspace.write
    - tests.run
```

Paths resolve relative to the file that contains them; here `workspace: ..` names
the directory above `runner/`. Process/role names are project-local references to
the selected definitions. `default_process` handles a start that omits an explicit
process choice; it does not interpret or automatically dispatch incoming messages.

The role's normal execution setup applies unless an existing named profile is
selected. Here `local` would have to exist in the implementer's role definition;
it does not silently substitute a local model or imply that profile is qualified.
Selecting a different profile chooses its whole coherent setup, not just a model.
The process still describes the work and input/output connections.

Context entries name available sources, not material automatically copied into
every role's input. The role/process selects required context using the agreed
context contracts; those selected sources and original run inputs are recorded
with the effective assignment. Explicit references may select shared definitions/
knowledge within existing authority, without hidden inheritance or directory
precedence. Editing this project's selections does not alter another project's.

Project-specific tool bindings are optional. Roles select available tools; a project
does not need to repeat the installed tool catalog. The example binds the logical
name `tests` to the project's tool configuration in a separate readable file.
That file owns its supported command/argument/settings definition; its concrete
schema is still open. File references are useful for overview or reuse; small
bindings can also be inline. Neither form is mandatory or categorically preferred.
Test scope is selected when invoking the tool; declaring it does not run tests.
Runner has no mandatory built-in test-tool concept; ordinary domain capabilities
provide testing behavior.

Availability is not a grant to every role. A dispatch must fit the role's tool
selection, project permissions, instance policy and caller authority, with actual
enforcement supported by the executor/capability.
Permissions are an allowlist; unlisted actions such as publication are not granted
by this example. Invalid selections use normal configuration/readiness errors,
not silent fallback. Resource allowances continue to use their agreed scopes;
this file does not add another mandatory layer of per-project budget settings.

### Working named execution profiles

Named complete profiles are the working direction. The field names and examples
below remain provisional; adapter/profile qualification is separate work.

When a role needs multiple execution setups, keep named profiles inside its role
file. The role's purpose, required inputs/results and common instructions remain
fixed. Replace its single `execution` block with `default_profile` and `profiles`;
do not keep competing execution defaults in both forms. A role with one setup can
keep the simpler single-setup form above. Both resolve to one effective setup.

For `roles/writer.yaml`, this proposed fragment illustrates two complete choices:

```yaml
default_profile: sol

profiles:
  sol:
    harness: codex
    model: gpt-6.1-sol
    reasoning: xhigh
    instructions_file: instructions/writer-sol.md
    skills: [writing-sol]
    tools: []
    context:
      max_input_tokens: 24000

  local:
    harness: pi
    model: qwen3.8-27b
    reasoning: low
    instructions_file: instructions/writer-local.md
    skills: [writing-local]
    tools: []
    context:
      max_input_tokens: 6000
```

Names, numbers, adapter/settings support and referenced guidance/skills are
illustrative, not installed or qualified defaults. The local-profile shape reflects
Dan's reported need for small context, lower reasoning and more explicit guidance.
It is not evidence that these exact settings work for a writing role.

A project selects a complete profile explicitly:

```yaml
roles:
  writer:
    file: roles/writer.yaml
    profile: local
```

If omitted, use the role's declared default setup. The process keeps `role: writer`.
Do not merge settings from other profiles or silently choose another model when
the selected setup is unavailable. Readiness checks supported settings, referenced
resources and enforcement; role acceptance and project permissions still apply.
Required inputs/context cannot be silently dropped to satisfy a smaller allowance.
Common role instructions remain applicable; profile-specific guidance supplements
them. Keep stable selected shared/role material before profile/task material where
the executor permits, under the existing context/cache agreement.

The resolved profile and actual observable assignment are recorded with the
attempt. Comparisons select these same profiles through ordinary isolated runs;
profile syntax does not require another experimental execution path. This remains
configuration design, not role development or authorization to run models.

### Working escalation handling

Configured bounded escalation is the working direction. Concrete authoring
fields below remain provisional.

Support optional, configured escalation through the existing process/attempt/wait
mechanisms. It can select another permitted profile for the same role, route work
to a named specialist/decision step, or ask an authorized human. It does not need
a separate escalation agent, scheduler or run status. A role may request help with
a specific reason and relevant retained work; it cannot authorize its own extra
scope, access, spending or new process steps.

The role exposes its supported outcomes and compatible profiles. The process
defines the trigger, permitted destination, bound and exhausted behavior; project/
instance authority and remaining resource allowance still constrain dispatch.
Domain judgment about uncertainty or inadequate results belongs to the configured
capability/reviewer. The core applies the declared rule; it does not call a model
to reconsider every failure. Model confidence alone is not a universal correctness
signal; any threshold must be qualified for its task.

For a project that selected `local`, a step could explicitly permit one escalation:

```yaml
- id: implement
  role: implementer
  escalation:
    on: needs_help
    profile: sol
    max_times: 1
    exhausted: wait
```

`needs_help` would be a declared outcome with a reason and any useful partial
results; it does not claim the implementation is complete. The normal success
contract remains unchanged. This syntax is a proposal, not a configured real role.
The target is a compatible named profile, not a claim that a larger model always
improves a task. It receives the original request, authorized decisions, relevant
work/evidence and escalation reason, meeting its own input requirements without
inheriting an entire conversation automatically.

Keep transient execution retries, normal domain rejection/repair and escalation
distinct. An infrastructure error does not by itself justify a more expensive
model. Settle or reconcile the previous attempt and its partial effects before
dispatching a replacement; unknown execution cannot be bypassed by escalating.
A profile change creates a new attempt, retaining history, consumed usage and
recovery counters. All transitions remain bounded within the existing run/step
allowances; changing profile must not reset budgets or introduce an automatic loop.

Without an authorized applicable policy, or after its allowance is exhausted,
retain the work and expose the needed decision using normal waiting/failure
handling. Human escalation uses the existing input/approval wait and ends the
agent session. Record the reason, source/target profile or step, evidence and cost
with the existing history. Status shows attention and the next permitted action;
independent eligible work continues. Any external notification uses a configured
reporting capability, not an implicit messaging action.

## Agreed applying changes

Configuration edits normally affect future runs. An operation can explicitly
target a named run, future defaults or both,
with its effect made clear. Existing authorization is sufficient where it covers
the requested change; editing alone does not create another approval requirement.

| Change | Effect on an existing run |
| --- | --- |
| Display information or progress notes | Update directly, retaining approval and progress. |
| Retry or run allowance | Apply at the next scheduling decision, retaining usage and attempt counters. Current attempts retain their declared limits unless explicitly stopped. |
| Profile, skills or assignment inputs | Apply to specified future attempts. Changing work already executing requires an explicit stop/replacement of the affected attempt. |
| Process structure | Initially, edits affect new runs; an existing run retains its process structure. Arbitrary in-flight process migration is outside the first core. |

Raising a retry allowance creates no new attempt by itself; normal eligibility
and budget rules still apply. Lowering an allowance below recorded consumption
blocks additional work rather than rewriting history. Retain earlier outputs and
evidence, and reuse them where applicable; an obsolete result cannot satisfy a
changed assignment merely because it was previously accepted.

Permission revocation is an enforcement operation: restrict or stop affected work
as the executor supports, and reconcile uncertain execution. It must not be deferred
as an ordinary future profile edit or reported as stopped without evidence.

Record the target, before/after values, initiating authority and effective point
of a change. Report what changed and what can happen next using normal operations;
no whole-task checksum/reapproval cascade. Replacement runs may explicitly take
retained artifacts as input; automatic migration and evidence reuse rules remain
open.

## Agreed durable state and restart

Each instance has one authoritative local SQLite state store and one coordinator
responsible for dispatch. External executions can
run concurrently. Persist the current operational state and enough history to
explain important changes:

- Runs and their selected process/configuration.
- Step executions and attempts, including ownership, outcomes and available usage.
- Wait conditions, resource claims, approvals and authorized changes.
- Stored results and references to artifacts/logs; large files can live outside
  the state store.

Use short transactions for state changes. Record the attempt before external
dispatch, and persist its result before releasing dependent work. Running a
harness or awaiting user input must not keep a storage transaction open.

After restart, read the saved current state and reconcile unfinished executions
with their executors. Resume eligible work, retain completed work and preserve
waits. If launch or completion happened without a recorded acknowledgement, retain
uncertainty until reconciliation establishes the outcome. History is explanatory;
normal restart does not require replaying every past event.

Check an artifact when the relevant operation needs it; a missing artifact affects
its consumers, with a specific recovery reason. Avoid a global checksum sweep as
a prerequisite to resuming unrelated work. SQLite fits this local application-state
role ([SQLite's application-state guidance](https://www.sqlite.org/appfileformat.html)).
Schema, backup and corruption-recovery details remain open.

## Agreed resource lifecycle and cleanup

Runner must manage the disk footprint of sessions, files, worktrees, branches and
temporary material it creates. Durable results/history and disposable execution
material have different lifetimes.

Record ownership and retention alongside the run/attempt when creating managed
resources, preferably in designated working/temp
locations. Cleanup uses those records and the owning integration's supported
operations, rather than scanning arbitrary project or harness directories.

- Release temporary/intermediate files once their consumers no longer need them.
- Expire raw sessions/logs after required results, usage and diagnostic evidence
  are retained, subject to configured retention and recovery needs.
- Remove disposable worktrees/build directories after preserving needed changes
  and establishing that no execution or consumer still uses them. Retain branches
  containing needed undelivered work; remove owned branches after integration or
  another explicitly authorized disposition.
- Preserve promised final outputs and compact run history, including archived
  runs. Shared installations/caches and user-owned files need their own policy.

Automatic cleanup operates within an authorized retention policy. Show retained
and reclaimable resources, why something is retained, and cleanup results through
normal control/status operations. Failed cleanup is a visible maintenance issue;
it does not retroactively fail completed work. Retry limits remain bounded. If
space is insufficient, stop admitting affected work and explain the remedy.
Recovery requirements take precedence over age-based expiry.

Keep this within normal Runner maintenance, with short resumable cleanup actions
and no per-step whole-disk scans or extra checksum/locking protocol.

### Proposed storage bounds

Storage must be bounded from the first build, not deferred until real agents are
installed. A per-instance managed-storage allowance covers state/history, results,
executor spools, logs, temporary workspaces and Runner-owned backup/release copies.
Track those categories so an apparent cleanup does not merely move bytes elsewhere.
Include SQLite auxiliary files in accounting. Shared model installations/caches
and user-owned project files retain their own policy; report the scope clearly and
apply free-space admission checks as well.

Proposed starting shape for instance configuration (values are illustrative):

```yaml
storage:
  max_managed: 10GiB
  min_free: 5GiB
  database:
    max_size: 256MiB
    max_record: 16KiB
  logs:
    max_per_attempt: 2MiB
    keep_for: 48h
  sessions:
    keep_raw: false
```

The database allowance is a separate bound within the managed-storage total, not
an additional allowance. Include its main file and SQLite auxiliary files when
qualifying physical enforcement; backups remain counted under managed storage.
`max_record` illustrates an enforced serialized record-payload limit, not a claim
that SQLite's row/index/page overhead has the same size. These limits are proposed
values, not applied settings.

- SQLite holds compact current state, bounded transition history and small evidence
  receipts: the check/command, tested candidate/source reference, outcome, timing
  and a short relevant note. Do not put raw test output, conversations, patches,
  coverage reports, screenshots or encoded attachments into evidence/JSON/BLOB
  columns. Core-required small values still have explicit size limits.
- Enforce record/payload and collection-size limits before persistence. Record one
  relevant check receipt rather than every test case or repeated copy of stdout.
  Keep original inputs/resolved material once at their required scope with compact
  references from attempts. Bound retained record counts/history as well as bytes;
  small rows accumulated forever are not a bounded design.
- Large evidence is retained only for a declared review, recovery or final-output
  purpose, using admitted artifact storage with size, ownership and retention.
  Do not automatically spill rejected oversized evidence into files; moving an
  unlimited evidence dump outside SQLite would not solve the storage problem.
- Terminal/archived history follows its authorized retention/capacity policy.
  Preserve required/pinned work, reclaim eligible history, and stop new admission
  if no safe space can be made. Reject oversized evidence with an actionable reason;
  never quietly drop required proof and report success.
- Bound control payloads, persisted input/result metadata and log writes. Preserve
  a useful capped log tail plus truncation counts; never silently truncate a
  required result and call it successful.
- Do not retain full model conversations/session copies by default. Retain the
  original task, resolved selections, required outputs/evidence, usage and a short
  explanation. Store shared material once at its needed scope rather than copying
  entire configurations, repositories or tool catalogs into every attempt.
- Record current observations in place; append meaningful transitions, not another
  history row/full transcript for every unchanged poll or streamed progress tick.
- Give active scratch/output data an admitted bounded allowance. Reclaim owned
  intermediates after their last consumer and executor recovery no longer need
  them. Secure selected outputs without keeping complete execution workspaces.
- Completed/archived runs, final outputs, diagnostics and backups have configured
  retention/capacity rules. Promised retention, explicit pins, consumers and
  recovery prevent premature deletion, but retained bytes still count toward
  capacity. Cleanup only eligible data under the authorized policy.

Reserve room for final result/state writes and recovery before admitting execution.
At pressure, reclaim eligible disposable data; if insufficient, stop affected new
dispatch with sizes, retention reasons and available actions. Do not silently
expand limits or delete needed results. Required-output overflow requests supported
stopping/recovery of the affected attempt and reports the storage reason.

Enforce limits at controlled write boundaries or through a supported quota; a
periodic directory scan/free-space check alone is not a hard storage guarantee.
Adapters must account for their native sessions/logs and declare enforcement limits.
If the configured hard bound cannot be supported, readiness must expose that
before dispatch. Exact values, retained-history policy and platform/adapter
enforcement remain qualification decisions. No live cleanup is authorized here.

Database qualification must cover actual on-disk behavior, not only row counts.
A main-file page limit is not a complete auxiliary-file budget; WAL growth and
checkpointing need bounded operational handling
([SQLite WAL guidance](https://www.sqlite.org/wal.html)). Deleting rows normally
makes pages reusable without necessarily shrinking the file; any compaction must
fit maintenance space/time policy, not run after every task
([SQLite VACUUM guidance](https://www.sqlite.org/lang_vacuum.html)). Do not report
physical bytes reclaimed when only logical rows were deleted.

## Goals and reference cases

Runner is a local, configurable process engine for broader work, with software
development as its first use case. A user should be able to submit work, inspect
and steer execution, understand waiting and recovery, assess the result and see
resource use across projects and machines. External boards and code hosts are
optional capabilities.

| Reference case | Illustrative process |
| --- | --- |
| Software development | Requested change, implementation, verification, review and delivery; planning when needed. |
| Puzzle videos | Puzzle input, asset preparation, rendering, result checking and finished video. |
| Content creation | Brief, research, planning, text/media creation, assembly and finished content. |
| New Jack's Break puzzle | Requirements, candidate generation, game-specific validation and a puzzle ready for use. |

These examples test the design's boundaries; they do not mandate specific roles,
tools, approval stages or publication. A project can be a publication or puzzle
channel, with no Git repository. A validated puzzle can explicitly become input
to a later video run.

## Agreed boundaries

- One Runner instance coordinates independently progressing project work and
  provides a unified overview. Multiple machines can have identifiable instances
  queried and controlled through MCP. Global status does not become worker context.
- Each project owns its process configuration. Editing one project's process
  must not change another's. Global limits, skills, tools and knowledge can be
  shared through explicit selection. Global capacity can cause an explained wait;
  unrelated project waits must not become a global barrier.
- Runner owns local execution state, authorization, attempts, recovery, evidence
  and accounting. Domain capabilities own puzzle rules, Git operations, rendering,
  research and other specialized behavior. Ordinary coordination is deterministic.
- CLI and MCP expose the same underlying operations. Configuration changes are
  validated, explain their effects and remain observable. Detailed update rules
  for active work are still open.
- Intake, reporting, actions/delivery and control are separate capabilities.
  Slack, Trello or GitHub may supply several. External updates enter through
  explicit Runner operations; local state remains authoritative. Optional reporting
  outages do not stop unrelated execution.
- Roles define responsibility and expected results; skills provide guidance;
  tools provide executable capabilities. Their actual content and tuning are deferred.
  Domain policies, including context selection and required review inputs, belong
  in process/role/project/profile configuration. The core supplies and enforces
  generic mechanisms; it does not hardcode software roles or their context lists.
- Orchestrator is the conversational team lead, using maintained records and
  live observations to route and explain work. A team is a useful composition of
  roles, processes, context and capabilities; a separate team entity is not decided.
  Runner is independently useful and a candidate execution foundation for Cortexium.

## Agreed working concepts

| Concept | Meaning |
| --- | --- |
| Project | An optional organizational/context scope owning its process configuration. |
| Process definition | A versioned recipe of steps, relationships and completion conditions. Project processes are independently configured; standalone work supplies an explicit process. |
| Run | One execution of one process version, with particular inputs and at most one project context. |
| Step definition | A bounded purpose, execution capability reference, input/result contracts and policy references within a process. |
| Step execution | A particular execution of a step definition within a run. |
| Attempt | One actual try at that execution, retaining its inputs, ownership, result and available usage evidence. |

A run retains identity/purpose, resolved inputs and configuration references,
execution progress and waits, results/artifacts and resource accounting. Overall
status follows recorded facts. Retain the process revision or saved definition
used by the run; editing the reusable definition does not silently rewrite work
underway or invalidate it. Changes follow the agreed configuration-reload and
active-run update rules above.
A retry stays within the run and preserves prior
attempts and other completed work. Repetition after a normal domain outcome is
different from recovery after an execution problem; their allowances and histories
remain separate.

A step receives its resolved assignment, bound inputs/artifact references,
selected context/configuration versions, permitted resources/actions and relevant
recovery information. It returns an execution result, domain outputs/artifacts,
evidence/diagnostics and unresolved input needs. Progress can be reported while
active. Runner/executors capture actual state, timing and reported usage rather
than requiring an agent to estimate them. Unreported usage or cost stays unknown.

The process controls connections and next actions. A worker's response does not
itself authorize new work. An agent role, ordinary program or human interaction
can carry out a step. A step can contain several calls serving one coherent
purpose; useful boundaries include a change in ownership, inputs, execution
method, approval or recovery. Roles need not be permanent conversations.

For example, puzzle rejection is a normal validation result. A crashed validator
is an execution problem. Rejection does not automatically authorize generator
repairs. Producing a draft, accepting it and publishing it are distinct outcomes.

## Agreed step outcomes and transitions

A completed execution and an acceptable domain result are separate facts. A
validator can complete normally and reject a puzzle. The process defines the
allowed routes after normal results; Runner follows those routes. Worker output
does not invent subsequent work.

| Situation | Runner behavior |
| --- | --- |
| Completed with a valid result | Retain outputs/evidence and follow the process's declared route for that result. |
| Required input or prerequisite is missing | Retain progress, show what is needed and wait for the stated condition; avoid repeatedly invoking the same unresolved work. |
| Execution failed with a known outcome | Retain the failed attempt and apply the configured recovery policy. An allowed retry creates another attempt of the same step execution. |
| Execution outcome is uncertain | Preserve ownership/evidence and reconcile what happened before repeating work that could duplicate effects. |
| A configured repetition or retry limit is reached | Stop that loop and expose the reason; do not manufacture success, extra allowance or new work. The process's terminal handling remains to be agreed. |

In the puzzle example, acceptance can lead to packaging; rejection can lead to a
new generation execution within a candidate limit. A validator crash can instead
lead to a validation retry within a separate recovery allowance. Candidate
repetition and execution recovery have different purposes and counters.

Run completion requires the process's declared successful finish conditions;
an executor's success message alone does not complete the run. Detailed routing,
cancellation and recovery rules remain open.

## Agreed waiting and resuming

A wait is a persisted condition that must be satisfied before affected work can
continue. Runner retains the affected step, the reason, what can unblock it and
the permitted continuation. Reasons include missing input, required approval,
unavailable capacity and an execution outcome needing reconciliation.

For a configured puzzle approval, Runner saves the candidate and the pending
decision. The agent session ends; waiting requires no ongoing model calls.
Approval through an authorized interface is recorded and makes the configured
continuation eligible to execute. Detailed mechanisms for yielding and resuming
an interrupted step remain open.

Runner restarts preserve waits and completed work. A lost machine connection
requires reconciliation before repeating an action that might already have taken
effect. Unrelated steps and projects can continue where their dependencies and
resource limits permit. The overview should expose a concrete reason such as
"Waiting for Dan to approve puzzle candidate 3", with the candidate and available
actions attached.

## Agreed step results and artifacts

Each step definition declares its normal outcomes and the named outputs required
for each. An execution that completes
normally returns one of those outcomes and the corresponding outputs. Outputs can
be small values or references to artifacts; explanations and evidence are required
only where the step contract needs them.

An artifact reference identifies the retained output and its producing execution/
attempt, with a usable location. Reuse normal record identifiers and native source
references where useful. Required retained outputs must survive cleanup of the
producing session or workspace. Large files can remain outside SQLite.

The process explicitly binds named outputs to later inputs. Pass the selected
values/references and relevant context; session history and entire workspaces are
not implicit handoff inputs. For example, generation supplies a candidate reference
to validation; validation returns acceptance or rejection with required reasons;
delivery receives the particular accepted candidate. Repetition must not turn that
reference into an ambiguous "latest candidate".

Runner checks the declared result structure before routing it. An unknown outcome
or missing required output is an unusable handoff, handled through execution
recovery rather than interpreted as domain rejection or success. Check artifact
availability where handed off or consumed. Domain validity belongs to the configured
capability or validation/review step; checksum checks need a specific identity
requirement under the proportional-safeguard rule.

The consuming executor needs authorized access to the selected input. Cross-machine
use requires an explicit supported access/transfer mechanism; a local path alone
does not provide it. Required consumers and retention policy determine when managed
artifacts become eligible for cleanup. Transfer details remain open.

Execution failures, uncertain outcomes and requests for missing input use the
separate recovery/wait mechanisms. They do not require invented domain outcomes.
Runner/executors retain observed timing, usage and attempt state separately from
the worker's domain result. The result format and validation mechanism remain open.

### Proposed bounded Value representation

Use one small internal value shape for named inputs/outputs:

```go
type Value struct {
	Inline     json.RawMessage
	ArtifactID string
}
```

Exactly one field is present: bounded inline JSON, or the ID of a registered
artifact. `json.RawMessage` is the standard Go JSON value representation here,
not permission for an arbitrary-sized blob. Role/capability contracts declare
which output kind/type is valid, with payload and collection limits applied at
the boundary. Oversized inline evidence is rejected rather than automatically
converted into a stored artifact. Binary/file outputs use the artifact route.

The artifact's bounded registry record stores its owner/producer, location, byte
size and retention information once. Consumers carry the ID instead of duplicating
the file or its metadata into every attempt. Use ordinary identifiers rather than
a task-approval checksum. Initial run inputs can be artifacts owned by the run
without inventing a producing attempt. Resolve access only for explicitly selected
inputs under existing project/caller authority; an ID is not an access grant.

For the first slice, use managed local files. The adapter's output collection
secures a declared output into retained storage, registers it and supplies its ID;
the core then commits the result and may Release eligible executor buffers.
Do not publish an arbitrary worker-supplied path as a retained artifact without
the owning collection/access/size checks. Prefer moving eligible files or reusing
retained references over copying whole workspaces. Already registered output IDs
are reused on repeated observations, not imported again on each poll.

Registered outputs are stable candidates: replacing content produces another
artifact identity. Retain files while a required consumer, promised retention or
recovery needs them, then use ordinary cleanup policy. Track expiry/availability
so a retained historical reference does not imply the bytes are still available.
Native source references and cross-machine transfer remain capability-specific;
this does not introduce a general storage/plugin protocol.

This representation is internal. Human-authored process bindings keep their named
input/output form; users do not have to hand-create artifact IDs or JSON wrappers.
The first slice should prove one bounded inline handoff and one retained-file
handoff, including source-workspace cleanup without loss of the required result.
These fields remain a proposal, not implemented Go code.

## Agreed overall run status

Derive the run's status from recorded step states and the configured process.
Show a reason, relevant step details and the next available action alongside the
status.

| Status | Meaning |
| --- | --- |
| Queued | No execution is active; eligible work is awaiting dispatch or capacity. |
| Running | At least one execution is active. Another branch can still need input or attention. |
| Waiting | No execution is active or eligible to start until a recorded condition changes. |
| Completed | The declared successful finish conditions are met and execution is settled. |
| Failed | The process has ended without success and execution is settled; no configured continuation remains available. |
| Cancelled | A cancellation request has been carried out and execution is settled. |

Queued is the ordinary dispatch/capacity wait; Waiting covers conditions such as
input, approval, a scheduled retry or reconciliation. A failed attempt followed by
an allowed retry does not by itself make the run Failed. An unresolved execution
outcome prevents a terminal status; a stop request alone does not confirm that
execution stopped. Stopping and reconciliation details stay visible.

Derive an attention indicator separately from lifecycle status so a required user
decision remains visible while another branch runs. Detailed stop/cancellation
semantics and how to establish settled execution remain to be agreed.

## Agreed archiving

Archiving removes a run from normal work lists while preserving its stored
results, history and resource accounting. The
run retains its execution status, for example "Archived / Failed". Archived runs
remain accessible through an explicit archive view or query, and their usage
continues to count in historical totals.

Initially, allow archiving Completed, Failed or Cancelled runs. Work still underway
must finish or be cancelled and settled before archiving; an archive action does
not silently cancel it. Unarchiving restores visibility without restarting work.

## Agreed execution boundary

Runner decides when a step is eligible, records an attempt before dispatch and
supplies its resolved assignment. An
executor adapts that assignment to the chosen execution mechanism, such as an
agent harness or an ordinary command. It provides execution identity, observations,
results, diagnostics and available measured usage. It carries out supported stop
and reconciliation operations without choosing process routes or extra work.

Runner owns the process state, routing and recovery decisions. Executor capability
limits must be explicit: loss of contact does not establish failure, and a stop
request does not establish that work stopped. If an executor cannot establish an
outcome, Runner retains the uncertainty and follows the configured reconciliation
path before any potentially duplicating retry. This agreement does not select a
particular Codex interface, executor API or implementation.

## Agreed execution readiness and setup

Runner needs checks and setup handling for harnesses, models, skills, tools and
permissions on the selected machine. Configuration alone does not establish that
an environment can execute it.

Resolve the selected configuration into requirements, compare them with observed
availability and check that the executor can enforce the required boundaries.

| Area | What readiness needs to establish |
| --- | --- |
| Harness | A compatible installation and supported execution features. |
| Model | The requested model/settings are supported and access is available, as far as can be checked before execution. |
| Skills/context | Selected sources and versions are available and can be supplied in the intended scope. |
| Tools | Required implementations, bindings and dependencies are usable. |
| Permissions | Required actions fit the allowed scope and the executor can enforce it. |

The core coordinates these checks; integrations own mechanism-specific detection
and setup. Keep readiness separate from temporary capacity. Report missing,
incompatible, denied or unverified requirements with actionable reasons, while
allowing independent work to proceed. Runtime-only checks and later failures
still need normal execution/recovery handling; readiness is an observation.

Setup is an explicit operation with visible changes and verification. It may run
automatically within a previously authorized setup policy; otherwise report the
specific action or authorization needed. Use supported installers, retain resolved
versions and avoid silent model substitutions, permission expansion or changes to
other projects' environments. Credentials stay outside recorded configuration
and diagnostics.

Reuse check results while their relevant installation/configuration inputs remain
valid, with lightweight dispatch validation. Refresh on relevant changes, expiry,
failures or explicit request rather than repeating expensive probes for every
step or status read. Exact check contracts, freshness rules and supported setup
operations remain open.

## Agreed execution configuration

Keep the role's purpose and result contract stable while allowing its execution
profile to select a coherent combination of
model, harness, guidance, context and tools. Their interaction can determine
quality; skills are not assumed to work equally well with every model or harness.
Use explicit references to reusable definitions and retain the resolved settings
and content used. Existing version identifiers can supply provenance where useful.

| Configuration | Responsibility |
| --- | --- |
| Role | Purpose, input requirements, constraints, required capabilities and result contract. |
| Execution profile | Selected executor/settings; for agent execution, the harness, model/reasoning settings, instructions/skill versions, examples, context policy and tool selection. |
| Project configuration | Selected role/profile configuration, project context, tool bindings and authorized actions. |

Project configuration selects a compatible profile for a role. Profiles may be
role-specific: a local-model profile may use a smaller context allowance and more
explicit guidance/examples than a larger-model profile. This reflects Dan's
reported experience, not a verified claim about a particular model. Shared guidance
can be referenced by several profiles without copying it. Ordinary command steps
can use a profile without acquiring an agent role or model. Prefer explicit named
selections over layered overrides; shared edits must not silently rewrite another
project's selected configuration.

Required capabilities must fit within the project's authorization and applicable
global/machine policy, and the executor must support enforcement. A profile cannot
grant itself wider access or change the role's scope or acceptance requirements.
An unmet requirement is reported by readiness checking rather than silently removed
from the assignment.

Resolve and record the effective assignment before dispatch. Include Runner-supplied
context and observable harness additions in context accounting; expose measurement
limits. Installation/readiness evidence is distinct from evidence that a profile
performs a role well.

Evaluate selected complete profiles on representative tasks to decide what to run.
For experiments investigating a specific factor, deliberately hold other factors
fixed and state the narrower question being tested. Retain versioned evidence,
observed costs and its limits. Concrete syntax, profile qualification policy and
update rules for active work remain open.

## Agreed resource control

Resource policy is explicit and enforced by Runner and its executors, with these
separate scopes where configured:

| Scope | Purpose |
| --- | --- |
| Shared capacity | Bound Runner's concurrent work on the resources it coordinates, such as a machine or provider. |
| Run allowance | Account for the whole run, including all branches, retries, repetitions and participating agent calls. |
| Step/attempt limits | Bound individual work, with separate configured repetition and recovery allowances. |

Before dispatch, check readiness, capacity and available run allowance. Account
for allowances committed to concurrent attempts. An explicit reserve can protect
required verification/finalization from new work consuming its allowance. Apply
that reserve when admitting work; running attempts follow their own declared
limits rather than speculative early cutoffs.

If allowance is insufficient, retain progress and expose an explained wait with
the available actions. Increasing the budget or changing the process requires an
authorized operation. Exhaustion cannot waive completion conditions or produce a
success claim; terminal handling follows the configured process.

Distinguish enforceable limits from advisory thresholds. A required hard limit
must be supported by the executor, as checked during readiness. Usage reported
only after execution cannot support a precise live token/spend cutoff by itself.
Retain unknown or pending usage explicitly. Record elapsed time, active execution
time and available usage/cost separately, including failed work and coordination
calls without double counting shared totals.

Concrete budget units, reservation rules, fairness, timeouts/deadlines and limits
across independent instances remain open. This agreement does not imply a shared
distributed queue or silently increase authority when a limit is reached.

## Agreed dependencies and safe parallelism

Work may start when the configured process permits it, required inputs/dependencies
are satisfied, readiness checks pass and
its required resources and run allowance are available. Parallel work requires an
explicitly permitted independent path and compatible resource requirements.

The process's authorized planning/approval path establishes dependencies and
resource requirements before dispatch. For software work, a configured planning
role can propose which tasks are safe to run together. Runner then enforces the
recorded decision deterministically; domain capabilities remain responsible for
accurately declaring conflicts and providing the required isolation.

A waiting step blocks work that depends on it. Other permitted work can continue,
subject to shared resources and an explicit process-wide stop policy. For example,
drafting an article and creating a cover image from an approved brief may proceed
together if the process declares them independent; assembly waits for both.

Exclusive access to a declared shared resource serializes competing work even
when there is no dependency between it. An uncertain execution retains potentially
held resource claims until reconciliation establishes they can be released. Exact
dependency/join syntax, generated-plan admission and fairness rules remain open.

## Context and tools

Select sufficient context explicitly. Keep necessary shared material stable and
reusable, followed by role-specific material and changing assignment inputs/results
where the harness permits. Preserve consistent tool definitions and ordering within
compatible profiles. Tool popularity alone does not determine cache reuse.
Irrelevant context is not justified by cache hits. Measure actual cache behavior,
latency and cost; the core should not depend on a particular provider's cache.

Steps can select shared context beyond upstream outputs, including original run
inputs where relevant. The configuration owns those selections.

**Agreed context sharing.** Reuse named input/artifact bindings to select context
from existing sources:

| Source | Purpose |
| --- | --- |
| Selected company/project knowledge | Applicable design, brand, coding conventions and architecture guidance. |
| Run inputs and recorded decisions | Original request, agreed scope/acceptance requirements, constraints and authorized changes. |
| Relevant step outputs | Findings, rationale, artifacts and verification evidence, including known limits. |

Use the run's saved inputs and decisions as the shared source of task intent.
Keep the original request accessible alongside authorized amendments. Agent
findings and summaries do not gain authority to redefine it. Summaries can aid
navigation while retaining access to their sources; do not require a separately
maintained narrative or a long handoff document for every task.

Process/role configuration selects sources, required/optional inputs and which
results are shared with later steps. Project configuration supplies concrete
knowledge sources and access bindings. The execution profile selects presentation,
ordering, context allowance and supported delivery/read mechanisms, while satisfying
the declared inputs. Stable-first assembly is the default cache-oriented guidance;
the actual layout depends on the configured profile and harness.

For a software workflow, a useful configured final-review default is the original
task description, applicable agreed changes/criteria, candidate and relevant
verification evidence. Other processes can select different context or omit that
role entirely. Required material can be supplied directly or through an explicit
required read; optional bulky evidence can remain available on demand. Runner
enforces the configured input contract and reports insufficient context allowance
rather than silently dropping its requirements. There is no built-in reviewer
checklist in the generic core.

Steps may publish useful findings as concise artifacts for selected consumers.
Updating durable project/company knowledge follows a configured authorized
operation, which may be automated within its granted scope; no mandatory human
promotion step is implied. Findings do not automatically become global guidance.
Fresh sessions share selected
sources, with project/access boundaries preserved. Record the context actually
supplied and available source references, reusing stored shared material where
practical. Detailed assembly/read mechanisms remain open.

Separate reusable tool definitions, context-specific bindings and role access.
A globally available Code Atlas implementation can have separate repository
bindings for different projects. An attempt records its resolved tool set and
versions; execution enforces access within its supported isolation boundary.
A tool may be called by an agent or directly by a deterministic step. Plugin
packaging, installation and public extension APIs remain undecided.

Code Atlas is a candidate software-development capability and an example of
bounded structural evidence with exact source references and explicit limits.
Its hints do not establish runtime coverage or correctness. Its prototype is
not a dependency of the generic core.

## Agreed core control operations

Define a small common set of operations for MCP and CLI. They invoke the same
core behavior and return recorded state, rather
than keeping separate workflow logic in each interface.

| Operation | Purpose |
| --- | --- |
| Start | Create a run with a selected process, inputs and optional project context. |
| Inspect | Read status, reasons, relevant results, available usage and available actions. |
| Supply input/decision | Satisfy a named wait within the caller's authority. |
| Retry | Request another attempt of eligible failed execution, preserving its history and checking inputs, allowance and ownership. |
| Cancel | Record cancellation, prevent new dispatch for that run and request supported stopping of active execution. |
| Change configuration | Apply an authorized change to explicit future defaults/run targets under the agreed update rules. |
| Archive/restore visibility | Change visibility for eligible runs without changing their execution outcome. |

Persist an accepted change before acknowledging it. Long work continues outside
the control call; acknowledgement means the command was recorded, while actual
completion comes from run/attempt state. Return the target identity, current
state and relevant reason/next action. Cancellation is confirmed only after
execution settles. An uncertain previous execution must be reconciled before a
potentially duplicating retry.

Standalone role invocation can use Start with a one-step process and optional
project context, preserving normal permissions, accounting, results and cleanup.
For example, requesting a plan need not save it into a product repository; that
requires the relevant configured action.

Readiness/setup and cleanup operations use the same core boundary under their
agreed policies. Concrete API/tool names, schemas and transport/authentication
details remain open. Machine/project routing by the
Orchestrator stays outside the per-instance control operations.

## Agreed cancellation and manual recovery

Recovery policy belongs to the configured process and executor/capability. Keep
intervention local to the affected work:

| Situation | Behavior |
| --- | --- |
| Execution has failed and stopped | Retry the affected execution within policy when repetition is safe, or use its supported continuation/recovery action. Keep earlier attempts, inputs and unaffected completed work. |
| An allowance is exhausted | Wait or fail as configured. An authorized manual request may include a scoped allowance adjustment and another attempt, retaining consumed usage and counters. |
| Contact or outcome is uncertain | Reconcile the existing execution first. Show the specific executor/operator recovery actions available; preserve potentially held resources until resolved. |
| Cancellation is requested | Save the request, prevent further dispatch in that run and request supported stopping. Confirm Cancelled only when execution settles. Preserve completed results and known effects. |

A failed exit can have partial external effects; failure alone does not establish
that repeating an action is safe. Apply relevant recovery through the capability
that owns those effects. Ordinary repeatable work can follow its configured retry
policy without an additional approval or general integrity-check procedure.

An explicit Retry can reopen a Failed run at the eligible failed execution, after
checking the requested authority, allowance and previous execution outcome. Save
that intervention and derive Queued/Waiting/Running from the resulting facts;
do not reset history or grant a new automatic retry loop implicitly. For the first
core, Completed and Cancelled runs remain historical; explicitly requested further
work starts a new run with selected retained inputs.

Normal domain rejection follows the process's declared route and repetition
allowance. It is distinct from retrying an execution failure. Cancellation does
not undo effects already performed. Executor stop deadlines/escalation, supported
manual reconciliation procedures and concrete failure/retry policies remain open.

## Agreed process recipe

A process definition is a small declarative recipe, with these responsibilities:

| Part | Meaning |
| --- | --- |
| Inputs | Named starting values and context/artifact bindings. |
| Steps | Named capability references, input/output contracts and applicable execution/context/resource policy references. |
| Connections | Entry points, outcome routes, dependencies and explicitly permitted parallel paths/joins. |
| Repetition | An explicit bounded scope, its carried inputs/results and what happens when its allowance is exhausted. |
| Finish | Declared successful/unsuccessful finish routes and required final outputs. |

Start with routing by named outcomes. Capabilities own domain decisions and return
the outcomes; the core matches the declared route. Execution recovery policies
remain separate from repetition after normal outcomes. Recipe loading checks its
declared references and bindings with specific errors, without blanket approval
hashes or a general-purpose expression language.

For a puzzle example, Generate supplies a candidate to Validate. Acceptance leads
to successful finish with that candidate as the retained final output. Rejection
starts a new generation/validation iteration within the configured candidate
allowance; exhaustion waits or fails as configured. A validator crash instead
uses its execution recovery policy. Finishing need not create an extra agent step
when the required output already exists.

Bind each result to its particular execution/iteration. Branch joins must identify
which selected paths are required, so unselected alternatives do not cause a wait.
Concrete syntax and repeat/join representation remain open. Before implementation,
exercise the recipe against a multi-task software journey as well as this small
puzzle example.

### Agreed task expansion

A process may declare a group of steps to run for each item in a collection
supplied by planning or another capability. Items
have stable identities, their task-specific inputs and declared dependencies.
Collection acceptance follows the configured policy; a human approval step is
optional. Admission checks required inputs and dependency references, including
impossible dependency cycles, with direct errors.

Planning is a configurable role/step; supplied work collections can use the same
expansion mechanism. Ship a default software planner, with actual prompts/skills
developed in the later domain phase. By default, one planning step considers task
boundaries, dependencies, permitted profile selection and the task-specific detail
needed downstream. A separate task-preparation role remains an optional variation
to evaluate if it earns its cost.

The planner receives concise descriptions of the allowed downstream roles/profiles:
required inputs, context limits, available tools/permission boundaries, guidance/
example requirements and scoped capability evidence or known limitations. Use
selected configuration and retained experiment evidence; avoid loading the full
global skills/tool catalog or inferring competence from a model name alone.
Readiness observations establish availability, not task suitability.

The default planner shapes assignments for the selected profiles. For a profile
that needs narrow context and explicit guidance, provide the relevant task detail,
source references and examples within that profile's input requirements. Keeping
cohesive work on another allowed profile can be preferable to extra decomposition
and coordination. Profile guidance supplies reusable instructions; the planner
supplies the task-specific brief.

An accepted collection creates instances of the configured steps within the same
run. The plan supplies the work and dependencies; the process supplies the allowed
execution structure. Item data cannot grant new permissions or insert arbitrary
roles/process steps. Profile choices must come from options explicitly allowed
by the process/project for the existing roles. Existing scheduling, context,
accounting, recovery and cleanup apply to those instances. A later profile change
requires checking the affected future assignment's declared requirements, with
targeted rebriefing through a configured capability where needed; it does not
invalidate unrelated work.

Each item's completion follows the configured group's finish conditions. Dependent
items receive the relevant predecessor outputs, with specific execution references.
For software work, the workspace capability must make predecessor code results
available; scheduling order alone does not establish the correct source state.
Independent items can proceed under the agreed parallel-safety/resource rules.

Shared final steps wait for the declared required item results and execute at the
collection join, so common verification/delivery need not repeat for every item.
Completed items remain visible when another item waits or fails. The current
`for_each` syntax supports bounded serial dependency order and a shared owned
source workspace. Item amendments and concurrent workspace composition remain
open; the implemented syntax is documented in the operator reference.

## Experiments on a selected task or step

Runner should support comparing configurations for one specific task/role,
including selected finished tasks as reference cases. This is an agreed future
capability direction; concrete implementation remains for later qualification.

Reuse standalone runs: select the task's original inputs and starting state,
choose alternative profiles and create isolated comparison runs linked to the
source task. Record the configuration differences. For code implementation, use
the original starting source state; a completed patch can serve as an evaluation
reference. Do not expose the expected answer through the candidate's context unless
that is intentionally part of the experiment. Use test bindings for external
writes and avoid following the production run's delivery routes.

Keep the requested work and evaluation criteria fixed for the comparison. A
configured evaluator can use existing deterministic checks and focused judgment
where needed. Known acceptable/rejected examples or deliberately introduced,
relevant defects can provide positive/negative controls. A Completed status alone
does not establish correctness, and alternative valid implementations need not
match the reference patch exactly.

Report quality evidence, elapsed/active time and available usage/cost, including
setup, failed attempts, evaluation and model coordination. Retain measurement
limits and relevant environment/cache observations. A historical result is a
useful reference; a current matched cost/time comparison needs a fresh baseline
under comparable conditions.

Retain reproducible inputs for selected experiment cases explicitly, using saved
artifacts and native source references where practical. Normal cleanup remains
applicable to other resources; do not preserve every worktree/session for possible
future benchmarking. The core supplies ordinary runs, profile selection,
isolation hooks, accounting and retained inputs; comparison/evaluation uses these
as a capability. Concrete experiment interfaces, fixtures and reporting remain
for the later qualification phase.

### Observed case: Genny label replacement

Scope confirmed by Dan: change Genny's label to Internal/Internal tool. This
focused read-only audit uses [PR #95](https://github.com/techbase/techbase-website/pull/95),
base `2cbeb71eac3f9c08093dcddcf9e3ecbc4c8bf0cd` and reviewed candidate
`a653189e22d5f9649b5aa1f10b58406ad3c42269`.

The label-specific production patch affects five files:

- Switch the Genny badge lookup in LandingPage.vue and GennyPage.vue from the
  shared development key to a new `shared.status.internalTool` key.
- Add Internal tool / Internt værktøj to en.json / da.json, with catalog revision
  updates and the existing translation-manifest receipt for the new Danish key.

The separate key preserves the development label used by other products. No new
route, page registration or generated routing file was technically required for
the label. The generator metadata changes in the combined PR register other work.

Label assertions changed in three existing test files: LandingPage.test.ts,
localization_test.go and the existing Genny case in site_identity_test.go.
No new test file was added for the label. However, the new landing assertions also
checked unrelated experimental/ShowIP statuses. The retained
[verification record](https://github.com/techbase/techbase-website/blob/a653189e22d5f9649b5aa1f10b58406ad3c42269/docs/evidence/standalone-notes-2026-10-28/verification.json)
reports two Danish failures from misspelling the unchanged experimental label.
This is a concrete repair caused by expanding the test scope beyond the label.

The [QA report](https://github.com/techbase/techbase-website/issues/94#issuecomment-6473500069)
reused implementer evidence rather than rerunning dynamic checks. Its accepted
candidate merged on 28 October 2026 at 16:43:45 Europe/Copenhagen; merge does not
establish deployment. The issue/PR and their check reports combined multiple
requests, so no isolated label-only elapsed time or token total is available.
The previously cited coordinator-turn totals must not be attributed to the label.

Proportionate handling: a bounded edit, the required translation bookkeeping and
focused checks of Genny's two locations/locales and preserved shared status.
Keep applicable repository/release gates, reuse adequate unchanged evidence and
avoid extending unrelated tests. Maintain request-level completion/attribution
even if a release batches changes. No live source, service or other chat was changed.

### Candidate experiment: typed decision models

Consider Jev, Clef and Clef Flash for narrow judgments with predefined answers,
such as request classification or selection among permitted workflows/profiles.
These are candidate capabilities for later evaluation, not core dependencies or
an authorization to run models. Cloudflare publishes Clef/Clef Flash as open
decision models; Ollama exposes them through its decision API
([Cloudflare announcement](https://blog.cloudflare.com/clef-decision-models/),
[Clef](https://ollama.com/library/clef),
[Clef Flash](https://ollama.com/library/clef-flash)).

Use the existing step/input/result contract. Scheduling, permissions and recovery
remain deterministic core responsibilities. Valid output types do not establish
correct judgments; reported confidence needs evaluation on our task distribution.
Compare a small set of labeled real requests against simple rules and an existing
small general model, including ambiguous requests, fallback calls, model loading,
latency and total cost. No extra decision layer or benchmark framework is implied.

Dan's comparison references:
[model directory](https://decisions-api.dev/models) and
[community guide](https://huggingface.co/blog/a2aprotocol/openai-decisions-api-vs-jev-a-practical-guide).
Use provider documentation to establish actual supported behavior.

## Agreed coordinator loop

One deterministic coordinator per instance turns current stored state into
permitted actions. Its normal cycle is:

1. Record incoming control operations and executor observations through short
   state changes.
2. Determine relevant transitions, satisfied waits and eligible work from the
   selected processes and saved results.
3. Admit eligible actions within their authority, readiness, dependency, resource
   and allowance constraints. Record attempts and resource claims before dispatch.
4. Dispatch external work outside the storage transaction, then continue handling
   other operations and eligible work.
5. Record observations/results and make affected downstream work eligible, repeating
   the cycle when new information or a relevant deadline arrives.

Control operations and meaningful executor/task state changes wake the coordinator
after the change is saved. Resource release can immediately make other work
eligible. Coalesce redundant wakeups while retaining all saved state changes.

Use a configurable fallback reconciliation interval, initially **five minutes**
following Dan's operational experience. Check relevant unfinished work/waits to
recover from missed notifications or observe integrations without push updates.
Such integrations may take up to the configured observation interval to reveal a
change; allow a shorter interval where needed. A fallback check is observation,
not a timeout or permission to retry uncertain execution.

Startup reconciliation runs immediately. Explicit retry/stop/timeout deadlines use
their own timers, so the fallback interval does not delay those actions. Executors
use notifications where supported or scheduled observation of their active work.
Maintenance follows its own configured timing. The core does not need a separate
broker or replay of all history. Exact wakeup/notification mechanisms remain open.

One coordinator can dispatch multiple independent executions; it does not wait
for a harness call to finish before serving other work. Inspect reads recorded
state with observation freshness available. Ordinary routing/status changes require
no model call. Reconcile unfinished external work on restart as already agreed.
Readiness, code scans and tests run where their configured policy requires, not as
blanket checks on every wakeup. Concrete Go boundaries and storage transaction
details are the next implementation-design questions, not selected by this loop.

## Agreed Go components and runtime ownership

Keep one running service per instance/state store. It owns the coordinator and
accepts control operations. CLI and MCP reach
that same service; additional clients do not create additional coordinators.
A separate stdio MCP process, if selected, forwards operations to the live service.
Status and mutation behavior remain in the shared core operations.

Use a small set of Go boundaries, with these proposed package responsibilities:

| Part | Responsibility |
| --- | --- |
| `runner` | Process/run/step types, core operations, input/context resolution and coordination policy. |
| `config` | Read/edit YAML/JSON files, resolve file references and report reload changes/errors. Core rules validate meaning and decide when a valid change takes effect. |
| `sqlite` | Persistence, short transactions and saved-state queries. |
| `execution` | Concrete execution adapters: start, observe, collect results and stop/reconcile where supported. |
| `control` | CLI/MCP request translation and service communication. |
| Program entrypoint | Wire concrete components, load startup settings and manage service lifetime. |

Keep storage/execution contracts next to their consumers in the core. Concrete
adapters implement them; the core does not import SQLite driver, harness or MCP
implementation details. Separate files within cohesive packages as needed rather
than creating a package/interface per noun. Domain roles, skills and processes
remain selected configuration/capabilities.

### Proposed first-build layout

```text
cmd/cortexium-runner/main.go
internal/runner/           core types, operations and coordinator
internal/runner/config/    YAML/JSON loading, edits and reload
internal/runner/sqlite/    operational state and bounded history
internal/runner/execution/ deterministic executor; harness adapters later
internal/runner/control/   MCP service handlers and CLI client
```

Keep the current executable name. The core owns the small contracts it consumes;
the adapters depend on those contracts, and main wires their concrete instances.
Core execution/recovery decisions do not live in CLI/MCP handlers. Configuration
parsing/editing has a concrete responsibility because files are authoritative;
it is not another configuration service or policy layer. Basic artifact/resource
logic can remain cohesive files in the core rather than requiring more packages.

After implementation authorization, use a clean isolated worktree/branch in the
Runner repository. The new runtime is a clean replacement on that branch, with
existing Runner history/deployment preserved as references. Carry forward the
agreed design and update source authority/operator docs for what the slice
actually implements. No installed service, other project or production cutover
is part of the first build. The authorized slice now uses this layout; the actual
implemented boundary is documented in the code walkthrough.

File-based configuration ownership and automatic reload are agreed above. The
closure proposals below cover executor recovery, control transport and service
lifetime. No public plugin framework is needed for these package boundaries.

## Illustrative request and interruption walkthrough

This is a design walkthrough, not an executed qualification or implementation.
Use a software request whose configured planning step supplies tasks A and B,
with B depending on A:

1. `control` translates Start into the core operation. `runner` saves the request,
   selected process and run identity through `sqlite`, then acknowledges creation.
2. The coordinator records a planning attempt and dispatches it through `execution`.
   Its task collection enters the run under the configured acceptance policy.
3. A's configured steps receive their selected profiles/context and execute through
   the same mechanism. B remains dependent on A's required result.
4. If Runner stops during A, startup reads the saved attempt and asks its executor
   about that execution. A confirmed result is recorded; active work is observed;
   known failure follows recovery policy; unresolved outcome stays an explained
   wait. B is released only after its actual dependency is satisfied.
5. B receives the specific predecessor outputs, including the correct code-state
   input through the workspace capability. Configured final steps consume the
   required task results and original task context. Their finish conditions
   determine completion.
6. The run retains its final results/history and releases disposable execution
   resources according to the agreed cleanup policy.

The executor proposal below addresses the dispatch/launch acknowledgement gap.
Actual adapter recovery still needs qualification; the walkthrough does not prove
that a harness survives or reconnects after restart. Workspace composition remains
a separate capability contract to qualify for software work.

## Proposed closure: recovery, service control and upgrades

**Concrete recommendations for Dan's agreement; not yet accepted decisions.**

### Executor recovery

Use Start, Observe and Stop for execution, plus Release for the temporary-data
handoff described below. Correlate them by the Runner attempt ID saved before
dispatch. Preserve a native execution/session handle when
available. Repeated Start for the same attempt must return the existing execution
or uncertainty, rather than start a second execution. A positively confirmed
unstarted attempt can still be launched under normal admission rules. A new
recovery attempt gets a new attempt ID; reconnecting to the existing execution
retains its original ID.

Proposed Go boundary, owned by the core that consumes it:

```go
type Executor interface {
    Start(ctx context.Context, assignment Assignment) (Observation, error)
    Observe(ctx context.Context, attemptID string) (Observation, error)
    Stop(ctx context.Context, attemptID string) error
    Release(ctx context.Context, attemptID string) error
}
```

The working assignment/result shapes are deliberately small; concrete field names
remain provisional:

```go
type Assignment struct {
	AttemptID string
	Setup     ExecutionSetup
	Inputs    map[string]Value
	WorkDir   string
	OutputDir string
	Access    AccessPolicy
	Limits    ExecutionLimits
}

type StepResult struct {
	Outcome string
	Outputs map[string]Value
	Reason  string
}
```

`Setup` is the selected resolved role/capability and profile: executor/model
settings, instruction/skill references, tool selection and ordered context
references. It excludes unrelated profiles, project catalogs and conversations.
`Access` is the effective authorized access; `Limits` is the admitted execution/
storage allowance. Inputs contain only the declared task values and selected prior
results. Working/output locations are supplied when needed with explicit ownership;
an executor must not infer access to another project from its own current directory.

`Value` represents either a bounded JSON value or a registered artifact reference.
Inline values, numbers of bindings/references and total serialized payloads have
limits. Artifact references resolve to authorized managed resources with recorded
size, producer and retention; they are not arbitrary paths the worker asks Runner
to trust. The output collection/handoff must secure them before result commit and
Release as described below. This is a logical contract; concrete Value/artifact
fields and adapter collection mechanisms still need agreement.

Normal results name a declared `Outcome` and its required outputs. `Reason` is
required only where the outcome's contract needs it, such as `needs_help`.
Evidence is an optional named output when required, using the same bounded values/
artifact mechanism. There is no universal unbounded evidence field, mandatory
report or per-test-case ledger. A role's reported outcome does not establish domain
correctness; the configured validation/review capability supplies that judgment.

Proposed observation shapes make actual execution facts explicit:

```go
type Observation struct {
	State        ExecutionState
	NativeHandle string
	End          *ExecutionEnd
	Usage        Usage
	Reason       string
}

type ExecutionEnd struct {
	Kind   EndKind
	Result *StepResult
	Reason string
}
```

`State` uses the four observations below. A confirmed Finished observation has
an `End` describing one of three facts:

- `returned`: a usable StepResult is available; its domain outcome might still
  be rejection or a request for configured escalation.
- `failed`: the execution failed, with a bounded failure reason.
- `stopped`: stopping has been confirmed, not merely requested.

`Result` applies to `returned`; the other kinds do not invent a domain result.
Unknown reports a reason and retains reconciliation rather than fabricating a
terminal result. Run/step status remains a core decision from these facts and the
process. A normally finished attempt returning `needs_help` can leave its step
unfinished for escalation; it does not make the run Completed.

Timing and observed usage come from Runner/executor accounting rather than the
agent's StepResult. Normalize usage as cumulative per-attempt observations and
update the saved totals; do not add the same totals again on each poll. Missing
metrics remain unknown rather than zero. Native measurement support/limits still
need qualification. State/result/reason/usage metadata stays within the same
bounded record contract; this observation is not another transcript or event dump.

These are execution envelopes, not permission to persist expanded prompts or
materialized context repeatedly. Persist bounded selections/references once at
their needed scope and retain only the agreed result/receipt data. Exact Go fields,
serialization and native adapter normalization remain provisional. No Go source
file is created by these sketches.

Start returns promptly after launch/acknowledgement rather than waiting for the
whole task. Observe obtains facts without creating another execution. Stop is a
repeatable request; subsequent observation establishes stopping. A Go `error` or
cancelled control-call context does not establish that execution never started,
failed or stopped; reconcile the saved attempt when the outcome is uncertain.

Observe distinguishes these facts:

| Observation | Meaning |
| --- | --- |
| Not started | The executor can establish that launch never occurred. Absence of a handle/file alone is insufficient. |
| Running | The identified execution is active as of the reported observation. |
| Finished | The execution ended, with a retained result/outcome and available usage. |
| Unknown | Launch, continued activity or outcome cannot be established; provide a reason and supported recovery action. |

Retain terminal results until Runner has secured required outputs outside the
executor's disposable buffers and committed their result/references to SQLite.
Saving a reference into a file about to be deleted is not a completed handoff.
Only then call repeatable Release to reclaim eligible executor-owned session/log/
spool data. Release cannot discard active/uncertain execution or outputs still
owned by a consumer/recovery obligation. A cleanup error remains visible maintenance
work; it does not retroactively fail a saved successful result.

The core never re-dispatches a saved terminal/released attempt and never reuses
its ID. Compact correlation/history follows normal retention; idempotency does not
require retaining an entire executor conversation forever. Duplicate/late
observations apply to their own attempt without restarting downstream work or
overwriting a confirmed terminal outcome with stale progress.

Adapters declare supported lookup, stop, result-retention and restart behavior.
Use native harness facilities or minimal managed execution records where needed;
do not add another general scheduler. Qualify the first supported unattended adapter
against the contract. A weaker adapter exposes its limits through readiness and
recovery policy. If automatic observation cannot resolve an outcome, provide the
owning capability's operator procedure to inspect/stop the execution or supply
evidence of the result. No automatic exactly-once guarantee for external effects
is implied; normal domain recovery and repeatability rules still apply.

### Service and control

Run one foreground Go service per instance, supervised by the platform service
manager when installed. Use standard MCP Streamable HTTP for CLI and MCP clients
at the same endpoint. Bind locally by default; explicitly configured remote access
uses HTTPS and MCP authorization, integrating an existing authorization provider
where available. TLS can terminate in the normal deployment proxy. Enforce request
authorization and the transport's origin protections. Platform installation still
needs qualification; HTTP itself does not impose the previous Unix-only scope.

The earlier socket/SSH proposal reused OS/SSH authentication, at the cost of a
custom transport/relay path. It is superseded by this HTTP(S) proposal following
Dan's feedback. Socket or SSH access can remain optional deployment choices if a
concrete need arises; they are not required core infrastructure. A stdio adapter
is only needed for a client that cannot use the standard HTTP endpoint.

Use the standard transport and authorization facilities rather than another
control protocol or a new Runner identity service
([Streamable HTTP](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http),
[MCP authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)).

Keep one non-inherited, OS-managed ownership guard per instance to prevent duplicate
coordinators. It releases with service exit. This protects actual dispatch ownership,
without task-file locks, expiring leases or manual stale-lock repair as normal
operation.

Initial control is for authorized instance-owner clients. Restricted workers must
not inherit owner credentials/control access. Their execution boundary must enforce
the required separation from those credentials and instance state; role labels
alone do not establish isolation. Any delegated control capability needs explicit
scoped access under the configured policy. Credential/provider installation
details must be settled for the actual deployment before remote exposure.

An acknowledged Start/Retry records the caller's request identity with the created
run/attempt. Repeating that request returns the recorded result; a different action
cannot silently reuse that identity. Use ordinary identifiers and transactions.
Client disconnect or MCP request cancellation does not cancel already saved work;
the explicit Cancel operation controls the run.

Service shutdown stops new dispatch and persists state. Executions with qualified
independent ownership/result retention may continue; others receive Stop within a
configured grace period. Preserve unresolved outcomes for startup reconciliation.
Stopping the service does not cancel its runs. Startup reconciles immediately.
Clients report an unavailable service clearly; the explicit service command or OS
supervisor owns service startup/restart, rather than each client starting a daemon.

### Supported upgrades

Upgradeability is a required core/operator journey. Keep binaries separate from
the persistent instance data, configuration and outputs. Upgrading Runner must
preserve run identities, attempts, approvals and results; it must not trigger task
reapproval, completed-work replay or project test suites merely because the binary
changed. Harness/skill/tool and process-template updates are separate explicit
configuration/setup operations.

Provide one supported upgrade procedure:

1. Stage the target release and establish platform/schema compatibility using
   its release metadata and supported schema range.
2. Stop admitting new executions. By default, wait for active executions that
   cannot survive/reconnect after restart; do not wait for whole runs, pending
   approvals or an empty backlog. Qualified independent executions may continue.
   An explicit interrupted upgrade uses the normal stop/recovery rules.
3. Quiesce the old service's state writes for cutover, take a consistent SQLite
   backup through supported database facilities and retain the previous working
   binary/configuration needed for rollback
   ([SQLite backup API](https://www.sqlite.org/backup.html)). Avoid copying entire
   workspaces or assuming a raw live database-file copy is a consistent backup.
4. Switch to the staged binary through the normal installer/service manager.
   Apply supported ordered schema migrations transactionally; do not replace an
   incompatible database with an empty one.
5. Confirm the new service can open its state and serve read-only control/health
   operations before processing further control mutations, executor results or
   new work. After that validation, resume operational writes, reconcile unfinished
   execution and resume admission. Preserve externally produced results throughout
   the cutover. The endpoint/instance identity remains stable so clients reconnect
   to the same Runner.

Retain a usable pre-upgrade recovery point. Automatic rollback to the prior binary
and, if necessary, its database backup is limited to the boundary before further
operational writes or new work are accepted after the backup. Once new state/results
have been created, restoring an older backup requires explicit data-preserving
recovery; do not silently discard that work.
An old binary is only usable directly when it supports the current stored schema.

Use the existing service manager/simple installer, with visible upgrade state;
no model agent or second workflow scheduler is required. Retain the current and
previous working release plus versions still needed by active execution, then
apply normal cleanup policy. Qualify upgrades with representative pending/running/
finished work and migration/rollback failures before calling them supported.
Exact packaging, provider settings and supported compatibility ranges remain
implementation/release decisions.

## Agreed first Codex adapter: ownership and launch

Agreed 29 October 2026. The owner subsequently authorized implementation and the
bounded native qualification. The input-only text adapter is now implemented;
broader workspace, skills and tool execution remain outside this slice.

Use `codex app-server` from the Codex CLI as the proposed native integration;
qualify its actual recovery behavior before supporting real execution. Its
[documented interface](https://learn.chatgpt.com/docs/app-server#api-overview)
separates creating a thread, starting a turn, reading state and interrupting work.

| Owner | Responsibility |
| --- | --- |
| Runner core | Process position, assignment, permissions, budgets, deadlines, retry policy, final status and retained results. |
| Codex adapter | Harness communication/lifetime, durable attempt-to-thread/turn mapping, result/usage collection and owned resource cleanup. |
| Codex harness | Conversation context, model requests and permitted tool execution within the assignment. |

Use one fresh Codex thread and one requested turn per Runner attempt. A turn may
contain several model requests and tool calls. Reconnection preserves the same
attempt/thread/turn identities; a newly authorized attempt receives fresh ones
and its explicitly selected context.

Launch order:

1. Save the Runner attempt with its assignment, selected settings and deadline.
2. Create an empty Codex thread before requesting task execution.
3. Durably associate the returned thread ID with that attempt.
4. Record that turn execution is being requested, then call `turn/start`.
5. Save the returned turn ID and observe that execution until its result is secured.

A lost acknowledgement between turn dispatch and saving its ID requires inspecting
the recorded thread and reconciling any existing turn. Do not unconditionally send
another turn. Unresolved identity/activity remains Unknown/Waiting with a specific
recovery reason. The adapter's small durable record holds the correlation and
dispatch facts; the core continues to own lifecycle policy.

On normal Runner shutdown, request interruption and preserve whatever settlement
can be confirmed. After a crash/disconnection, reconcile existing work before
replacement. Continued execution while Runner is offline is not qualified for the
first adapter. Native session persistence alone does not establish active-work
survival. Secure required outputs and commit their references/results before
cleaning up the owned session.

The first launch-recovery proof should lose the turn-start acknowledgement,
reconnect and establish that only one task execution occurred. Native enforcement
of the assignment and lifecycle/resource qualification remain the next design/
verification work; no adapter code is added by this agreement.

### Agreed Codex result handoff

Agreed 29 October 2026. Reuse the core's StepResult contract rather than inventing
a Codex-specific process result. Codex returns a declared outcome such as `ready`
or `needs_help`, together with that outcome's required named outputs.

1. The adapter confirms the native turn ended and collects its bounded result and
   available native usage observations.
2. Runner validates the output names/types and secures required retained files.
3. Runner commits the result/references to SQLite, then follows the configured
   outcome and releases eligible temporary resources.

A confirmed native completion with unusable output is an execution failure with
a reason, subject to bounded recovery policy. Lost contact with uncertain
completion remains Unknown/Waiting and requires reconciliation of the existing
execution. A `ready` result establishes that declared outputs are available;
domain correctness belongs to the configured validation/review capability. Native
completion alone does not establish that the whole step or run is successful.

### Agreed resolved Codex assignment

Agreed 29 October 2026. Resolve one assignment before launch:

| Part | Contents |
| --- | --- |
| Task | Requested work and its completion requirements. |
| Inputs | Declared values, relevant original instructions and selected earlier outputs. |
| Execution profile | Exact harness, model/reasoning settings and selected instructions, skills and tools. |
| Access | Working location, readable/writable resources and permitted external capabilities. |
| Limits | Deadline, input/output/storage bounds and supported usage controls. |
| Result contract | Allowed outcomes and required named outputs. |

The adapter translates the assignment into native settings and verifies that
required restrictions can be enforced. Unsupported settings produce an actionable
readiness error rather than a silent fallback. The resolved assignment records
what Runner supplied; adapter qualification establishes the effective native setup.

Assemble relevant stable shared material first, then selected role/project guidance
and task-specific inputs last, where the harness permits. Measure actual context
and cache behavior; ordering alone establishes no savings.

The first fixture task summarizes supplied document text directly, using an
explicitly selected model/profile, no external tools or filesystem write access,
and one declared `summary` output. The adapter captures and retains the result.
This assignment is now implemented for the input-only text fixture. Broader
access, context/skills/tools and other profiles retain their qualification boundary.

### Agreed first Codex adapter qualification

Agreed 29 October 2026. Keep the existing two-step fixture: one Codex step
summarizes a tiny supplied document, then a deterministic step consumes its result.
The next adapter slice has this finish line:

- Successful execution produces the required summary facts, a valid result and
  retained output usable by the dependent step.
- A lost launch acknowledgement reconciles the existing turn without starting
  another execution.
- Runner restart preserves attempt identity, deadlines, consumed allowances and
  completed outputs.
- Cancellation reports settlement only after it is confirmed.
- Effective native settings/restrictions match the selected assignment; unsupported
  requirements are exposed rather than silently relaxed.
- Required results survive cleanup; eligible owned session data is removed, with
  enforced resource limits distinguished from observed usage.

Reuse sufficient existing core coverage. Add focused adapter protocol/fault tests
without model calls rather than duplicating the whole core failure matrix.
The initial native qualification is capped at three short task executions covering
success, lost acknowledgement/reconnect and interruption/restart. Record elapsed
time, reported tokens and owned disk usage; preserve unknown metrics as unknown.
If this allowance is insufficient, report the remaining gap before spending more.
The implementation and three native task executions are complete within the
[recorded qualification boundary](architecture.md#codex-adapter-qualification-29-october-2026).
The initial generation allowance is consumed. Remaining unsupported capabilities
and unqualified failure environments are explicit in that record.

## Agreed first writable workspace lifecycle

Agreed 29 October 2026, following the core/adapter checkpoint `eba1c74`.
This is a design agreement; writable implementation and further native generation
remain separate authorization decisions.

One Runner-owned Git worktree belongs to a run and follows its sequential steps.
An attempt owns its session and scratch data; it does not own the only copy of
the code result. Record the selected Git base and resource ownership once. Git
isolation preserves the user's checkout; it does not itself enforce worker access.
Filesystem/tool permissions are agreed in the next section.

When a writable attempt fails:

- Confirm that the previous execution has settled before admitting another writer.
  Unknown execution keeps its workspace and stays in reconciliation.
- Preserve current edits, including new source files. A failed session or failed
  check does not automatically reset the worktree or repeat completed steps.
- An eligible configured retry/repair uses the preserved workspace and a fresh
  session. Supply the original task/accepted changes, relevant current diff and
  previous failure reason rather than the full previous conversation. Preserve
  consumed allowances. Ordinary configured recovery need not involve a human.
- For the first writable fixture, keep one automatic attempt per step. Restarting
  from the selected code baseline is an explicit recovery choice; preserve the
  discarded changes first and restrict the operation to this owned worktree.

Apply the existing resource lifecycle to successful, failed and cancelled runs:
retain required code changes and check results outside disposable resources before
cleanup. Once execution is settled, preservation is confirmed and no consumer
requires the live worktree, retention/storage policy may remove it. Failed
worktrees need not occupy disk forever. If required preservation fails or exceeds
its allowance, expose the storage problem and constrain admission rather than
silently discarding code. Avoid copying build caches into the retained code result.

Base selection, attempt-start source identity and the retained change format are
agreed below. Native access requires the qualification described below. No new
task-content hash, approval checksum or per-file lock is implied by Git change
provenance.

## Agreed first writable permission boundary

Agreed 29 October 2026. Keep the first writable fixture to a small Go project
using the standard library. Runner owns Git operations and resource management;
the worker receives the code and access needed for its assigned step.

| Resource | Worker access | Owner |
| --- | --- | --- |
| Run worktree source | Read/write for a coding step; read for a checking step. | Run |
| Attempt scratch, Go build cache and temporary test output | Read/write, disposable after required results are secured. | Attempt |
| Selected Go installation | Read/execute. | Host |
| Git administrative data, including the worktree's `.git` entry | No access. Runner supplies the relevant diff and performs Git operations. | Runner |
| Runner database, control credentials/socket and native account/session data | No access. | Runner/native harness |
| Unrelated projects and private user files | No grant. | User |

Use native OS-enforced permission profiles for commands, including the programs
started by `go test`. A prompt instruction or Git worktree is insufficient.
The trusted native harness retains its selected authentication; its task commands
must not receive that authentication or Runner control credentials through files
or environment variables. Use a small explicit command environment, a non-login
shell and attempt-owned `TMPDIR`, `GOTMPDIR` and `GOCACHE`. Keep toolchain selection
local and automatic dependency/toolchain downloads disabled for this fixture.

No task network or local-service/socket access is needed for this fixture. Model
service traffic belongs to the trusted harness and is separate from task network
access. Do not inherit apps, MCP servers, web search, browser access, hooks,
subagents, host skills or project-selected capability/configuration additions.
Supply the selected instructions explicitly. Other projects may later declare
additional capabilities; this fixture does not install or enable them.

Resolve actual owned paths before assigning access. Keep the writable instance,
native home, worktree and scratch outside global system temporary directories;
do not grant all of `/tmp` or the user's home for convenience. Exact exclusions
protect known private locations. Treat code edits as results to inspect, not as
permission to execute changed scripts or configuration outside the sandbox.

**Readiness evidence, not writable-adapter qualification.** On macOS with
`codex-cli 0.164.0`, a temporary standard-library Go program was run through
`codex sandbox` and then `codex app-server`'s `command/exec`, selecting a named
permission profile. Outside the system temporary directory, the native command
path allowed source/scratch writes and blocked Git metadata writes, reads of dummy
native-private data, unrelated files, a denied subtree and a symlink escape.
Loopback TCP connection attempts failed with an OS permission error. No thread,
turn or model generation was started, and no actual credential was read.

The equivalent fixture under `/tmp` failed the file exclusions, including after
canonicalizing it to `/private/tmp`; network denial still worked. The same file
checks passed when the fixture moved outside system temporary directories. This
is a concrete native-runtime limitation observed here, not proof that aliases
alone explain it. Do not rely on nested exclusions inside system temporary
directories to protect Runner data. The owned probe files and services are
disposable; retain this result rather than the test caches or native histories.

The installed protocol exposes named `permissions` on thread/turn creation and
`permissionProfile` on standalone commands. These cannot be mixed with the older
`sandbox`/`sandboxPolicy` fields. The current input-only adapter uses the older
settings and remains unchanged. A writable implementation must resolve and check
its effective native policy without silently widening it. Before enabling real
writable turns, verify every enabled file-edit/read path, command environment,
private-directory boundary and cancellation/recovery behavior through the actual
adapter. The command probes do not qualify built-in editing tools, Linux runtime
enforcement or a production project. Unsupported boundaries remain a visible
readiness failure; they do not trigger an unsandboxed fallback.

The native permission semantics and separate capability controls are described in
the [official Codex permissions documentation](https://learn.chatgpt.com/docs/permissions).
Disk isolation is not a hard quota: retain the existing distinction between
Runner-controlled storage bounds and observed native growth/stop thresholds.
This agreement adds no per-file locks, content acceptance checksums or automatic
approval escalation. Writable implementation and additional native generations
remain unapproved.

## Agreed Git starting point and retained code result

Agreed 29 October 2026. This defines the first local writable slice, not an
implementation or permission to modify another project's checkout.

**Starting point.** The project selects a local repository and default Git ref;
a run may explicitly select another permitted ref or commit. Do not guess a branch
name, fetch, pull or merge as part of preparation. When preparing the run's first
workspace, resolve that selection to one exact commit, record it and create the
detached run worktree from it before admitting a worker. Before preparation,
status shows the requested ref and that the base is not yet selected. Afterward,
branch movement, configuration edits, retries and Runner restarts do not select a
different base. A comparison can request the original exact commit explicitly.

The user's uncommitted edits are not implicit input and need not block this
isolated run. Preserve their checkout and index. Including uncommitted user work
requires an explicit future input capability; the first fixture starts from a
committed baseline. Keep one recorded Runner-owned Git ref protecting the selected
base while retained changes or recovery need it. This is ordinary Git object
retention; it neither locks the user's branch nor creates an acceptance gate.

**Saved result.** Retain a Git binary patch from that base to the current source,
plus small source/provenance metadata in the normal run/attempt records. The patch
is an ordinary bounded file artifact outside SQLite. The record identifies the
repository, exact base, artifact and producing attempt; normal outcome and check
records state whether this is partial work or an accepted result. No complete
repository archive, new task ledger or second evidence format is needed.

Capture tracked changes, staged or unstaged, and new non-ignored source files in
the selected source scope. Include deletions, binary content and executable-bit
changes. A rename can be represented as deletion plus addition without changing
the restored source. Use the project's declared collection/ignore policy; keep
build caches and scratch outside the source result. Required ignored outputs need
an explicit collection declaration. Unsupported file types or an uncollectable
required output produce a visible collection failure, not a claim that all work
was saved. Symlink/submodule source support is outside this first Go fixture.

Runner performs collection with hooks, external diff/text conversion and project
filter execution disabled. Temporary capture indexes/Git objects belong in owned,
accounted scratch; do not leave snapshot commits or loose capture objects in the
shared source repository. The first fixture has no custom Git filters. Native Git
capture/restoration details still require implementation qualification.

**Attempts and cleanup.** The first attempt starts from the recorded base. A later
attempt records the particular saved source result it receives, then uses the
same run workspace. After a writer has settled, secure its changed source before
handoff or workspace cleanup, including partial changes from a failed/cancelled
attempt. Saving code does not mark its task successful or its checks passed.
Unknown execution remains in reconciliation before another writer or cleanup.
Reuse the existing source artifact when a checking step makes no source changes;
do not create repeated snapshots on observation polls.

Secure the bounded patch and persist its reference before allowing worktree
removal. If collection fails or exceeds its allowance, keep the recoverable
worktree and expose the storage/collection problem; do not silently truncate,
reset or repeat development. Account for the worktree, capture scratch and patch
under the existing resource policy. Track the base ref as a retained resource and
release it once no retained patch, consumer or recovery requires it. Do not run
aggressive garbage collection on the user's repository as routine cleanup.

Restoration starts from the recorded base in an owned clean workspace and applies
the saved patch. A missing base or failed application is a visible inability to
restore; it does not select a newer branch or silently omit changes. This first
contract depends on the registered local repository retaining its pinned base.
Transfer to another machine or preservation independent of that repository needs
an explicit base-transfer/archive capability later. Existing retention rules
decide which intermediate results remain needed; do not keep every checkpoint
forever for possible experiments.

**Format evidence.** One disposable local Git fixture restored the exact source
tree from its base plus a 982-byte patch, covering a modified Go file, new file,
deleted file, binary change and executable-bit change. Ignored scratch was absent
from the restored result. This used no model calls; the entire owned fixture was
removed. It establishes the patch format's suitability, not the implementation's
crash, storage or native-worker behavior. No writable runtime code was changed.

## Agreed first writable fixture and qualification

Agreed and subsequently authorized 29 October 2026. The bounded writable slice
and its one-turn native qualification are now implemented. The
[actual code path and qualification](architecture.md#follow-the-writable-go-process)
record the completed evidence and remaining limits. Role/skill catalogs and model
quality comparisons remain later work.

**Task.** Seed one disposable Go module with a package declaration and a fixed
table-driven contract test. Codex adds only `seats.go`, implementing
`ParseSeats(text string) (int, error)`. Use Go's standard decimal integer
conversion: a positive value succeeds; failed conversion or a non-positive value
returns zero and an error. Error wording and distinct error types are not part of
the contract. Supply these cases up front:

| Input | Value | Error |
| --- | --- | --- |
| `"1"` | 1 | No |
| `"12"` | 12 | No |
| `""` | 0 | Yes |
| `"many"` | 0 | Yes |
| `"0"` | 0 | Yes |
| `"-1"` | 0 | Yes |

The baseline intentionally lacks the implementation required by the supplied
tests. Protect the contract test and module files from edits; creating other
source files, changing the contract or adding dependencies is outside the task.
This fixture supplies tests to isolate execution behavior. It does not evaluate
test authorship or establish that Runner improves on a clean Codex session.

**Process.** Use two sequential configured steps in the one run workspace:

1. **Implement:** one fresh Codex thread/turn, `gpt-6.1-sol` / `xhigh`, original
   brief and supplied fixture context. Enable the sandboxed local command tool
   for file reads/writes, with the agreed access boundary and other capabilities
   disabled. Runner secures the candidate patch and provenance after settlement;
   the worker returns only its bounded declared result. Do not ask it to run tests
   or perform Git operations.
2. **Check:** run the fixed command `go test ./...` once, without a model, against
   that candidate. Source is read-only; Go cache and temporary output use this
   step's owned scratch. This is the entire six-case suite for one tiny package.
   Retain its bounded result linked to the candidate. Only passing checks and a
   secured source result permit successful completion. A failed check preserves
   the candidate and reports failure without an automatic repair turn.

Implement only the command/check support this configured journey needs; do not
introduce a general shell workflow framework. The normal status/control interface
must identify the active step, fixed base, saved candidate, check result and any
recovery/storage reason. Cleanup trouble remains a maintenance issue after a
successful result, under the existing lifecycle rules.

**Proof ownership.** Reuse the core's existing lifecycle tests. Extend the owning
adapter/workspace tests with these source-specific assertions, using controlled
helpers and explicit synchronization rather than spending model turns on faults:

- Lost launch acknowledgement/reconnect leaves one attempt and one execution;
  reconnect does not repeat a source write or replace the run's base/workspace.
- Cancellation after a confirmed partial write, followed by Runner restart,
  preserves that source as unaccepted work. Unknown activity holds its workspace
  and writer capacity until settled; it does not admit a replacement writer.
- Interrupted result collection and an oversized result preserve recoverable
  source, expose the collection/storage reason and reconcile retained artifacts
  without duplicate payloads or premature worktree cleanup.
- After original workspace removal, the saved patch restores the same source
  from the pinned base, including new files. The collector's format coverage owns
  deletion/binary/mode cases; do not duplicate that matrix in paid native runs or
  rerun the Go tests merely to establish identical restored bytes.

Before a paid turn, use zero-generation native command/readiness checks for the
actual edit and check profiles: allowed source/scratch access, protected tests
and Git data, denied private data/symlink escapes and denied network access.
Include effective input-only restrictions where shared adapter settings change.
An unenforceable boundary prevents dispatch. Native success then proves the real
coding-to-check-to-retention journey. These proofs do not claim abrupt host-loss
survival, native Linux support or general project compatibility.

**Authorized allowance (now consumed).** One native coding turn total, with no automatic retry or
model substitution. A turn can contain multiple model requests; it is not a hard
token cap. Allow 15 minutes for native qualification overall, at most five for
the coding turn, reserving the remainder for readiness, checking, result capture
and cleanup. Stop before another turn if it fails to finish. Keep the isolated
instance's managed allowance at 512 MiB, minimum free space at 1 GiB, native stop
threshold at 32 MiB and retained patch at most 32 KiB. Worker/native growth limits
remain monitored stop thresholds that can overshoot; controlled retained writes
use enforced bounds. Do not silently enlarge limits or erase unresolved work to
fit the allowance.

Report the source/check outcome, execution count, elapsed time, reported native
tokens/cache usage and peak observed managed storage, plus recovery and cleanup
results. Include setup/check/coordination work where measured and mark unavailable
usage explicitly; do not present a native subtotal as total cost. This is lifecycle
qualification, not an A/B efficiency benchmark. The implementation must pass the
repository's required Go gates; ordinary tests keep native generation opt-in.

The actual native coding setup explicitly selects one local environment and its
workspace roots. Input-only execution retains empty environments/roots. Failed
worktrees remain available for retry until history retention; successful/cancelled
worktrees may be released once their source is secured. Instance-level protected
source paths and concurrency one bound this first capability. The
[workspace example](../examples/workspace/README.md) is the supported schema.

## Agreed writable owner-process recovery qualification

Authorized 29 October 2026 after checkpointing the writable slice. Exercise one
abrupt Runner owner-process exit while its instance-owned Codex service and a
worker command remain active. Reopen the same operational state, reconcile the
same attempt/thread/turn/workspace, reject replacement while active, then cancel
through the existing control operation. Confirm that the original command has
settled, the pre-interruption source bytes survive in the retained patch, and
owned resources can be released. This does not qualify loss of the native service
or host, or authorize replacement when child settlement is unknown.

The initial allowance was one new `gpt-6.1-sol` / `xhigh` coding turn, at most five
minutes, within fifteen minutes for the exercise. Reuse the existing Go fixture, native
adapter and durable one-turn guard. Normal tests skip the explicit
`RUNNER_NATIVE_RECOVERY=1` exercise. Preserve failed evidence and do not spend a
second turn to repair a failed qualification. No installation or other-project
changes are authorized.

After the first exercise missed the readiness signal and completed without fault
injection, the owner explicitly approved one additional Sol/xhigh coding turn,
capped at five minutes, for the corrected readiness-file synchronizer. Keep the
first result and receipt, use a fresh private receipt directory, and retain the
fifteen-minute exercise limit. This is not permission for further repair turns.

The corrected exercise passed, as recorded in the
[recovery qualification](architecture.md#writable-recovery-exercise-29-october-2026).
Both authorized recovery turns are consumed. The runtime required no change for
the qualified owner-process failure; the correction was to the test synchronizer.

## Authorized first implementation slice

**Authorized by the owner's subsequent “Yes please” to starting this isolated
build.** Build one local Go
service around a tiny two-step process: step A produces a named value/artifact;
step B consumes it and produces the final result. Use a deterministic executor
with controlled outcomes so core behavior can be checked without model calls.

Use one bounded implementation owner with this slice's code and proof as the
finish line. Deliver a runnable local core and a short walkthrough of its actual
Go responsibilities. Role/skill development and benchmark tooling are separate
later work, not prerequisites or additions to this slice.

Keep the slice complete across the existing component boundaries:

- Human-readable configuration, automatic validated reload and visible load errors.
- Run/step/attempt state and retained results in SQLite.
- Dependency dispatch, bounded execution recovery/escalation and a simple
  concurrency limit.
- Start, inspect, retry and cancel through the same local MCP operations, with a
  thin CLI client. Remote access and installation are later qualification work.
- Executor Start/Observe/Stop and restart reconciliation, exposing uncertainty
  rather than blindly repeating an execution.
- Bounded managed storage/logs, output retention handoff and repeatable Release
  for deterministic executor-owned temporary data.

Completion evidence should cover the successful output handoff, a known failure
with bounded retry, restart with completed work preserved, uncertain execution
without duplicate dispatch, cancellation, and a valid/invalid configuration edit.
Also show a declared help outcome causing at most the configured profile change,
with acceptance requirements and consumed allowances preserved. Use deterministic
setups for both profiles; no model call is needed to prove that core transition.
Use a second run/project to show that one waiting run does not block independent
eligible work. Exercise shared behavior through focused checks rather than
duplicating every scenario for each control client.
Prove that repeated observations do not duplicate stored payloads, required results
survive until secured, temporary data is released after commit, and storage pressure
prevents unbounded new writes/dispatch while preserving recovery and clear status.
Include oversized evidence rejection and retained-history/database growth under
the configured bounds, accounting for auxiliary files instead of checking only
artifact-directory size.

The deterministic executor qualifies the core contract only; it does not prove
that Codex or another harness can reconnect, stop or retain results. Actual AI
roles, skills, planners, Git workspaces, reporting integrations and benchmarks
follow later. Full native-session/worktree cleanup and upgrades remain requirements
to qualify in their owning slices; the bounded storage/release behavior above is
part of this first slice.

The runtime examples in `examples/demo` are the concrete supported format. The
first slice uses a sequential step list, owner-only loopback HTTP MCP and fixed
live storage policy. Remote access, real harnesses and broader process syntax
remain later work. Existing installations and the earlier checkout are untouched.

## Overall path

1. **Agree on the core design together.** The first slice's package boundaries,
   file ownership, sequential process and deterministic recovery contracts are
   now concrete. Broader process and cross-product contracts remain open below.
2. **Review the clean first implementation together.** The isolated Go build is
   implemented with focused deterministic evidence. Walk through its actual code,
   configuration and failure behavior before extending the runtime. This does
   not authorize installation or replacing any existing service.
3. **Then qualify domain capabilities and process variations.** Add actual roles,
   skills, context strategies, tool bindings and harnesses incrementally. Compare
   outcomes, maintenance, time and complete observed costs on representative work.
   Use the existing evaluation tools; include coordination and preserve failures.
   Support bounded comparisons on selected tasks/roles, including retained
   positive/negative reference cases.

Use Go for the owned core and supporting tooling; JavaScript/TypeScript is
appropriate where the platform requires it. Do not introduce Python for convenience.
The first implementation slice is authorized as recorded above. No new paid
experiment, installation or deployment follows from this record alone.

## Decisions still open

The first slice settles its implemented interfaces, SQLite schema, sequential
bindings, bounded retry/escalation and local service transport. The following
questions concern broader capabilities, rather than prerequisites left unfinished
in that deterministic journey.

- Result-format details and concrete routing, repetition, terminal and cancellation
  rules. Step results/artifact handoffs, overall run statuses, archiving, waiting/
  resuming principles and the separation of execution state from domain results
  are agreed, as are cancellation/manual-recovery principles; detailed mechanics
  remain open.
- Detailed executor protocol, context/artifact contracts, execution-profile syntax and
  readiness/setup mechanisms, including standalone role calls, recorded configuration
  and usable references across machines. The core/executor boundary and readiness/
  setup principles and the execution-configuration split are agreed.
  Context-sharing selection principles are agreed; concrete binding/read formats
  remain open.
- Dependency/resource scheduling, fairness and recovery. Resource-control and
  active-change principles are agreed; concrete reservation, enforcement and
  operation mechanisms remain open. Independent instances do not imply a shared
  distributed queue.
- Precise edit impact and approval scope under the agreed proportional-safeguard
  constraint. Historical snapshots must not become blanket edit/reapproval gates.
- Resource retention defaults, disk limits and integration-specific cleanup,
  including diagnostics and recovery needs. Cleanup, SQLite and durable-state/
  restart principles are agreed; schema and backup details remain open.
- Precise ownership between Runner, Cortexium and the existing Worker concept.
  Older Cortexium records place surrounding workflow ownership in Cortexium and
  assignments in Worker; reconcile that boundary before changing their contracts.
- Further schema evolution and production adapter qualification. The first slice
  does not automatically approve broader execution or an installed-service cutover.

## Supporting history

- [Design discussion](codex://threads/01a177d1-4c92-7a81-a2fa-671015fe85ea).
- [Current process-core architecture](architecture.md) and
  [the earlier runtime's construction and qualification history](runner-redesign-proposal.md).
- [Code Atlas prototype discussion](codex://threads/01a15ea9-b8e7-73e2-8a78-527dd74ae32c).
- [Earlier Cortexium execution and Worker concepts](/Users/dan/Projects/cortexium/docs/product-concepts/execution.md).

The clean process-core direction takes precedence for this branch. It does not
rewrite existing operational contracts, claim existing installations use the new
design, or settle the open cross-product boundary.
