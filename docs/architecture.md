# Architecture

This repository is the sole architecture authority for Runner; parent and
sibling Cortexium documents do not apply unless linked here. Runner's accepted
target direction is recorded in
[ADR 0001](decisions/0001-event-action-runner.md). The implementation is a
polling event-and-action coordinator.
Continuous mode now observes and reconciles unrelated events while harness work
runs. Dependencies now resolve across planning batches and require a valid
Runner-signed successful state. Harness actions reserve their Project item and,
for implementation or review, their exact repository branch; selection skips
conflicts and continues looking for safe work. Serialized integration is
independent of harness capacity: reconciliation permits one automatic
integration owner per repository/base and refreshes only that candidate. There
is no backward-compatibility requirement during this pre-stable transition.
Trust-gated autonomous issue intake is recorded separately in
[ADR 0002](decisions/0002-autonomous-issue-intake.md). Optional read-only
evidence from other local checkouts is defined by
[ADR 0003](decisions/0003-pinned-repository-references.md). Typed workflow
composition over the three fixed role contracts is defined by
[ADR 0004](decisions/0004-typed-workflow-composition.md).

The local GitHub Project Runner is a modular monolith: one CLI process with packages divided
by responsibility and reason to change. The command package is the composition
root. Internal packages do not parse CLI flags or reach back into `cmd`.

## Package responsibilities

