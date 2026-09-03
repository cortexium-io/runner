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
[ADR 0003](decisions/0003-pinned-repository-references.md).

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
| `internal/metrics` | Append-only attempt and fixed-name stage events, harness-reported usage values, durable local history, and aggregates | Prompts, transcripts, raw harness output, free-form stage payloads, or estimated cost |
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

Configuration v2 has no runtime overlay or fallback profile. Privileged commands
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
work; `run`, `status`, and `metrics` operate it; and `role` manages extensible role profiles.
`init` is idempotent for an existing config and owns GitHub Project field/status
synchronization, local prerequisite checks, and bundled skill installation.
`doctor` owns static config validation and live readiness inspection. Its
explicit `--probe-harnesses` mode additionally proves real authentication,
model invocation, and structured output once per distinct execution profile;
normal doctor remains non-billable and does not call a model.

The command package attaches an optional metrics observer and history reader to
the engine. The engine emits start and completion events for every card or
interactive planning attempt. It also emits fixed-name stage events around
workspace and repository preparation, harness execution, result validation,
workspace verification, Project transitions, and PR publication.
Execution adapters parse only counters exposed by the native harness. Events are
appended to a runner-keyed JSONL file in the user configuration directory. The
event boundary keeps telemetry failure non-fatal to workflow execution and
preserves unfinished attempts and stages after a process interruption. Stage
events carry identity, timing, enums, and usage only. The history deliberately
excludes prompts, transcripts, raw responses, command arguments, raw local
errors, and free-form stage payloads. `metrics` reads and aggregates this store,
including completed harness-invocation counts and exact saved-result resumes for
planning and implementation. `status` presents a compact aggregate and the
current admission decision. GitHub
cards receive only fixed bounded execution, recovery, and QA classifications;
detailed usage and model-authored evidence remain local.

Execution adapters map allowlisted adapter-owned structured failures and
Runner-observed failures to a stable failure class plus `manual` or `none`
retry disposition. Model-authored text and raw stdout/stderr never create
provider-capacity, session-limit, retry, or retry-timing authority. Opaque
harness failures stay unknown and are never automatically retried. The
optional rolling admission budget is evaluated from local history before agent
claims. Exhaustion pauses all new claims, including QA, without canceling
in-flight attempts; PR reconciliation still runs. Reported-token and cost
ceilings fail closed when attempts are unfinished or lack the required reported
usage, and harness-time ceilings fail closed for unfinished attempts.

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
cancellation; it terminates descendants and reaps the direct process before
returning. Structured-result inputs use one `internal/securefs` artifact
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
publishes a bounded correction, clears the unusable checkpoint, and sends an
explicit retry through implementation. Operator-supplied retry feedback also
clears the checkpoint before changing the card, while unchanged Runner-side
post-processing failures retain it.

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
source-and-evidence audit that cannot run dynamic checks. All concrete
unresolved questions enter a fresh focused-verification invocation, even when
another key already failed; that invocation sees no resolved proof obligations.
Runner merges those observations and derives the verdict.
Operator-selected `standard` or `high`
task sizing changes only decomposition and specificity for implementer and
reviewer roles. Runner never infers capability from model names, and the shared
plan schema and acceptance rigor remain unchanged. A claim records `Planning`,
`Implementing`, or `Reviewing` in the visible `Runner Activity` field. Accepted
QA changes that activity to `Awaiting human review`, or `Waiting for CI / merge`
when automatic merge is enabled, while the card remains `PR Ready`. Ordinary
pull-request reconciliation preserves that waiting signal; leaving `PR Ready`
clears it. A blocked transition records its originating agent lane in the hidden
`Runner Phase` field; `retry` uses that explicit destination and never guesses
from a summary string. The separate hidden `Runner Transition` field is a
fail-closed lock around non-atomic Project updates. Authorization rejects locked
cards, completed locked state is resumed in place, and partial state is returned
to assessment. Phase and activity remain bound to authenticated action state;
the lock is checked independently.

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

Indexed gitlinks form recursive manifest nodes. Their index path and recorded
object ID are always present; initialized nodes add their own HEAD, index/status,
protected metadata, and nested gitlinks. Capture never fetches, initializes, or
copies a submodule, and rejects an indexed path that is missing, symlinked,
cannot be opened through the pinned parent, or is deinitialized but non-empty.
Runner constructs a clean task-branch commit before Agent QA through a
privileged Git profile that pins the linked-worktree administration, index, and
common object store. The engine records that candidate HEAD and tree in the
pre-review manifest and compares the complete manifest around Agent QA before
entering push, pull-request publication, or worktree cleanup paths. An accepted
unchanged candidate receives an exclusive private publication record keyed by
its commit and binding the item/content identity, commit/tree, approved base,
repository, and full destination branch ref.
Publication replays that record under a sanitized privileged Git profile,
re-fetches and compares the approved base, re-resolves the accepted tree,
refreshes Project authority, validates the configured remote repository, and
pushes only the recorded commit OID to the recorded full ref. Base refreshes
remain local until their resulting tree completes implementation and QA and
receives a replacement publication record.

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

Optional `repository_references` are a fixed extension of planner and reviewer
profiles, including custom roles with those contracts. Implementer and probe
profiles never receive them. Normal doctor and every eligible launch resolve
symlinks and verify that each reference is an exact, non-overlapping Git root
with a clean tracked/untracked state and `HEAD` equal to its full configured
commit. Runner does not mutate or synchronize reference checkouts. Codex gets
explicit filesystem reads; Claude gets repeated additional-directory reads,
explicit write denials, and disabled instruction loading from those additional
directories. Pi requires `host` for references because it cannot enforce a
read-only root. Host mode remains unrestricted and is not narrowed by the
reference list. Reference content is labeled untrusted evidence, and the whole
root, including ignored files, is readable; operators use dedicated checkouts
when that distinction matters.

A sandboxed Codex or Claude implementer or reviewer receives Runner's bounded
development profile by default. Package commands run inside the native
filesystem sandbox; implementer network access is limited to the npm registry
and loopback. The filesystem profile exposes the assigned workspace, minimum
system runtime files, and the implementer's npm cache instead of the operator's
home directory. The Runner-owned `runner_browser` MCP definition is pinned,
headless, temporary-profile, loopback-only, external-DNS-disabled,
telemetry-free, and independent of ambient harness MCP configuration. A role
may explicitly disable this profile. In inherited mode Runner adds this server
alongside the ambient MCP configuration.

Pi implementer and reviewer roles receive the same three browser operations
through a temporary Runner-generated extension that forwards to the pinned
browser server. Ambient Pi extensions stay disabled in isolated mode and load
only after an explicit inherited-configuration opt-in. Navigation through the
Runner browser remains loopback-only. This boundary does not change Pi's
explicit host-access requirement for shell and edit tools.

A Codex role can additionally add an explicit named MCP allowlist. Runner reads the native
Codex MCP catalog before launch, rejects missing, disabled, remote, or
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
worktree, while reviewers use a private neutral directory with the repository
added read-only. These boundaries reduce prompt-injection exposure; they do not
turn Runner into a credential broker or sandbox the harness process itself.
