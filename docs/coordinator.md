# External coordinator contract

Runner owns the configured delivery process. An external chat owns understanding
the user's intent, submitting authorized work and reporting results or decisions.
It must not become a second worker or scheduler when something fails.

## Mobile evidence intake and native gate

The candidate-evidence and independent image-review version of
`deliver-planned-frontend` is active on Black Magic and Mega Mini. Its source,
fixed Chromium/WebKit dependency, protected evidence contracts and closed native
gate were qualified before activation. Normal product implementation, regression
checks, source review and draft publication may proceed in the complete process.
Native Safari trusted input and safe host isolation remain unqualified; the
mandatory downstream gate refuses merge preparation even after an approving
image judgment. Activation does not establish Safari or repaired-product proof.
Do not waive that gate or substitute the older process definition.

Stage the approved review
through the declared `stage-review-evidence` command process, then supply its
returned `review_evidence` artifact ID to the complete frontend process. The
operator binds the stager executable and read-only reference directory on the
execution host; restricted intake never receives arbitrary paths, commands or
artifact-store write access. The pinned report is
`5a1d26b5c61b08290947e806f3aee234a618688467fd1220cbd7354672c71382`.
Only that report and its directly linked flat PNG/JSON evidence are staged; the
archive manifest marks them as historical reference, never candidate proof.

The installed restricted calls are `runner_run` followed by
`runner_result(wait: true)` (and optional `runner_status`). Submit `stage-review-evidence` once with a stable request ID
and no inputs. Rejoin its result and use the returned artifact ID, not a private
filename. Submit `deliver-planned-frontend` once with another stable request ID,
the exact accepted `source_ref`, and declared inputs `request`, `package`,
`tests`, `benchmark` and `review_evidence`. The configured frontend checker
interprets `tests`; the process does not declare a separate `frontend_tests`
input. These are process
inputs, not permission to perform product checks in the external coordinator.
The actual definition declares its required inputs; no old frozen run changes.
Preserve saved merge/production decisions and rejoin the same run on disconnect.

The portable `runner-intake` launcher still takes `--execute --cli PINNED_CODEX
--config RESTRICTED_CONFIG --auth LOCAL_AUTH --token LOCAL_OWNER_TOKEN --request
REQUEST_FILE --deadline RFC3339`. Its restricted config must expose only the
three control tools above. Local authentication is linked, never copied between
hosts or staged into review evidence. This maintenance does not execute an
intake or create a product run.

## Producer upgrade and retained source boundary, 12 November 2026

The mobile run admitted under policy
`3d5f16c27943b9f1b8ae63a71718478e0f096df611739162e58d875303b41ee0`
retains that operator policy and its frozen process. A prepared browser-producer
update is not a supported hot swap. The operator hashes the resolved configuration,
including its immutable browser image. Reconciliation of an existing operation
requires its saved policy. Existing predecessor handles authenticate the accepted
private journal and exact public receipt. Readiness alone is not a migration grant.

Card-owner work is source implementation; its patch/summary and internal source
pin can accurately report unexecuted browser coverage. Checks, source review and
built-image qualification belong to later stages. A source-only card may finish,
but publication is contingent on its normal card and source-review outcomes.
Before publication, card pins and partial work are preserved inside Runner and
are not accepted publication handles. No configured operation rewrites or skips
an accepted card to accommodate a producer change.

For a settled failure, normal configured retries/corrections may apply only to
their exact eligible failure, within the original completion reserve. An explicit
within-budget retry is root-only and preserves frozen definitions. Correction
recovery requires an exhausted eligible returned source rejection; its forensic
preservation option applies only to positively owned blocked source cleanup.
Collection recovery applies only to its recognized evidence-collection failure.
None transfers unpublished card pins to a new policy/process. Do not manufacture
a receipt or copy source into a substitute worker. A failed run can remain retained
without losing its accepted pins or authorizing another execution.

After an accepted draft publication exists, retained adoption requires the exact
unaltered public publication receipt, original run and publication key, policy
hash, source/patch artifacts and immutable PR/head/base. The owning journal and
live owned PR/ref must still match. A configured `retained_publication_transition`
durably claims one new run/attempt and strips prior build/qualification authority;
it never reuses approvals. The existing `deliver-planned-retained-frontend`
definition lacks the independent experience review and mandatory native suffix.
It cannot replace the current complete mobile process unchanged. The smallest
post-publication proposal is a static retained definition using its existing
`source-adopt` admission, followed by the normal source checks/review/publication,
fresh built-image qualification, authenticated experience export, independent
read-only experience review and mandatory native gate before protected preparation.
It must add the declared `review_evidence` input, the existing feedback bridge and
shared qualification/visual correction counter. The configuration validator must
recognize that exact retained definition with the same closed suffix; its current
ID restriction accepts only `deliver-planned-frontend`. This is bounded template
and validation work using existing authentication, not a general migration API.
Routine authoring/qualification of that static definition is within approved
maintenance; it does not inherently require another human approval merely because
configuration is involved. This observation change prepares the proposal rather
than installing or dispatching it. Orchestrator owns the exact settled-state,
publication-transition and execution/reserve decision under the existing authority.

