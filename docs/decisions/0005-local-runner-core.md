# ADR 0005: Local Runner core

- Status: Accepted; phases 1 and 2 implemented through the local operator interface. Delivery, release qualification and optional integrations remain pending.
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

The existing architecture, operator documentation and bundled skills continue to
describe the current runtime. Replacement assignments use the plan's role and
testing contracts; their bundled instructions must be qualified as the relevant
slice lands. Do not change the current runtime's behavior as a documentation side effect.

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
