# ADR 0005: Local Runner core

- Status: Accepted; core implementation, R1–R5 and real-project pilots are qualified within their recorded boundaries, and PR #106 merged on 2026-10-16. The [release checklist](../runner-redesign-proposal.md#frozen-first-release-checklist) records evidence. The dated Issues extension below defines the subsequently authorized bundle; Projects/Trello views remain deferred. Publication and installation remain separate.
- Date: 2026-10-14

## Context

Project-owned identity, approval and recovery make GitHub Projects a requirement
and spread provider details through Runner's core and tests. The replacement
must make the process reliable and understandable before extending integrations.
The agreed direction is a fresh core, simple package responsibilities, explicit
test ownership and no backward-compatibility requirement.

## Decision

1. Runner owns tasks, versions, approvals, attempts and recovery in one private
   transactional local store, outside agent-writable workspaces. Use SQLite;
   the first slice must prove the selected Go driver and durability behavior
   against the existing macOS/Linux packaging targets before relying on it.
   One coordinator owns execution. A second active host, distributed queue and
   full event sourcing are outside scope.
2. Preserve continuous event/action reconciliation, Plan and Ready entry points,
   dependency-aware admission, three role contracts and independent review.
   A model call does not block unrelated deterministic work. Integration remains
   serialized per repository/base with current-base proof immediately before merge.
3. Define one default process and resolved execution policy. Persist recovery
   allowances and effective attempt settings. A transient retry repeats the same
   stage/profile; only a confirmed reviewer defect advances the configured
   implementation ladder. Missing proof or a human decision cannot become a pass
   or an automatic repair merely because a budget is exhausted.
4. Separate intake, board views and code delivery. Initial boards publish
   Runner-owned progress in one direction and cannot approve work or declare
   successful completion. Use built-in adapters and consumer-defined interfaces
   where substitution is real. Do not build a universal plugin API or loader.
5. Establish repository quality requirements at setup. Before coding, the task
   author or planner specifies behavior, expected results, evidence boundary and
   existing coverage to extend. The implementer writes the tests; the independent
   reviewer checks their adequacy and simplicity. Consolidate redundant coverage
   within the touched behavior and retain distinct guarantees.
6. Build useful complete journeys in a fresh implementation, using current code
   and tests as references for concrete invariants. Reuse only components that
   fit the new contracts directly. Do not extract a transitional Project backend
   or port old tests wholesale. Keep old and new execution from owning the same
   work during construction and cutover.
7. Ship a deliberately breaking release. A small configuration/draft importer is
   optional; importing old approvals, in-flight execution or recovery history is
   not required. Preserve old artifacts and explicitly resubmit remaining work.

The [core redesign plan](../runner-redesign-proposal.md) is the single build
specification and implementation sequence. It owns release scope, policy
semantics, the first slice's contracts and requirement-to-proof mapping. Later
slices receive detailed briefs when their prerequisites exist; this decision
does not create a parallel backlog or authorize coding.

## Relationship to existing decisions

- ADR 0001 retains its scheduling, success and integration guarantees. Local
  task identities and explicit version-bound commands replace Project item IDs
  and board-lane authorization.
- ADR 0002 retains its trust and source-completion guarantees when optional issue
  intake is implemented. That capability is deferred from the first release.
- ADR 0003's pinned, read-only reference guarantees remain applicable. Additional
  repository references are deferred; omission is not permission for broad host access.
- ADR 0004 retains three contracts and typed internal actions. Project lanes,
  the v5 schema and advanced user-authored composition are not required in the
  first release. A later composition feature must use the same coordinator.

The promoted `cortexium-runner`, architecture and operator guide now describe
the local runtime. Qualified role instructions live in the execution adapter.
R4 retires the old Project entry point and bundled skill catalog; historical
decisions and qualification records remain reference evidence. The release
checklist records completed R5 qualification and the subsequently qualified
pilots. Publication and installation/cutover require separate authorization.

## Product clarification, 2026-10-16

The owner reaffirmed that this is a new Runner, not a feature-for-feature copy.
The product target is a complete local Plan/Ready journey without a required
code host, issue system or board. GitHub remains the first optional delivery
integration and GitHub Projects the likely first optional view. Harness access
and repository verification prerequisites remain explicit requirements; this is
not a promise that a cloud model or unavailable test service works offline.

The R1–R5 qualification stopped local work at an accepted candidate; it did
not establish local integration or satisfy dependent Plans without GitHub.
R1–R5 evidence remains valid within its recorded scope, not proof that this
broader product target was complete. The subsequent brief specifies local
completion proof and combined-plan outcome before implementation.

Reassess all Runner role skills, embedded instructions, relevant operator skills
and their references against current contracts. Carry forward supported lessons
from the two research sessions and observed failures, with source and evidence;
do not import legacy schemas, permissions, extra roles or workflow machinery.
The plan's [reassessment](../runner-redesign-proposal.md#product-and-skill-reassessment)
records findings and the design boundary. No runtime, installed skill, service
or integration change is authorized by this clarification alone.

## Local completion design, 2026-10-16

The owner requested the [local completion and combined-plan brief](../runner-redesign-proposal.md#local-completion-and-combined-plan-verification-brief)
and selected configurable delivery and merge permission. The default local result
is a verified result branch; an optional configured-branch target waits for human
merge or uses explicitly enabled automatic merge. Runner performs guarded Git
operations; agents retain their existing implementation/review boundaries.

A Plan produces one combined candidate. Reviewed child inclusion can satisfy
within-plan dependencies, but final completion requires the shared approved
outcome, combined verification/review and the selected destination's receipt.
GitHub delivers that parent candidate through its existing guarded path. The brief
defines check timing, bounded repair, recovery and required proof. Implementation
was subsequently authorized in the same worktree. Its qualification record in the
plan, rather than the older R1–R5 evidence, establishes this expanded behavior.

## GitHub Issues extension, 2026-10-18

The owner requested GitHub issue requests with a distinct owner acceptance step
and a useful overview before release. The plan's
[Issues brief](../runner-redesign-proposal.md#github-issues-and-owner-acceptance-brief)
adds explicit snapshot intake and an optional one-way progress view through
separate narrow contracts. Existing local approval and Plan release remain the
only authority. A labeled issue, its comments and closure cannot authorize or
change work. The connection is independent of code delivery. GitHub Projects,
autonomous intake and remote approvals remain deferred. A FlowStack handover
requires a safe ownership boundary and explicit authorization; active execution
and historical approvals are not imported.

## Consequences

The first milestone produces a reviewed local candidate, not a merged task.
The first release adds a complete configurable core and GitHub delivery; board
views follow. An unavailable board cannot stop local work. Code-host outages
still limit fetching, publication and confirmed integration.

Local transactions remove remote partial-write states but do not make Git,
subprocesses or remote publication atomic. Durable intent, validated saved
results and conservative reconciliation remain required at those boundaries.

Revisit the storage choice if platform or durability qualification fails. Revisit
public plugins, additional roles or more workflow machinery only for a concrete
unsupported use case after the core journeys and a second board adapter work.
