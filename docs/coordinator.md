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

The [approved release process](../examples/approved-release/README.md) adds the
model-free Jack's Break release operator. It owns qualification reuse, protected
merge, backups, explicit migration, Compose deployment and live verification. Two
saved decisions separate merge from production approval. Intake presents the
receipt and exact decision ID to the owner and must never approve it. Operator
configuration/credentials remain separate from coding roles. Uncertain remote
effects require inspection; a client disconnect grants no replacement operation.

The [connected Jack's Break process](../examples/jacks-break/README.md) joins
development, bounded measurement/correction, review, draft publication and product
qualification to those release gates. Select `deliver` for a full delivery request;
do not split it into chat-coordinated development and release runs. Omit
`source_ref` to use its configured latest-main admission, or supply an explicit
full commit. Supply `benchmark: "none"` when no benchmark is relevant. Present the
exact saved merge/production/recovery decision and its receipt; approve none of
them on the owner's behalf. `runner_result.decision_outputs` previews the saved
decision attempt's outputs before final process outputs exist; use its receipt
to present exact approval facts without inspect access. These previews have a
4 KiB total bound and mark truncation explicitly. Known configured recovery stays
inside the process.

Select the complete `deliver-frontend` variant for a scoped Vue/mobile request.
Its `tests` input is a JSON array of explicit frontend-relative test filenames;
the fixed command retains real executed assertions for independent review. Use
`deliver-product` for the explicitly combined frontend and Go investigation;
its separate `frontend_tests` and Go `tests` inputs keep both scopes verifiable
before a single publication and protected release suffix. Missing browser
prerequisites stop admission; missing frontend tools stop the configured check,
rather than transferring checks to the chat. Chromium and WebKit qualification verifies native touch, saved
edits and every advertised geometry at 320/360/390 pixels. Headless emulation
and WebKit's public native touch-drag limitation remain explicit in the report.

Frontend delivery checks the admitted source before the first coding visit and
supplies the complete bounded check artifact to both correction and review.
The artifact retains the actual Vitest report, file identities and assertion
diagnostics. Missing or inconsistent execution evidence blocks the process;
conclusive assertion failures use its configured correction route. A short text
summary is not a replacement for the report. Scratch preparation copies only
explicitly imported JSON fixtures from the repository's `docs/` and `testdata/`
directories, with containment and regular-file checks; it does not copy those
trees wholesale or follow fixture symlinks. Select `deliver-retained-frontend`
with its original base and `patch` artifact to continue an unreviewed mobile
candidate. It restores and checks that source before another coding visit, keeps
the failed run intact and uses the same protected release suffix.

Select `research-nonogram` for the separate reproducibility investigation. It
measures the committed baseline, prepares test-only evidence collection, runs
scoped checks and bounded measurements, and independently reviews the complete
retained evidence. A supported negative or not-reproduced finding may complete
the research task. Missing records, malformed collection and unsupported claims
remain failures. Research completion returns evidence and a retained patch;
it neither integrates nor publishes source and grants no product-fix or release
approval. The original release benchmarks and acceptance conditions stay intact.

For an authorized continuation of saved research, select
`research-retained-nonogram` and supply its `prior_patch` artifact with the
original base. Runner measures pristine committed source and records its scoped
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
