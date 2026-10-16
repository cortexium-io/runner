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
[core redesign plan](docs/runner-redesign-proposal.md). The local replacement is
promoted to `cmd/cortexium-runner`; packages live in
`internal/runner`. Feature scope is frozen after `aedc8bd`. The plan's
[five release bundles and finish line](docs/runner-redesign-proposal.md#frozen-first-release-checklist)
are the release work source. R1 delivery, R2 backup/restore, R3 operator
qualification, R4 promotion/packaging and R5 final qualification/cutover rehearsal
are complete within their recorded evidence boundaries. The owner subsequently
added [real-project pilots before merge](docs/runner-redesign-proposal.md#real-project-pilots-before-merge).
The owner has authorized completion of the remaining release work: documentation
reconciliation, bounded check-environment fixes, project pilots, renewed delivery
and platform qualification, main reconciliation and PR preparation. Genny uses
an isolated copy of PR #576 for verification/adoption and demonstrated gaps;
do not duplicate or take ownership of its existing implementation. Publication and
installation require separate explicit authorization. No further implementation
bundle is implied by the completed R1–R5 checklist.
The owner has also authorized the [local completion and combined-plan brief](docs/runner-redesign-proposal.md#local-completion-and-combined-plan-verification-brief),
including configurable local delivery/merge permission. Implementation is now
authorized in this worktree; publishing and installation remain separate.
Implement only those release obligations or concrete fixes to the frozen contract;
additional features require explicit owner direction. Do not create a separate
task ledger. Architecture and the [operator guide](docs/operator-reference.md)
describe the local runtime. Historical qualification records may name retired
commands and suites; do not restore Project coupling from them.

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

Feature parity with the old Runner is not the acceptance target. Follow the
plan's [product and skill reassessment](docs/runner-redesign-proposal.md#product-and-skill-reassessment):
the target is a complete local journey without a required code host, issue system
or board. GitHub is the first optional code-delivery integration; GitHub Projects
is the likely first optional view. The current bundle adds local completion and
dependent-plan delivery under that brief; its qualification evidence defines the
verified boundary.
Reassess role instructions, operator skills and research against the new contracts;
do not inherit their legacy workflow, permissions or result schemas. This direction
does not authorize implementation or changes to installed skills/services.

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
`go test -race -timeout 15m ./...` and `go vet ./...`, with platform/release checks at release.
