# External coordinator contract

Runner owns the configured delivery process. An external chat owns understanding
the user's intent, submitting authorized work and reporting results or decisions.
It must not become a second worker or scheduler when something fails.

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
Its live default remains `deliver` while the planned mobile journey awaits
qualification. The latest complete retained-mobile run published to the existing
draft PR, then stopped on three native HTTP 408 failures before browser
qualification; see the [qualification record](architecture.md).
Planned processes are selectable explicitly
for authorized qualification; their installation alone is not a default cutover.
After that qualification and operator cutover, new Vue/mobile delivery selects
`deliver-planned-frontend`. Bounded retained Nonogram research selects
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
the exact saved patch and authenticated publication handle. Their source-adopt
step validates the original journal, live owned PR/ref and accepted source before
seeding work. Publication continues that authenticated PR with a head comparison;
it reuses no old review, qualification, merge or production approval. Supplying a
PR number or branch name in task data grants no publication authority.

Planned investigations retain the first complete candidate measurement as a
separate artifact before entering the combined correction owner. The fixed copy
step is anchored before that owner so later correction loops cannot supersede
it. Independent review receives the pristine controls, this pre-correction report
and the latest measurement as separate protected inputs. Their case coverage and
source/seed/environment differences remain explicit; original controls alone do
not prove new-case coverage or an original incident's reproduction. This uses the
existing command/artifact mechanism and unchanged process/model limits. The
coordinator submits the complete request rather than coordinating these stages.

New planned investigation collectors route a known stdout overflow to their
configured owner, for at most three collection-correction uses within the shared
64-attempt ceiling. This requires settled successful Go execution and safely
collected sidecars; mixed failures, uncertain execution or malformed evidence
remain blocked. Console retention stays at 256 KiB, structured sidecars at 8 MiB
total and the report at 16 MiB. Overflow records bounded output, its digest and
incompleteness; it never becomes a timing or coverage claim. Source producers
write complete structured evidence to sidecars and keep console output compact.

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
Only adoption of its authenticated original publication may cross that boundary.
The new run must qualify under the current policy and obtain fresh approvals;
old qualification, merge, deployment and recovery receipts gain no exception.

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