The existing same-run `qualification_transition` is narrower: a settled,
nonretryable browser **admission** failure, exact accepted publication, failed
qualification key and report SHA, original policy and PR/head/base. It cannot
upgrade a card-stage run or repair missing scenario/vendor/native capability.
There is currently no general supported policy migration for that frozen run.
If publication never occurs, no retained-publication route applies. The minimal
remaining decision concerns the actually retained state once settled. Existing
eligible same-run recovery can preserve and continue its exact failure, but cannot
upgrade policy or skip a card. Without a publication handle, a new admission path
for unpublished pins would change the authentication/source-transfer contract and
needs a concrete bounded design/authorization before implementation or execution.
No receipt reconstruction, private-store writes, automatic fresh budget or general
migration mechanism follows from a static process proposal.
A settled card deadline failure with captured but unaccepted source has no
publication handle and is not one of those eligible recovery states. Remaining
family headroom alone does not authorize a retry, pin transfer or failure reset.
Any new bounded preservation/admission contract needs its exact source/ownership,
spent-budget, idempotence, settlement and downstream-gate rules agreed first.

## Coordinator boundaries

The coordinator may:

- Clarify a material missing decision and resolve the target project/process.
- Submit a concise request, original acceptance conditions, relevant file/line
  references and the process's declared inputs. Keep reusable guidance in roles.
- Call `runner_run` once with a stable request ID. Rejoin with `runner_result`
  and `wait: true` after a disconnect; a client timeout never authorizes a new run.
- Supply the agreed checkpoint as `source_ref`. Subsequent dependent work can
  select the latest accepted Runner candidate commit; do not submit against an
  older base and describe separate branches as combined integration.
- Read compact status when asked and report actual completion, resource usage,
  missing proof and cleanup/settlement limits.
- Present an unexpected blocker, its consequence, a recommended resolution and
  the specific user decision required. Retain the existing run identity.

It must not edit product code, run product tests/builds, integrate patches, launch
repair agents, alter configuration, change permissions, increase budgets, retry
or bypass a failed gate unless the user explicitly authorizes that operator action.
Correction, benchmark repetition and recovery already configured and authorized
in Runner do not need a new decision. The chat does not coordinate those iterations.
The [bounded operations policy](operations-policy.md) also covers safe
infrastructure retries, archive retention and reconciliation waiting within the
original limits. A parked integrity cleanup condition remains an explicit owner
maintenance decision; it never grants another product attempt.
Missing process/capability is a blocker, never permission for external execution.
Explicit user requests to investigate or maintain Runner itself are separate work.

`Waiting` alone is not an owner decision. A parent snapshot can briefly retain a
child's old wait while that child has already queued its next step. Rejoin the
same run with `runner_result(wait: true)`; complete waiting follows current-group
progress while still returning saved decisions and genuine unresolved blockers.
Nonwaiting reads retain the saved status, task states and execution/cleanup facts.

An explicitly approved `runner_recover_corrections` operator action can recover
an exhausted source-correction route in the same run. Bind the request to the
current failed attempt and set `max_uses: 3` for at most three total uses of each
eligible check/review/qualification correction route. This is not three extra
uses. Runner records an idempotent audit grant beside the frozen definition and
routes the saved validated `changes_requested` result into a new owner visit;
it does not repeat the unchanged failed check. Original attempts and evidence
remain in history, while fresh downstream checks, review and qualification are
required. Same-run publication advances the existing owned PR with its normal
head comparison.

With a separate owner authorization, `recover-corrections --within-budget`
uses existing aggregate headroom instead of a new per-route use allowance.
This mode requires `max_uses: 0`, settled root work and no preservation override.
It records one finite upper bound only for the currently exhausted source
backedge, leaving other routes and counters unchanged. Before every repair,
Runner reserves the full checked/protected completion path. It stops with the
saved failure when another repair would consume that reserve, even if unused
aggregate attempts remain. Queued work independently rechecks the reserve.
Every new candidate still needs its normal checks, independent review and full
qualification; merge, production and restoration approvals are unaffected.

An explicitly authorized `runner_retry` can grant another current root-visit
attempt from its existing aggregate budget with `--within-budget --extra-attempts 1`.
This leaves the frozen total ceiling unchanged, preserves
history and reserves the finite ready/protected completion tail. The allowance
ends at the current visit; it does not replenish source-correction routes or
grant release approval. Model execution still needs its existing authorization.
Child work, cleanup-only retries and incomplete
completion reserves cannot use this mode. Omitting `--within-budget` retains
the older retry contract, which also increases the total ceiling.

This recovery requires settled execution and the retained candidate's normal
ownership/source validation. Cleanup normally must be complete. An explicitly
authorized `--preserve-source` recovery can preserve an incomplete owned checkout
in private Runner forensics before reconstructing the saved candidate. It records
the preservation receipt, accepts none of that checkout's unexplained changes and
refuses ambiguous ownership, active execution or an uncertain move. It cannot
override approvals, unknown external effects, execution failures or the total
attempt ceiling. A restricted intake must not expose or call it. The CLI
equivalent is:

