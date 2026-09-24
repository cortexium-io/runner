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

Previewed migration creates only the exact release TEXT field and activates the
existing operator-selected complete gate after quiescence and exact config/Project
revalidation. Previewed cancellation fences the signed parent while retaining
members, work, release authority and history. Neither operation drains or starts
work implicitly, overwrites intervening operator edits, or closes a published PR.
Migration treats structurally complete legacy Done batches only as inert
administrative history, even when old action assertions are stale or absent.
It does not renew their execution/dependency authority, relabel them delivered,
or rewrite historical QA/PR evidence. Completed delivery manifests retain
authenticated historical authority: the signed terminal parent, original
immutable release, exact member contracts and signed integrations must remain
intact. A status-only move from integrated Backlog to Done after issue closure
does not invalidate that proof. Current model/profile or complete-gate changes
do not undo delivery, but cannot renew execution authority or pending acceptance.
Retired rows remain authenticated inert history, never successful dependencies.
Missing membership, altered evidence or nonterminal work still prevents rollout.
No historical assertions, counters or acceptance records are rewritten by this
read-only distinction.
Amendments preview the complete contract and advance an explicit
ordinal revision. Changed local contracts invalidate the old/new dependency
closure; shared changes conservatively invalidate all members. Exact original
acceptance is carried only for unaffected members with historical provenance.
The parent always needs renewed delivery acceptance. Existing protected parent
evidence holds the exact approved before/after intent; a `plan_amending` fence
precedes partial writes and restart finishes only that intent before admission.
Counts, history and integrated code are retained, not reset or deleted.
Membership is append-only history within that one manifest: additions adopt exact
open, unapproved Assessment issues; removals explicitly retire existing rows.
The signed union still includes every active and retired row. Original staging
batch sizes remain immutable provenance, while the revision-scoped manifest and
release authenticate exact current membership. Retired scope cannot execute,
satisfy dependencies, reactivate or become Done. The preview identifies retained
accepted changes; retiring a row never removes its integrated code. A remaining
dependent needs an explicit contract amendment, and an entirely retired plan
must be cancelled instead. Ordinary Project/body edits are never amendments.
The core and operator
milestones are not permission to enable live
delivery before all required controls and the rollout have been independently
reviewed. Eligible observed command failures use one durably spent existing
reviewer audit to identify exact approved owning-card repairs, within the parent
QA rejection allowance. Setup, timeout, provenance and unresolved-cleanup failures
block without classification; missing ownership or dynamic proof requires input.
Production-entrypoint fixtures cover failed gate, owned repair, new acceptance,
and one final PR, plus interruption before/after classification and refusal to
replay uncertain work. Unresolved classifier processes retain their workspaces
and quarantine admission. This evidence is deterministic, not a live UX/cost claim.

The heavy launcher supports one explicit dependency preparation command under
the same claim/deadline, before the stable dependency baseline. Source, authority,
configuration, external runtimes and undeclared worktree paths remain pinned;
only declared untracked dependency roots may change. Reviewed non-executable
cache exclusions do not omit installed executable dependency bytes. Preparation
does not constitute passing check proof.

The launcher can reuse independently protected historical receipts when freshly
observed applicability still matches; the original execution remains historical.
The catalog can additionally require the exact current candidate. Parent
acceptance supplies prior proof only from protected coordinator progress;
recovery preserves the original heavy receipt and separately records the renewed
current-candidate guard. The immutable publication record is not overwritten.
Pending-merge reconciliation observes exact proof applicability without running
a model or gate; changed proof returns to admitted QA while the action remains
authorized and the PR is still pending. Lost authority fails closed and requires
operator coordination of any already-armed external auto-merge; this is not a
continuous atomic guarantee over GitHub state. Confirmed exact merges
complete before live checkout/tool requirements. Real Git/command fixtures cover
these recovery and refusal boundaries. Live rollout remains pending independent
review and the approved trial; it must not omit dependency inputs or treat
changed inputs as the same execution.

Literal `runtime_paths` bind complete declared external installations, names,
modes, content and internal links using bounded streaming. Doctor uses the same
collector without executing preparation or checks. Binding an executable wrapper
alone is not proof of the browser engine or runtime modules it loads. The real
trial catalog's runtime collection cost/readiness must be measured before rollout.

Authenticated parent review names the configured post-review complete gate.
Product-and-engineering acceptance is not delivery and does not claim that an
unrun gate passed. Merely pending that gate does not demand duplicate full
validation during QA; explicit approved pre-review proof obligations still hold.

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
