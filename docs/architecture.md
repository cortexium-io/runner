# Runner architecture

Runner is a local event-and-action engine. The private SQLite store is authority;
GitHub is an optional code host. A board can later project state through a narrow
adapter, but cannot grant approval or own the lifecycle. [ADR 0005](decisions/0005-local-runner-core.md)
and the [redesign plan](runner-redesign-proposal.md) define the accepted replacement.

## Responsibilities

| Package | Responsibility |
| --- | --- |
| `cmd/cortexium-runner` | Process signals, version and CLI entry point. |
| `internal/runner/cli` | Commands, exact previews and attention-first status; no scheduler. |
| `internal/runner/config` | Strict configuration, repository quality agreement and policy validation. |
| `internal/runner/core` | Task versions, approvals, evidence bindings, observations and action types. |
| `internal/runner/store` | Private transactional persistence, reservations, leases, effect claims and freshness guard. Store format 16 rejects earlier formats. |
| `internal/runner/engine` | One coordinator derives safe actions, admits within limits, launches bounded work and reconciles results. |
| `internal/runner/execution` | Codex capabilities, contained role/check execution, optional offline check images, structured results and pinned role instructions. |
| `internal/runner/workspace` | Task workspace binding, exact candidates, base refresh and integration evidence. |
| `internal/runner/github` | Narrow draft, base, CI and merge operations; no Project dependency. |
| `internal/runner/backup` | Drained same-host snapshots, strict manifest validation and guarded restore. |
| `internal/workspace`, `internal/subprocess`, `internal/securefs`, `internal/sandboxpath` | Reused Git, process, private filesystem and path protections with their owning tests. |

There is no second scheduler, public plugin framework or installed role-skill
catalog. Interfaces belong at actual execution/delivery boundaries; configuration
does not select arbitrary runtime implementations. The CLI delegates state
transitions to the engine/store rather than editing lifecycle fields.

## Authority and progress

Ready authorizes one specified task after an exact-version preview. Plan approval
authorizes planning only; release separately authorizes the shared outcome,
exact children and their dependency graph. Content, quality and repository/base
bindings prevent an old approval from silently authorizing changed work.

The coordinator derives every currently safe action. Dependencies and resource
conflicts determine safety; configured parallelism limits admission. Long-running
model work does not block unrelated deterministic reconciliation. Integration
serializes by repository/base, observes current base and CI and validates retained
evidence before a write or completion.

Implementers work in isolated task worktrees. Candidate construction uses
config-free privileged Git and checks identity, object storage, index and tree.
Checks run in a disposable source copy. Completed source with honest implementer
proof gaps can reach audit after every required check passes; only independent
review can establish acceptance. The reviewer first audits evidence against
the exact candidate; focused verification is a separate bounded action only when
needed. A concrete defect can consume repair allowance; missing evidence alone
does not justify a code rewrite. Repository policy and the approved brief own
test expectations. Named quality checks select `task`, `completion` or `both`
(default); Ready checks every applicable obligation and parent repairs/refreshes
also recheck child guarantees. Child evidence is labelled with its historical
candidate and does not substitute for required combined checks.

Quality checks default to native Codex containment. An owner-approved exact local
Docker image can instead supply offline tools/dependencies and container-private
loopback. The existing disposable source-copy boundary and one execution owner
remain; image root is read-only, host/external networking is absent and no Docker
socket enters the container. The process record pins the container name and local
daemon endpoint before launch. Cancellation confirms its removal; recovery and
backup retain ownership while either process or container is unresolved.
A model result also requires a successful terminal protocol event; exit zero and
a result file cannot establish completion after an interrupted stream.

## Delivery and recovery

Local delivery creates a verified result branch and a provider-neutral completion
receipt. Optional local base delivery uses a guarded fast-forward under human or
automatic merge permission; targets checked out in any worktree are refused.
Owned ref intents precede Git compare-and-set transactions; dispatch is consumed
before the effect, and restart observes uncertain writes rather than replaying.
Plan-head inclusion and final completion update the related tasks atomically.

Plan children start on the admitted private Plan head, including prerequisites.
Independent child work remains concurrent; inclusion refreshes and serializes
against that head. Inclusion releases only dependencies in the same Plan. The
combined candidate receives completion checks and independent outcome review,
with bounded parent repair when needed. One accepted parent candidate enters the
same destination path as Ready. Final receipt settles parent and children together.
Shared scope, membership and quality stay immutable after release.

GitHub delivery publishes one draft, verifies/refreshes the current base,
synchronizes that draft, assesses exact required CI and performs bounded repairs. Changed source
invalidates acceptance and runs the required verification again. Unknown remote
outcomes are observed, not blindly repeated.

Human integration confirms an ordinary merge receipt. Explicit merge or applied
automatic policy additionally admits one guarded remote merge. Host protection,
exact candidate/base/CI bindings and a durable consumed dispatch constrain writes.
An API success alone is not completion. External dependencies need final delivery;
internal Plan dependencies require exact private inclusion.

Attempts retain selected profiles, counters, processes, results and evidence.
Restart reconciles ownership before admission. Unknown ownership blocks; a saved
valid result can continue, while a repeated model invocation requires explicit
permission. Drain stops admission and lets active work settle. Revocation retains
history and cannot undo a dispatched remote effect. An operator may explicitly
classify a complete failed required check as an in-scope source defect with
`task repair-checks`. It retains the task, source and receipts, uses the existing
repair/invocation allowance and pinned profile, and requires fresh checks and
independent review. Unavailable checks, changed source or unresolved ownership
cannot authorize that repair; retry/resume retains its diagnostic assignment.

Backup requires drained, idle authority and captures the matching config,
repository, workspaces and evidence. Restore uses the same binary, host/user and
original absent paths. The original external guard establishes freshness; stale
or lost freshness allows inspection only. Restore cannot revive consumed writes.

## Verification ownership

Core/store tests own bindings and transactional invariants. Engine journey tests
own lifecycle, admission, interruption and delivery behavior. Adapter tests own
provider parsing and process/Git boundaries; CLI tests own operator commands.
Low-level safety suites remain beside the reused primitives. Release scripts test
the actual executable and archives without a second orchestration harness.

Use `go test -race -timeout 15m ./...` and `go vet ./...`. Model behavior is
qualified separately by paired live cases in `internal/runner/execution`.
Historical Project workflows and retired tests remain in Git history; they do
not define the supported command surface. See the [operator guide](operator-reference.md)
for commands and the plan's release evidence for qualification boundaries.