```sh
cortexium-runner recover-corrections --data STATE --run RUN --attempt ATTEMPT \
  --max-uses 3 --request-id AUTHORIZED-STABLE-ID
```

An explicitly approved `runner_recover_frontend_check` action can check a patch
captured by the settled, blocked correction owner in `deliver-planned-retained-frontend`.
It requires the observed run revision, exact attempt and patch digest, qualified
frontend helper digest, and authenticated draft publication head. Runner verifies
the retained capture, prior check and accepted cards before queuing the existing
read-only check with the original selectors in a new visit. The original blocked
attempt and earlier check remain in history. This grants no additional attempts
or release approvals. A fresh actionable result uses the existing correction
routes. Incomplete evidence stops the run. Restricted intake must not expose or
call this owner action.

An explicitly approved `runner_recover_frontend_timeouts` action can diagnose
conclusive five-second test timeouts after that first recovery. It also binds
the immediately preceding failed check attempt and report digest. It queues
the original full check on the same retained tree. When that check times out,
the helper samples isolated cases within the same attempt and deadline. An
isolated pass is diagnostic evidence. Acceptance still requires the full
uninstrumented check. Complete diagnostic evidence can authorize one repair
visit and a fresh check through the existing workflow. The run must have
room for both visits and the full protected release reserve before diagnosis.
The aggregate cap, ordinary correction limits, selectors and release
approvals are preserved. Restricted intake must not expose or call this action.

The same operation also admits a settled `deliver-planned-retained-frontend`
check that exhausted its configured correction route immediately after a ready
source owner. In that case `attempt_id` and `check_attempt_id` name the same
failed check; the patch digest names its preceding captured owner. Exact report,
helper, publication, accepted cards and observed revision remain required.
The diagnostic preserves ordinary assertion failures while sampling only the
confirmed five-second timeout subset, up to the existing eight-case bound.
Complete fresh full-suite evidence can earn one distinct repair visit, including
when the prior timeout no longer reproduces and an ordinary assertion still fails.
Every current timeout requires its matched complete profile evidence. Neither an
isolated pass nor diagnosis grants acceptance; a fresh normal full check, review
and qualification remain required. The original correction counter and total
attempt ceiling do not change.

After that diagnostic repair, a separate owner authorization can permit one
focused correction of a single remaining ordinary assertion. Use the existing
`recover-frontend-check` action with `--assertion-sha256` naming the exact failed
full-report digest, alongside its current attempt, revision, retained patch,
helper and publication identities. It admits only the diagnostic repair's
immediately following exhausted full check, with one assertion failure, no
timeouts, matching full-suite identities and passing compiler evidence. It
preserves the old grant and original route counter, records a distinct one-use
repair, and passes the full current report to the owner without repeating the
unchanged check. A fresh normal full check, independent review and complete
qualification remain required within the original completion reserve and cap.
This mode cannot combine with serial execution or timeout diagnosis; restricted
intake must not expose or call it.

An explicitly authorized `recover-browser-errors` action can repair the evidence
handoff after a retained frontend qualification has already returned to its
settled source owner. Bind the observed revision, exact qualification attempt and
current draft publication head, and supply the complete saved browser report with
`--report-file`. Runner checks its digest against the original failed receipt,
accepted source and cards, and live publication ownership. Only complete standard
ResizeObserver assertion evidence is admitted. Unknown or incomplete errors stay
blocked. The current owner's diagnostic gains classified error counts and hashes;
the original attempt, opaque handle, frozen process, status, limits and route uses
remain unchanged. The action records an idempotent audit but dispatches no work;
any subsequent retry still requires its existing authorization and reserve.
The full report stays private. Bounded gzip transport preserves the existing
64 KiB control envelope, with 32 KiB compressed and 128 KiB raw report bounds.
The ordinary release operator now derives the same safe feedback from known
legacy page-error assertions, without changing browser acceptance or its driver.
Restricted intake must not expose or invoke this owner action.

Preserved checkouts live under `STATE/forensics/RUN/ATTEMPT/source`; their sibling
manifest binds the captured patch hash and ownership identity. These files and
links count toward the existing storage limit, are never followed as candidate
source, and remain until separately authorized cleanup. Routine workspace and
temporary-file cleanup does not purge them. New workspace cleanup records a
durable deletion intent before removal, so a canceled cleanup can reconcile its
own partial deletion without treating unrelated changes as disposable.

Use a fresh short intake with only `runner_run`, `runner_result` and
`runner_status` exposed through the existing native Codex MCP configuration.
Disable shell, editing, browsing, subagents, apps, skill/plugin discovery and
unrelated MCP servers. Give it an empty read-only workspace. Supply the private
owner token through the process environment, never its prompt or tracked files.
This enforces the boundary for that intake; instructions in a general desktop
chat cannot revoke its already-available tools. Tell such a chat the contract,
then use the restricted intake for new work.
The portable [intake profile and launcher](../examples/intake/README.md) require
explicit operator paths and spend no extra coordination turns between stages.

