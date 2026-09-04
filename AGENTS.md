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

Use minimum sufficient complexity. Prefer a small explicit coordinator and
existing authenticated Project state over additional schedulers, queues, state
journals, compatibility layers, or abstractions without a demonstrated need.
Protect behavior with focused tests and review the complete diff before
finishing.
