# Decision: Pinned repository references

Date: 2026-09-02

## Context

Planner, implementer, and reviewer work sometimes needs evidence from a second local
repository, such as the implementation that an unfinished replacement must
match. Requiring host access makes that possible but weakens the default
containment boundary. Copying selected files into the primary repository is
safer but creates a second, manually maintained source that can silently drift.

Runner needs a small way to expose an existing checkout as evidence without
turning repository synchronization, snapshots, or general filesystem mounts
into a new subsystem.

## Options

1. Require host access whenever a role needs another repository.
2. Copy the needed evidence into the primary repository before each run.
3. Add optional, commit-pinned local repository references to the existing
   planner, implementer, and reviewer execution profiles.
4. Build a Runner-managed clone, snapshot, and mount service with per-role
   access lists.

## Decision

Choose option 3.

An operator may configure `repository_references` as named absolute Git roots
with full commit IDs. Runner never clones, fetches, checks out, resets, cleans,
or otherwise maintains those repositories. Normal `doctor` and every eligible
harness launch resolve symlinks and require each path to be the exact root of a
clean checkout whose `HEAD` is the configured commit. References may not
overlap the primary repository, worktree roots, or one another. Git metadata
must be contained inside the declared root, so linked worktrees and external
Git directories are rejected rather than silently widening read access.

The role boundary is fixed rather than configurable: planner, implementer, and
reviewer contracts receive references as read-only evidence; probe contracts
never receive them. Custom roles follow their base contract. Codex
gets explicit read roots. Claude gets explicit additional read roots and write
denials, and Runner suppresses instruction discovery from those additional
directories. Pi references require explicit host access because Pi cannot
enforce a read-only filesystem boundary.

Reference content is untrusted evidence, not Runner or project authority.
Prompts label it accordingly. A reference exposes its entire configured root,
including ignored files, so operators should use a dedicated checkout that
contains no credentials or unrelated private material. Host-access roles may
still access anything available to the Runner operating-system account; the
reference list does not narrow host mode.

Amended 2026-09-05: the original planner/reviewer-only boundary prevented
implementers from verifying legacy API contracts even when operators had
configured the required evidence. All three execution contracts now receive
the same references. Implementation launches reuse the reference validator on
every attempt and keep reference reads separate from worktree writes. Planner
cards should identify relevant source behavior without having to copy every
possible implementation detail out of a reference checkout.

## Consequences

The common case is one optional config list and reuses the current execution
profiles and native harness controls. There is no new queue, cache, snapshot
format, synchronization state, role ACL, or compatibility path. Pin and
cleanliness drift fails immediately before model invocation, so evidence cannot
silently change between `doctor` and use.

Operators own checkout preparation and updates. Advancing a reference means
updating the checkout and its configured commit together, then rerunning
`doctor`. Ignored files are not part of Git cleanliness and remain readable;
this is an explicit operational boundary rather than an exact-tree sandbox.

## Revisit When

Revisit if operators need reproducible historical trees that are not already
checked out, references with materially different role visibility, or measured
operational pain from maintaining dedicated clean checkouts. Those needs may
justify Runner-managed snapshots or clones, but not before they are concrete.
