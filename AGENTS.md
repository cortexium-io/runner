# Runner working agreements

## Repository authority

This repository is the source of truth for Cortexium Runner. Parent-directory
documents and sibling Cortexium repositories are not Runner architecture or
product authority unless a document in this repository links to them
explicitly.

Use these sources in descending order of authority:

1. Explicit user direction for the current work.
2. This file.
3. Accepted decisions in `docs/decisions/`.
4. `docs/architecture.md` and the implemented contracts and tests.
5. The remaining Runner documentation.

The accepted replacement direction is [ADR 0005](docs/decisions/0005-local-runner-core.md).
Its build specification and designated implementation sequence are in the
[core redesign plan](docs/runner-redesign-proposal.md). Phases 1 and 2, including
the local operator interface, and the first three phase 3 bundles (draft publication,
current-base verification and bounded refresh, remote draft synchronization) are
implemented in `cmd/runner-next`;
their qualification and the remaining delivery/release boundaries are recorded
in that plan. Continue from that evidence only when the next implementation is
authorized, not from a separate task ledger.
`docs/architecture.md` and the existing operator guides describe the current
Project-backed runtime until the replacement lands. Existing code is evidence
for required guarantees, not authority to restore superseded Project coupling.
The documentation decision does not itself authorize implementation.

When implementation and documentation disagree, identify and resolve the drift
inside this repository. Do not preserve obsolete behavior for compatibility;
Runner is pre-stable and replacement work should remove superseded paths and
update their consumers.

## Product direction

Runner is a local event-and-action engine. It should observe state, derive every
currently safe action, and start as many as configuration admits. A long-running
harness action must not block unrelated deterministic reconciliation.

Dependencies and resource conflicts define safety. Global, provider, or other
configured limits are admission controls, not workflow lanes or batches.
Implementation, review, and pull-request preparation may proceed concurrently;
integration into one repository/base branch is serialized and refreshes the
candidate against the latest base immediately before merge.

Humans can enter work in two direct ways:

- `Plan`: ask the planner to propose a dependency-aware set of cards for review.
- `Ready`: authorize one sufficiently specified card for implementation.

## Engineering standard

Use minimum sufficient complexity. The replacement has one explicit coordinator
and one private transactional local store. External boards are optional views;
intake and code delivery have separate, narrow adapter contracts. Do not add
another scheduler, distributed queue, full event-sourcing system, compatibility
layer or public plugin framework without a demonstrated need.

Before implementation, define the supported behavior, representative expected
results, verification boundary and existing coverage to extend. Repository
policy owns the quality requirements; the task author or planner defines the
verification brief; the implementer writes the change; the independent reviewer
checks adequacy and unnecessary complexity. Extend or consolidate the owning
suite when sensible. A test needs a credible defect it detects, not merely a
coverage target. Existing sufficient evidence can mean no new test.

Follow the redesign plan's testing policy for replacement work. Do not copy the
old test suite or change shipped role prompts merely to make the documentation
look consistent; qualify the replacement instructions with their owning slice.
Review the complete diff before finishing. Implementation gates remain
`go test -race ./...` and `go vet ./...`, with platform/release checks at release.
