# Decision: Approved outcome delivery on one plan branch

Date: 2026-09-22

## Context

Accepting individual cards does not prove that their combined result delivers
the approved outcome. Running complete validation for every card also repeats
expensive checks before the combined candidate exists. Runner already has
authenticated Project batches, independent review, retained workspaces,
protected acceptance records, serialized Git publication and owned subprocesses.
The approved overhaul must extend those boundaries, not add another scheduler
or task ledger.

## Decision

For explicitly enabled new delivery plans, use one durable issue-backed Project
parent, including CLI plans. Its canonical manifest contains the shared request,
outcome, criteria, scope, decisions, repository, destination, complete-gate
settings and exact ordered members, dependencies, implementation profiles and
selection reasons. Each selected implementation profile also binds its resolved
settings and reachable configured escalation profiles; changing a model,
reasoning, harness/access policy, tool grants or timeout under the same name
does not preserve authority. The immutable release authenticates the manifest revision
and exact child contents. Mutable delivery state uses the existing signed
action and transition fields separately. Moving the parent through review must
not release new children or invalidate its unchanged release.

Create a deterministic plan branch only after approval. Each child retains its
own branch and worktree. Card QA uses the approved shared context and focused
proof; acceptance is integrated under one plan-head owner, without a child PR.
`plan_integrated` is explicitly not delivery. It satisfies only authenticated
within-plan dependencies. External dependants wait for confirmed delivery.

When every member is integrated, the existing independent reviewer evaluates
the combined candidate against the shared outcome and engineering obligations.
Concrete failures may identify existing approved owning members through typed
failed-check references. Runner validates complete in-scope ownership and spends
the configured whole-plan rejection allowance before requeueing those members.
Missing proof, unowned work or a consequential new decision does not authorize
extra cards. Repairs start from the current plan branch and retain accepted
sibling changes, history and rejection counts.

The signed integrated plan head remains distinct from a rejected local
candidate that includes a newer destination base. Repair recovery retains both
identities; it combines accepted repairs with the retained refresh and requires
fresh whole-plan review. QA refuses unexpected remote plan changes rather than
adopting them. Publication leases the exact authenticated remote head. A base
conflict or failed final-PR CI with no reviewed owning card blocks explicitly;
it does not route the parent to an ineligible implementation lane.

After combined acceptance, Runner executes the configured complete verification
entrypoint through its owned heavy launcher. The operator-owned catalog defines
argv, timeout, source/dependency inputs and toolchain identities. The launcher
does not turn model output into proof or bypass role containment. The initial
engine integration requires explicitly approved host access and otherwise
fails readiness/verification closed. A protected acceptance record binds the
actual receipt digest, exact candidate and plan revision before one final PR.
The existing automatic-integration policy still controls delivery; only a
validated merge makes the plan delivered.

Recovery uses existing signed Project transition intents and private protected
acceptance/evidence records. An integration intent pins its member, accepted
candidate and expected head before Git mutation. An ambiguous final PR response
is read back by exact repository, branch, base and accepted candidate, including
terminal PRs; it must not create a second PR or repeat model work. Unexpected
remote state and changed authority fail closed.

## Consequences

The rollout is default off. Standalone Ready cards keep their individual-card
path. Historical planning-complete records are not relabeled as delivered
outcomes. New delivery plans require the `Runner Plan Release` Project TEXT
field and an explicitly configured maintained complete gate. Doctor diagnoses
missing prerequisites without creating fields or changing configuration.

The approved end state also needs previewed operator migration, amendment and
cancellation controls. Those controls must preserve historical authority and
recoverable work; ordinary Project/body edits are not amendments. The initial
core milestone is not permission to enable live delivery before these controls
and the rollout have been independently reviewed.

The first core path executes complete verification for each newly accepted
combined candidate and requires already-prepared declared dependencies. The
next verification bundle must wire applicable historical receipt reuse into
the production acceptance path and prepare dependencies under the same owned
claim/deadline before the stable dependency observation. Existing applicability
helpers alone do not complete those production contracts. The FlowStack trial
must not proceed by omitting dependency inputs or treating changed inputs as
the same execution.

The reserved `runtime_paths` catalog field is rejected when nonempty until
bounded runtime-artifact collection is implemented and verified. Binding an
executable wrapper alone is not proof of the browser engine or runtime it loads.

## Alternatives rejected

- Child PRs followed by another plan PR: duplicates publication and obscures
  which outcome has actually shipped.
- Treating integrated children as Done: admits external dependants against
  work that has not reached the destination.
- Putting mutable delivery state into the immutable release: invalidates
  unchanged child authority during normal progress.
- A separate scheduler or local plan journal: duplicates existing Project
  authority, admission, transition and recovery machinery.
- A mandatory specialist chain or nested critique loop: adds model work and
  control states without being necessary for this delivery boundary.

## Verification

The early milestone uses production `RunCycle`, authorization, workspaces,
native Git, review parsing, the real complete launcher and PR publication,
substituting only paid-provider and GitHub transport responses. It covers two
dependent members, card repair, a broken combined journey, lost integration and
PR responses, restart reconciliation, and changed-authority refusal. Full
integrated tests, independent review and a safe audit-first trial remain gates;
fixture success does not establish improved UI judgment or production cost.