| Package | Owns | Does not own |
| --- | --- | --- |
| `cmd/cortexium-runner` | Root commands, flags, terminal output, and dependency composition | Workflow rules or integrations |
| `skills` | Embedded skill catalog, hashes, and manifests | Installing into harness homes |
| `internal/config` | Strict JSON decoding, explicit-contract validation, initialization templates, roles, lanes, repository-reference contracts, raw-to-runtime resolution | GitHub or process I/O |
| `internal/engine` | Work selection, transitions, retries, planning, implementation/QA sequencing, PR reconciliation | Harness command syntax or GitHub transport details |
| `internal/execution` | Assignment envelopes, native harness invocation, role-scoped primary and reference read roots, schema-backed structured results (including Pi's provider-compatible temporary result extensions), reviewer evidence, planner invocation | GitHub workflow state |
| `internal/github` | Project schema/items, intake, approvals, process locks, branches, and pull requests | Agent execution |
| `internal/metrics` | Append-only attempt and fixed-name stage events, reported usage, context fingerprints, local history, aggregates, and draft recurring-failure projections | Prompts, transcripts, raw harness output, active guidance, free-form stage payloads, or estimated cost |
| `internal/setup` | Capability inspection, doctor readiness, skill installation, allowlisted prerequisites | Work execution |
| `internal/workspace` | Task-scoped isolated worktree creation and validated cleanup, plus immutable-reference validation, without modifying configured checkouts or deleting task branches | Agent prompts or publication |
| `internal/subprocess` | Process execution, bounded output, and process-group cancellation | Domain behavior |

## Dependency direction

`cmd` composes the system. `engine` depends on `config`, `execution`, `github`,
`workspace`, and `subprocess`. Integration packages depend only on lower-level
contracts; they do not import `engine`. `config` has no dependency on an
integration or executor.

The persisted JSON type is `config.Config`. Calling `Resolve` validates it and
produces `config.RuntimeConfig`, which contains the effective workflow, roles,
parallelism, optional rolling admission budget, Project status mapping, and
optional pinned repository references. Before invoking a harness, the engine
derives a narrow `config.ExecutionConfig` for one role and harness invocation.
This prevents computed state and harness-specific overrides from being hidden
inside JSON structs.

Configuration v5 has no runtime overlay or fallback profile. Its workflow keeps
external lane names separate from typed trigger/action rules, which are
validated and compiled before the engine receives them. Privileged commands
resolve an explicit path or the project-local default, then provenance-check the
file before decoding role or harness selections. `init` defaults to an ignored
project-local config, while deliberately tracked and external configs remain supported. `init`
persists harness commands plus role model, reasoning, skills, and timeouts.
Permission boundaries are not persisted configuration. `internal/execution`
owns one immutable `ExecutionProfile` representation for planner, reviewer,
probe, and implementer. Custom roles inherit the ceiling of their base
contract and cannot enlarge it.

GitHub transitions consume domain strings such as summary, branch, pull request,
and accepted commit; `internal/github` does not depend on executor output types.
Implementation work is recovered through the retained branch and pull request,
not a second patch-manifest representation.

## Command layout

`cmd/cortexium-runner` is idiomatic for a Go executable named
`cortexium-runner`. Putting its files directly in `cmd` would make the package's
import path and binary ownership ambiguous as soon as another maintenance
command is added. Multiple files in the command directory split flag handling
by subcommand; they still compile into one `main` package and one binary.

The public command surface keeps first-class operations at the root: `init` and
`doctor` prepare and diagnose the runner; `plan`, `approve`, and `retry` manage
work; `run`, `status`, `metrics`, and `guidance` operate it; `role` manages
extensible role profiles; `workflow validate` and `workflow explain` inspect the typed workflow;
and `harness check` qualifies configured execution profiles against a private
temporary Git repository.
`init` is idempotent for an existing config and owns GitHub Project field/status
synchronization, local prerequisite checks, and bundled skill installation.
`doctor` owns static config validation and live readiness inspection. Its
explicit `--probe-harnesses` mode additionally proves real authentication,
model invocation, and structured output once per distinct execution profile;
normal doctor remains non-billable and does not call a model.
`harness check` is the explicit paid adapter-conformance boundary. It invokes
each configured execution-role profile through the production planner,
implementer, or shared-reviewer path, verifies that read-only roles leave the
fixture unchanged, and verifies an implementer's exact isolated-worktree
artifact through the normal integrity checks. Optional `--browser` checks use
the same configured safe-tool profile. The command performs no GitHub
operations and removes its private fixture after the checks.

The command package attaches an optional metrics observer and history reader to
the engine. The engine emits start and completion events for every card or
interactive planning attempt. It also emits fixed-name stage events around
workspace and repository preparation, ordinary harness execution, the planner's
repository-aware outline and tool-free details calls, the reviewer's evidence
audit and optional focused-verification call, result validation,
workspace verification, candidate construction and its correction-admission
guards, Project transitions, and PR publication.
Execution adapters parse only counters exposed by the native harness. Events are
appended to a runner-keyed JSONL file in the user configuration directory. The
event boundary keeps telemetry failure non-fatal to workflow execution and
preserves unfinished attempts and stages after a process interruption. Stage
events carry identity, timing, enums, usage, and prompt-context fingerprints.
The history deliberately excludes prompts, transcripts, raw responses, command arguments, raw local
errors, and free-form stage payloads. Completed attempts may additionally carry
a fixed publication-operation enum and a bounded attempt count; provider error
text is never persisted. `metrics` reads and aggregates this store, including
completed harness-invocation counts and exact saved-result resumes for planning
and implementation. `status` presents a compact aggregate and the current
admission decision. GitHub
cards receive only fixed bounded execution, recovery, and QA classifications;
detailed usage and model-authored evidence remain local.

Completed QA attempts also retain failed-check summaries and the reviewed
candidate commit after the existing integrity checks. Blocked proof and warning
findings are not recorded as confirmed defects. The `guidance` command projects
this private history into unapproved drafts; the running service replays it once
and examines each newly completed attempt for a threshold crossing. There is no
second journal, scheduler, model call, or prompt-injection path. Distinct cards
count as incidents; retries do not. Project, repository, role, harness, failure
operation, and finding category separate patterns. Exact QA wording (apart from
whitespace) and fixed Runner classifications are signals for human investigation,
not proof of a shared cause. Publishing guidance remains an explicit reviewed
repository-documentation or skill change. Drafts neither grant authority nor
change admission, acceptance, retry, or workflow rules.

Runner renders pinned skill/capability guidance and fixed stage instructions
before variable assignment and evidence data. Each attempt records the distinct
`prompt_contexts` used; each stage pins its context at start. `layout` identifies
Runner's prompt layout and `guidance_digest` hashes the exact pinned skill and
capability text. These fields do not fingerprint repository files, subsequent
tool reads, a whole provider request, or provider cache state. Repository
instructions still come from the normal isolated execution workspace. Native
harnesses retain ownership of conversation rendering, tools, schemas, model
routing, and cache controls; Runner does not trade isolation or reviewer
independence for a more reusable prefix.

Execution adapters map allowlisted adapter-owned structured failures and
Runner-observed failures to a stable failure class plus `automatic`, `manual`,
or `none` retry disposition. Non-review agent outcomes `needs_input` and
`blocked` receive `needs_input` and `agent_blocked` respectively, with manual
recovery and retained retry lanes when stopping in `Blocked`. Their private
blocker text does not grant automatic retries or public-diagnostic authority; Runner does not infer a
provider failure from it. A structured blocked QA verdict becomes
`review_incomplete` with manual retry, not a capability diagnosis. Its fixed
remote label is "QA evidence incomplete"; model-authored detail remains local.
Runner/adapter-detected capability failures retain `capability_unavailable`.
Neither classification consumes a QA rejection.
Codex provider classification reads only the terminal
`turn.failed` event in its native JSONL stream. One narrow startup exception
recognizes Codex's exact fatal `thread/start` envelope for a required
`runner_browser` MCP startup timeout: Runner must have granted the browser,
stdout must be empty, stderr must contain only the known fatal line and optional
fixed prompt-reading/session-error lines, and the subprocess must return a plain
exit-status-1 error after successful teardown. Additional diagnostics, other
servers, output truncation, cancellation, or cleanup errors do not qualify.
This produces `browser_startup`, never provider-capacity or QA-rejection evidence.
Progress events, model-authored text, and arbitrary stdout/stderr phrases have
no recovery authority. A recognized transient Codex service failure or browser
startup timeout returns the card to its current role lane as
`Waiting for harness provider` and retries after 30 seconds, two minutes, and
five minutes. A fourth consecutive failure moves it to the configured error
lane for manual recovery. These operational retries do not increment `QA
Failures`; a process restart may retry sooner because the short backoff is
deliberately in-memory. Opaque harness failures stay unknown and are never
automatically retried. Browser startup failures retain their bounded local
startup diagnostic; Project reports use only a fixed browser-startup template.
For unknown Codex exits, bounded local diagnostics prefer the terminal
`turn.failed` reason, falling back to the tails of both output streams. Opening
progress and a partial model result do not replace the terminal failure. This
does not grant new retry authority or expose raw diagnostics in Project reports.
The optional rolling admission budget is evaluated from local history before agent
claims. Exhaustion pauses all new claims, including QA, without canceling
in-flight attempts; PR reconciliation still runs. Reported-token and cost
ceilings fail closed when attempts are unfinished or lack the required reported
usage, and harness-time ceilings fail closed for unfinished attempts.
The long-running engine reuses parsed admission history until an attempt starts
or completes; stage-only telemetry does not cause another full JSONL replay.

An optional implementer ladder is a validated ordered list of implementer role
profiles. It never retries within one execution attempt. After a reviewer
returns a valid `needs_changes` verdict, the existing authenticated `QA
Failures` Project field advances the next implementation to the corresponding
profile; the last configured profile is reused until `max_qa_rejections` is
reached. This makes selection restart-stable without a second local
state journal. Other failure classes do not change the persisted QA count and
therefore cannot trigger automatic model escalation. Metrics and admission use
the selected profile's actual role, harness, model, and reasoning settings.

Every native harness command runs in an owned process group. The subprocess
boundary enters one teardown path after success, command failure, timeout, or
cancellation; it terminates the owned process group and reaps the direct process
before returning. Security after a sandboxed implementer does not rely on that
group containing a process that creates a new Unix session: Agent QA receives a
new private detached checkout that was never writable by the implementation
sandbox. Structured-result inputs use one `internal/securefs` artifact
contract: a unique effective-user-owned mode-`0700` directory with pre-created,
pinned mode-`0600` regular files. Codex output is read through its pre-launch
descriptor while its schema and Pi's generated extension must retain their
original identity and metadata. Cleanup unlinks known artifacts relative to the
pinned directory and does not follow a substituted invocation path.

Successful implementer verification is persisted as private mode-`0600`
evidence bound to the approved item/content identity and the literal candidate
commit and tree. Agent QA may reuse adequate evidence but receives it only as
untrusted historical context; evidence text cannot authorize commands or
change the approved proof obligations. A candidate or criteria mismatch
fails closed instead of reusing stale evidence.

A separate private implementation checkpoint prevents completed model work from
being repeated after a Runner-side candidate, evidence, or Project-transition
failure. It binds the approved content, semantic comment and QA context,
repository/base/branch identity, proof obligations, and exact workspace
snapshot. When a candidate exists, its commit and tree are bound as well. Only
an exact match reconstructs the successful output, with zero new harness usage;
any mismatch removes the stale checkpoint. The checkpoint is cleared after the
successful transition to Agent QA, so it cannot bypass later independent QA.
Ordinary candidate-content failures such as unresolved conflicts or
`git diff --cached --check` errors are not workspace-integrity failures. Runner
clears the unusable checkpoint and gives the same implementer one immediate
corrective pass inside the current action, after revalidating approval. The
pass retains the worktree, approved scope, QA feedback, and earlier verification
as untrusted historical evidence; Runner restages and rechecks the result before
QA. Both harness calls count toward usage, but neither candidate failure consumes
a QA rejection. A second candidate-content failure blocks with a bounded,
privacy-safe correction and requires an explicit retry through implementation;
integrity failures never enter this automatic correction path.
Operator-supplied retry feedback also clears the checkpoint before changing the
card, while unchanged Runner-side post-processing failures retain it.

Pi result attribution comes only from its native JSON event stream. Explicit
`lmstudio/...` stages with tools must produce one session-provenanced
empty-finalizer start/end pair with an unchanged call ID and invocation-bound
extension provenance, followed by exactly one successful JSON assistant
response and no later tool call. Tool-free synthesis stages instead force that
single native JSON response on their initial request and reject any tool event.
Working turns receive the configured reasoning effort. Where Qwen chat-template
controls are supported, Runner maps the Pi role's optional inherited
`preserve_reasoning` setting to `preserve_thinking`; its effective default is
`false`. Final structured formatting always forces both thinking and
preservation off, independent of that working-turn setting. These request-level
controls do not depend on the LM Studio UI preset.
Other providers retain the schema-backed result-tool path, which requires
matching call arguments and details. Unmatched, duplicated, lookalike, and raw
JSON output fail closed. Explicit Claude MCP readiness
likewise requires the exact configured server entry to report success or
connection rather than merely appearing in inspection output.

Pi extension discovery is disabled for these invocations; only Runner's pinned
result extension is loaded explicitly, preventing discovered extensions from
observing and forging its provenance.

Planner, implementer, and reviewer content share one local representation
compatibility policy before their role-specific strict decoders. Runner may
unwrap one whole-response JSON object fence and remove only the exact stray
top-level JSON Schema residue `"type":"object"`. Missing substantive fields,
all other unknown fields, malformed JSON, and semantic contract failures are
rejected without another model invocation.

Workspace verification follows every terminal harness result, including a
command error. Execution, result-contract, and integrity errors are joined for
local diagnosis when they coincide. Integrity failure takes precedence for
workflow control, so a simultaneous harness failure cannot enable success,
retry authority, or publication.

Remote Project fields are composed only from fixed Runner templates,
allowlisted failure/retry enums, bounded structured retry fields, and bounded
review status counts. Raw CLI diagnostics remain local. For executable
issue-backed cards, bounded model-authored QA summaries and evidence may be posted as a marked,
idempotent issue comment; repository visibility then governs that detail.
Planning children remain drafts only while unapproved. The authorization
boundary converts them to issues in the configured intake repository before
execution, while private authenticated feedback remains the recovery path if a
comment cannot be published.

Project claims use one fresh board snapshot for all cards selected by a poll,
so dependency and planning-batch checks remain board-wide without re-listing
the Project per card. Operations that already hold an authenticated item ID
reload only that exact node. Multi-item planning and issue-boundary reloads use
bounded `nodes(ids: ...)` batches. Authenticated transitions retain a separately
visible begin/end transition lock, but commit their phase, activity, result,
approval, status, and task-specific fields in one GraphQL mutation between
those lock writes. This preserves interrupted-transition recovery while
removing most Project write round trips.

Routine pull-request observation fetches only identity, state, refs, merge
state, and auto-merge state. Runner requests comments, reviews, and the current
GitHub actor only when trusted human feedback can affect a rework or
base-refresh transition.

Every harness uses the same two-stage planner contract. The first repository-aware
stage returns the outcome and ordered card/dependency outline. The second
tool-free stage returns fixed-key details for those Runner-owned cards. Runner
assembles the canonical plan, rejecting missing, extra, reordered, or
semantically incomplete details. Every work item has a local objective,
acceptance criteria, proof obligations, selected assumptions, dependencies, and
a natural review boundary sized for one configured implementer invocation. Runner appends the
original approved request, project outcome, cross-cutting criteria, and
constraints to every generated child card. Implementers and reviewers therefore
receive the stable product and task contract through ordinary Project data
rather than a hidden local plan store. Runner extracts the exact approved
proof obligations from that immutable card body and passes them to every
downstream harness. The implementer chooses the smallest reliable proof method;
the shared reviewer first receives fixed Runner-owned proof keys for a
source-and-evidence audit that permits read-only file/Git/log inspection,
including shell commands, but cannot run dynamic checks. Runner supplies the
exact base and candidate commits rather than asking the reviewer to guess the
comparison from local branch names. Already merged dependencies are part of that
base and their current source remains available for integrated checks. Rejected
reviews retain an immutable binding to the repository, approved content,
proof obligations, and base, plus the bounded comment context visible during that
review. When those bindings and the prior assessment remain valid and the prior
commit is available, the next assignment gives the existing reviewer both the
prior and complete current comment context. Operational coordination changes can
therefore preserve applicable conclusions; additions, edits, or removals that
materially affect the task reopen only the affected proof and review scope. The
reviewer does not infer trust from a comment prefix, claimed author, or QA-like
marker, and unchanged comments retain the ordinary follow-up behavior. Missing or
malformed history and any repository, approved-content, proof, or base mismatch
renew the cumulative review. All concrete unresolved questions enter a fresh
focused-verification invocation, even when
another key already failed. That invocation receives the pinned comparison,
repair baseline when present, and original recorded evidence for unresolved
proofs. An unresolved repository-rule or maintainability check also receives the
approved scope; this context does not reopen resolved proof keys. The handoff
does not claim that unresolved source inspection already happened. Runner merges
the observations and derives the verdict and final summary from the merged
checks, not from superseded stage summaries.
The bundled work-role guidance runs heavyweight verification commands
sequentially within each assignment, without changing a command's configured
workers or timeout. A server required by the active check is allowed; unrelated
test suites, browser runs, builds, and installs must not overlap. The focused
review prompt reinforces this rule. It is not a host-wide resource lock or a
change to admission of independent cards. When accidental overlap contributed
to a timing failure, the bounded confirmation corrects that scheduling and
records the difference instead of claiming an unchanged reproduction or proof
of concurrent-load reliability.
The bundled implementer supplies self-contained command, scope, settings, and
outcome evidence in the existing candidate-bound record. Failed checks and
reruns include affected test identities and both outcomes; temporary reports
are not copied across role workspaces. The reviewer distinguishes concrete
defects from unexplained timing failures. Its focused stage permits one
unchanged diagnostic confirmation of a known check, counting an existing
adequately diagnosed unchanged retry toward that bound. If historical details
are missing, it gathers fresh evidence for the unresolved behavior using
documented repository settings, without inventing historical settings or
reopening resolved obligations. Both available historical and fresh results,
diagnostic observations, and uncertainty belong in structured evidence. Fresh
focused success does not establish an unknown full-suite result. Genuinely
inconclusive proof remains blocked. This is guidance inside the existing harness
invocation, not another Runner retry mechanism or a change to rejection accounting.
Operator-selected `standard` or `high`
task sizing changes only decomposition and specificity for implementer and
reviewer roles. Runner never infers capability from model names, and the shared
plan schema and acceptance rigor remain unchanged. A claim records `Planning`,
`Implementing`, or `Reviewing` in the visible `Runner Activity` field. A card
whose authenticated dependencies are incomplete records `Waiting for
dependencies`. A recognized transient Codex provider failure or pre-session
browser startup timeout records `Waiting for harness provider` while its bounded
retry remains in the same role lane; the result classification distinguishes
provider failures from `browser_startup`.
The Project owns one read-only eligibility classifier for agent-lane cards.
Queue selection, final claim validation, and operator status consume that same
decision, so an ineligible card cannot be presented as executable. Status keeps
such cards in a separate waiting collection with fixed non-sensitive reasons
for transition recovery, invalid current authority, dependencies, incomplete
planning batches, and invalid batch or sibling authority. Classification does
not adopt, approve, or rewrite a card; the action path performs any authorized
manual-intake adoption or invalid-card reclassification afterward.
Accepted QA changes activity to `Awaiting human review` or
`Waiting for CI` while the card remains `PR Ready`. Automatic integration then
distinguishes `Waiting for integration slot`, `Waiting for CI`, and `Waiting for
merge`. A terminal failed check disarms auto-merge, releases the repository/base
integration resource, and returns the retained pull request to implementation as
`CI failed — rework queued` without incrementing the Agent QA rejection count.
Leaving `PR Ready` for other reasons clears activity. A blocked transition
records its originating agent lane in the hidden
`Runner Phase` field; `retry` uses that explicit destination and never guesses
from a summary string. A status-only human move from `Blocked` to the configured
`Ready` lane is a distinct authorization event: it explicitly retries through
implementation, preserves the existing result and QA failure count as context,
and replaces the prior signed lifecycle state with a signed `Ready` action.
Runner validates every other signed field before doing so, so editing content or
workflow metadata at the same time fails closed instead of laundering changed
work through the shortcut. The separate hidden `Runner Transition` field is a
fail-closed lock around non-atomic Project updates. Authorization rejects locked
cards, completed locked state is resumed in place, and partial state is returned
to assessment. Phase and activity remain bound to authenticated action state;
the lock is checked independently.

The immutable publication tuple also retains the bounded accepted QA report and
issue comment. Before starting a reviewer, Runner checks for an existing tuple
bound to the exact item, delegated content, repository, branch, base revision,
candidate commit, tree, and clean workspace snapshot. An exact match resumes
only the idempotent comment, push, pull-request lookup or creation, and Project
transition. If only the workspace snapshot differs (for example, after cleanup
and recreation for a CI-only retry), Runner runs fresh QA against the unchanged
candidate. It never transfers the old acceptance to the new snapshot. A fresh
accepted review gets a separate immutable snapshot-specific record; the first
record remains the candidate identity and prior-publication lease anchor.
Changed item/content, repository, destination, base, or commit/tree bindings and
malformed records still fail closed. A retained-acceptance blocker explicitly
reports that no reviewer ran and directs the operator to local acceptance state.

Approval and staged-batch authority carry one canonical delegated-content
digest over the exact approved body snapshot, repository, immutable dependency
IDs, and authenticated planning provenance. Immediately before constructing an
implementer or reviewer assignment, Runner refreshes Project-backed content and
requires the same digest. The harness receives the approved snapshot and digest;
the issue URL is provenance only and is not emitted as an authoritative context
reference. Title or URL presentation changes cannot change the delegated-content
identity, while changed execution-defining content returns the item to assessment
without invoking a harness.

For an ordinary unsigned item, placing it in the configured `Plan` or `Ready`
status is the human authorization event for that lane. Runner converts a draft
to an issue in the configured intake repository when necessary and signs that
exact snapshot before the planner or implementer can claim it. The enqueue-only
`add` command performs only draft creation and the status mutation, and remains
usable while the coordinator process owns its execution lock. An optional exact
`## Dependencies` list accepts Project item IDs or issue URLs and is part of
that signed snapshot. Complete-batch approval performs the same conversion for
every released planner child.
Nonempty invalid approvals are not replaced, and staged planning provenance must
already prove complete-batch release. Issue comments are fetched separately only
for the claimed card immediately before assignment; they are bounded historical
context and are not part of delegated authority.

Autonomous issue intake reuses these same authorization and planning contracts.
The optional `autonomous_issue_intake` object enables the policy. A labeled
issue is eligible when the configured intake repository itself is private, or
when its public author matches `trusted_authors` case-insensitively. Project
visibility is deliberately irrelevant because a private Project may contain a
public issue. Runner obtains visibility and issue identity from GitHub, previews
the ordinary approval, rechecks trust immediately before applying it, removes
the intake label, and moves the signed source into the configured initial
planner lane. It never routes issue text directly to an implementer by keyword
or shape.

The existing planner is the sole ambiguity boundary. Open decisions create no
children; Runner posts the questions to the issue and blocks the source for an
explicit human retry. A complete plan stages the normal authenticated batch.
For a still-trusted source, deterministic reconciliation rechecks trust and
applies the same exact-batch approval used by the operator command. These
authorization actions remain independent of harness capacity. Public issues
whose authors are not allowlisted, and all issue intake when the policy is
absent, retain the human assessment and batch-approval boundaries.

Issue completion is a deterministic reconciliation action. After Runner
observes a merged pull request and records the card's authenticated successful
outcome, it closes that implementation card's issue with GitHub's `completed`
reason. A planning source can be `Done` while its issue remains open: Runner
closes that source issue only when every exact child in its authenticated
released batch has its own merged-pull-request outcome. A missing, changed,
blocked, closed-without-merge, or manually moved child keeps the source open.
Pull-request bodies deliberately contain no source-closing keyword because no
single child PR is necessarily the final one. Closure failures are reported and
retried on a later poll without consuming harness capacity or blocking other
safe actions.

Workspace authority combines that delegated-content digest with the exact
resolved base commit and records them with the immutable Project item ID,
repository, branch, and normalized worktree path in one private record outside
the mutable checkout. Git registration and every recorded field must match
before reuse. Implementation preparation recoverably moves an incompatible
owned worktree and renames its retained branch into a collision-safe quarantine,
then starts clean from the current approved base; unregistered paths are
refused. QA, refresh, publication, and cleanup never substitute a new workspace:
a mismatch preserves the old work and returns the item for safe reimplementation
or operator recovery. Quarantines remain inspectable and are not deleted
automatically.

Repository integrity is a content-free manifest built through
`internal/securefs`. The current index (including security-relevant flags),
worktree registration, HEAD/ref chain, common and enabled worktree config,
explicit Git metadata surfaces, and ignored or concealed Git-control files are
opened or hashed relative to pinned directories without following symlinks.
Explicit metadata surfaces include repository ignore/attribute files,
sparse-checkout, alternates, grafts, replacement refs, and default hook names.
The manifest records filesystem identity as well as content so replacement with
an equivalent-looking object is still drift.

Individual Git control-file reads, including initial loose HEAD-reference
pinning, require their parent directories to retain no-follow identity and safe
permissions, not unchanged timestamps from unrelated sibling-ref updates. The
exact child is hashed before and after reading and its pinned state is verified;
content changes, substitution, and a missing target appearing still fail closed.
Protected Git metadata paths likewise verify their shared administration root
by identity, so unrelated `FETCH_HEAD` updates cannot invalidate a candidate.
Their traversed descendants and explicitly pinned protected directories remain
metadata-strict. General repository path snapshots are unchanged.

Task checkpoints use that complete manifest. Active-checkout and QA-boundary
comparisons exclude only `branch.*` entries from the shared repository config,
because unrelated concurrent branch publication and maintenance legitimately
change those tracking entries. All other local config, including
security-relevant Git controls, remains integrity-bound.

Indexed gitlinks form recursive manifest nodes. Their index path and recorded
object ID are always present; initialized nodes add their own HEAD, index/status,
protected metadata, and nested gitlinks. Capture never fetches, initializes, or
copies a submodule, and rejects an indexed path that is missing, symlinked,
cannot be opened through the pinned parent, or is deinitialized but non-empty.
Runner constructs a clean task-branch commit before Agent QA through a
privileged Git profile that pins the linked-worktree administration, index, and
common object store. It then materializes that exact commit through the same
config-free boundary in a new mode-`0700` private detached checkout outside the
implementation sandbox. The engine records the candidate HEAD and tree in the
private review manifest and compares that manifest, the retained implementation
worktree, and the active checkout around Agent QA before
entering push, pull-request publication, or worktree cleanup paths. An accepted
unchanged candidate receives an exclusive private publication record keyed by
its commit and binding the item/content identity, commit/tree, approved base,
repository, and full destination branch ref. Subsequent accepted reviews of the
same candidate in a different workspace snapshot are additionally keyed by the
snapshot digest. Neither the original nor a snapshot-specific record is
overwritten, and publication requires the exact record for its current snapshot.
When the source audit leaves a concrete dynamic check unresolved, the focused
review stage receives a disposable source copy inside its private neutral
workspace. It contains no Git administration and does not reuse implementation
dependencies or build artifacts. The reviewer may restore existing locked
dependencies and generate build/test output there, but not alter candidate
source, tests, manifests, or lockfiles. Runner bounds copying with the existing
snapshot limits, refuses external symlinks, and verifies the copied source with
no-follow hashes after the harness returns, including on failure. A changed
source invalidates the result. The canonical review and implementation snapshots
remain unchanged and are still checked by the engine. Only focused reviewer
safe-tool invocations receive the bounded npm and public Go package hosts in
addition to loopback; dependency and build caches remain in private temporary
space. Audit-only invocations do not prepare this copy or gain package-network
access. All harnesses use the same copy lifecycle; Pi still requires explicitly
configured host access.
Publication replays that record under a sanitized privileged Git profile,
re-fetches and compares the approved base, re-resolves the accepted tree,
refreshes Project authority, validates the configured remote repository, and
pushes only the recorded commit OID to the recorded full ref. Base refreshes
remain local until their resulting tree completes implementation and QA and
receives a replacement publication record. During tracked pull-request rework
in `rebase` mode, workspace synchronization permits the expected local history
rewrite only when the remote branch still resolves to the card's exact recorded
QA commit or to an immutable private publication record for the same
item/content/repository/destination tuple. The latter recovers an interrupted
Project update without trusting arbitrary divergence. The rewritten history
remains local through implementation and QA; publication is still the only
boundary that may replace the remote branch, and does so with that authenticated
remote commit as its exact force-with-lease value. A successful Project
transition records the replacement QA commit and removes the stale-field
condition.
Recognized transient network and GitHub 5xx failures during this deterministic
publication are retried in-process up to three total attempts. Each retry
revalidates the immutable tuple and current Project authority, reuses an already
published exact commit or PR, and never invokes the reviewer again. Unknown,
rate-limit, authorization, validation, and cancellation failures are not
automatically retried.

QA publication ends at `PR Ready`; it never requests merge directly. In
automatic mode, pull-request reconciliation claims
`integration:<repository>/<base>`, using GitHub's enabled auto-merge state as
the restart-stable owner. It recovers an existing owner before item ordering,
disarms duplicate owners, and compares only the selected candidate with the
latest base. A moved base returns the candidate through implementation and QA;
a clean reviewed candidate is bound to its exact head and base before Runner
enables GitHub auto-merge. Manual-review PRs remain at the human gate without
Runner refreshing them after unrelated merges.

One budget is shared by the recursive manifest, including protected controls,
ordinary worktree paths, Git metadata, and initialized submodules. Directory
discovery and Git-derived path collections reject the next entry before the
configured count is exceeded; regular files and symlink payloads are bounded
individually and in aggregate before further reading. Git and GitHub command
capture, Project collections and pagination, pull-request feedback, and public
intake mutation fan-out use fixed fail-closed caps. Public intake is scheduled
after recovery, pull-request reconciliation, and admitted execution. In
continuous mode its bounded local failure does not cancel in-flight work or
discard the result when that work finishes.
Effective Git behavior redirected by the environment or external
configuration—including includes, custom hook paths, and external
ignore/attribute sources—remains the separate finding #23 boundary.
The enforced filesystem implementation and confidentiality guarantee cover the
release matrix of macOS and Linux; other platforms do not receive an equivalent
claim.

Delegated planner fan-out has a 1,000-child emergency ceiling in both the model
schema and a streaming production preflight that runs before child decoding
allocation. The ceiling prevents unbounded output and Project-write loops; it
is not a target, preferred count, or task-sizing rule.
Runner normalizes the complete plan before Project writes and stages every
child unapproved in assessment. Project-driven planning
leaves its source in the `planner_approval` phase. Ordinary intake uses
`approve`, which previews and revalidates every exact child, destination, and
source, then requires a default-No terminal confirmation before release.
Trust-gated issue intake performs the same revalidation automatically after
rechecking source trust. The source carries a
Runner-authenticated staging marker bound to the ordered exact batch and a
fresh generation; public metadata or phase text alone cannot establish
provenance. Before the first child write, Runner stores the normalized
executable plan in a private mode-`0600` checkpoint bound to the delegated
content, exact planning context, role, lane, destination, repository, and batch
fingerprint. An exact retry skips the planner and reapplies only that saved
batch, reusing matching children already staged before an interruption. Changed
context removes the stale checkpoint; malformed state fails closed. Plans with
open decisions are not checkpointed because they create no children. Runner
records the authenticated marker before the source lifecycle writes and clears
the local checkpoint after staging succeeds, so either recovery path resumes
the exact batch without rerunning the planner; changed or partially authorized
children are rejected.
Interactive direct CLI planning treats the displayed-plan Yes as authorization,
stages the full batch, and reloads and revalidates it immediately before release.
Explicit `--create` provides the same stage, revalidate, and release behavior
for scripted planning. Explicit `--stage-only` returns a deterministic batch
fingerprint; the separate interactive `plan --approve-staged` action reloads
the exact staged set, displays it, asks for explicit acceptance, and revalidates
it before release. The
planning source receives an authenticated complete-batch release commit only
after every child is released. Polling and claiming validate that commit and
all siblings, so interrupted release remains fail-closed even when compensating
cleanup also fails.

Direct CLI JSON includes the original planning request and configured Project,
repository, base-branch and destination identity. `plan --plan-file` loads that
untrusted proposal without a harness call; optional `--stage-only` uses the same
normalization, exact-child matching, provenance checks and short mutation guard
as fresh staging. Existing receipts are not imported as authority. Open decisions,
changed targets, changed or partially released children, and other unapproved
local batches prevent staging. An unchanged saved proposal resumes partial
staging without changing its fingerprint or creating duplicate children. This
uses operator-retained JSON, not another persistent planning store; imported
plans still require a separate complete-batch approval.

Local coordination separates the worker lifetime from standalone planning.
`run` alone owns the worker lock and runtime-status file. Standalone `plan`
commands serialize with one another, not with the service. Both CLI-owned
engines use per-Project OS-locked execution slots for `max_parallelism`; locks
are released by the OS on process exit. A short admission gate covers the budget
check, claim, and durable attempt-start reservation, never the harness invocation
or human review. Admission reloads shared history under that gate so another
process's reservations cannot be hidden by an engine-local cache. Claimed starts
are recorded before dispatch, and already-claimed work is still completed if a
later claim fails. Budget and capacity exhaustion do not authorize extra work.

CLI batch writes, approvals, retries, and explicit reauthorization exclude
interrupted-state recovery, with recovery's snapshot acquired inside the same
short mutation guard. While a CLI mutation is in progress, the worker continues
observation and ordinary reconciliation but defers recovery. The guard is not
held during planning, previews, or human confirmation. Existing whole-batch
authority and exact-content checks remain responsible for preventing execution
of partial or unapproved plans.

`retry --reauthorize` is a narrow operator recovery path for an unpublished
implementation parked in assessment with its approval missing. It requires a
retained registered worktree and private identity matching the exact delegated
content, item, repository, branch, path, and configured base ref. It never
creates an identity as recovery evidence. The operator must confirm the exact
card and runtime-state preview in a default-No terminal prompt. Runner rechecks
that preview and the private identity under the mutation guard, and validates
every batch sibling and any source release before changing only this card.
Recovery preserves private feedback, worktree changes, and the QA failure count;
it replaces the recovery error with a fixed retry classification and returns to
implementation, never directly to QA, publication, or completion. Missing or
changed content bindings, unapproved/incomplete batches, non-implementation
phases, existing PRs, and QA commit snapshots fail closed. This is explicit new
operator authority for retained work, not automatic reconstruction of a lost
signature. It adds no persistent journal or approval store.

`retry --reauthorize --qa-only` is a separate synchronous operator boundary for
an existing Blocked reviewer-phase candidate whose approval was withdrawn. It
does not create an `AuthorizedAction`, sign Project authority, or enter the
workflow graph. A default-No terminal confirmation binds a single-use in-memory
preview to the current item/context, exact retained candidate and workspace,
PR identity, configured reviewer, and immutable repository references. All
bindings are rechecked before and after the review. The private historical
workspace content identity is inspected, not rebound: changed requirements get
fresh review without discarding old feedback/proof or reusing old acceptance.
The original candidate, PR, card, approval, and rejection counts remain
unchanged on every verdict. A per-item local review lock and existing execution
admission bound concurrency; no new scheduler, persistent approval store, or
Project fields are introduced. No implementation, base refresh, publication,
merge, sibling authorization, or automatic retry is reachable from this path.

Every process launch resolves two independent role settings before adapter
arguments are built. `access` selects Runner's containment boundary
(`sandboxed` by default or explicit `host`). `harness_config` selects whether
Runner suppresses ambient harness configuration (`isolated` by default) or
loads the operator's native user/project configuration (`inherit`). Planner and
reviewer use disposable mode-`0700` neutral directories and explicit read
roots; probe receives only structured output; implementer uses the prepared
issue worktree. Codex and Claude can inherit configuration while retaining
Runner's native shell/filesystem sandbox ceiling, although inherited
out-of-process MCP servers, plugins, hooks, or extensions can retain their own
OS permissions. Pi cannot safely combine inherited ambient tools with
`sandboxed` because it has no native OS sandbox, so that combination
is rejected. `host` plus `inherit` is deliberately unrestricted agent execution
under the Runner OS account. Runner still owns non-interactive invocation,
structured results, worktree identity, and repository-integrity verification.
The live readiness probe always forces `sandboxed` plus `isolated`. Setup and
production launches share the same required-flag table, and unsupported
installed CLIs fail before model invocation.

Codex invocation policy is supplied to the `exec` subcommand, alongside model,
reasoning, and MCP overrides. Only the root-only approval flag precedes `exec`.
Keeping all configuration overrides at the same command level prevents
subcommand overrides from displacing Runner's permission and isolation policy.

Optional `repository_references` are a fixed extension of planner, implementer,
and reviewer profiles, including custom roles with those contracts. Probe
profiles never receive them. Normal doctor and every eligible launch resolve
symlinks and verify that each reference is an exact, non-overlapping Git root
with a clean tracked/untracked state and `HEAD` equal to its full configured
commit, including implementation retries. Implementation edits remain in the
assigned worktree; reference source inspection is explicitly permitted in the
launch prompt. Runner does not mutate or synchronize reference checkouts. Codex gets
explicit filesystem reads; Claude gets repeated additional-directory reads,
explicit write denials, and disabled instruction loading from those additional
directories. Pi requires `host` for references because it cannot enforce a
read-only root. Host mode remains unrestricted and is not narrowed by the
reference list. Reference content is labeled untrusted evidence, and the whole
root, including ignored files, is readable; operators use dedicated checkouts
when that distinction matters.

A sandboxed Codex or Claude implementer or reviewer receives Runner's bounded
development profile by default. Package commands run inside the native
filesystem sandbox. Runner discovers the installed Node and Go executables and
exposes only their resolved executable/runtime roots read-only. Go build,
module, configuration, and home state use the invocation-private runtime
directory. Its public module path is fixed to `proxy.golang.org`,
`storage.googleapis.com` for the proxy's archive redirects, and the authenticated
`sum.golang.org` checksum database; direct VCS fallback is disabled.
Implementer network access is otherwise limited to the npm registry and
loopback. Planners and audit-only reviewers receive no package-download network
access. The filesystem profile exposes the assigned workspace, minimum system
runtime files, and the implementer's npm cache instead of the operator's home
directory. The Runner-owned `runner_browser` MCP definition is pinned,
headless, temporary-profile, loopback-only, external-DNS-disabled,
telemetry-free, and independent of ambient harness MCP configuration. Its cwd,
user/global npm configuration, and cache live in a separate mode-`0700`
host-owned directory that is absent from harness sandbox write grants. A role
may explicitly disable this profile. In inherited mode Runner adds this server
alongside the ambient MCP configuration.

Each native harness invocation establishes its own configured MCP connections.
Runner's stdio browser server and isolated profile are invocation-scoped, not
shared across cards or between implementation and independent QA. The private
npm package cache is reused across invocations. A card can require multiple
invocations (including separate review stages); each initializes its own
connections. Runner does not maintain a persistent browser service or pool.

Pi implementer and reviewer roles receive the same three browser operations
through a temporary Runner-generated extension that forwards to the pinned
browser server. Ambient Pi extensions stay disabled in isolated mode and load
only after an explicit inherited-configuration opt-in. Navigation through the
Runner browser remains loopback-only. This boundary does not change Pi's
explicit host-access requirement for shell and edit tools.

A Codex role can additionally add an explicit named MCP allowlist. In isolated
mode Runner reads the native Codex MCP catalog from a private neutral cwd rather
than the project worktree. It rejects missing, disabled, remote, or
inline-secret definitions, and reconstructs only the selected local stdio
servers in the otherwise empty invocation config. Their tools are auto-approved
for non-interactive use and execute as separate trusted processes outside the
Codex shell sandbox. Doctor derives readiness requirements from the same role
allowlist. Custom unlisted MCP servers and all ambient project/user policy remain
suppressed in isolated mode. In inherited mode the native Codex catalog remains
loaded and explicit role MCP names act as documented expectations rather than
the complete tool ceiling.

Sandboxed Codex launches use scoped permission profiles with minimum runtime
reads and only the assigned repository/worktree. Sandboxed Claude launches deny
operator-home reads and re-allow the assigned root; implementers run in the task
worktree, while reviewers use a private neutral directory with the newly
materialized candidate checkout added read-only. These boundaries reduce prompt-injection exposure; they do not
turn Runner into a credential broker or sandbox the harness process itself.