The [reviewed Go example](../examples/reviewed-go/README.md) supplies the complete
first configured delivery process. Verification uses fixed argv; review is
read-only; local candidate integration requires an approving review. The same
core follows bounded outcome routes and expands declared task templates; task
data cannot supply new roles or permissions. Model choice, repetition and step
limits remain explicit configuration. Saved decisions are resumed only by an
explicitly authorized owner action through `runner_decide`; a restricted intake
must not approve its own work.

Completion means the process's declared outcome. Report a local reviewed
candidate as ready for integration/release decisions; never describe it as
merged, deployed or fully qualified beyond the actual proof. Coordinator token
usage remains additional to the Runner result and must be included in any
end-to-end benchmark.

Jack's Break dispatch is owned by the process-core instance at
`/Users/dan/.codex/jbg31` on Black Magic. Black Magic owns the Core, state and
local MCP service; Mega Mini owns the remote release operator. From another host,
use the configured SSH connection to Black Magic for the existing Runner CLI
run/result/status commands. Do not copy `owner.token` or guess a network endpoint.
Its live default is `deliver-planned-frontend` after full native/browser
qualification and operator cutover on 11 November 2026. The retained-mobile
continuation on PR #338 passed 222 frontend cases, TypeScript, independent review,
native qualification, 36 Chromium/WebKit journeys and 456 geometry checks at
head `24cb06d`. After separate owner merge and production approvals, the same run
completed release and live verification of merged commit `e442d173` in 61 of
64 attempts, with cleanup settled and no database restoration. The matching
restricted intake profile is active. A fresh native
session replayed the exact original request and rejoined the same result with
zero model turns or new product attempts. See the
[qualification record](architecture.md#qualified-planned-frontend-default-11-november-2026).
New Vue/mobile delivery selects `deliver-planned-frontend`; Go/backend
investigation delivery selects `deliver-planned-investigation` explicitly.
Bounded retained Nonogram research selects
`research-planned-retained-nonogram`, which ends with reviewed evidence rather
than release. Keep the older processes for history, compatibility and their
explicitly supported handoffs. Do not infer Jack's Break dispatch or current work
from the legacy GitHub board/Operations `runner_config`; that configuration remains
compatibility metadata for the generated-ownership checker. Use the pilot's own
submission/result/status interface and present its saved protected decisions.

For other explicitly configured planned work, select the complete
`develop-planned`, `deliver-planned-frontend` or `deliver-planned-investigation`
process once. The
planner reads source and returns bounded card data, dependencies and literal write
scopes. Runner validates the unaccepted plan, executes eligible cards in isolated
workspaces, joins their accepted pins, then runs combined checks, independent
review, corrections and qualification. Do not submit or supervise each card from
the chat. Parent results expose their statuses and aggregate attempts/usage.
New example processes share 64 attempts across the entire family, allow up to
eight cards and at most two card actors. These settings do not modify existing
run definitions or grant additional attempts to an old failed run.

Once a completed card's exact source pin is durably accepted by its parent and
all its attempts are released, normal owned cleanup can remove its full checkout.
Dependent cards and the final join authenticate retained metadata, Git pins and
patches; those, along with history and artifacts, remain until normal retention.
Failed, unaccepted or ambiguous work keeps the existing preservation boundary.

For an already published retained candidate, the planned retained variants accept
the exact saved patch of the published draft and its authenticated publication
handle. Their source-adopt step validates the original journal, live owned PR/ref
and accepted source before seeding work. A newer unpublished patch cannot satisfy
this exact-source adoption check. Publication continues the authenticated PR with a head comparison;
it reuses no old review, qualification, merge or production approval. Supplying a
PR number or branch name in task data grants no publication authority.
Pass the returned public receipt JSON unchanged. Do not rebuild or enrich that
handle from private journal metadata; strict authentication compares it with
the accepted public output.

`deliver-retained-catalogue` uses that same source adoption for a reviewed
draft with a scoped change to New puzzle availability. It runs separate Go and
Vue checks and supplies both complete receipts to independent review. It has
28 attempts and uses the same publication, native and browser qualification,
and protected release steps. The catalogue change needs direct regressions for
reopening and importing previously issued puzzles outside New.
The frontend check runs the complete selection with one Vitest worker and a
five-second case timeout. An owner can use `recover-frontend-check --serial` to
reopen the exhausted frontend check of a settled catalogue run once. It pins the
saved attempt, patch, checker and draft head, then runs a fresh full check inside
the original attempt cap and protected release reserve.

`frontend-retained-diagnostic` compares a saved patch with earlier frontend
evidence using one Vitest worker and the five-second case timeout. Supply the
original source commit, retained `patch`, selected `tests` as a JSON array encoded
as text, and the `case_file` (a single-file array) and exact `case_name` for the
isolated check. Both reports are returned when the tests execute, including
when an assertion fails. The process uses two attempts and has no publication
stage. Its reports are diagnostic evidence for the delivery workflow.

Planned investigations retain the first complete candidate measurement as a
separate artifact before entering the combined correction owner. The fixed copy
step is anchored before that owner so later correction loops cannot supersede
it. Independent review receives the pristine controls, this pre-correction report
and the latest measurement as separate protected inputs. Their case coverage and
source/seed/environment differences remain explicit; original controls alone do
not prove new-case coverage or an original incident's reproduction. This uses the
existing command/artifact mechanism and unchanged process/model limits. The
coordinator submits the complete request rather than coordinating these stages.

Complete investigation processes also retain ordered measurement history. Later
successful measurements append through the fixed `runner-measure-history`
command; the first complete report and pristine controls remain separate and
unchanged. Review can compare an intermediate profiled baseline with a later
experiment. History contains at most eight complete candidate reports within
16 MiB, preserving their exact JSON bytes, original rounds, source stamps and
hashes. Failed collection does not enter accepted history. Models should parse
the history programmatically and inspect the relevant complete reports, without
copying the entire archive into conversational output.

`deliver-measured-investigation` is the complete handoff for an already measured
unpublished patch. Its closed thirteen-input contract adds the passing check and
four ordered measurement artifacts to the existing request/selectors, patch,
original baseline, manifest and authenticated publication. The authorized
submitting operator verifies the retained check/measurement producer and source
records during intake. Runner authenticates publication before materialization,
recomputes the last report's source snapshot against the restored patch, then
performs independent read-only review before any new coding. The history command
preserves the old check as context; it does not manufacture check acceptance.

An approving review permits model-free capture and integration only after exact
owned seed bytes/tree are verified again. Subsequent correction uses fresh
checks, measurements, history and review before the ordinary same-PR and
protected release stages. This process has an eighteen-attempt ceiling and
preserves its full release/recovery reserve. It grants no additional allowance to
a failed run; a continuation's combined ceiling must be authorized explicitly.
The larger root contract changes no native role, model byte, record, artifact,
storage or access limit. Older frozen definitions and failures remain unchanged.

If task admission rejects an entire collection, the planner receives the
admission error and the saved rejected proposal when both fit the normal input
and record limits. The rejected cards confer no task or publication authority.
Card admission uses the same protected input staging limits as dispatch.

For a settled retained investigation whose planner blocked immediately after
such a rejection, the owner can rejoin the exact run once with
`recover-planning --data STATE --run RUN --attempt ATTEMPT --revision REVISION`.
Runner verifies the retained source and baseline, then queues one new planner
visit with the rejected proposal and unchanged allowances. Recovery admits a
single observation card, then uses the configured measurement, review,
qualification and approval stages.

The strict investigation collector opts into `--bind-source` and
`--require-evidence`; legacy helper invocations retain their existing behavior.
Source binding streams a versioned SHA256 snapshot of regular source files,
framing relative paths, executable bits, sizes and bytes. Only root
`.runner-tmp`, `.git`, `.codex`, `.agents` and `.aws` are excluded. This is a
content-snapshot identity, not a Git commit or historical incident identity. The
outer report retains its method, digest, file/byte counts and post-measurement
metadata verification. Hashing and verification share the existing ten-minute
measurement deadline.

Directory enumeration reads bounded name batches before inspecting entries, so
excluded tooling metadata is never statted through the sandbox. Qualify this
boundary with `TestNativeBoundMeasurementCommand` and
`RUNNER_BOUND_MEASURE_PREFLIGHT=1`, supplying explicit Codex, helper-compiler and
benchmark-Go paths. The zero-model exercise uses the real command permissions,
nested source and denied metadata, checks report/sidecar binding and reuses the
source/Git/private-file/symlink/network denial probe. Lifecycle fixtures with a
sandbox shim do not establish native permission compatibility.

The helper supplies `RUNNER_MEASURE_ROUND`,
`RUNNER_MEASURE_SOURCE_IDENTITY`, `RUNNER_MEASURE_SOURCE_SHA256` and
`RUNNER_MEASURE_SOURCE_DIGEST_METHOD`, replacing inherited overrides. Collectors
copy these values into their evidence rather than inventing provenance.
`RUNNER_MEASURE_EVIDENCE` is an existing directory: write named `.json` files
inside it, not a file at the directory path. Console retention stays at 256 KiB,
sidecars at 64 files and 8 MiB total, and the report at 16 MiB.

Strict candidate collection requires successful Go execution, verified source,
safe nonempty JSON sidecars and a benchmark result before continuation. A known
stdout overflow or missing required sidecars after settled Go exit zero can
request the configured owner's bounded collector repair, for at most three uses
within the process ceiling (64 for planned families). Exit one, crashes, uncertain
execution, unsafe evidence and source-binding failures remain blocked. Failed or
incomplete collection never enters the before-copy step or proves successful
timings.
Retained JSON presence still requires independent semantic and coverage review.
The source-bound pristine baseline does not require newly invented sidecars;
its selected controls, unsuccessful samples and absent coverage remain explicit.

An incomplete collection preserves `previous_complete_round` (zero before the
first complete measurement). Its correction route does not consume the separate
one-use measurement-continuation route. The first complete measurement is still
round one and is copied before owner correction; later complete measurements
retain their ordinary round sequence. Collection failures cannot replace that
before artifact or the pristine baseline. New planned baselines explicitly route
`blocked` to failure. A frozen legacy baseline without that route can reject the
domain outcome before indexing its diagnostic artifact; do not adopt private
files as accepted evidence or rewrite its frozen routes.

For an explicitly approved legacy measurement failure reporting
`measurement output exceeds 256KiB`, the owner-only same-run operation is:

```sh
cortexium-runner recover-collection --data STATE --run RUN --attempt ATTEMPT \
  --request-id AUTHORIZED-STABLE-ID
```

It requires the exact settled failed attempt, completed cleanup, authenticated
current candidate and the frozen owner/check/measurement loop. It preserves the
failed result and definition, records an idempotent audit, and gives the existing
owner a new visit with a diagnostic marked `collection_failure` and incomplete.
That diagnostic establishes only the saved collection error, with no fabricated
timings, samples or seeds. Original request, baseline, plan, checks and accepted
cards remain available; downstream evidence is invalidated normally. The action
increases no attempt allowance or other ceiling and bypasses no protected
approval. Normal dispatch spends the remaining original allowance. Restricted
intake must not expose or invoke this operation; rejoin the same request after a
disconnect.

For an owner-approved continuation of already joined investigation work, select
`deliver-retained-investigation`. Supply the exact unpublished `patch`, preserved
`original_baseline` and accepted `manifest` artifacts, authenticated `publication`
handle, original request, package, scoped tests and benchmark, with the patch's
exact original base as `source_ref`. Its 35-attempt ceiling, one attempt per visit,
one infrastructure retry and twenty-minute attempt deadline remain fixed.

The input-only `publication-reference` step first invokes the existing operator
`adopt` action to authenticate project, original journal and live owned PR/head/base.
It binds publication authority only, without restoring the older published source.
Runner then seeds the unpublished retained patch and runs its scoped check before
any model visit; an unexpected check failure stops the handoff. It neither reruns
the planner/cards nor creates another PR. The supplied original baseline and
manifest remain separate evidence inputs, without being relabeled or rerun.

If the first collection requests bounded repair, readiness admits the existing
owner to repair collector/check source only. Behavior or algorithm changes wait
for a successful complete source-bound cohort. The first complete measurement is
copied into the stable before artifact before evidence-based correction; later
checks, measurements and independent review use original/before/current evidence.
Accepted publication advances the authenticated original PR using its latest
receipt and head CAS. Full qualification and the same protected merge, production
and restoration approvals follow. This is a complete handoff, not authorization
to submit new work or change the default `deliver` process.

For bounded research without release, select
`research-planned-retained-nonogram`. Supply the pristine `baseline_benchmark`,
current `benchmark`, scoped `tests` and authorized `prior_patch` proposal. Runner
measures committed controls and checks pristine source before planning. It
materializes the proposal as an owned reference artifact, then supplies the
baseline, source-check proof and proposal to planners and cards. The proposal is
evidence to reuse, never a claim that source was already restored or approved.
After joining, the configured research checks, measurements and independent
review own correction and the final conclusion. A supported `NOT_REPRODUCED`
result may complete with reports and a retained patch. This process has no
integration, publication, qualification, merge or production stage.

The [approved release process](../examples/approved-release/README.md) adds the
model-free Jack's Break release operator. It owns qualification reuse, protected
merge, backups, explicit migration, Compose deployment and live verification. Two
saved decisions separate merge from production approval. Intake presents the
receipt and exact decision ID to the owner and must never approve it. Operator
configuration/credentials remain separate from coding roles. Uncertain remote
effects require inspection; a client disconnect grants no replacement operation.

Before presenting or accepting a release approval, and before starting a new
production stop, Runner reserves enough remaining attempts for the configured
closed release suffix and its unused recovery routes. Insufficient reserve leaves
the qualified evidence intact and waits before the protected action; it grants
no extra attempts. A pending approval saved by an older build is retired with an
audit receipt if it lacks that reserve. An already started operation is reconciled
normally. Any additional allowance still requires explicit owner authorization.
A saved, positively undispatched Stop is also checked before dispatch, with its
existing attempt counted once. If held for reserve, it remains active until
cancellation settles or its original deadline expires; Retry cannot change an
active attempt's allowance.

The [connected Jack's Break process](../examples/jacks-break/README.md) joins
development, bounded measurement/correction, review, draft publication and product
qualification to those release gates. The retained `deliver` process is its scoped
Go predecessor and the rollout default described above. Select one complete
process using that routing; do not split work into chat-coordinated development
and release runs. Omit `source_ref` to use its configured latest-main admission,
or supply an explicit
full commit. Optional-measurement delivery and frontend processes accept
`benchmark: "none"` when no benchmark is relevant. Both planned investigation
variants require an actual named baseline benchmark and reject `none` before
model work; their review requires the retained pre-correction report. Present the
exact saved merge/production/recovery decision and its receipt; approve none of
them on the owner's behalf. `runner_result.decision_outputs` previews the saved
decision attempt's outputs before final process outputs exist; use its receipt
to present exact approval facts without inspect access. These previews have a
4 KiB total bound and mark truncation explicitly. Known configured recovery stays
inside the process.

The earlier `deliver-frontend` variant remains available for compatibility.
The frontend process's `tests` input is a JSON array of explicit frontend-relative
test filenames;
the fixed command retains real executed assertions for independent review. Use
`deliver-product` for the explicitly combined frontend and Go investigation;
its separate `frontend_tests` and Go `tests` inputs keep both scopes verifiable
before a single publication and protected release suffix. Missing browser
prerequisites stop admission; missing frontend tools stop the configured check,
rather than transferring checks to the chat. Chromium and WebKit qualification verifies native touch, saved
edits and every advertised geometry at 320/360/390 pixels. Headless emulation
and WebKit's public native touch-drag limitation remain explicit in the report.
Reload/resume comparisons preserve exact saved snapshots and visible semantic
cell content while excluding only nonpersistent `.wrong-mark` feedback. Failed
comparisons retain full private evidence and a bounded qualification diagnostic
with cell identity, expected/actual state and admission transport facts; missing
or truncated diagnostics remain explicit.

Frontend delivery checks the admitted source before the first coding visit and
supplies the complete bounded check artifact to both correction and review.
The candidate check adds new and modified frontend test files from the owned
source to the explicit selection and records the combined list in its receipt.
The baseline and isolated diagnostic roles still use their explicit selections.
The artifact retains the actual Vitest report, file identities and assertion
diagnostics, together with the existing project's TypeScript check in owned
scratch. A complete compiler failure and assertion failure are retained together
for one correction. A proved compiler error can request correction when matched
test suites cannot transform; their assertions remain explicitly unexecuted.
Missing or inconsistent verifier identity/runtime evidence blocks the process;
conclusive assertion failures use its configured correction route. A short text
summary is not a replacement for the report. Scratch preparation copies only
explicitly imported JSON fixtures from the repository's `docs/` and `testdata/`
directories, with containment and regular-file checks; it does not copy those
trees wholesale or follow fixture symlinks. Select `deliver-retained-frontend`
with its original base and `patch` artifact to continue an unreviewed mobile
candidate. It restores and checks that source before another coding visit, keeps
the failed run intact and uses the same protected release suffix.
Frontend and Nonogram research variants permit three uses of each configured
source-correction route under the existing total attempt ceiling. Benchmark
continuation and protected approval policies remain separately bounded.

The earlier `research-nonogram` process supports the separate reproducibility
investigation. It measures the committed baseline, prepares test-only evidence collection, runs
scoped checks and bounded measurements, and independently reviews the complete
retained evidence. A supported negative or not-reproduced finding may complete
the research task. Missing records, malformed collection and unsupported claims
remain failures. Research completion returns evidence and a retained patch;
it neither integrates nor publishes source and grants no product-fix or release
approval. The original release benchmarks and acceptance conditions stay intact.

The earlier `research-retained-nonogram` continuation remains available for
compatibility; new planned retained research uses the evidence-only route above.
Its `prior_patch` artifact is supplied with the original base. Runner measures
pristine committed source and records its scoped
test execution before the coding role reads and applies that patch as a reference.
It does not seed the candidate before the original baseline. Candidate checks,
measurement, correction and independent review then stay inside that process.

Research review receives complete selected-test execution receipts rather than
an exit-code tail. Original benchmark logs are collected verbosely within the
existing output ceiling. The original benchmark need not emit the new collector's
structured sidecars; the candidate collector must still provide complete records
for semantic review. Native roles receive their existing whole-response JSON byte
budget explicitly. Oversized responses remain execution failures and cannot be
adopted as review decisions.

After verifier/infrastructure maintenance, the model-free `release-reviewed`
process can continue an accepted reviewed publication without another coding or
review turn. Submit its original publication receipt and exact base commit; the
operator authenticates that journal and the live PR/head/base. It reuses the same
PR and performs full qualification before fresh protected approvals. It does not
change the failed original run or inherit an earlier approval. A new continuation
still needs the owner's authorization; the chat never edits history to simulate
a successful retry.

If an authorized verifier upgrade changes the operator policy, an owner-configured
`publication_transition` can bind one previous policy hash and exact PR/head/base.
That grant only supports adoption of its authenticated original publication.
The new run must qualify under the current policy and obtain fresh approvals;
old qualification, merge, deployment and recovery receipts gain no exception.

A separate `retained_publication_transition` can authorize one retained-source
adoption of an owned draft. It binds the previous policy and exact PR, head,
base, run and publication key. The operator durably claims the grant for the
first new run and attempt. The continuation still needs fresh source checks,
review, qualification and protected approvals.

An explicit `retained_publication_followup` can grant one more adoption of the
same exact draft when the first claim matches its configured run, attempt and
policy. The new claim is separate. It leaves the first claim, prior journals
and other PR grants intact.

A separate `qualification_transition` can authorize one fresh qualification
attempt on the same run after a settled browser admission failure. It binds the
old publication, failed attempt and browser report to the exact owned draft.
The operator claims the allowance durably before dispatch. A successful
qualification issues a current-policy receipt, and protected approvals stay
separate. A failed handoff retains its evidence and stops before another
qualification or source repair. The adoption grant is independent.

For an investigation with a retained benchmark scaffold, select the complete
`investigate-retained` process and supply that patch as its `patch` artifact
input, its exact base, and the normal request/check/benchmark inputs. Its first
read-only command measures the scaffold before infrastructure readiness and any
model turn. Unsuccessful baseline samples remain explicit failure evidence for
the investigator, not a passing candidate measurement. For an already committed
scaffold, select `investigate-source` without supplying a patch. The owner and
reviewer receive the full retained baseline and measurement reports through
protected artifact inputs. Before the first candidate measurement, its configured
binding falls back to the baseline report. Command output tails are diagnostics,
not a substitute for that evidence. Benchmarks can write bounded JSON sidecars to
`RUNNER_MEASURE_EVIDENCE`; missing or truncated evidence remains incomplete, and
the reviewer still judges completeness and acceptance. Existing checks,
bounded measurement, review, publication, qualification and the protected release
suffix run normally. Preserve
the blocked original run; never advance it by rewriting its outcome. For a new
benchmark task without a scaffold, the initial owner hands its runnable scaffold
to downstream measurement without claiming the correction has already passed.

For an authorized continuation of an unreviewed retained product patch, select
`deliver-retained-product`. Supply `request`, `package`, Go `tests`, explicit
`frontend_tests`, `benchmark`, and the `patch` and `original_baseline` artifact IDs,
with the patch's exact base as `source_ref`. The first read-only command restores
and measures the candidate before readiness; it is not a new original baseline.
Readiness and initial frontend/Go checks then precede the owner. The owner and
reviewer receive the unchanged original baseline plus current candidate evidence;
the owner also receives the exact frontend selection and authoritative deadline.
The same bounded correction, review and single-publication release path follows.
Preserve the original failed run. Submission needs fresh owner authorization;
merge, production and restoration remain separate saved approvals. Limits do not
increase, and the coordinator performs none of these product checks itself.

## One-use unpublished source-card recovery, 12 November 2026

Dan approved this bounded recovery contract separately from browser maintenance.
The owner tools `runner_preflight_unpublished_card_recovery` and
`runner_recover_unpublished_card` preserve the same failed root and child. They
are outside restricted intake. This operation is for a settled initial
seed/owner/pin card whose original twenty-minute owner timed out with a captured,
unaccepted ready patch. It grants one owner visit, never direct source acceptance.

Preflight binds `run_id`, `revision`, `group_attempt_id`, `child_run_id`,
`child_revision`, `attempt_id` and `patch_sha256`. It authenticates the original
seed, dependency manifest, every other completed pin, source scope, cleanup and
writer leases. It returns `bindings_sha256`, spent/ceiling and the unchanged
completion reserve. Invoke admission with those exact fields, the returned digest
and one stable `request_id`. Drift or insufficient full reserve refuses without
a grant. The CLI is `cortexium-runner recover-unpublished-card` with corresponding
hyphenated flags; `--preflight` performs observation only. No limit/profile/input
or policy override is accepted.

For this planned family, `revision` is the observed root-plus-children revision
from `Get`/inspect/result, matching the other planned-family owner recoveries.
`status` instead reports the stored root contribution. Preflight returns both
`revision` and `stored_revision`; neither value is rewritten. The approved saved
family has observed revision 658, stored root revision 164 and failed-child
revision 81. Passing 164 as the request revision refuses. The full bindings digest
also authenticates every individual child record, so changed children cannot
reuse stale bindings even if their aggregate revision sum stays unchanged.

The atomic grant preserves failed attempts and pins, records the old group/root/
child failure facts, and requeues only that same owner. Ordinary Runner workspace
restoration reconstructs its authenticated captured source. Normal pin, group
manifest, join, checks, measurements, source review, publication, qualification,
experience/native and protected decisions remain in the frozen process. A failed
or cancelled recovery cannot receive another card-owner visit, including after
restart or rejected pin. Existing downstream correction boundaries remain intact.
The operation creates no new root, publication handle or policy-migration right.

Main owns independent review, installation and live invocation. Before cutover,
retain the exact current binary/config identities and a consistent private
forensic checkpoint using the supported upgrade procedure: settle relevant work,
quiesce the owning Core through its service manager, confirm it exited, and use
SQLite's backup facility on the closed operational database. Preserve artifact,
job and Git-ref inventories; keep credentials in place, out of exported evidence.
This is an operational checkpoint, never permission to restore product data.

Full source validation runs in the existing durable native preparation before
workspace restoration or native dispatch. Start and Observe remain short joins;
preparation retains the ordinary original twenty-minute owner deadline. A slow
validation must not be retried as another owner visit. Restart revalidates the
same undispatched assignment after its worker lease settles; cancellation also
settles that lease before Release. No successful validation is cached.

No SQL schema migration occurs. New grants use record format 5 with an immutable
one-use audit; the installed d0e513ff format-4 Core refuses them before dispatch.
Formats 0/3/4 remain readable by the new Core. Before a grant, d0e513ff can reopen
unchanged state. After a grant,
retain a compatible new Core and all later records; settle through normal controls
if recovery fails. Do not roll back to a refusing reader, rewind the operational
database, remove failures, decrement usage or fabricate a publication receipt.
No browser-producer activation, frozen-policy migration or native/egress waiver
is part of recovery installation.
