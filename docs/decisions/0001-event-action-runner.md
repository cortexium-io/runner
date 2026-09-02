# ADR 0001: Event-and-action Runner

- Status: Accepted
- Date: 2026-09-02

## Context

At the time of this decision, Runner described and executed work in polling
cycles. The cycle was a useful observation boundary, but it was also an
execution barrier: after claiming agent work, continuous mode waited for every
harness process in that cycle before observing another Project or pull-request
event. This left safe QA, planning, pull-request reconciliation, and newly added
work idle behind an unrelated implementation.

The previous documentation also tied dependency satisfaction to same-batch
planning provenance and a generic `Done` lane, and treated both merged and
closed-without-merge pull requests as successful completion. Those constraints
do not express the intended product.

## Decision

Runner is a local, polling event-and-action engine:

1. Polling observes external state and derives actions. A poll is not a batch
   whose actions must all finish before the next observation.
2. Runner starts every action that is currently safe and admitted.
3. An action is safe when its authority is valid, its dependencies have
   succeeded, and its required resources do not conflict with an in-flight
   action.
4. Configuration may impose global, provider, budget, or other admission
   limits. Limits reduce admitted actions; they do not define workflow order.
5. Deterministic actions such as state reconciliation do not consume harness
   capacity and must not wait behind unrelated model work.
6. Dependencies are first-class item relationships, may cross planner batches,
   and are satisfied only by successful outcomes. A merged pull request is a
   successful implementation outcome; a pull request closed without merge is
   not.
7. Implementations, QA, pull-request creation, and human review may proceed in
   parallel when their dependencies and resources allow it.
8. Integration is serialized per repository and base branch. Immediately before
   merge, Runner updates the candidate against the latest base. A clean update
   reruns required verification and proceeds; a conflict returns the card to
   implementation with concrete conflict context.
9. Stale branches are refreshed lazily at integration by default. Eager refresh
   on every unrelated merge is unnecessary churn.
10. A human moving a sufficiently specified card to `Ready` directly authorizes
    implementation. Moving a goal or request to `Plan` asks the planner to stage
    a dependency-aware proposal for human approval.
11. Runner is pre-stable. Replacements remove obsolete behavior and contracts;
    no backward-compatibility layer is required.

## Resource model

The target scheduler derives explicit resource keys. The first required keys
are:

- `item:<project-item-id>`: at most one action mutates a card at a time.
- `workspace:<repository>/<branch>`: at most one action mutates a task workspace.
- `integration:<repository>/<base>`: at most one candidate integrates into a
  given base branch.

Additional resource keys should be added only for observed correctness needs.
Provider limits belong to admission control rather than this correctness lock
set.

## Intended event flow

```text
card enters Plan
  -> planner proposes cards and dependencies
  -> human approves proposal
  -> approved cards enter Ready

card enters Ready and dependencies succeeded
  -> implementation
  -> Agent QA
  -> pull request ready for human/integration policy

integration requested
  -> acquire repository/base integration resource
  -> update from latest base
  -> resolve conflict through implementation, or rerun QA
  -> merge
  -> mark successful outcome and release dependants
```

## Delivery sequence

This direction is implemented in reviewable slices:

1. Separate observation from harness completion so continuous mode keeps
   reconciling unrelated events while actions are in flight.
2. Replace same-batch/`Done` dependency checks with first-class successful
   outcomes, including distinct merged and closed-without-merge semantics.
3. Introduce explicit resource claims and work-conserving action selection.
4. Move base refresh to a serialized integration action and remove eager
   refresh behavior that is no longer needed.
5. Make the `Plan` and direct-`Ready` intake paths obvious in CLI and operator
   documentation.
6. Add one native harness-config switch that uses an existing harness setup
   while Runner still injects its required result, access, and workspace
   contracts.

As of 2026-09-02, all six slices are implemented. Continuous mode maintains local
in-flight resource ownership, continues polling at the base interval while
actions run, excludes conflicting items from reconciliation, and performs
interruption recovery only when no local action remains. Dependencies may cross
planning batches and require a Runner-authenticated successful state; merged
PRs provide that state, while closed-without-merge PRs move to `Blocked`.
Harness selection reserves item and repository/branch workspace resources and
continues past conflicts to fill available global capacity. Pull-request
reconciliation owns `integration:<repository>/<base>` independently of harness
capacity. GitHub's enabled auto-merge state is the restart-stable claim: Runner
recovers an existing owner before considering item order, permits one owner per
repository/base, and disarms duplicate claims. QA publication only queues a PR;
reconciliation lazily compares the selected candidate with the latest base,
returns updates or conflicts through implementation and QA, and requests
automatic merge only for a clean reviewed candidate. Manual-review PRs and
rework requests are not eagerly refreshed. Humans can enqueue a planner request
or sufficiently specified implementation card with `add plan` or `add ready`
while continuous mode is active. Direct ordinary cards in either lane are
converted to issues and authenticated from their exact observed snapshot before
claim. Native harness configuration is selected independently from access:
`inherit` loads the existing harness setup while Runner still injects its
required result, access, and workspace contracts. New configurations accept one
shared `init --harness-config` value. Existing configurations accept one atomic
`role edit --all --harness-config` operation for the three built-in role
contracts; complete-config validation rejects the change before replacement if
any resulting harness/access combination is invalid.

## Consequences

Continuous mode needs an in-flight registry and completion channel, while
one-shot mode can retain synchronous completion. Interruption recovery must not
mistake a locally running action for abandoned work. Eventual consistency is
expected: polling latency remains, but unrelated events no longer wait for the
slowest harness action.

This ADR is the target architecture. Existing code and docs that describe
cycle-wide barriers, same-batch-only dependencies, or closed pull requests as
successful are migration surfaces, not competing decisions.
