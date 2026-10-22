# Runner core redesign plan

Status: The local replacement, delivery bundles and R1–R5 are implemented and
qualified in `codex/runner-local-core`. The subsequent local-completion,
real-project pilot, main-reconciliation and final platform/package obligations
are complete within the [continued qualification record](#continued-real-project-qualification).
Current store format is **21**. The [frozen checklist](#frozen-first-release-checklist)
remains the scope boundary; historical briefs below are construction evidence.
Runner PR #106 merged into main on 2026-10-16; api-mcp pilot PR #6 remains a draft.
Release publishing and running-installation/cutover changes remain separate.
The owner subsequently requested the [GitHub Issues bundle](#github-issues-and-owner-acceptance-brief)
before release. Other views, extra harnesses, autonomous intake, merge queues
and additional hosted merge methods remain deferred.
The owner subsequently requested the [reusable check environments and controlled
online audits brief](#reusable-check-environments-and-controlled-online-audits-brief).
E1 environment/output support and E2 controlled npm audits/freshness are implemented
in the existing worktree. [Code Atlas qualification (E3)](#e3-code-atlas-qualification--2026-10-22)
is complete within its recorded Linux/browser and native focused-verification
boundaries. Pilot launch, public release/package publication and
installation changes remain separate.

Runner should own its work, approvals, execution and recovery state locally.
GitHub Projects should become an optional view. External tools should connect
through small integration contracts for intake, progress publication or code
delivery.

The first priority is a reliable, understandable core process. A user should
always be able to see what Runner is doing, why work is waiting, what needs a
decision, and what will happen next. Configuring models, reasoning, escalation,
retry limits and parallelism should not require understanding the workflow
implementation or a board's field schema.

The direction is recorded in [ADR 0005](decisions/0005-local-runner-core.md).
This is the designated replacement specification and build sequence, under
[AGENTS.md](../AGENTS.md). The [architecture](architecture.md) and
[operator guide](operator-reference.md) describe the local runtime now promoted
to `cmd/cortexium-runner`. Remaining release work is bounded by the frozen
checklist and requires a separate implementation instruction.

Construction briefs and qualification records below retain their historical
`runner-next` names, store formats and command lines. Links to retired code are
pinned to the pre-promotion commit; they are reference evidence, not current
commands or permission to restore Project coupling.

The owner's 2026-10-16 [product and skill reassessment](#product-and-skill-reassessment)
clarifies the target beyond the qualified first-release boundary. Legacy feature
parity is not the finish line; useful local completion and optional integrations
are. This is design direction, not authorization to start another implementation
bundle or change the running installation.

The [local completion and combined-plan brief](#local-completion-and-combined-plan-verification-brief)
defines the implemented local delivery and merge-permission boundary. Its local
qualification and the subsequent real-project/connected results are recorded
below; historical briefs are not outstanding implementation work.

Start with [release scope](#first-release-scope), [core contracts](#core-contracts),
and the [release checklist](#frozen-first-release-checklist).
The [first implementation slice](#first-implementation-slice) and subsequent
bundle briefs retain the construction history.
The [coverage map](#requirement-coverage-map) connects important requirements to
their implementation phase and proof. Keep work status and evidence there as
slices land; do not create another plan or task registry.

## Reusable check environments and controlled online audits brief

**Defined 2026-10-21; E1/E2 implementation and E3 qualification authorized separately.** The owner
requested reusable Runner support instead of per-project workarounds. This
supersedes the proposed private Code Atlas wrapper/image and preparation-audit
workaround. E1 reusable environment/output handling and E2 controlled npm audits
and freshness are implemented; E3 results are recorded below. Release/package
publication, installation/service changes and pilot launch remain outside this
brief. The operator guide documents the shipped configuration and commands.

### Outcome and scope

An operator describes tools, check commands, output paths and audit policy in the
existing quality agreement. Runner prepares the environment, executes checks,
retains evidence and refreshes online observations. Onboarding a supported project
needs configuration, not application edits, a handwritten Dockerfile, cleanup
script, copied wrapper or an exception to source/network protections.

Initial support is local Linux Docker checks for Go and Node/npm projects, optional
locked Playwright Chromium, version-pinned Go command tools such as Genny, and one
online audit adapter for npm's public registry. Project-owned test commands and
Genny's generated-ownership requirements remain project policy. No language-wide
test discovery, automatic dependency upgrades, general network-enabled shell,
private-registry audit credentials, remote worker, service or public plugin
framework is included. Unsupported tooling or dependency forms block explicitly.

The user's normal journey is:

1. Declare the supported tool versions, module/package directories, ordinary check
   commands, disposable outputs, retained reports and typed audit requirements.
   Keep `at: task|completion|both`, deadlines and CPU configuration. Commands,
   tools, outputs and outbound audit data appear in the exact preview.
2. Use one explicit environment-preparation preview/apply operation. Runner owns
   reusable preparation recipes; it resolves exact input/image identities, checks
   capabilities and reports missing inputs. It neither runs a model nor starts
   task admission. Preparation can fetch dependencies under the rules below.
3. Use the existing quality preview/check/apply journey. Preparation is not a
   passing baseline or approval. Baseline and task/completion checks use the same
   execution path; fresh state remains drained until work is separately approved.
4. Inspect status and retained results. It distinguishes preparation needed,
   running/failed/unavailable checks and audits needing refresh, with the reason
   and next action. An expired audit does not require a new image or full test run.

### Runner-owned environment preparation

Extend the existing image/quality boundary rather than introduce another runtime.
The first built-in recipe composes Go, Node/npm, optional Playwright Chromium and
explicit pinned Go command tools. It takes ordinary relative project directories;
no Code Atlas/FlowStack paths, branch names or private qualification scripts belong
in Runner code. Exact versions come from explicit tool declarations and inspected
lockfiles, not an unrecorded latest-version lookup. Node/browser/platform identity
must match the resolved dependency graph. Genny remains pinned to the project's
declared generator version; preparation does not regenerate or upgrade it.

Preparation separates **fetching inert inputs** from **executing project code**:

- The Runner-owned fetch operation admits only approved tool/package sources,
  verifies available checksums/integrity and records exact identities. It cannot
  run a repository Dockerfile, lifecycle hook, arbitrary prepare command or
  candidate-selected executable while it has network access.
- Dependency installation, necessary lifecycle hooks, tool compilation and browser
  probes execute offline in disposable containment. Unsupported network-dependent
  install hooks are unavailable capabilities, not permission to enable networking.
- Existing verified private module archives may be imported through the same
  explicit preparation operation. Import only the required pinned modules; do not
  mount a user's HOME, credentials, whole module cache or installed application.
  Automatic private credential provisioning is outside the initial scope.
- Preparation produces an immutable local image/cache manifest keyed by recipe,
  tool versions, dependency manifests/locks and platform. Each check receives
  private writable scratch; checks never write back into a reusable cache. Partial
  or changed preparation cannot replace a previously approved result. Missing,
  mismatched or tampered inputs require preparation rather than a network fallback.

Image pulls/builds/downloads remain explicit preparation effects. Ordinary `run`
does not silently pull an image, prepare a changed dependency graph or execute an
unreviewed recipe. Changed environment authority uses quality preview/apply and
the existing explicit adoption rules; it cannot rewrite a released Plan's policy.
An already-prepared exact local image remains usable through the current path;
there is no legacy migration or parallel execution mechanism.

### Ordinary build outputs without source workarounds

Declare literal dependency/build/report output paths in quality configuration.
Runner provides private writable output areas at those ordinary paths during
checks, backed by its reserved output storage. Commands keep their normal working
directory and output locations. Reports may be selected from those declared
outputs without changing application/test code to write `.runner-output` paths.
Existing bounded artifact manifests and read-only review grants remain the
retention mechanism; caches are not reviewer artifacts by default.

The source snapshot, Git controls, manifests/locks, quality sources and all
non-output source remain protected. Reject traversal, links/escapes, root-wide
outputs, overlaps and declarations covering source code or control files. An
existing tracked build placeholder is allowed only as an explicitly listed file,
shown with its original identity in the approval preview. New tracked files under
an output declaration cannot silently acquire that permission. The isolated
output view may replace that placeholder while building; its underlying source
and eventual candidate must retain the approved bytes. Generated outputs never
become an implementation candidate or integration delta through check execution.
Do not restore arbitrary modified source to manufacture an integrity pass.

For example, Code Atlas can declare `frontend/node_modules`, `frontend/.genny`,
`internal/app/http/frontend_dist`, `frontend/test-results` and `artifacts`,
with the exact tracked `frontend_dist/index.html` placeholder identified separately.
Retain `artifacts/browser` beneath its writable parent so Playwright can recreate it.
These are configuration data, not built-in paths or permission to edit the app.
Preserve its existing Go/frontend tests, build, generated-file verifier, browser
entrypoint, assertions, workers and deadlines. Native macOS chooser interaction
remains manual proof; Linux checks of simulated selection do not establish it.

### Controlled online npm audits

Add a typed npm-audit operation to the existing quality-check contract, alongside
offline command checks. It has an owner-approved package directory, omission and
severity policy, timeout and existing task/completion timing. It is not a shell
command plus `network: true`. Ordinary checks, lifecycle hooks and model roles
retain their existing network denial, container-private loopback, non-root and
resource/process ownership boundaries.

The audit adapter receives only validated dependency metadata from the exact
source snapshot. It owns the pinned npm audit implementation and registry
transport; it neither evaluates project code nor inherits npm/Git configuration,
proxy settings, credentials, arbitrary arguments or endpoint URLs from the
candidate. No `npm audit fix`, install, manifest rewrite or automatic repair.
Initial supported inputs are a single npm package with a consistent lockfile and
public-registry dependencies. Workspaces, private registries, local/git dependency
forms and unsupported lock semantics are explicit unsupported cases until qualified;
they cannot be silently omitted from a clean result.

Use npm's maintained audit evaluation, with conformance against the pinned npm
version for supported graphs, omission rules, thresholds and transitive/meta
vulnerabilities. A direct-advisory-only approximation is insufficient. Separate
the evaluator from a small Runner-owned HTTPS transport so package-controlled
code never gains network authority. Keep that transport private to this adapter,
not a general proxy service or extensible network API. Enforce the approved
registry and allowed request shapes, bounded requests/responses/deadline, validated
public destinations and no redirects to host/private/link-local services.
Project configuration cannot redirect it. Logs and reports contain no secrets.

The preview discloses dependency names/versions sent to the public registry.
Source code, absolute paths, project identity and credentials are not outbound
audit inputs. npm documents its Bulk Advisory request, broader Quick Audit
fallback and meta-vulnerability processing in the
[npm audit reference](https://docs.npmjs.com/cli/v11/commands/npm-audit/).
The initial adapter permits the bulk request and the package metadata needed by
the maintained evaluator. It must not silently fall back to a broader Quick Audit
payload. An unavailable supported endpoint is unavailable proof. Additional
egress or disclosure requires a separately reviewed capability, not model consent.

### Freshness, evidence and completion

Keep the approved audit policy in the quality revision and volatile observations
outside policy identity. Each immutable receipt records dependency-input digest,
adapter/tool/policy identity, registry, query time, outcome/findings and report
digest, with explicit attribution to the candidate being assessed. Refreshing a
receipt does not reapprove tasks, reset allowances or change the environment.

The default `max_age` is **one hour**, configurable explicitly in the audit
policy. A successful result means only that the service reported no findings at
or above the chosen threshold for those inputs at the recorded time. It cannot
promise detection of advisories disclosed afterward. Reuse a still-valid matching
receipt without relabelling its query time; changed dependencies, adapter or audit
policy require a fresh query regardless of age. Clock uncertainty/backward movement
cannot extend validity. Preparation or baseline receipts are not permanent passes.

At each applicable review/admission or final-completion/delivery boundary, validate
the receipt and its age. Refresh only the audit when needed, under the existing
coordinator's action ownership and admission controls. Do not rebuild the image,
rerun unrelated passing tests, call a model or require an operator to refresh a
snapshot manually. An audit expiring while a PR waits must be renewed before the
merge claim; a completed historical receipt retains its actual observation time.
There is no background polling/monitoring after completion.

Threshold findings are a **failed** required check. Service refusal, malformed or
oversized output, unsupported dependencies, timeout, stale proof or incomplete
ownership are **unavailable** proof. Both block the affected review/completion or
delivery; an outage cannot reuse an expired clean result or become a baseline
exception. No waiver mechanism is added. Unrelated safe work may proceed. Queries
are bounded by their check deadline; there is no new automatic retry loop or
allowance reset. Existing explicit retry/recovery preserves original observations.

### Responsibility and verification boundary

Use existing responsibilities: config owns declarations and policy validation;
execution owns prepared environments, contained checks and the narrow audit
adapter; workspace owns immutable source/output views; engine owns agreement,
freshness and action admission; store/core own receipts and ownership; CLI owns
preview/status. Extract a cohesive private helper only where needed. Keep one
coordinator/store, existing role contracts and no public plugin registry. New
authoritative fields must reject incompatible state safely rather than let an
older binary ignore required proof. No migration of installed state is included.

Extend owning suites for concrete regressions:

| Contract | Existing owner and smallest meaningful proof |
| --- | --- |
| Environment declarations and identity | `config/quality_test.go` and existing engine quality cases: exact tools/platform/locks/outputs pin approval; unsupported or changed inputs block; refresh of audit data alone preserves authority. |
| Reusable preparation | Existing execution/container coverage: one shared recipe handles two small independent layouts; missing/tampered inputs and attempted networked lifecycle hooks fail; mutable output cannot poison the next check's cache. |
| Source and output isolation | `workspace/workspace_test.go`, execution and retained-artifact tests: normal generated files and explicit placeholder output work; source/Git/lock tampering, links and broad output grants fail even after exit zero; original candidate and reports remain bound. |
| Audit meaning and egress | A focused adapter suite uses deterministic registry fixtures and the pinned maintained npm evaluator: clean/vulnerable/transitive findings, omissions/thresholds, hostile config/URLs, private-host redirects, malformed/oversized replies, outage and cancellation. No real registry required in ordinary Go tests. |
| Freshness and recovery | Existing engine/store/delivery cases with an injected clock and explicit synchronization: valid reuse, dependency change, new findings on unchanged locks, expiry before completion/merge, restart and overlapping callers. Audit refresh neither resets budgets nor repeats passing tests/model work; one slow query cannot block unrelated reconciliation. |
| Operator result | Existing CLI, process and backup owners: clear blockers/next action, redacted bounded evidence, retained receipts after reopen/backup and no authority release before process/container/query settlement. |

Implementation verification runs focused owning tests during work, then required
`go test -race -timeout 15m ./...` and `go vet ./...`, one executable operator smoke
and actual container/egress/cancellation probes. Reuse applicable earlier native
containment evidence; requalify changed boundaries. A controlled real npm audit on
an explicitly approved public dependency graph verifies live transport and records
the observed result, not an assumption that it is clean. No model calls are
necessary unless role execution contracts change, which is outside this brief.

### Build order and finish line

1. Implement reusable environment preparation and source/output handling in Runner.
   Prove ordinary commands work with unchanged project source and existing isolation.
2. Add the typed npm audit, enforce its transport and receipt freshness through
   completion/delivery, and finish the owning and required verification above.
3. Qualify the same shipped mechanisms against a pinned, isolated Code Atlas copy:
   configure normal project commands, run one passing complete baseline and inspect
   retained reports/read-only access and cleanup. No custom product wrapper,
   Dockerfile, audit snapshot, assertion/timeout change or source patch is acceptable.
   Missing capability blocks rather than reopens a workaround.

The finish line is a reviewed Runner change, usable declarative configuration and
source/tool-bound qualification evidence, with the old installation and current
Code Atlas app/data unchanged. Stop before adding/approving a product task,
starting `run`, publishing a release/package or overview, or changing installations.
Public PR preparation/merge is not part of this bundle. Current
Code Atlas preparation resumes only after the reusable support is separately
authorized, implemented and qualified. Pilot launch remains a later decision.


### E3 Code Atlas qualification — 2026-10-22

**Boundary and result.** Qualified Code Atlas in an independent clone, using
private format-21 state and a separately built Runner executable. The authority
is empty and drained: no task, attempt, model invocation or pilot. The original
checkout, app and old Runner were not written or restarted by this qualification.
The separate Code Atlas owner chat advanced its source/app during the work; those
external changes are recorded separately from this run's protected observations.
No publishing, installation change or public PR was performed.

The initial pin was `85f6335b42c9f0d53e92701ce71c9ea83cb57e3d`. After reviewing
the owner's five changed UI/test files, the clone adopted
`cd43df217e77a5f6e19673f47efbe5b7c6b5bc81`; dependencies, tool pins and check
configuration were unchanged. The owner separately approved one selector repair:
filter the stale-guide `status` by its expected text, avoiding the concurrent
`Arranging packages…` status. Clone-only commit
`a81c0467f897fdd83e7a3dbe361dece34f2c19cb` retains the same `toContainText`
assertion and every other proof step. Focused browser verification passed before
the fresh complete baseline. This patch still requires adoption by the project
owner; it is not present in the original checkout.

**Verification agreement.** The normal README checks remain:
`go test ./... -count=1 && go vet ./...`, all frontend unit tests, pinned
`genny doctor --verify-generated`, the production frontend build followed by the
complete Playwright suite, and typed npm audit equivalent to
`--omit=dev --omit=optional --audit-level=high` with one-hour freshness.
Build and browser share one check so the server embeds that build. Browser
settings remain one worker, zero retries, 60s tests and 10s assertions. No project
wrapper, Dockerfile, audit snapshot or application-source patch was introduced.

The reusable recipe pins Go 1.26.9, Node 24.21.0, Playwright 1.64.1 and Genny
`v0.2.3-0.20261019123550-d5717ed19ca0` with its declared archive checksum.
Only explicitly requested, verified private Genny module archives were imported.
Ordinary dependency/build/report paths use declared output overlays; the tracked
frontend index is an explicit placeholder. `artifacts` is the writable report
parent so Playwright can recreate `artifacts/browser` without widening source
access. Preparation is explicit; check execution stays offline.

**Generic fixes established during qualification.** Each defect was diagnosed
before its bounded fix; failed attempts remain in the private evidence record.

- Lazy Go graphs: validate with offline `go mod download` after fetching all
  checksum-declared inputs. `download all` unnecessarily demanded dependencies'
  own test modules absent from a valid root graph. Metadata-only reproduction
  and real project Go tests establish the result; manifests remain byte-identical.
- npm omissions: derive graph flags before validation and allow missing children
  only inside policy-omitted subtrees. Included missing requirements and invalid
  installed edges still block. The existing pinned-npm conformance table adds
  omitted dev/optional/peer cases and included/required negative controls; full
  report/exit-code comparisons and the live registry probe passed.
- Source modes: verification copying uses an exclusive, no-follow exact-mode
  writer. Ordinary writes and archive extraction retain their umask behavior.
  A child-process regression proves 0600/0640/0755 files survive `umask 0077`;
  existing CI no-op/mode-only controls and baseline-exception matching pass
  under that mask. The new regression fails with the original copy call.
- Container HOME: create mode-0700 `/tmp/runner-home` under the check UID in
  private tmpfs. Host-backed HOME caused intermittent root-owned review-cache
  observations in Linux. Prepared and direct checks use the same boundary;
  dependency caches and report mounts stay private. Recipe `go-node-v2` requires
  explicit re-preparation. Saved agreements with a superseded recipe are rejected
  before command execution, preventing reuse of an old embedded HOME bootstrap.
  The owning input-identity test covers current, superseded and changed-input cases.
  Existing live-recipe assertions cover location, owner and mode; source-bound
  direct/prepared probes and the complete Linux suite qualify the actual change.

**Evidence.** Fresh preparation took **229.8s**. The complete Code
Atlas baseline passed in **141.7s**, report
`2341d7c74e37d41478e0fb55211db7e4`: Go tests/vet, 19 frontend tests, 40 generated files, both browser
journeys with six retained screenshots, and the online audit. Image:
`sha256:b2bdb063c15b565c18c1b691fc13860668e7519bb94492218dcc2bd8332309f0`. The audit queried at `2026-10-22T07:28:11.967345Z` and reported
zero findings under the unchanged policy. The source/policy/image-bound agreement
is revision `141d24c94c4f4f43932cba1bbce1fdaddce4e8a9b44b1af4ed671823c3cfc763`; expiry requires an explicit fresh audit,
not a snapshot exception.

The retained screenshots/logs pass the production evidence-grant validator and
native Codex sandbox: evidence readable; writes, original source, raw check copy
and sibling evidence denied. Direct/prepared container probes establish HOME
ownership/mode, private loopback, app/external-network denial, no inherited secret,
source/index write denial even on successful exit, writable outputs and unchanged
two-CPU/4-GiB/512-PID limits. Cancellation and all owned processes/containers and
preparation scratch settled. Report-backed copies remain retained under Runner's
existing backup contract.

Runner's complete `go test -race -timeout 15m ./... && go vet ./...` passed on
frozen Linux source in **219.7s**, report `0d3361c8d8a40724679a390bb402f963`. Native
owning suites, strict-umask integration controls and vet passed. The complete
native race run reached its unchanged 15-minute package cap with no assertion
failure; it did **not** pass. The Linux result does not establish native macOS
folder-dialog behavior. No assertion, deadline, retry or sandbox limit was weakened.

Exact source/tool/binary identities, declarative configuration, immutable reports,
failed/successful gate records and protected observations are retained at
`/Users/dan/.local/share/cortexium-runner/code-atlas-e3-20261022/summary.json` and
its `evidence/` directory. Pilot launch remains a separate owner decision.

### PR review of E1–E3 — 2026-10-22

Review the complete unpublished environment/audit/qualification diff against
current `main`. Preserve project assertions, check budgets, installations and the
unlaunched pilot. Review follow-ups stay inside the approved contracts:

- npm graph omission must also recognize dependencies shared only by development
  and optional subtrees when both are omitted. Extend the existing pinned-npm
  conformance table with that graph and a single-omission negative control.
- An `npm-shrinkwrap.json` must block reusable preparation and later check input
  validation: npm otherwise selects a different lock from the approved
  `package-lock.json`. Extend the owning unsupported-input coverage; do not add
  shrinkwrap support or a project workaround.
- Reconcile README/operator guidance with implemented audits and output layout.

Reproduce the concrete failures before fixing them. Run focused owning checks
and live evaluator conformance, then the full Linux race/vet gate. Reuse the
source/image-bound Code Atlas baseline where unchanged; exercise affected
prepared-environment behavior without changing project source. Retain the native
full-suite timeout limitation. Prepare a PR with public verification summaries;
private project artifacts remain local. No merge or release is authorized here.

**Review outcome.** Both regressions failed before their fixes. The full live
pinned-npm evaluator/oracle table (including both mixed-subtree cases), public
registry probe and evaluator/query cancellation passed in 14.3s. Native owning
race suites and vet passed. A separately built executable previewed the existing
Code Atlas preparation and agreement and inspected its status; all succeeded,
with no task/attempt and drained authority. The unchanged project baseline and
retained-evidence/isolation proof remain the E3 record above; this review does not
extend its audit query time or native dialog coverage.

The full race/vet gate passed on frozen Linux source in **204.8s**, report
`0877bee5e8d6a1c14a7db3cf8e855a7a`, with Go 1.26.9 and unchanged two-CPU/4-GiB
check limits. Synchronizing current `main` introduced no source changes. The
earlier native full-race timeout remains explicitly unqualified. Review logs,
runtime hashes, executable identity and protected-installation observations are
retained under the E3 evidence root's `pr-review-20261022/` directory.

### E2 implementation and qualification — 2026-10-22

Implemented in the same worktree, stopping before E3. A quality check now chooses
an offline command or typed `npm_audit` policy. The approval preview names the
package directory, public dependency metadata, severity/omission policy, deadline,
freshness and pinned image. No role prompt, model permission, external service,
project wrapper or second scheduler was added.

The private adapter runs maintained npm **11.19.0** from the exact local Node
image without network, with only sanitized manifest/lock graph data. It uses
npm's Arborist audit report and meta-vulnerability calculator. Runner's separate
HTTPS transport permits one Bulk Advisory operation and bounded public-registry
package metadata required by that evaluator. It ignores project/host npm config,
credentials and proxy configuration; validates resolved public destinations;
rejects redirects and Quick Audit; and never runs project code. Deterministic
registry fixtures compare full reports and severity exit codes with the same
pinned npm's own `Arborist.audit`/`npm-audit-report` path, including transitive/meta
findings and dev/optional/peer omissions. Input/transport tests cover unsupported
forms, allowlists, malformed/oversized responses, private destinations and outages.
The actual contained cancellation probe confirms both query and evaluator settle.

Receipts are immutable local records with metadata/policy/tool/image identity,
conservative query-window start and settlement times, source attribution,
status/findings and the retained report digest. Reuse preserves the original
record and time while explicitly attributing its assessment to the current source.
Completed history does not trigger polling. A persisted clock high-water mark
blocks backwards time, including after restart. Online observations are excluded
from quality-policy identity; baseline exceptions cannot waive audits.

The coordinator derives audit-only refresh attempts before applicable review and
local/connected delivery. They share task/workspace ownership and global admission
with existing actions, spend no model/repair/base-refresh allowance and retain
source, review and passing offline checks. Failures/unavailability retain attention
for explicit `task resume`; no new automatic query retry loop exists. Focused
engine evidence covers restart, reuse, dependency changes, expiry at publication
and merge authority, findings on unchanged inputs, preserved allowances and
unrelated work admitted while a refresh waits.

`quality check --report ID --preview TOKEN` renews only a retained baseline's
audits. It preserves offline evidence and policy identity, returns a new immutable
report and retains the existing preview/apply approval boundary. Review admission
rechecks expiry after Git inspection and preserves pending retry/resume profiles.
An audit result that expires during downtime uses the existing explicit recovery
to repeat only that query, retaining its original observations and allowances.

The private executable smoke qualifies preview/check/apply, clean public audit,
unchanged receipt reuse, audit-only baseline renewal, expired-baseline refusal,
status and exact-binary
backup/restore with **two immutable audit receipts retained**. It stays drained,
with zero tasks/model calls and unchanged synthetic source. A separate controlled
live audit of public `lodash@4.17.20` observed **failed**, with one affected package
at high severity; it exercises real Bulk Advisory and package-metadata transport
without assuming a clean result. The required `go test -race -timeout 15m ./...` gate passed in **802.8 seconds**
(engine: 792.7 seconds), and `go vet ./...` passed. The first race attempt reached
the engine package's unchanged 15-minute cap without an assertion failure; the
audit-free path now reuses pinned quality and skips unnecessary audit lookups,
and the fresh full run passed. No assertion, timeout or admission limit was
weakened.

Evidence is retained at
`/Users/dan/.local/share/cortexium-runner/npm-audit-e2-20261022/`:
`focused-verified.log`, `live-fixtures-final.log`, `live-public-final.log`,
`operator-qualified/summary.json`, `race-final-result.json`, `vet-final-result.json`
and `verification.json`. Store format **21** rejects earlier authority without
migration; no installed state was converted. The shipped scope is public npm
lockfile v3, ordinary semver graphs and the qualified pinned evaluator. Workspaces,
private/local/git/alias dependencies, other npm versions and oversized inputs or
responses remain unavailable capabilities. E3 Code Atlas qualification is the next
separate boundary; no product work, release, installation change or pilot started.


### E1 implementation and qualification — 2026-10-21

Implemented in the existing worktree, stopping before E2. `quality prepare --file`
previews the built-in `go-node-v1` recipe; explicit `--apply --preview` prepares it
while drained and holding the existing coordinator exclusion. Preparation does
not configure quality, admit work or consume task/model allowances. The existing
quality preview/check/apply and explicit adoption rules remain the approval path.

Config owns exact tool/module/package/output declarations. Execution owns official
image resolution, metadata-only public fetching, explicit private archive imports,
offline tool compilation/installation, private check caches and receipts. Workspace
owns source protection and literal output overlays. Engine connects these to the
existing check/retention path; there is no additional scheduler or plugin framework.
The cache index is replaceable only after complete preparation; approved identities
remain in transactional quality history. Store format **20** rejects older state.

Normal outputs keep their declared paths; only explicitly pinned tracked
placeholders appear in the writable view. Underlying source and Git controls stay
unchanged. Reports use existing bounded retention/read-only grants. Manifests,
locks and placeholder identities are pinned without automatically embedding their
bodies in model prompts. Offline Go checks use `GOTOOLCHAIN=local`, `GOWORK=off`,
`GOPROXY=off`, `GOSUMDB=off` and `GOFLAGS=-buildvcs=false`: verification Git contains
file inventory, not candidate history. This is disclosed in preview and the
operator guide; disposable build outputs get their provenance from Runner receipts.
No application assertion, test deadline, worker/retry policy or role contract changed.

Preparation records and settles both Docker clients and containers. Cancellation
or an uninspectable daemon retains unresolved ownership and blocks preparation or
backup. Settled scratch is discarded; logs/receipts remain. Check clients use an
empty private Docker configuration so host proxy settings/credentials are not
injected. Ordinary checks retain network denial and container-private loopback;
preparation never executes repository code or lifecycle hooks online.

| Evidence | Result and boundary |
| --- | --- |
| Owning suites | Config authority/output validation, absent-vs-empty input identity, private archive/metadata mismatch, source/placeholder/link guards, existing artifact-review journey extended for ordinary output paths, and unresolved-preparation backup refusal. |
| Real recipe | `RUNNER_LIVE_PREPARATION=1 go test ./internal/runner/execution -run '^TestLiveReusableEnvironment$' -count=1 -v -timeout 15m` passed in 123.364s. Two independent layouts cover public Go/npm fetching, a pinned private Go command, missing/tampered private archives, offline lifecycle execution, Chromium startup, source write denial, retained reports, private cache poisoning/reuse, proxy-credential canary and check/preparation cancellation. |
| Executable operator journey | Fresh init → preparation preview/apply → quality baseline/approval → show/status → backup passed; admission stayed drained, zero tasks/model calls, original fixture source unchanged. Same-binary restore preserved the final quality/receipts; a separate different-binary probe was correctly refused. Final executable/state/report evidence is under `/Users/dan/.local/share/cortexium-runner/check-environment-e1-20261021/final-operator/`. |
| Required gates | `go vet ./...` passed (0.399s). Final `go test -race -timeout 15m ./...` passed in 894.963s (engine 884.655s). Logs are under `/Users/dan/.local/share/cortexium-runner/check-environment-e1-20261021/`. An earlier race run was deliberately interrupted when the Docker proxy boundary finding required a fix. |

Live qualification used macOS arm64 / Go 1.27.1 and a local Linux arm64 Docker
daemon. Recipe tools were Go 1.26.9, Node 24.21.0, npm 11.19.0, Playwright 1.64.1
and Chromium 156.0.8078.4; exact image/input identities are retained in receipts.
The guide describes the supported initial dependency forms and complete pinned
Go graph requirement. Other platform release qualification and the full unchanged
Code Atlas baseline are not claimed by this fixture evidence.

The old Runner executable remains byte-identical at v0.7.4. Code Atlas's checkout,
app binary, database, generator guide/project metadata and running PID 75063 are
unchanged against preparation evidence. No release/package/image was published,
no installation changed and no pilot launched. Online npm audits, controlled audit
transport/freshness (E2) and Code Atlas baseline qualification (E3) remain next steps,
with their original boundaries; do not substitute a frozen audit receipt or a
project wrapper.

## GitHub Issues and owner acceptance brief

Status: implemented with the review fixes below and merged in
[PR #107](https://github.com/cortexium-io/runner/pull/107) on 2026-10-18 as
`0cd64f9c4e19ab61d817705ca3404731455dfad1`. This bounded extension uses the existing replacement
worktree. No publication, installation change or takeover of active FlowStack
work is included.

### Supported journey and authority

An issue is a request, not permission to work. One optional GitHub Issues
connection names a repository, an intake label (default `runner:request`) and
an existing overview issue whose body Runner may maintain. It is independent
of code delivery: local completion can publish progress and GitHub delivery
can run without an Issues connection. No GitHub Project is required.

1. Create an issue and apply the intake label. A read-only inbox command lists
   open matching issues and their existing local mappings. Discovery/import is
   explicit in this bundle; there is no autonomous intake polling or webhook.
2. Import one exact issue snapshot as a Plan request or a sufficiently specified
   Ready brief. An optional operator-authored file supplies a clarified brief.
   Retain provider/object identity, source URL and content revision locally.
   Repeated import returns the existing task, including after restart; changed
   issue content cannot replace an existing task or reset its counters.
3. The owner inspects and approves the exact local version using the existing
   approval command. Plan approval authorizes planning only; release still
   approves the shared outcome, children and dependencies. Unapproved imports
   cannot invoke a harness. No new owner agent, approval label or remote-comment
   interpreter is added.
4. Run the normal local process. Closing/editing an issue, commenting or moving
   a board card cannot approve, stop, change or complete local work. A source
   snapshot is historical after import; changes require an explicit local
   replacement and new approval, under the existing released-Plan rules.
5. Follow one GitHub overview with task titles/source links, Plan relationships,
   waiting reasons, owner/attention/working/completed distinctions, PR links and
   the last successful update time. Included children stay visibly undelivered
   until the whole Plan completes. Stopped work is separate from completed work.

### Minimum architecture and failure behavior

Keep source observation and progress publication behind separate narrow
consumer-defined contracts. Reuse the GitHub transport; core approval and
coordinator rules stay provider-neutral. One durable source mapping prevents
duplicate intake; one configured existing overview issue avoids uncertain issue
creation. Validate that the configured overview is an issue, not a PR or intake
request. Runner owns that issue's body; it does not rewrite request bodies,
close requests or manipulate repository labels/protections.

The overview publishes only an allowlist of public progress fields. Never copy
task bodies, approval digests, filesystem paths, raw attention/error details,
transcripts or private evidence. Show bounded category-level wait/recovery
guidance; inspect precise diagnostics locally. Escape untrusted titles and links.

Use one bounded asynchronous projection action admitted by the existing
coordinator, plus an explicit sync command. Serialize manual and automatic writes
per connection; coalesce to the latest snapshot and record sync health locally.
No model slot is consumed by a view. Timeout, missing permission or GitHub outage
affects the connection only and must not stop local implementation or reviews.
Persist one pending body before dispatch. An uncertain response permits only
observation of that exact receipt before any newer write, including after restart.
If it never appears, hold the view for operator reconciliation while local work
continues. A definite rejection permits a fresh attempt after correction. No
remote outcome repeats work approval.
Read-only status/inbox must not write local authority or publish anything.
Backup/restore retains mappings and sync health; inspection-only restore cannot
publish. Use fresh state for an incompatible format; no legacy approval import.

### Verification brief

Extend owning suites rather than build another orchestration harness:

- Store: real SQLite, concurrent/repeated import and reopen retain one source
  mapping and unchanged approval/counters; existing backup round-trip covers the
  added records. Reject incompatible state.
- Engine: imported Plan/Ready remains unapproved; explicit approval follows the
  existing process. One controlled journey covers pending owner, dependency wait,
  child inclusion versus completion, attention, PR link and stopped work. A
  blocked/unavailable view cannot hold unrelated deterministic reconciliation or
  model admission. Synchronization and recovery use channels, not timing sleeps.
- Adapter: GitHub fixtures cover pagination, issue/PR identity, malformed or
  unavailable responses, scoped transport writes and public output redaction.
  Reuse existing process containment and local lifecycle evidence.
- CLI/config: optional connection, explicit import mode/brief, existing owner
  approval commands, read-only inbox/status and bounded sync health reporting.
- Run `go test -race -timeout 15m ./...`, `go vet ./...`, executable smoke and
  a private synthetic GitHub issue/overview exercise. No live model requalification
  is needed unless role instructions or execution semantics change.

### Code Atlas local pilot preparation — 2026-10-21

**Historical preparation pause, superseded by E1/E2 and the [E3 qualification](#e3-code-atlas-qualification--2026-10-22).** The owner rejected per-project
workarounds and requested the [Runner-owned brief](#reusable-check-environments-and-controlled-online-audits-brief)
above. A lockfile-bound preparation audit was proposed, but was **not approved**;
it is not the accepted policy. The custom image/wrapper approach is superseded.
The observations and original preparation boundary below are retained as history.

The owner authorizes a separate local Runner build and isolated check qualification
after the folder-picker work finishes. Stop before a pilot task, role invocation,
public release/package publication or installation change. Preserve the existing
Code Atlas checkout, ignored database/imports/artifacts, running app and old Runner.

The owning chat is [Explore codebase architecture tools](codex://threads/01a15ea9-b8e7-73e2-8a78-527dd74ae32c).
Its completed folder-picker revision is `0ecde915b38fba6b03eca078f29154e795f1f026`,
tree `3d03f08aa0d3793bbe7f2d380e4136485b2bf0ba`, on `codex/code-atlas-mvp`.
There is no Git remote or existing Code Atlas Runner authority. Preparation uses
an independent committed clone of that source; later source movement needs a
fresh impact assessment before launch. Completed MVP work is not resubmitted.

Build Runner from reviewed main `d95aceeab6bc2d22c7fd38acb4980d341a7385e5` into
a private tool directory, recording source/build/binary identities. Use fresh
store-format-19 configuration/state/worktree roots, local result delivery, human
integration, `gpt-6.1-sol / high`, one worker and bounded existing allowances.
Do not replace a PATH binary, install a service, import legacy state/approvals,
create a product task, start `run`, or publish an overview. A fresh drained init
and quality preview/check are preparation, not a pilot launch.

The project's AGENTS.md/README and pinned Genny version own check requirements.
Use the existing Go/frontend/generated-output suites and unchanged Playwright
journeys; add no product tests or framework. Task checks cover Go/frontend
regressions and generated ownership. Completion checks add the original frontend
build and browser journeys. A standalone Ready task runs both. Baseline checks
run once after cheap capability probes pass; do not repeat sufficient proof for
documentation-only changes. Preserve assertions, one browser worker, zero retries,
60s test/10s assertion budgets, real scanner/persistence and narrow-screen proof.
Native macOS chooser interaction remains separately manual; Linux browser mocks
of its returned path do not prove an operating-system dialog.

Prepare one credential-free, pinned local check image with locked offline Go/npm
inputs and the exact Playwright browser, retaining preparation provenance. Checks
use Runner's production container boundary: private loopback, no host/external
network, non-root, read-only image, 4GiB/two CPUs/512 processes. Generated dependency,
build and browser output must finish below `.runner-output/`; preserve every source
byte, logical index and Git control. Retain selected reports/screenshots through
the source-bound artifact contract. Qualify loopback and host/network isolation,
normal source integrity, an exited-zero source-corruption control, read-only
reviewer access and cancellation/ownership cleanup through the actual boundary.

The required npm audit needs external network. The earlier proposal to bind a
preparation snapshot to manifests/lockfile is superseded by Runner-owned live
auditing and freshness in the new design brief. No snapshot exemption, relaxed
network setting or audit policy was applied during preparation.

Finish with retained check receipts, source/tool identities, applicable limits,
fresh empty/drained status and before/after installation/app observations. Leave
any approval or capability blocker explicit. Private preparation material belongs
under `/Users/dan/.local/share/cortexium-runner/code-atlas-pilot-20261021/`.

At the pause, the separate binary reports `cortexium-runner local-d95acee` and an
independent committed Code Atlas clone exists. No fresh Runner store, quality
baseline/agreement or product task was created. A public Playwright base image was
downloaded privately; no tailored check image was built or published. Offline
module-input staging stopped while resolving the pinned generator's own checksum
metadata; nothing in the project or installed toolchain was patched. These are
preparation observations, not qualified checks or an end-to-end pilot result.

### FlowStack pilot brief and safe handover

**Current disposition, refreshed 2026-10-21:** the original six-card outcome is
already delivered. The legacy owner's live status reports no active, queued,
waiting, blocked or integrated-undelivered work. Parent #507 records successful
delivery through [PR #514](https://github.com/flowstack/webapp/pull/514), merged
on 2026-10-19 into `develop` as `c75ef8973b9a1ad1c23d6939213e623a513a62b6`.
Both previously remaining cards, #510 and #511, are accepted. The old private
plan branch is no longer a remote ref. Do not recreate, adopt or dispatch its
completed implementation obligations.

Current `develop` is `607e785b603dab54a45810b9dcb15aa4c4f1f1ac`, including
the independently qualified browser/runtime work merged through PR #515. Its
tree is `0c4ad1a707244ee3566b0e450cb8b496bb4ab2ee`; the authorized dev Docker
update runs that same revision. The existing Runner binary/configuration/service
remain unchanged, with legacy PID 66004 still polling. No new Runner pilot,
hold, intake issue, quality application or task approval has occurred.

The owner has authorized reviewing/merging the pending Runner support changes
and progressing toward a bounded pilot; release/package publishing and installed
binary/configuration changes remain separate. After learning that the proposed
remainder is empty, the owner chose **a different useful task before launching**,
rather than a verification/adoption repeat of this completed plan. There are no
open webapp issues at this observation. Select and brief that new outcome before
any new request, quality application, approval or pilot launch; do not invent a
defect or dispatch completed scope. No old-owner hold is needed for this
preparation. The 2026-10-18 observations and proposed handover below remain
historical evidence, not current work or launch authority.

**Prepared 2026-10-18; authorization is observation and this brief only.** No
FlowStack issue, source, approval, process, service, installation, protection or
container was changed. The proposal below needs a separate launch decision.
It does not reopen Runner feature construction or authorize product deployment.

#### Observed ownership and retained work

At approximately **14:15 Europe/Copenhagen**, the real legacy `status --json`,
process/launchd observations, Git refs and GitHub APIs established:

- The session is [New FS Frontend 2](codex://threads/01a10c72-e48d-7690-95b6-371f9e503664?hostId=remote-ssh-discovered%3Aalways-on).
  Its working directory is `flowstack/frontend`; the requested new frontend is
  the **separate `flowstack/webapp` repository**, confirmed by Operations registry,
  the installed config and source Plan [#507](https://github.com/flowstack/webapp/issues/507).
- Legacy Runner **v0.7.4**, binary SHA-256
  `6baac0a523255d69c7faa1c8744450f6500130932d29860d91908fbc38d1eb72`,
  remains running as PID **66004** on `always-on`. The launchd service is
  `gui/501/io.cortexium.runner.flowstack-webapp`, with `KeepAlive` and `RunAtLoad`.
  There were **zero active/queued jobs, one blocked card, one dependency wait and
  four integrated-undelivered cards**. Idle is not a handover or revocation.
- The remote `develop` is `fc7b45fafb1839da7c9b5c26f42265d8e205cc51`.
  The clean ordinary checkout is `bc6f6c7661daaf4a9ce6e9e38dcef4e85d6277a9`,
  fourteen commits behind. Do not use that checkout's HEAD as the pilot base.
- The published private plan branch is
  `runner/plan-d7ed9795adfd88748ced75ce10713653`, head
  `3fe7a48c223c80e51b27496abcb735dca3363907`: seven commits ahead of that remote
  `develop`, with 26 changed files. No open webapp PR was observed.
- Docker still runs `flowstack-webapp:develop-20261010-185726-fc7b45fafb18-2d992782`.
  Private plan integration does not mean destination delivery or deployment.

| Original card | Observation | Pilot treatment |
| --- | --- | --- |
| [#508](https://github.com/flowstack/webapp/issues/508), schema search/sort | Integrated; candidate `ec09ef22ca4a7fa73f63c7817c1373bbafa2a67c` | Preserve; verify combined behavior. |
| [#509](https://github.com/flowstack/webapp/issues/509), rooted collection editing | Integrated; candidate `2882cff075c85fc10ed3ab816a35d5606b148dde` | Preserve; verify navigation, identities and save boundaries. |
| [#510](https://github.com/flowstack/webapp/issues/510), guided segments | Blocked at `agent_qa`; retained attempt says **“Base branch is not ready for QA”**, classified `transient_external` | Inspect/adopt source, then obtain fresh checks and independent review; do not assume a product defect or a pass. |
| [#511](https://github.com/flowstack/webapp/issues/511), relationship/membership authoring | Ready, waiting for #510 | Complete the remaining supported behavior after guided adoption. |
| [#512](https://github.com/flowstack/webapp/issues/512), controlled Vue Flow canvas | Integrated; candidate `630be39355ecf85a2fddc91515988e8fe2d0e643` | Preserve; verify graph/history/persistence. |
| [#513](https://github.com/flowstack/webapp/issues/513), palette/connections | Integrated; candidate/plan head `3fe7a48c223c80e51b27496abcb735dca3363907` | Preserve; verify atomic connections and reconnection. |

Guided source is a clean, **unaccepted** commit
`81bc447317ecf67d6d784e5be6161d68d077ab47`, parent
`2882cff075c85fc10ed3ab816a35d5606b148dde`, in
`/Users/dan/Projects/flowstack/.runner-worktrees/assignment_pvti_ladoba7pyc4bh7-9zhbbxjo-dcf072f08f9aa827568504b402e26b53`.
It changes twelve files, including existing segment suites. It is not published
as an assignment branch. A copy-only 72,130-byte source patch and commit/tree/parent
identity were retained privately during preparation; patch SHA-256 is
`948dad92fecfa87e16235760b6b1d827df1e28b205e491d5a50740ca1ee34e60`.
This protects the unique delta without approving or applying it. A full verified
bundle remains part of handover before any old-owner cleanup.

A second observation at **14:27 Europe/Copenhagen** confirmed the same refs,
card counts, PID and Docker image. The controller was processing a poll, with
zero active assignments; it continues to own the work.

#### Proposed outcome and delivery boundary

Use **one new Plan**, starting from an isolated, freshly approved source baseline
that preserves the four integrated cards. Its shared outcome is the original
six-card UX result, with only two remaining child responsibilities:

1. **Guided adoption:** inspect the retained #510 delta, adopt sound work and fix
   demonstrated gaps. New/eligible segments default to guided editing; searchable
   fields, typed readable conditions and nested all/any groups work. Pending edits
   have apply/cancel and gate save/preview. Raw recovery preserves unsupported or
   malformed source, explicit conversions and saved-versus-draft preview semantics;
   server totals, duplicate-submit protection and account/route isolation remain.
2. **Relationships, dependent on guided adoption:** establish producer-backed
   discovery, authenticated bounded choices and typed identities, then expose
   relationship comparisons, membership and collection having/not-having. Distinguish
   storage lookup keys from relationship values. Preserve missing references and
   unavailable states; enforce current-segment, child-segment and referenced-parent
   restrictions. Save/reopen must retain equivalent query meaning; unsupported
   forms must not broaden or silently replace a query.

The owner reviews the actual proposed children and proof obligations before
release. Reused work is source/evidence to assess, never inherited approval.
Do not rebuild four accepted cards or add unrelated parity/features. Preserve their
shared guarantees: schema metadata and canonical order/identity, dirty/invalid
inputs, rooted/inline collection save boundaries, Flow topology, undo/redo,
producer/sendout identity and uncertain-write reconciliation. No backend, protos,
Genny or Runner implementation changes are included; a demonstrated dependency
issue returns to its owning repository and pauses only affected work.

**Proposed pilot completion is a verified local result branch**, with human merge
permission and the optional GitHub Issues overview. Neither the working checkout
nor `develop` is updated. The receipt must say local result, not merged/delivered
into `develop`. Preserve a final diff against current remote `develop` for human
review. Opening a product draft PR, rebase/merge and deployment require later
explicit authorization and applicable product checks.

This boundary is deliberate: effective GitHub rules require linear history and
**rebase-only PR integration**; the new Runner's hosted completion supports ordinary
merge receipts. Also, `develop` has strict/admin-enforced protection but **no
required check contexts**; hosted `Validate` runs only for PRs to `main`. Do not
weaken those rules, invent a CI context or enable automatic merge for this pilot.
The [Operations generated-file policy](https://github.com/techbase/operations/blob/main/docs/workflow.md#generated-file-gate)
and its coordinator guidance additionally require `Techbase / generated ownership`
before hosted product admission. Its context is absent from current required
checks. The local-result policy below retains the ownership checker without
claiming hosted admission; protection onboarding remains a prerequisite for later
PR delivery. This is a product-policy boundary, not a new Runner requirement or
permission to edit protection. Operations registry is not a replacement scheduler.

#### Verification brief

Repository `AGENTS.md` and the exact pinned quality agreement own required proof.
The task author fixes expected behavior and evidence boundaries; implementers
extend owning tests only for credible defects, and independent review checks
adequacy and redundant complexity. No separate test framework or coverage target.

| Boundary and expected result | Owning coverage to inspect/reuse or extend |
| --- | --- |
| Guided: typed comparisons, nested groups and apply/cancel preserve exact source until deliberate apply; invalid/pending edits cannot save or preview | `segmentBuilder.test.ts`, `SegmentBuilder.browser.test.ts`, `SegmentEditing.browser.test.ts`, `SegmentPreview.browser.test.ts`, `tests/e2e/segments.spec.ts`. |
| Relationships: schema-selected typed identity, bounded authenticated retrieval, missing references, segment restrictions and nonempty includes/negation survive author/save/reopen | `profileContract.test.ts`, `schemaValidation.test.ts`, segment/service/browser suites above; extend their owners for the concrete new forms. Producer source inspection is separate evidence from fixture assertions. |
| Preserved four-card work: presentation sorting leaves canonical drafts/selection unchanged; rooted navigation preserves metadata and save boundaries; Flow movement/connections preserve topology, history and producer identities | `schemaDraft.test.ts`, `SchemaEditor.browser.test.ts`, `SchemaRoutes.browser.test.ts`, `flowCanvas.test.ts`, `flowGraph.test.ts`, `useFlowDraft.test.ts`, `FlowRoutes.browser.test.ts`, schema/Flow e2e suites. |
| Usable combined result: wide/narrow keyboard/touch journeys, long labels, pending/empty/error/recovery states and save/readback/reopen | Candidate-bound browser receipts and screenshots at **1440×900** and **390×844**, compared with the pinned problem captures. Fixtures use synthetic `example.test` records, intercept API traffic and abort unapproved origins; they establish UI/fixture behavior, not live-service compatibility. |

Existing suites were located in the retained source, not guessed. Inspect focused
selection and apply the repository's `test:affected` limits for dynamic/global
changes. Batch relevant browser selections through **`npm run test:e2e`**, never
invoke Playwright directly or override its owned output/worker lifecycle. In the
quality agreement, name task-level checks for both remaining obligations and one
`at: completion` combined gate. Baseline runs all declared checks. Final combined
validation requires **`npm ci` followed by `npm run validate:complete`**, sequentially,
with the repository's fixed workers/timeouts and required Chrome and WebKit
projects. Do not prepend `validate:fast`, repeat sufficient exact-candidate proof,
weaken assertions or treat a failed/interrupted gate as an accepted baseline.
These are the logical repository gates, not a qualified check command yet:
Runner's `VerifyCopy` rejects added source/JSON/manifest files outside
`.runner-output`, including ignored files. Ordinary `node_modules`, browser JSON
reports and build output therefore need a source-preserving preparation/output
route. Qualify that route through the existing entrypoints and allowed output
boundary; do not bypass the source guard or treat ignored files as trusted.

The new Runner's native workers have no browser tool or external networking.
Its supported offline Docker check boundary is a possible execution route, **not
yet qualified for this repository**. The host's Node 24.20.0/Chrome/WebKit and old
heavy wrapper do not prove container readiness. The observed images included only
plain Node images, not a prepared browser check image. Before launch, prove locked
dependency installation/output placement, Chrome's required `chrome` channel,
matching WebKit, private fixture loopback, artifact retention/access for independent
review, and cancellation in the **actual**
check boundary, under its fixed 4 GiB/two CPU/512-process limits. Pin the exact
already-local image ID and preparation inputs; bake in no credentials. Do not
substitute browser engines, expose host networking/socket/config, expand sandbox
permissions or change source/limits to manufacture a passing baseline. An
unavailable capability blocks launch and gets a separately scoped decision.

References are pinned data, not execution authority. Retain the source Plan #507
contract and the visual reference at `a0eed1c8b1be56830f7cbe31bc5f17bb75df6755`
(`comparison.json` and its three problem PNGs). Producer references remain backend
`e1af66652a6a3f34dbc79854f3e199c379911845`, protos
`c6c8e775231f9279f1d4df061df665e5d75b9307`; legacy frontend
`714128eaeb8e3805431f8fdeaa49a570e2830cea` and old webapp
`442f5e67d74a1e914568a9a771a6e606057964e6` supply interaction examples only.
The new runtime does not inherit the old external reference-path grants. Prepare
only the necessary inspected producer excerpts, problem captures and retained
segment patch as inert, repository-local pilot inputs in the isolated baseline,
with original commits/content hashes. Do not copy credentials, `.git`, old task
metadata or whole private reference trees. Exclude those temporary inputs from
final product delivery after retaining their provenance privately. If safe input
preparation is not possible, hold the pilot rather than add a reference framework.

#### Safe handover and launch gates

These are **future steps**, requiring explicit authorization to hold the old
owner and launch the isolated pilot. They were not executed during preparation.
Qualify the check environment and resolve the product-policy question **before
requesting the hold**, using a frozen isolated source copy. Those capability
probes admit no product/model work and publish nothing. Refresh source-bound
evidence when handing over; do not leave the old owner stopped while discovering
whether the proposed environment can run the repository at all.

1. Re-observe the session, real status, processes, remote `develop`/plan refs,
   retained worktree and open PRs. If anything advanced, update this disposition
   before approving remaining scope. Let the existing owner finish if it completes
   the batch first; select only a demonstrated remainder. Preserve unique source
   and receipts before requesting any old-owner change, then refresh/verify the
   snapshot after it stops. Never duplicate dispatch.
2. Use the installed owner's supported graceful stop, scoped to this config:
   `cortexium-runner stop --config /Users/dan/.config/cortexium-runner/flowstack-webapp.json --wait --timeout 5m`.
   It stops admission and lets active work settle. Verify process exit, launchd
   stop acknowledgment and no replacement/KeepAlive respawn; check outstanding
   model, heavy-check, browser and remote-publication ownership. A timeout or an
   idle status is not proof of relinquished ownership. Never kill unrelated work.
3. Retain matching binary/config/launchd hashes, status, metrics/evidence, original
   requests, branches and all tracked/untracked source. Export the plan and guided
   commits into a verifiable Git bundle; record parents, trees and file hashes.
   Keep the originals untouched. Resolve any pending remote effect before another
   owner can write. Do not import old approvals, counters or result classifications.
4. Use a new private root, proposed
   `/Users/dan/.local/share/flowstack/runner-pilot-20261018/`, with sibling
   `source`, `state`, `worktrees` and `evidence`. Build/copy Runner from reviewed
   main `0cd64f9c4e19ab61d817705ca3404731455dfad1` into that root and record its
   hash; never replace `~/.local/bin/cortexium-runner`, installed skills or launchd.
   In the new repository only, pin the refreshed private plan head as the source
   baseline, retain the unaccepted segment delta as inert input, and record the
   resulting operator-input commit. Preserve a diff against destination `develop`;
   do not mistake the seed for destination delivery.
   Compare the seed with freshly observed remote `develop`; source drift/conflicts
   require reviewed adoption and a fresh baseline. Later destination movement needs
   fresh delivery verification; the local result receipt does not cover that move.
   Configure `repository.base_ref` as `refs/heads/runner-pilot-baseline` in that
   isolated repository, and delivery as `{"mode":"local","local_target":"result"}`.
   This local baseline is distinct from the eventual hosted `develop` destination.
5. Revalidate the prepared check environment and product-policy decision. Complete
   fresh init and the quality preview/check/apply flow; inspect each
   result, including all baseline checks. Existing failed proof or unavailable
   prerequisites cannot be excused as pilot defects or silently waived.
6. Only after these gates, create a new request and a separate dedicated overview
   issue in `flowstack/webapp`. Proposed intake label: **`runner:pilot-request`**;
   never apply the old owner's `needs-assessment` label. The overview has no intake
   label and its body is Runner-owned. Import as a Draft Plan, inspect/approve its
   exact local version, then inspect/release the proposed children separately.
   Labels/comments and previous authorizations cannot approve the new tasks.
7. Proposed settings: Codex **gpt-6.1-sol / high** for all three roles, planner
   20m/implementer 60m/reviewer 30m; max parallelism **2**, one repair, one transient
   retry, one base refresh and **8 harness invocations per task version**, no
   escalation ladder. Check timeouts are separate and must be measured in the
   qualified environment before applying quality. Delivery is **local result**,
   merge policy **human**. These are fresh visible limits, not migrated budgets.
   Keep private diagnostics local; verify overview owner/working/waiting/attention
   and final local-result categories, with no private proof or premature completion.
8. Stop after the combined checks and independent outcome review establish the
   original shared outcome, one durable local result receipt covers parent and
   children, and the overview matches. Retain final candidate/delta, evidence,
   allowances, recovery observations and remaining limitations. No automatic
   closure of old issues, product PR/merge, deployment or installed-owner restart.
   Re-enabling the old service after any new local/remote effects requires explicit
   reconciliation; it must not resume the original authorized batch concurrently.

Preparation evidence is private at
`/Users/dan/.codex/runner-qualification/flowstack-pilot-20261018/`: legacy status and
attempts, source Plan/contract, retained plan tree/compare, browser lifecycle
source and observation hashes. This is **a prepared brief with launch gates**, not
proof that the new runtime has executed FlowStack or that visual parity is achieved.

#### Browser/check qualification and product policy, 2026-10-18

**Initial result: policy defined; environment not ready to launch.** The owner
authorized isolated qualification and policy resolution, stopping before pilot
launch. The two initial blockers are recorded below. The subsequent retained
artifact bundle resolves proof access; email check reliability still blocks
launch. The legacy process, service, source and deployed container remain untouched.

**Local pilot policy:** preserve FlowStack's repository-owned local verification
requirement, and enforce generated ownership locally using the trusted Operations
checker. Do not add `required_ci` to local delivery or fabricate a hosted status.
This resolves which checks apply to the proposed **local result**, without
declaring that result admitted to `develop` or ready for a hosted merge.

- Task checks cover each child's approved proof using existing lower-layer and
  browser suites. The final combined check is `at: completion`: locked `npm ci`
  followed by `npm run validate:complete`, sequentially, with the repository's
  original workers, browsers and timeouts. Baseline must run all declared checks;
  focused smoke evidence is not a complete baseline or pilot acceptance.
- Generated ownership applies to both task and completion boundaries. Its source
  is the clean canonical Operations commit
  `5c1a988485b687f33db847b7ece14e899b4d2394`; reuse `generated_guard.py` and
  `regeneration.py`, not a second protected-path policy. Its 22 owning tests pass.
  On frozen source `3fe7a48`, it reports success with no protected paths or required
  regeneration recipe. The disposable source index produces tree
  `6baa331dac7b428c734174f05c8a1f3bc7ca0b5d`, exactly matching that Git commit.
  The check constructs a private verification commit from that inventory and
  pinned baseline objects. It verifies **tree contents**, not the actual product
  commit identity; it publishes nothing and executes no candidate program.
  A future protected-file change must satisfy the same reproduced-output/recipe
  rules or block. Pin checker, baseline objects and wrapper in the check image;
  rebuild/requalify them if the approved baseline changes at handover.
- Operations' hosted requirement remains a separate prerequisite for later
  hosted product admission/PR delivery: strict/admin-enforced
  `Techbase / generated ownership` protection and its actual published PR-head
  result. Neither its absent context on `develop` nor rebase-only integration is
  waived or changed here. The generic Operations workflow still describes the
  older hosted/Project route; this local-result qualification does not migrate
  that workflow or grant hosted merge readiness.
- Missing capabilities, failed or interrupted checks still block readiness.
  Browser fixtures prove only their declared synthetic-service scope. No real
  backend, visual parity, guided adoption or relationship work is accepted by
  this environment exercise.

Qualification uses a private export of Runner main `0cd64f9` and a caller of its
unchanged `Codex.Check`, source-copy verifier and native permission builder.
It runs on `always-on` against a separate archive of frozen source `3fe7a48`;
the original checkout, Git metadata, legacy state and service are not mounted.
Private preparation helpers and images are not shipped Runner or product code.
The fixed boundary remains non-root, read-only image, no capabilities, private
IPC, network disabled, four GiB, two CPUs and 512 processes.

The final prepared image is
`sha256:ba1c43c211e5f97f81260c26d5b2f103c0753f8ef7121ca28fba5767ed8fde82`
on the native Intel Docker host. It uses Node **24.20.0**, the locked Playwright
**1.62.1**, actual Chrome channel **155.0.8059.84** and its matching WebKit
**26.5**, from the Debian Bookworm Node base pinned at
`sha256:be23f54a88d34e8824c741b19b91064094f92c1c97b194144bfc8b50d67258e2`.
Earlier image/run identities are retained separately, not relabelled as final.
No image was published or installed as a service.

| Probe | Observed result |
| --- | --- |
| Containment and fixture loopback | Chrome and WebKit render; host service/external network, Docker socket, host configuration and inherited sentinel access are denied. The final-image cancellation checkpoint repeats these assertions with both browsers and the fixture server live. |
| Locked install and output route | Offline `npm ci` installs 408 packages in 17–19 seconds in the real source root. After the original scripts exit, only newly generated dependencies, reports and build output move under `.runner-output`, with internal relative links preserving paths. The browser run retains about 280 MiB. Normal runs pass original source, logical Git-index and control integrity checks. |
| Earlier lower-layer smoke | `segmentBuilder.test.ts`: 18 passed; `SchemaEditor.browser.test.ts`: 38 passed. These ran before the two Git capture setup corrections below. |
| Repository end-to-end selection | Through `npm run test:e2e`, six cases use the original two workers, 30-second test deadline, 15-second assertions and zero retries. Three pass: both schema layouts and mobile Chrome email creation. Desktop Chrome and both WebKit email save/reopen cases time out. The 124.378-second browser receipt correctly says failed/incomplete, with zero skips/flaky outcomes. |
| One bounded focused reproduction | The identical desktop WebKit email creation/save/reopen case passes alone through the same entrypoint, source, browsers, resource limits and deadlines: one passed in 18.7 seconds, one active worker. The final image changes only the wrapper's ability to accept that native file/project selection. This is focused proof, not renewal of the failed selection. |
| Source-corruption control | A command returns zero after appending to the disposable `package.json`; `VerifyCopy` rejects it as changed source. The frozen source remains unchanged. Git output exclusions do not bypass this guard. |
| Cancellation | After both browser processes and the fixture are live, actual context cancellation returns **unavailable**, `stopped: true`; the owned container is absent and the intact source copy passes verification. No timeout is mistaken for passing proof. |
| Independent reviewer proof access | Native audit policy reads the permitted check log but gets `PermissionError` for its retained PNG. **Blocked.** Log grants do not cover browser artifacts. |
| Complete gate | **Not run.** Required smoke proof already fails and required independent artifact access is unavailable. No complete baseline, quality application, Plan, task approval or model invocation is claimed. Check timeout selection remains pending a passing complete run. |

Two preparation failures were corrected **only in the private image**: Docker
Desktop's Git ownership mapping, then changing Runner npm logs included in browser
capture. Trust is limited to `/workspace`; Git source discovery excludes only
untracked `/.runner-output/`. Tracked files remain included. No source/index
controls, repository entrypoints, assertions, browsers or limits were changed.
The first native-access probe also misparsed macOS Python diagnostics as JSON;
its bounded rerun separated diagnostics and confirmed the same access denial.
All failed attempts remain in the evidence rather than disappearing after retry.

The sampled six-case container peaked at **3,097,903,104 bytes**, below its four
GiB limit, with zero observed cgroup OOM events/kills; Docker also reported no OOM
kill. This supplies no evidence of missing writes or a memory kill. Passing the
same WebKit case alone suggests a load-sensitive timing/performance problem;
one focused pass does not establish its precise cause or make the two-worker
gate reliable. Preserve the failed save/readback and reload states for diagnosis.

**Smallest follow-up boundary, before any pilot or old-owner hold:**

1. **Runner proof access:** define and implement bounded read-only access to
   retained check artifacts for candidate and combined-Plan review. Bind artifacts
   to the applicable check/candidate and validate containment/integrity; do not
   grant the whole check copy, another attempt, host paths or writable evidence.
   Use the existing engine quality/planning and execution owning suites for the
   concrete risks: a real PNG/report is readable, writes and sibling/escaping
   paths remain denied, stale/tampered evidence cannot satisfy current proof.
   Requalify native access and run Runner's race/vet gates. No new evidence service
   or browser permission framework is needed.
2. **FlowStack check reliability:** diagnose the retained email timeouts in the
   product's existing browser lifecycle under the same frozen source and limits.
   Establish whether cold compilation/load, fixture synchronization or application
   behavior owns the delay; the current evidence does not settle that distinction.
   Any product/tooling fix belongs to a separately authorized FlowStack change.
   Preserve the assertions, fixed workers and deadlines; do not accept a failed
   baseline or use a narrow passing rerun as a waiver. Once resolved, obtain fresh
   representative browser proof and a passing `npm ci` → `validate:complete`, then
   pin measured timeouts and the actual quality agreement before handover.

At **15:07 Europe/Copenhagen**, the legacy service still runs as PID **66004**:
zero active/queued, one blocked, one waiting and four integrated-undelivered.
Binary/config/launchd hashes match preparation; deployed container/image and both
remote `develop`/private-plan refs are unchanged. All **seven** owned qualification
containers are absent. No source adoption, remote publication/status write,
issue/PR/protection change, deployment, installation or service change was made.

Private evidence is retained at
`/Users/dan/.codex/runner-qualification/flowstack-check-environment-20261018/`
and on `always-on` at
`/Users/dan/.local/share/flowstack/runner-check-qualification-20261018/`:
preparation input hashes, trusted checker/source exports, image/package metadata,
each actual check/process/integrity observation, failed and focused browser
receipts/screenshots, resource sample, native policy denial and final ownership
observations. This section is the follow-along record; a GitHub overview begins
only with separately authorized pilot launch. The passing setup controls do not
qualify the full FlowStack gate or visual outcome. The shipped Runner diff for
this pass is documentation only; no Go implementation gates were rerun.

#### Retained check artifacts: implementation brief

Authorized 2026-10-18: remove the demonstrated reviewer proof-access gap;
stop before the FlowStack pilot. Product check reliability, publication, installed
skills/services and the running installation remain outside this bundle.

Supported behavior and expected results:

- A quality check may declare literal `artifacts` files/directories relative to
  the disposable source root, strictly below `.runner-output/`, independently of
  the command's working directory. No glob, source root, parent traversal,
  duplicate or overlapping selection. Omission retains the
  existing log-only contract; changing selections changes the quality revision.
- After the check stops and source/index verification passes, retain only those
  selected regular files in a new private bundle beside its check log. Preserve
  relative report/image paths; do not retain caches/dependencies by default.
  A small manifest binds file hashes to the source fingerprint, check identity
  and log digest. Fixed bounds: 10,000 entries, 32 MiB/file, 256 MiB payload and
  8 MiB manifest. Missing/empty declared output, links, special files, unsafe paths
  or overflow make proof unavailable; never silently drop files.
- Candidate review receives its matching artifact bundle read-only. Combined-Plan
  review also receives correctly identified child evidence; baseline and explicit
  repair evidence retain their own source binding. Validate bindings and bytes
  before dispatch and again after the role returns. Changed/stale/relocated proof
  cannot satisfy current review. No check workspace, sibling attempt or host-root
  read grant, remote upload, browser permission or artifact execution is added.
- Keep the responsibility in the existing execution/check boundary and coordinator;
  no new service or plugin framework. The authoritative artifact declaration and
  receipt contract uses store format 19, preserving/rejecting older stores rather
  than allowing an older executable to ignore required proof. No migration or
  installation is part of this work.

Verification extends config validation, execution check coverage and engine
quality/combined-Plan coverage: real PNG/report retention, current-source grants,
baseline/child attribution, read-only containment, missing/changed/extra output,
links/escapes, limits and an exited-zero command that changes source. Reuse the
secure filesystem and backup suites; include the retained bundle in backup roots.
Qualify actual native sandbox reads/writes and recheck an already retained
FlowStack browser PNG/report in isolation, without rerunning the unrelated failing
email selection or launching work. Complete race/vet gates and full diff review;
record evidence and remaining product blocker here.

#### Retained check artifacts: implementation and qualification

Implemented in the same worktree, from `48f907e`. `QualityCheck.artifacts` is
an optional, owner-approved part of the quality revision. The existing execution
package owns retention and receipt verification; the coordinator captures only
after source/index checks and grants only the matching retained bundle.
The compact observation points to a hashed manifest instead of embedding every
file hash in model prompts or the transactional store. No dependency, role prompt,
new package, upload service or plugin framework was added.

Candidate and combined-Plan reviews get applicable source-bound artifacts.
Historical child attempts are filtered to each child's current candidate/version/
generation; child and baseline proof stays separately labelled. Explicit check
repair can read its original failed-check bundle. Evidence is verified before
dispatch and after return. Tampering is an **integrity** condition, not a resumable
interruption; unavailable checks preserve their actual missing-capability reason.
Capture failures remain unavailable and cannot become baseline exceptions.
Backups include retained bundle roots. Store format **19** rejects/preserves format
18; no existing state, installation or running service was upgraded.

Verification evidence:

- Existing owning suites now cover quality approval identity, output selection,
  source/index guard precedence, baseline/current attribution, combined/child
  access, before/after tampering, stale bindings, links, closed-set contents and
  fixed budgets. Existing store tests prove read-only/read-write rejection leaves
  older database bytes unchanged; backup and secure filesystem suites are reused.
- `go test -race -timeout 15m ./...` passed on macOS arm64 with Go **1.26.9**;
  the engine suite took **890.590 seconds** under concurrent qualification.
  Final failure classification and diagnostic refinements received fresh owning
  config/engine race coverage (**31.817 seconds** for engine); unchanged broad
  results are reused. Final macOS `go vet ./...` passed. No timeout or worker
  setting was increased to pass a check.
- Final Go sources passed `go test -race -timeout 15m ./...` and `go vet ./...`
  on Linux arm64, Go **1.26.9** (engine **93.708 seconds**). The disposable,
  network-disabled container used image
  `sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c`,
  UID 501, init, read-only source/module-cache mounts and private build scratch.
  Exact Go-source hashes match the final worktree. Both test containers were
  removed. The repository check environment's four GiB/two CPU limits are unchanged.
- `TestLiveRetainedCheckArtifacts` passed with Codex CLI **0.163.0** on macOS
  arm64 (**0.14 seconds**) and native macOS x86_64 on `always-on` (**0.38 seconds**).
  The same actual native permission probe and artifact suite passed on Linux
  arm64 (**0.25 seconds** for the native probe), UID 1001, inside a disposable
  Docker Desktop container using the existing qualified image
  `sha256:3fba821b15b02a016ac719ed9d3e5e409dc4c4b2180f503b58afe8c38a8b7346`.
  That container used `--init`, `seccomp=unconfined`, no network/host mounts or
  credentials, and was removed. This is native Linux containment inside Docker,
  not bare-metal/default-seccomp qualification. An initial copy/start sequencing
  error ran no tests; its log is retained separately from the successful run.
- The actual retained FlowStack six-case browser run
  `dc3f08d1-71e9-4a2d-9e56-16a69c7c56cb` was rechecked against a fresh production
  copy state: its original source, logical index and Git controls still match
  frozen `3fe7a48`. Eighteen report/image/context files were captured, preserving
  source fingerprint
  `8b3160ee07034cdffafb46869356efb339e9cf176bb84bbf8f011c5048c1b94b`.
  Actual native audit policy reads its WebKit PNG and browser JSON report while
  denying writes, the original workspace and sibling evidence. Retained hashes
  still match after the probe. Its failed/incomplete result remains **failed**;
  no product test execution or receipt timestamp was relabelled as passing.

At **15:41 Europe/Copenhagen**, the legacy process is still PID **66004** and the
binary/config/launchd hashes and deployed container/image match preparation.
No pilot Plan, approval, model call, issue/status/PR write, publication, protection,
deployment, installation or service change occurred. Private evidence is in
`/Users/dan/.codex/runner-qualification/flowstack-artifact-access-20261018/`, with
earlier/native logs under the prior check-environment evidence root, and remotely
in `/Users/dan/.local/share/flowstack/runner-artifact-access-20261018/`.

The remaining launch prerequisite is **FlowStack check reliability**, followed by
a passing complete product gate and fresh applied quality with explicit browser
artifact selections. This bundle qualifies evidence retention and contained read
access; it does not claim model visual judgement, product parity, a passing full
FlowStack baseline or permission to stop the old owner/launch the pilot.

#### FlowStack email-check reliability: implementation brief

Authorized 2026-10-18: diagnose and repair the load-sensitive email checks in an
isolated FlowStack checkout based on frozen plan `3fe7a48`. No pilot launch,
handover, publication, remote provider write, deployment or installation change is part of
this bundle. Product changes belong to FlowStack, with evidence recorded here.

Supported behavior and proof obligations:

- Creating a visual email makes no write before explicit Save. Save makes one
  creation request; success requires readback of the persisted record. Reload
  preserves the authored MJML, personalization and preheader. Existing rejection,
  lost-acknowledgement and copy behavior must remain covered by their owning suite.
- Reproduce the original six-case schema/email selection with the repository's
  managed `npm run test:e2e` lifecycle, original two workers, four browser/layout
  projects, deadlines and zero retries. Retain traces/timings and fixture request
  observations sufficient to distinguish missing writes, application delays,
  cold compilation and test synchronization. A focused pass cannot waive a
  concurrent failure; report all failed attempts.
- Keep the prepared Runner boundary: pinned dependencies/browsers, no network or
  credentials, non-root, four GiB/two CPUs, disposable source and Git inventory.
  Fix the demonstrated owning surface with minimum complexity; do not relax
  assertions, reduce concurrency or add a test framework. The owner's subsequent
  approvals permit only the named whole-journey budgets recorded below.
  Assertion deadlines and all other budgets remain unchanged.
- Extend or consolidate existing coverage only for a concrete regression risk.
  Run relevant lower-layer/tooling checks, repeat the representative concurrent
  browser selection with a bounded run count, then run locked `npm ci` followed
  by `npm run validate:complete` on the final candidate. Preserve source-bound
  receipts, actual outcomes and resource/cleanup observations. No accepted full
  baseline exists to reuse.

Review the complete product and documentation diffs, retain the isolated fix and
exact source identity, and report the cause, verification and remaining launch
gates. Any newly exposed unrelated failure blocks qualification and is recorded;
it does not authorize unrelated feature work or weakening the complete gate.

#### FlowStack browser-suite runtime audit: brief

The owner requests an audit of expensive setup and overlapping browser tests,
with a practical runtime target and no weakened assertions. Define the brief
before analysis; this pass authorizes investigation and a proposal, not suite
implementation. Keep publication, installation, existing Runner ownership and
pilot launch unchanged.

Use the accepted FlowStack tree `dec81715fb61f44f786035cdd4780be660a9e105`,
actual Check `complete-58472347` and native browser run
`5ac15549-cd08-4da1-b430-1b1a3feb3665` as the measured baseline. Its 144.326
minutes include 1,105 browser passes/seven expected skips, without failures or
retries. Inspect the installed runner/configuration, owning suites, fixture and
editor lifecycle, existing lower-layer coverage and retained traces. Keep old
diagnostic source identities separate from the accepted candidate.

Answer these bounded questions:

1. Where does elapsed time go: lower checks, project/file/test execution,
   browser startup, application/fixture setup, repeated navigation/reload and
   the behavior actually under test? Distinguish measured cost from source-based
   hypotheses; a long action alone does not identify its internal cause.
2. Which tests repeat the same proof, and which similar-looking journeys protect
   distinct risks such as native clipboard, selection, geometry, persistence or
   device layout? Map consolidation candidates to the obligations and existing
   owners that would preserve their proof.
3. What runtime is practical for focused development and the final complete
   gate on a named environment? Quantify the reduction required, separate
   justified suite changes from additional capacity, and describe how a later
   implementation would demonstrate the target without relaxing correctness.

The output belongs in this plan beside the existing qualification. Retain a
small reproducible timing analysis in the existing private evidence directory,
with source/run identities and references for findings. Reuse the passing full
run and existing traces; do not rerun the complete gate for this audit. Any
narrow diagnostic execution must resolve a specific remaining question through
the repository's native runner and retain its own outcome and identity.

No assertion, interaction, write count, source comparison, timeout, skip,
project, retry, dependency, worker or sandbox setting changes in this pass.
Preserve the 15s default assertions, explicit 45s readiness waits, two E2E
workers, zero retries and four-GiB/two-CPU baseline. A proposed consolidation or
matrix change must identify each displaced proof and its retained owner;
timing alone does not justify removal. Runtime targets are proposals, not a new
accepted quality policy or evidence of achieved performance. Finish by checking
the analysis against native records, reviewing the documentation diff and
confirming the FlowStack tree is unchanged. No new application tests or repeated
implementation gates are needed for documentation and read-only analysis.

#### FlowStack browser-suite runtime audit: findings and proposal

Audit date: 2026-10-19. The FlowStack source remains tree `dec81715`; the
accepted run and its earlier failures retain their original identities. No new
browser execution, test edit or quality-policy change was made for this audit.
The reproducible private analysis is `audit-browser-runtime.py` →
`browser-runtime-audit.json` in the existing qualification directory
`/Users/dan/.codex/runner-qualification/flowstack-email-reliability-20261018`.
`audit-browser-setup-traces.py` reads retained diagnostic traces over SSH and
writes `browser-setup-trace-audit.json`; it launches no process in the product.

**Measured cost.** The complete gate took **144.326 minutes**, of which
**133.720 minutes** were native browser execution and **10.606 minutes** were
everything else, including locked installation, lower checks, browser lifecycle
outside the execution interval, and build. The 350 distinct cases expand to
1,112 selections: 206 email cases in four projects and 144 other cases in two.
Seven selections skip for the already-recorded reasons; 1,105 pass once.

| Project | Passes | Summed case minutes | Mean case seconds |
| --- | ---: | ---: | ---: |
| Desktop Chromium | 348 | 50.262 | 8.67 |
| Mobile Chromium | 347 | 48.954 | 8.46 |
| Desktop WebKit | 205 | 95.946 | 28.08 |
| Mobile WebKit | 205 | 65.975 | 19.31 |

Summed case time is elapsed test time across concurrent workers, **not** wall
time, CPU time or a measurement of setup alone. Its **261.136 minutes** fill
97.6% of the two-worker execution window as an occupancy proxy. Resource samples
separately show **1.95 CPUs on average** across 142.8 minutes against a two-CPU
quota. Peak recorded memory reaches the four-GiB boundary; there is no OOM or
OOM kill. The next investigation belongs inside the work being executed;
scheduler idle time cannot explain the two-hour cost. More workers inside the
same CPU/memory limit have no demonstrated speed benefit.

Email families account for **92.3%** of summed case time. The three costliest
files account for **46.3%**:

| Owning suite | Selected project cases | Summed case minutes |
| --- | ---: | ---: |
| `tests/e2e/email-editing.spec.ts` | 160 | 65.502 |
| `tests/e2e/tiptap-migrated/authoring/email-inline.spec.ts` | 28 | 30.731 |
| `tests/e2e/tiptap-migrated/transfers/email-inline.spec.ts` | 60 | 24.610 |
| All `tests/e2e/tiptap-candidate/` fixtures | 336 | 64.576 |
| Other application families | 288, including four skips | 20.214 |

The candidate row spans multiple files and is separate from the first three
rows. A file's cost is a prioritization signal, not permission to delete it.
The longest individual case is desktop WebKit shared authoring history,
**196.676s**; its four project executions consume **8.138 case minutes**.

**Expensive setup and work identified.**

- The managed Vite server, frozen export and lazy-editor transform warmup happen
  once per browser invocation (`scripts/browser-run.mjs:107–164` and
  `scripts/browser-server.mjs:21–36`). Warming the server does not preserve a
  mounted editor across fresh test pages. Playwright already shares its browser
  fixture between tests while isolating pages/contexts; adding a shared mutable
  page or moving setup into `beforeAll` would introduce order coupling. See
  [Playwright fixture ownership](https://playwright.dev/docs/test-fixtures#built-in-fixtures).
- The application fixtures repeatedly authenticate, install routes, load the
  application and mount GrapesJS/Tiptap. `openAuthoringJourney` at
  `tests/e2e/tiptap-migrated/authoring/email-inline.spec.ts:20` also adds eight
  content sections and a legacy image to all three of its callers. Those callers
  include guarded replacement/navigation as well as long-page history and
  preview. A later change can separate minimal setup from intentional large-page
  proof, retaining all asserted source/attribute/image invariants and the long
  fixture wherever scrolling or geometry is part of the obligation.
- The existing four-case WebKit trace diagnostic `ab31d32a` measures
  **10.493–21.984s** from first navigation to editor readiness, with **180–198
  requests** before readiness. That is earlier diagnostic tree `fe1483da`, with
  tracing and different whole-test allowances; it is evidence of repeated setup
  work, **not** an estimate of setup's percentage in the final untraced run.
  Git comparison confirms that only README and the three budget-bearing spec
  files differ from the final tree; the application, fixture helpers and browser
  lifecycle being observed are unchanged.
  The final receipt does not separate navigation, parsing, mount, fixture routing
  and interaction time. That attribution remains a measured-profiling task.
- There are **52 static wildcard route sites** across the selected specs. For
  example `creationFixture`, integrated `start` and migrated `start` intercept
  `**/*` and continue same-origin assets. Routing disables HTTP caching, and
  matching requests wait for their handlers, as documented by
  [Playwright page.route](https://playwright.dev/docs/api/class-page#page-route).
  This makes repeated dev-module loading and interception a concrete profiling
  candidate. Fresh test contexts have a cold cache regardless; routing's cache
  effect matters especially to later loads within a case. Narrowing a URL pattern
  alone does **not** restore caching while routing remains enabled. Preserve
  unexpected-request rejection, sanitized fixture isolation and credential checks
  in any later serving/routing change.
- The selected files contain **235 navigation, 64 reload and 246 screenshot
  call sites**. These are source sites, not runtime invocation counts. In the
  four retained traced cases, three to seven screenshots take **2.143–9.293s**
  per case in aggregate. Capture cost is worth measuring; the existing records
  do not support attributing most of the gate to screenshots or removing visual
  evidence. Many slow journeys also execute dozens of real clicks, keystrokes,
  layout reads and history transitions after setup. Setup improvements alone
  have no demonstrated sufficient saving for the proposed finish line.

**Overlap assessment and retained proof owners.** Paths in this table are
FlowStack paths: candidate/integrated/migrated suites live under `tests/e2e/`,
compatibility owners under `src/features/email/compatibility/`, and the stock
picker under `src/features/email/`. These are consolidation candidates, not
findings that the named tests can already be removed. Timing inventory covers all
1,112 selections; semantic review covers the representative areas below. This
is not an assertion-by-assertion deletion manifest. The implementation brief
must enumerate the assertions/behaviors it moves and verify the receiving
owner's adequacy first.

| Repeated area | Existing overlap and proposed ownership | Proof that must stay |
| --- | --- | --- |
| Whole-source editing | Candidate `source/email-inline.spec.ts:54` and integrated `email-inline.spec.ts:113` both check source/result, apply, undo/redo and save. Let `compatibility/emailSourceEditing.browser.test.ts` own transformation/validation details and one production integration journey own persistence wiring; retain distinct UI assertions at their current owner until an equivalent receiving case is verified. | Candidate CodeMirror virtualization, local editor undo, Escape, focus and exact canvas bounds are additional proof; integrated route/metadata and refreshed-record assertions are additional proof. The two cases are not interchangeable wholesale. |
| Literal fallback | `email-editor-adapter.spec.ts:581`, candidate `sidebar/email-inline.spec.ts:12` and application `email-editing.spec.ts:1031` repeat one-occurrence mutation/history/reopen, costing **6.076 case minutes** together. `emailTiptapFallback.test.ts` already owns exact expression/occurrence semantics. Keep a canonical production fallback UI/persistence journey; assess which adapter/fixture source assertions add a distinct risk. | Repeated formatted occurrence, trim/unescaped source, selection retention, cancellation and untouched neighbours must keep explicit owners. Unit transactions cannot replace native focus or rendered target proof. |
| Formatting and history | Candidate `toolbar/email-inline.spec.ts:13`, application `email-editing.spec.ts:1998` and migrated authoring `email-inline.spec.ts:75` repeat formatting/history while adding different context, costing **11.054 case minutes** together. `emailTiptapFormatting.browser.test.ts` already checks exact model/selection/format behavior in both lower-layer engines. Consolidate repeated combinations into that owner and keep concise toolbar wiring plus an integrated history journey. | Full history across inline edits, blocks and images remains distinct from the transfer journey's native structural moves, delayed image and complete-group ordering. Keep both cross-feature orderings; repeated `Undo` assertions alone do not establish duplication. |
| Stock-provider variants | Three provider application journeys repeat history/save/reopen/copy across four projects, costing **6.038 case minutes**. Existing `services/stockPhotos.browser.test.ts` and `compatibility/emailStockPhotos.browser.test.ts` already parameterize all three providers; `EmailStockPhotoPicker.browser.test.ts` owns chooser/recovery behavior. Propose short provider-specific UI wiring checks and canonical complete delivery journeys after reviewing the gaps. | All providers retain wire/metadata/credit/URL assertions. Keep the Unsplash lost-response/NotFound readback, exactly-one-POST, hotlink policy, authorization/header checks and independent-copy proof. The picker currently seeds PIXABAY, so it is not already a replacement for all provider choices. |
| Four-project repetition | All 84 candidate-fixture cases run at both viewport sizes in both engines, alongside production integration and lower browser suites. Propose explicit per-behavior engine/viewport ownership rather than the current filename-wide WebKit selection. Pure transformation combinations can be owned by existing lower suites; native and responsive behaviors retain browser coverage. | Candidate fixture layout is not the production mobile workspace. Keep production narrow-screen overlay/inert/focus/layout checks. Native keyboard, rich clipboard, drag, caret and geometry require relevant engine proof; no blanket deletion of the candidate directory or mobile/WebKit projects. |

Two apparent duplicates are specifically **not** removal candidates without new
evidence. The transfer suite's short-prefix variant (`transfers:198`) performs
five cut/Undo handoffs to catch the focus-recovery regression; the original
prefix performs one. Adapter native-group probes (`email-editor-adapter:73,113`)
observe intermediate native sorter/transaction states that final saved-source
checks do not. Likewise, migrated transfer `copy/paste` helpers use synthetic
events for many non-desktop-Chromium cases (`transfers:75–100`); those passes do
not replace the explicit native clipboard owner test or its separately qualified
WebKit execution. Preserve this distinction in the coverage map.

The adapter fallback case also exercises a different factory: `emailDocument.ts:810`
defaults to `createEmailInlineEditing`, while production `EmailEditView.vue:79`
injects `createEmailTiptapEditing`. Similar source/history assertions across
these paths are not proof of implementation equivalence. Establish each supported
adapter contract before retiring any owner; preserve frame isolation,
hostile-source rejection and external-resource/credential protections regardless
of where their coverage lives.

**Proposed practical runtime target.** Keep the named baseline environment:
pinned Linux image and its image-owned offline package cache, two CPUs, four GiB,
two E2E workers, zero retries. Treat these as user-visible elapsed-time goals,
including cold browser-lifecycle startup and a fresh locked installation:

| Work boundary | Proposed target |
| --- | --- |
| Ordinary focused development checks | At most **5 minutes** for the impacted lower-layer/browser proof |
| Broader editor proof across engines | At most **10 minutes**, with an explicit selection and preserved native/device obligations |
| Final complete gate, including fresh `npm ci` and all required layers | At most **30 minutes**: aim for browser work within 18 minutes and other checks within 12 |
| Intermediate reduction checkpoint | At most **60 minutes** complete; progress toward the finish line, not routine-gate acceptance |

These are proposed acceptance goals, **not a forecast or an achieved quality
policy**. With the measured non-browser cost unchanged, a 30-minute gate leaves
19.394 minutes for browser execution. Even ideal two-worker packing allows only
38.788 summed case minutes, **85.1% below** the current 261.136; startup and tail
overhead require additional margin. The 60-minute checkpoint requires at least
62.2% less summed case time by the same ideal calculation. Selecting more workers
alone is insufficient: even four workers with proportionally larger resources
would have a theoretical 65.284-minute browser floor for unchanged case cost,
before startup, tail and other checks. This is arithmetic, not a hardware
benchmark. Any later capacity proposal needs its own cost and containment review.

The present evidence cannot promise the 30-minute target while preserving every
proof. It identifies the necessary scale of improvement. Even eliminating all
candidate-fixture executions would remove at most 24.7% of measured case time
under unchanged case costs, and is not justified by the overlap review. The
current findings do not establish a removable duplicate percentage. If measured
setup improvements and justified consolidation cannot supply it, return the remaining
tradeoff to the owner; do not silently remove proof, use synthetic input as native
evidence, relax assertions, or relabel a partial run as complete. Hosted CI's
20-minute job remains a separate unqualified environment/limit.

**Next implementation boundary to propose:** first measure and reduce repeated
editor startup through the existing lifecycle and fixture boundaries. Use a
small matched selection on the accepted source before changing anything: a
short native candidate traversal, contextual properties, shared authoring
history and protected-profile reload, in their required projects. Attribute
initial load, editor readiness, later reload, interaction and capture separately;
retain cold and subsequent page measurements and tracing overhead explicitly.
Choose the smallest demonstrated fix, preserve fresh contexts and all existing
proof, and compare the same selection/settings after it. A helper extraction
alone is not performance evidence. Propose any fixture-serving/build changes
inside that existing lifecycle, preserving the frozen candidate and HTML fixture
routes; add no new runner, mock framework or second evidence system.

Use the overlap table for a later bounded proof-ownership change, with exact
before/after obligations approved before any matrix reduction. Reuse or extend
existing suites rather than generating another set of broad journey tests. Run
the required fresh complete gate once at the delivery boundary for the final
combined implementation; do not rerun it for every audit or intermediate edit.
This audit changes documentation and private analysis only and stops before any
implementation, publication, installation change or pilot.

#### FlowStack editor startup: implementation brief

Retained candidate: `codex/email-check-reliability`, tree
`2fc25d92c72d616950232031d8c934d4362edc67`, based on accepted reliability tree
`dec81715fb61f44f786035cdd4780be660a9e105`. The owner authorizes measurement,
a demonstrated startup fix and verification in this same worktree. Publishing,
installed Runner/configuration/ownership, deployment and pilot stay unchanged.
Test consolidation, browser-matrix reduction and a new test runtime are outside
this bundle.

Owner decision, 2026-10-20: keep CodeMirror for now for FlowStack's existing
source and JSON editors. This choice is provisional; reconsider Monaco if the
editing requirements change. The startup measurements below compare CodeMirror
before and after deferred loading, not CodeMirror against Monaco.

The retained candidate passed its fresh complete gate and is an accepted complete
baseline. The original aim of a large, uniform first-opening speedup was not
achieved; the retained outcome removes proved unnecessary CodeMirror loading and
provides a modest measured readiness improvement. The complete gate still took
141.647 minutes. The broader 30-minute target remains open.

Supported behavior and implementation:

- Each test retains its isolated page/context, real native editor, frozen source,
  fixtures and existing source-module APIs. Authenticated routing, frame/resource
  policy, selection, history, exact source and verified writes remain observable.
- Source and Feed dialogs share one standard Vue asynchronous component for
  `EmailSourceCode.vue`. Visual startup does not request that component. Loading
  and failure feedback leave the containing dialog cancellable without a write;
  ordinary visual editing remains available after cancellation.
- The source editor owns requested focus after Vue's DOM flush. First loading
  and cached reopening retain native focus. Its existing public editing/history
  methods and the HTML editing route remain available.
- Keep 15s assertions, explicit 45s readiness waits, all approved whole-test
  budgets, two E2E workers, zero retries, pinned image and 4GiB/2CPU unchanged.
  No server-mode, dependency or installation change is retained.

The representative matched selection is these four existing titles in all four
configured projects (**16 cases**, no expected skips):

- `right-arrow crosses the chip and end-of-line without a ghost caret`;
- `edits contextual properties and navigates parents in the contained desktop and mobile workspace`;
- `replays inline formatting, blocks and image changes through one shared history and verified save`;
- `inserts protected profile chips at retained selection, preserves source and recovers from schema failure`.

Every operation and assertion remains. Before/after runs use normal tracing
settings and the same private reporter observer, which writes existing native
steps only after each case finishes. No phase markers or observer enter the
retained product candidate or final complete gate. Phase-instrumented diagnostics
attribute work separately; do not substitute their timings for normal-mode proof.

| Project | Initial entry mean, before → after | Reload mean, before → after |
| --- | --- | --- |
| Desktop Chromium | 7.252s → 7.373s | 5.011s → 4.496s |
| Mobile Chromium | 6.116s → 6.047s | 5.000s → 4.772s |
| Desktop WebKit | 6.964s → 6.792s | 7.567s → 6.250s |
| Mobile WebKit | 5.729s → 5.380s | 4.818s → 4.143s |

Combined entry/reload readiness across 28 observations changed from 171.429s to
161.348s (**5.9% lower**). Each Chromium project's combined result was 2.4% lower;
each WebKit project's was 9.2% lower. Initial-entry results are mixed, including
one desktop shared-history observation of 9.319s versus 7.839s under concurrent
load. These are elapsed journey intervals with polling/contention, not CPU
attribution, statistical guarantees or a whole-gate saving prediction.

Before: Check `startup-baseline-1234270831`, native run
`3368514c-4672-4c67-b302-f749aacf4cc2`, all 16 passed. After: Check
`startup-matched-3071087500`, native run
`2af1fd01-2dd1-4305-8d26-e6dfe2b4c771`, all 16 passed. Product trees are `dec81715`
and `2fc25d92`; private observer trees are `1edc6486` and `e2fc96f8`. Full identities,
per-case records and comparison are retained in
`/Users/dan/.codex/runner-qualification/flowstack-email-reliability-20261018`.

Verification ownership and selection:

- Extend existing owners. Four source-format component cases now also assert
  requested native focus after Vue's DOM flush; the existing source undo/cancel
  journey now checks cached reopen. Add one browser case for the concrete
  deferred-load failure, zero startup requests, cancellation and no write.
- Native focused proof caught a real first-load focus regression: the original
  15s focus assertion failed in both desktop engines. Moving focus to the editor's
  mount boundary after the DOM flush repaired it. Six CodeMirror owner tests and
  four native focus/history/cached-reopen cases passed in run
  `4adcdfe9-9456-4878-8711-b1e0ec3ce7a4`. The final complete gate also passed the
  retained deferred-load failure, source-history and cached-reopen cases in all
  four projects. Failed iterations remain separate evidence.
- Related-source discovery selected unrelated router suites. The 127 editor-route
  and six feed-dialog owner tests passed before a controlled stop; that whole
  Check is **unavailable/143**, never accepted passing evidence. Subsequent
  iteration uses explicit owning files and names. Do not repeat applicable
  passing checks or use static related selection as an automatic impact decision.
- After reviewing the complete diff, fresh locked `npm ci` plus
  `npm run validate:complete` passed once on the final uninstrumented source.
  Development used focused owning checks; neither a `validate:fast` prefix nor
  the old 52-case budget qualification was repeated. The new logical browser
  case adds four project executions: **1109 passes / seven unchanged expected
  skips / 1116 selected**, with zero failures, flaky outcomes or retried attempts.

Final Check `complete-1415226509`, native run
`5d95d9c1-63c2-4f8d-9186-9c4eb493c10a`, passed lint, architecture, types,
**4084 unit/component tests, 154 tooling tests**, the browser selection and build.
The browser fingerprint is
`6ab6801da3a2a983e18cf3dd74fbe0fcfdbaba84252b2951a903e7bd99dc0486`.
All 15 receipt/audit conditions passed, including the exact prior skip set,
source integrity and ownership release. Browser execution took 130.991 minutes;
the actual Check took 141.647 minutes versus the earlier 144.326-minute baseline.
These single full-gate observations do not establish an attributable runtime gain.

The independent source audit matched all **904 source files** to the retained
archive and recomputed the native fingerprint. No private observer or phase
instrumentation is shipped. All **10448 installed dependency files** matched the
prior accepted baseline, and the pinned image remained unchanged. Source digests
before and after Check matched. The 2026-10-20 02:53 CEST read-only audit confirmed
the installed Runner binary, configuration, service definition, PID `66004` and
deployed container/image were unchanged; none of 72 recorded owned Check
containers remained. No publication or pilot occurred.

`qualification-inputs.json`, `editor-startup-complete-outcome.json`, source and
dependency audits, the installation audit and matched comparison retain the
identities and results in the private qualification directory above. The final
`qualification-records.tar.gz` contains **1044 records**, SHA-256
`acb9e4fc93a6e8718ae349c3fdfbfcb84e7816612791d42258fe286e4ee0addb`;
the previous 733-record archive is preserved separately. Hosted CI's 20-minute
envelope and opt-in real-backend behavior remain unqualified.

Alternative measurements are retained, not shipped. Dependency minification
passed 16 cases but changed combined readiness only 1.5%. Exact-origin routing
passed two cases without a consistent module-loading gain. Vite 8.2.2 bundled-dev
phase diagnostics improved startup substantially, but the first compatibility
selection failed all four direct-source-module imports. Explicit-entry experiments
then exposed compiler-option/readiness/runtime-asset assumptions in the existing
frozen-serving contract. None qualified. Keep the supported Vue change; a bundled
runtime needs a separate brief preserving all current assertions and module
identity. No experimental server or source-module facade enters this worktree.

Finish with candidate-bound complete receipts, before/after measurements, source
integrity, owned-process cleanup and a read-only installation/deployment audit.
Stop before publication, installation changes, pilot or coverage consolidation.

#### Configurable check CPU budget brief — approved 2026-10-21

**Owner authorized this extension, including optional 25% CPU detection.**
FlowStack editor/transfer diagnostics found that the fixed Check CPU budget is a
material constraint. Identical instrumented source/image/native fingerprint,
six cases, two workers, 15s assertions, zero retries and 4GiB memory passed at
both 2 and 4 CPUs. Browser time changed 4.028 →3.268 min (18.87%); Check elapsed
4.915 →4.189 min (14.77%). This is one shared-host sample, not a prediction of
full-gate time. The installed Runner and deployment are unchanged. The first
capacity attempt never left 2 CPUs and was stopped exit 143; its partial receipt and
cleanup are retained, not accepted proof.

The FlowStack-only patch preserves all original native navigation keys/loops and
25 assertion sites in the mixed-transfer variants, adding three focus assertions
and direct native keyboard dispatch for repeated sweeps. The Frame-resolution
experiment was removed after failing to show a useful gain. Product code,
coverage selection and all assertion/journey budgets remain unchanged. This small patch alone
does not substantiate a large full-suite improvement.

The approved additional Runner bundle adds one optional integer `cpus` field to
an owner-accepted container quality check. Omitted means 2, preserving the current
default; explicitly accepted values 1–4 bound initial supported allocation.
FlowStack's private qualification agreement may request 4. Keep memory 4GiB,
worker/retry/assertion policy, image pinning, network/filesystem isolation and
process ownership unchanged. Do not modify an installed binary/config/service,
Docker host configuration or other repositories' agreements. This is resource
configuration, not another scheduler.

A check may instead request `auto_cpus: true`: detect the pinned local Docker
daemon's available logical CPUs, take 25%, round up and clamp to 1–4. This is
optional; omission still means 2. Reject combining `cpus` and `auto_cpus`, and
reject either setting without a container image. Resolve automatic allocation
during quality preview into an explicit `cpus` value before baseline/approval;
checks never recalculate an approved budget. A changed detected allocation makes
the baseline preview stale. Detection failure blocks preview, without a guessed
fallback. Test 1/4/5/8/16/32-core rounding and caps, unavailable/invalid daemon
observations, agreement identity and host-check refusal. The qualification daemon
has 4 CPUs, so automatic mode selects 1; FlowStack explicitly requests 4 for the
final gate. Record this changed resource boundary when comparing timings.

The CPU budget must be validated at intake/execution and included in quality
revision/evidence, so changing it invalidates old check approval/results normally.
Extend the existing configuration/agreement and container-argument tests for
omitted 2, explicit 4, invalid budgets and changed quality identity. Keep default
and other sandbox arguments unchanged; document the field and retain actual
container inspection. Run the required Runner race/vet gates and relevant real
container qualification, then run the FlowStack combined candidate's single
fresh-install complete gate at its explicitly accepted budget. Keep native
selection/assertions and all failures. Publishing, installation changes and
pilot remain outside the bundle.

The 2026-10-21 owner direction authorizes implementation in this worktree and
one final combined FlowStack gate. It does not authorize publication, installation
changes or a pilot.

##### CPU configuration qualification — 2026-10-21

Implemented in the existing Runner packages, with no new dependency or scheduler.
The existing configuration, agreement and execution suites cover invalid/host
settings, default allocation, rounding/caps, unavailable detection, stale preview
refusal and durable approval. Required `go test -race -timeout 15m ./...` and
`go vet ./...` passed. Four real container cases verified default 2, explicit 1
and 4, and automatic 1 on the four-core daemon; fixed/default cases also verify
4GiB memory and 512 processes. Every case released container/client ownership.
The qualified Runner source tree is `9bc6f28d6c3f6dbdcef0208f2d31c5ad89c969f7`;
subsequent Runner changes are documentation only.

FlowStack's single fresh-install complete gate passed on uninstrumented tree
`973c9ef16c34ea0d146c75384bd042b9efeb9503`: **4168 lower, 154 tooling,
619 browser passes /five exact skips**, static checks and production build.
Check `complete-1289597120`, native run
`c8f21269-343f-44eb-8a0c-ada723bc08b5`, fingerprint
`885d1a522eaeff409abd51d98e22b476068189ebdd07b5d6969de866910510bd`.
All 21 source/dependency/selection/settings/receipt/cleanup conditions passed.
Actual Check time was **49.218 min**, browser **41.843 min**, compared with
70.738 /60.373 min at 2 CPUs. This run explicitly used the new `cpus: 4`
field from launch, with no resource mutation. Memory stayed 4GiB, two workers,
15s assertions, all named/default journey budgets and zero retries. The different
quota and shared-host observation prevent attributing the gain solely to the
small keyboard-driver patch. The 60-minute checkpoint is met; 30 minutes is not.

Private evidence is retained under
`~/.codex/runner-qualification/flowstack-email-reliability-20261018/`, including
`editor-transfer-20261020/runner-cpu-inputs.json`, `runner-cpu-verification.json`,
`complete-outcome.json`, assertion/source/dependency audits and the qualification
index/archive. The read-only installation audit confirms unchanged binary/config/
service hashes, PID 66004 and deployment; all 102 recorded owned Check containers
are absent. Earlier diagnostic failures and resource experiments remain historical,
not relabelled as accepted proof. Hosted CI and opt-in backend gaps remain explicit.
No publishing, installation change or pilot occurred. Stop at this boundary.

#### FlowStack fallback consolidation qualification — 2026-10-20

The existing FlowStack audit defines the brief and before/after ownership map.
Candidate cancellation and formatted-occurrence/history/save now share one
session. All 17 original assertion and 18 native action sites remain. Five other
sidebar cases, helper, product/fixture/lower/configuration code and all budgets
are unchanged; distinct production, mobile, default-adapter, stale-form and
resting-button proof remain separate. Two duplicate cold entries are retired:
354 logical cases /626 executions become 353 /624.

The first focused D/W Check failed because the retained source assertion settles
inline editing; the combined positive phase then waited for an absent editor.
Retain that failure, explicitly re-enter through the existing greeting button
and add a focus assertion. Lint and both repaired native executions passed
without timeout changes. This illustrates why combining registrations needs an
explicit phase-entry check as well as an assertion-preservation audit.

Fresh install and complete validation passed once on uninstrumented tree
`872da102c0af97d2814fa8c0730a505a45848193`: **4168 lower, 154 tooling,
619 browser passes /five exact skips**, static checks and build. Check
`complete-4119455521`, native run `0ce21924-f6f9-43de-bd6b-fa1e1d224f56`,
has fingerprint `0286f8ca2611273210b0d18216d8a23b5a14bb1104572b0327243e0bbe13a129`.
All 21 acceptance conditions pass; 915 source files and 10448 dependency entries
match frozen inputs, and ownership is released. The pair changed 54.009 → 41.514
summed seconds; complete 71.231 → 70.738 min and browser 60.545 → 60.373 min.
There is no material overall runtime improvement; both runtime goals remain open.
Other fallback coverage has distinct proof; formatting review remains separate.

Private `fallback-20261020/` evidence and the 1372-record archive retain failures,
receipts and audits; the 1321-record predecessor is preserved. At 16:35 CEST the
installed Runner/config/service, PID 66004 and deployment were unchanged, with
all 93 owned Check containers absent. This Runner change is documentation only.
No publishing, installation change or pilot occurred. Real-backend persistence
and the hosted 20-minute CI envelope remain unqualified.

#### FlowStack whole-source consolidation qualification — 2026-10-20

The existing FlowStack audit defines the brief and exact assertion/owner map.
Local undo/Escape/cached-focus and apply/result/history/save now share one bounded
candidate source journey. All 24 original assertions and seven keyboard-operation
sites remain, with one added focus assertion. The positive phase retains its
active-inline entry. Product/fixture/helper code, four neighbouring cases,
production metadata/preview wiring, both HTML separator branches, lower coverage
and every budget/settings bound are unchanged. Two duplicate cold entries are
retired: 355 logical cases / 628 executions become 354 / 626; no native obligation
is dropped. Edited-spec lint and focused Chromium/GTK WebKit proof passed first.

Fresh install and complete validation passed once on uninstrumented tree
`3c3a44f3f43c3d27a397cb91186dc3e08c1902bf`: **4168 lower tests, 154 tooling
tests, 621 browser passes / five exact skips**, static checks and build.
Check `complete-824810471`, native run `3c4e1afa-0d41-4bc2-a89c-8fa0cac36a50`,
has fingerprint `223d74e2e2f2dff67fe3d6538f83e1ea31a9933fbf72bb0f9ec5a8f5de3f5262`.
All 21 acceptance conditions pass; 915 source files and 10448 dependency entries
match their frozen inputs; ownership is released. No Check failed in this bundle.

The affected pair changed 60.339 → 47.588 summed case-seconds. Complete time was
70.944 → 71.231 min and browser time 60.535 → 60.545 min: **no material overall
runtime improvement**. Both complete-runtime goals remain open; shared-host
observations do not justify a broader speedup claim. Further fallback/formatting
ownership review remains separate; no additional whole-source consolidation is
justified by this inspected overlap without changing distinct proof.

Private `whole-source-20261020/` evidence and the 1321-record thin archive retain
the audits/receipts; the 1286-record predecessor is preserved. At 14:54 CEST the
installed Runner/config/service, PID 66004 and deployment were unchanged, with
all 90 owned Check containers absent. This Runner change is documentation only.
No publishing, installation change or pilot occurred. Real-backend persistence
and the hosted 20-minute CI envelope remain unqualified.

#### FlowStack repeated MJML compilation qualification — 2026-10-20

The owner authorized a measured, bounded runtime fix in the same isolated
FlowStack worktree. The existing verification audit defines behavior, expected
results and proof before implementation. The document command now keeps one
successful compilation per editor, keyed by exact current serialized compiler
input, and returns copied results. Serialization still runs on every read;
changed input still receives full inspection, compilation and preservation.
Metadata remains current, invalid input fails closed, and unsuccessful work
cannot replace a successful entry or return it for a different source. No
revision framework, cross-document cache, renderer or dependency change was added.

The same three privately instrumented property/history journeys made 852 reads
before and after. **684 repeated compilations were avoided (990 → 306 compiler
calls)**; observed document-read time fell **12.125 → 3.429 seconds (71.72%)**.
Both snapshots around reload are retained. The earlier incomplete diagnostic
and a test-wrapper CommonJS interop failure remain recorded; neither is accepted
production proof. Two new real-compiler cases in the owning suite prove stable
reads, caller isolation, edit/undo and failure recovery. Focused affected evidence
has 568 lower passes, plus seven native passes and one exact non-owning clipboard
skip. The GTK OS-clipboard case passed with WebKit explicitly owning it.

Fresh install and complete validation passed once on uninstrumented tree
`4e0e0f6b7747dc0971cf0582444a0e1f59c31b50`: lint, architecture, types,
**4168 unit/component tests, 154 tooling tests, 623 browser passes / five exact
skips**, and build. Check `complete-3275093910`, native run
`2bc0230d-b0f2-48dd-b21c-1df85332bcb4`, has fingerprint
`007ed466f4d0853b9337af07b9ce307ae024efc269ffb9dc86549b9ad90c485e`.
All 21 acceptance conditions pass; 915 source files and 10448 dependency entries
match the frozen source and previously qualified installed inputs. Native
selection, assertions, budgets, workers/retries and sandbox remain unchanged.

Complete elapsed time changed **73.457 → 70.944 min (3.42%)** and browser
execution **62.622 → 60.535 min**. The read-path gain is real measured work
avoidance, but the observed whole-suite reduction is modest and shared-host
timing does not establish controlled causal attribution. The 60-minute complete
checkpoint and 30-minute goal remain open. Passing native cases still consume
118.976 summed minutes, an ideal two-worker browser bound of 59.488 minutes
before the other gate phases. Continue the existing source/fallback/
formatting proof-ownership review from measured remaining costs; do not weaken
native assertions or add another cache speculatively. Focused development checks
precede one combined complete delivery boundary.

FlowStack's existing verification audit owns the detailed brief and evidence.
Private `mjml-compilation-20261020/` records and the 1286-record thin archive
retain measurements, failures and receipts; the preceding 1184-record archive
is preserved. The 13:28 CEST audit
confirms unchanged installed Runner/config/service, PID 66004 and deployment,
with all 88 owned Check containers absent. This Runner change is documentation
only. No publishing, installation change or pilot occurred. Hosted 20-minute CI
and real-backend persistence remain unqualified.

#### FlowStack repeated editor work qualification — 2026-10-20

The next authorized bundle consolidated provider-copy proof and fixture setup in
the same isolated worktree. Six repeated copy-editor entries are removed; one
native UNSPLASH journey owns the common copy UI, while every provider retains
cold rendering, wire/credit policy, native history, save and readback. The existing
mounted picker case now covers all three providers; the both-engine document
suite retains their independent-copy mutation and original immutability. Eight
unused scrolling sections are omitted only from the guarded logout fixture.
The two long authoring owners keep their complete scrolling document. No runtime
product code, configuration, dependencies, budgets or matrix assignments changed.

A proposed complete-focus-cycle shortcut failed its new 35-step return assumption
in desktop/mobile Chromium and was removed; the original focus assertions and
sweeps remain verbatim. Its stopped partial Check, and the separate diagnostic
WebKit tracing timeout, remain in the evidence. Neither is accepted delivery proof.
Final focused native verification passed 13/13; owning lower tests passed 28.
Fresh install and complete validation passed once on tree
`3f7bd02a05732fdc97fbb971c083dc9e5c4990a5`: lint, architecture, types,
**4164 unit/component tests, 154 tooling tests, 623 browser passes / five exact
skips**, and build. Check `complete-3898945858`, native run
`6f8be9f3-2047-47de-a94d-772c199fc2d3`, has fingerprint
`7a91292de011ad4a78943965b5e4db922472dbeb572f52823c09d4672f575c31`.
All 21 audit conditions pass; 915 source files and 10448 installed dependency
entries match their frozen inputs, source integrity and ownership release pass.
The unchanged keyboard journey also passed normally without tracing.

Complete elapsed time changed **74.037 → 73.457 min (0.78%)**; browser execution
**62.762 → 62.622 min**. This is **not a material overall runtime improvement**.
The matched changed journeys consume 278.981 → 242.062 summed seconds, a small
part of 123.067 summed case-minutes. Shared-host observations are not controlled
causal attribution. Both the 60-minute checkpoint and 30-minute goal remain open;
the ideal two-worker browser bound alone is still 61.534 min.

Next, profile the full MJML compilation command used by history snapshots and
dirty-state semantic comparisons before designing a change. The call path is
confirmed; its timing impact is unmeasured. Preserve diagnostics, fail-closed
state, normalized source, readback and history ownership. Continue the documented
whole-source/fallback/formatting ownership review without dropping distinct native
proof or adding a speculative cache. Use focused checks during development;
one complete gate owns the combined delivery boundary.

FlowStack's existing verification audit owns the detailed brief and assertion
map. The private `editor-work-20261020/` evidence and 1184-record thin archive
preserve all experiments and final receipts; the preceding 1117-record archive
is retained. The 11:21 CEST audit confirmed the installed Runner/config/service,
PID 66004 and deployment unchanged, with all 81 owned Check containers absent.
No publishing, installation change or pilot occurred. The hosted 20-minute CI
envelope and real-backend persistence remain unqualified.

#### FlowStack behavior-based browser qualification — 2026-10-20

The owner resumed FlowStack development here after stopping the frontend chat,
then approved behavior-based coverage with an exact case map before removing
executions. The FlowStack source of work is
`docs/verification-audit/README.md` and its `browser-matrix.md` review. This
supersedes the earlier boundary stopping before coverage consolidation; publishing,
the installed Runner/deployment and pilot remain unchanged.

The isolated FlowStack branch adopted merged `develop` commit
`c75ef8973b9a1ad1c23d6939213e623a513a62b6`, preserving the previously qualified
reliability/startup work. Three native Playwright project filters and case tags
replace blanket repetition. Collection and actual execution match the reviewed
map: **1124 → 628 executions**, all **355 logical cases / 307 registrations**
retained. Source comparison verifies unchanged bodies, helpers, assertions,
budgets and skip predicates. Desktop Chromium owns the logical cases; explicit
WebKit/native and mobile/layout owners retain the reviewed boundary proof.
Omitted combinations are not called proved. No new test framework or tests were
needed. Focused native commands honor the same owners; complete validation stays
at final delivery.

Fresh locked install plus complete validation passed once on tree
`9f3cbbda5070eac19d1ac76fd253ecfd256704e9`: lint, architecture, types,
**4162 unit/component tests, 154 tooling tests, 623 browser passes / five exact
skips**, and build; no failures, flaky outcomes or retries. Native Check
`complete-1728191761`, run `bb9a6077-9211-4c52-8302-34c4726da11f`, has fingerprint
`a0343015e96308aeaea2e5233fd03a4245013cc0699dc760c25ba51ca62593e8`.
All 21 audit conditions pass, including exact selection/skips, all 915 source
files matching the archive, all 10448 dependency entries matching prior proof,
source integrity and ownership release. The explicit GTK WebKit OS-clipboard
owner also passed separately. Assertions remain 15s, readiness/journey budgets
unchanged, workers 2, retries 0 and sandbox 4GiB/two CPUs.

Observed complete time fell **141.647 → 74.037 minutes (47.73%)**; browser time
fell **130.991 → 62.762 minutes (52.09%)**. Different bases/host load limit causal
attribution. This first cut **misses both the 60-minute checkpoint and 30-minute
goal**. Summed passing case time is 123.530 min, whose ideal two-worker bound is
61.765 min; scheduling slack offers little headroom. Desktop Chromium alone costs
50.097 case-min, with 11.275 min outside browser execution. Further reduction needs
shorter editor setup and consolidated owning-layer proof. Application email
editing (27.333 case-min), migrated transfers (12.475) and recovery/layout (9.535)
are the largest remaining families. Preserve native geometry/clipboard and the
distinct cross-feature history orderings during that work.

Detailed outcomes, preliminary invocation errors, skip ownership and limits are
recorded in FlowStack's runtime brief and the private qualification directory
`/Users/dan/.codex/runner-qualification/flowstack-email-reliability-20261018/`.
`qualification-records.tar.gz` retains 1117 records, SHA-256
`189ee1760a647f485f8ade9b9336abb9128310b338edd2be5145b8d149998407`,
with the previous 1044-record archive preserved. The 09:33 CEST audit confirmed the
installed binary/configuration/service, PID 66004 and deployment/image unchanged;
all 77 recorded owned Check containers were absent. Hosted CI's 20-minute envelope
and real-backend persistence remain unqualified. No publication or pilot occurred.

#### FlowStack email-check reliability: implementation and qualification

Accepted reliability baseline before editor-startup work:
`codex/email-check-reliability`, tree `dec81715fb61f44f786035cdd4780be660a9e105`. The owner approved the Linux GTK
WebKit 60s default and, on 2026-10-19, the consolidated nine named whole-journey
budgets below. Both are applied. Every action, proof step, 15s assertion and
explicit 45s readiness wait is preserved. E2E retains two workers and zero retries
inside the unchanged four-GiB/two-CPU boundary. Chromium/macOS defaults remain
30s. The budget patch introduces no tests or timeout helper; README documents
the accepted platform policy.

The final immutable export SHA-256 is
`8d951f7d489280c903807193e726e65d6dfc8a5370fe020b5a9d5c8d4fa61273`.
Its 52-case focused native verification passes **49 cases/three expected
clipboard-ownership skips**, zero failures/flaky outcomes/retries. The fresh
locked-install and complete gate also passes on that immutable source: lint,
architecture, types, **4,084 unit/component tests, 154 tooling tests, 1,105
browser passes/seven expected skips, and production build**. Actual Check
`complete-58472347` passes/0 in **144.326 minutes**, inside the private
240m / 245m settle / 250m driver envelope. Native browser run
`5ac15549-cd08-4da1-b430-1b1a3feb3665` and its receipt/execution/run agree;
every attempt is initial with retry zero. The exact skip-set audit, source
integrity and owned-process cleanup pass. This is the accepted complete baseline
for tree `dec81715`, not proof for a different source or environment.

The earlier 1b10f251 WebKit matrix reaches its
120m limit with 381 passes/eleven whole-test timeouts/one 15s reload-assertion
failure/nineteen unfinished. The exact tail separately passes eighteen cases/one
expected clipboard skip, then four focused trace cases and 24 normal diagnostic
cases pass on a separate observation export. All retain their original source
identities. Every protected-profile 15s reload assertion passes in all four
projects in both final focused and complete verification. The earlier failure is
retained; the larger whole-test budget does not relax that assertion or establish
reliability under every load.

Earlier forty-case focused proof, interrupted complete execution (772
passes/thirteen new timeouts), the unchanged
thirteen-case diagnostic (eight passes/five timeouts), and the interrupted
intermediate WebKit matrix (66 passes/one additional thirty-second timeout) are
retained under their original candidate identities. Integrity and owned-process
cleanup pass for both controlled stops. The accepted platform policy below
supersedes the intermediate per-journey additions. The final 19:51 CEST read-only
audit confirms unchanged installed Runner binary/configuration/plist hashes,
service PID and FlowStack deployment, with all 54 recorded owned containers
absent. No pilot, publication or installation change has occurred. Opt-in real
backend persistence and hosted CI remain outside this qualification.

The isolated FlowStack branch is `codex/email-check-reliability` at
`/Users/dan/.codex/worktrees/flowstack-email-reliability/webapp`, based on frozen
`3fe7a48`. The first startup candidate has tree
`ec885deeea0793f19144eeb36ce2cb047f11a948`; only the managed browser server,
its existing frozen-serving test and the README change. No application
persistence, fixture assertions, dependency, project,
worker, timeout or retry setting changes.

The original six-case selection reproduced **three failures/three passes** with
tracing, run `ae32ab26-5382-486a-9495-a668fb617dea`. When Save was reached, the trace
records one successful creation and successful record readbacks; desktop Chrome
and desktop WebKit then exhaust the overall journey deadline around reload.
Mobile Chrome times out before creation; mobile WebKit passes. There is no
demonstrated missing fixture write. These outcomes remain failed/incomplete.
Private timing instrumentation also shows serialization and semantic comparison
usually taking a few milliseconds while module loading and editor startup take
seconds; its untraced concurrent six-case run passes in **52.1 seconds**. That
diagnostic source is retained separately and is not part of the fix.

The managed server now transforms the lazy email editor and waits for static
imports to settle before reporting readiness. It prepares frozen code without
mounting the editor or seeding application state. The existing lifecycle test
also verifies a dependent module is transformed before readiness, retaining its
original source-isolation assertions. No new suite, framework or product cache
is added. Startup remains subject to the existing 120-second lifecycle bound.

On the candidate, **154 verification-tool tests pass**. Three fresh executions
of the original concurrent browser selection pass **18/18** cases, zero failures,
skips, flaky outcomes or retries: **46.943 / 46.251 / 46.121 seconds**. The slowest
email case is **19.694 seconds**, below the unchanged 30-second test deadline.
All three receipts bind the same managed candidate fingerprint
`2fc6747ac443afb9ce19f44ef7b563b94a864a600517e8229a36c48b61a136e3`.
Source/index/control verification passes and the owned check container is absent.

Qualification image
`sha256:c066b11c908d6ea36ee5cd6b49b0069171c98a951a7868ddb3392bec8cafa323`
inherits the previously qualified image and changes only the private check wrapper
to run the tooling suite and bounded repetitions. Dependencies, browsers and
containment are unchanged. Diagnosis/focused runs use the prior private Runner
caller; complete validation uses the same caller built against Runner `b0bdb01`.
The sampled reliability check peaks at **3,464,237,056 bytes**, with zero observed
OOM events/kills. No source/index check or browser claim is bypassed.

**First complete gate is interrupted after failures:** fresh locked offline
`npm ci`, then the unchanged `npm run validate:complete`, through the actual
Runner container check boundary. The prefix passes lint, architecture, types,
4,084 unit/component tests (208 suites, 348.53 seconds) and 154 tooling tests.
The browser selection contains 1,108 cases and binds the same `2fc6747a…` managed
candidate as focused proof. At the deliberate stop, its partial report records
222 passed, ten failed and one skipped; the remaining cases are not completed.
The owned browser lifecycle receives SIGTERM, Runner records unavailable/143,
source/index/control verification passes and the container is absent. This
25m13.14s driver run is **not a complete baseline**; build did not run.

**Complete-gate finding and bounded brief extension:** the first complete run
passes lint, architecture, types, 4,084 unit/component tests and 154 tooling tests,
then exposes Tiptap fixture assertions placing inserted source before the final
period rather than after it. Stop that known-failing browser run through its
owned lifecycle; retain it as failed/interrupted, never as a complete baseline.
The affected fixtures use `ControlOrMeta+ArrowRight/Left` as line navigation and
`Alt+Shift+ArrowLeft` for word selection. Those gestures have different native
meanings on macOS and Linux. Confirm one failure alone under the unchanged Linux
boundary, then use the equivalent native line/word gestures in the existing
owning specs if confirmed. Keep all expected source, caret, clipboard, history
and reopen assertions; do not exclude comparison specs, synthesize DOM selections
or increase timeouts. Qualify the affected specs concurrently in both engines
and layouts before retrying the complete gate. No editor feature change or new
test framework is authorized by this finding.

The unchanged `typed each creates an alias-bound pair…` case also fails alone in
desktop Chrome, run `959ab6e5-c8a5-4ce8-abfe-ae1a92d83cae`: the inserted source
precedes the period rather than following it. This is a reproducible native-key
expectation mismatch, not another load timeout. The isolated correction adds
three native line/word key constants in `tests/e2e/keyboard.ts` and updates five
owning specs. A reconstruction check confirms every original assertion and other
behavior step is byte-for-byte unchanged. macOS keeps its original gestures;
Linux uses Home/End and Control+Shift+ArrowLeft. No synthetic selection is added.
This candidate has tree `48cb2a9c14dd1c0508bcb3679b6998eec8ab060b`; its 184-case
affected browser selection passes all 92 Chrome cases, then exposes WebKit native
clipboard failures. It is stopped with 110 passes/five failures; remaining cases
are incomplete. Source/index verification and container cleanup pass again.

**Environment finding and diagnostic boundary:** a single unchanged WebKit
clipboard case still fails alone. Private instrumentation records the trusted
copy event setting plain text, HTML and the custom binding, while paste cannot
retrieve the binding or HTML. A minimal native-event control also loses the
custom binding in the top document and one/two sandboxed frames; plain text
survives. This is not evidence of missing email writes. No application clipboard
fallback or source-scope relaxation is warranted. Before changing supported
configuration, qualify the same pinned WebKit's GTK embedder with a private
virtual display inside the unchanged container limits. The image has Xvfb but
lacks xauth; its first GTK attempt stops in setup, running no browser test. Add
that runtime prerequisite only to a disposable image, retain the failed attempt,
and test the native rich-clipboard roundtrip. Any adopted environment correction
must preserve real keyboard/clipboard proof, isolation between concurrent
workers, engine/version, assertions and deadlines, with owned display cleanup.

The GTK diagnostic passes the original clipboard/undo case in 13.8 seconds,
run `320c57e9-d9cf-4f5f-adf8-5d3ba12d561f`. Copy and paste carry the same custom
binding and HTML; the existing current-draft lookup succeeds. No clipboard data
is fabricated. Its disposable image adds only Debian xauth `1:1.1.2-1`, image
`sha256:be5894f02e242387cc9d1eaf739763aff48d517bec4eeddceb70c76bea0ecf99`.

**Supported Linux check correction, before implementation:** configure only
Linux WebKit end-to-end projects to use the installed GTK embedder with a virtual
display. A small source-owned launcher delegates display allocation and cleanup
to `xvfb-run`, one display per browser launch, so concurrent workers do not share
the system clipboard. Resolve the actual WebKit executable from the locked
Playwright installation in frozen configuration. Preserve macOS configuration,
Chrome, versions, workers, assertions and deadlines. Document Xvfb/xauth and add
the missing CI prerequisite without running or publishing CI. Verify real
concurrent clipboard isolation, affected native journeys and cancellation/owned
cleanup, then run a fresh complete gate on the final source. Keep application
clipboard parsing and source-scope authority unchanged; no mock clipboard,
display service or additional scheduling system is introduced.

The first two-browser GTK isolation probe establishes independent native bindings
on displays `:99` and `:100`, then removes the owned container on cancellation.
It preserves the original source, but its **disposable copy fails verification**:
the private shell wrapper was terminated before relocating `node_modules` into
reserved output. Its unavailable receipt is not qualified cancellation proof.
Correct only that private image wrapper: use an explicit `exec` command, handle
termination while waiting for a child, preserve failure status and relocate
generated output before exit. Require copy verification in the cancellation
caller, then repeat isolation/cancellation on the corrected image. No Runner
source guard or product assertion is relaxed.

**Owner-approved decision — one journey budget (2026-10-18):** GTK preserves the clipboard,
but the first concurrent GTK matrix times out in `typed each creates an
alias-bound pair, renames from endeach, and shares undo/save/reopen`. Its retained
state is `ready · verified`: save/readback already passed, and the 30-second
whole-test deadline expires at the following Undo click. This case also performs
creation, alias rename, two undo/two redo operations, another save and reopen.
The approved correction is **60 seconds for this one test body across its four
projects**, preserving 15-second assertions, two workers, zero retries and every
behavior/source assertion. Apply this explicit owner decision, verify that journey
across all four projects with two workers, then run a fresh complete gate on the
final source. Earlier failures remain retained; no complete pass or pilot
readiness is inferred from this budget change.

The GTK correction is implemented with candidate tree
`b40e17c596ab806a22da5165b71d94d86f0e4b59`: Linux WebKit configuration and a
seven-line executable launcher, declared Xvfb/xauth prerequisites in README/CI,
and the previously described startup/native-key changes. Application source,
all source/behavior assertions, workers, retries and deadlines remain unchanged.
The native GTK checkpoint confirms separate displays `:99`/`:100`, WebKit 26.5,
independent trusted copy/paste of plain text, HTML and current custom bindings.
After correcting the private wrapper's signal cleanup, the cancellation control
passes in 105.07s: unavailable/stopped, intact original **and disposable copy**,
and absent container. It uses final prepared image
`sha256:3b8e8f45c97f18789cbd5d148f6dedb2aebcf39180627b0f41bb1495526ae483`.

On this product candidate, the original six-case selection passes in 59.652s
while the larger matrix is active. The 184-case affected matrix finishes in
1,134.635s with **183 passes/one timeout**, zero skips/flaky outcomes/retries.
The failure is the long journey above; it is not waived by its unchanged focused
rerun (21.583s test body, 24.911s browser receipt, run
`f9d33940-01a5-4067-977d-7928ebf39243`). Both selections and that focused rerun
bind candidate fingerprint
`180a2288d0c7a5d07faee258e6f740f7503cb013567c2677617daf10bacf1d94`.
Their source/index/control checks pass and their containers are absent. Fresh
locked installation, lint, architecture and types pass on this exact product
tree through the final image; the scoped static check takes 139.01s and passes
both source inventories. It is not the complete gate. These observations precede
the owner's journey-budget approval. The earlier failed/interrupted complete gate
cannot supply an accepted baseline for the final candidate.

Complete diff review confirms the native-key changes preserve every existing
assertion and other behavior step. The only additional regression assertion
extends the owning frozen-serving test to prove editor preparation precedes
readiness. No application persistence or clipboard code, dependency, assertion
deadline, worker limit or retry policy changes. The future CI prerequisite is
unpublished; hosted CI and its existing 20-minute job budget are unqualified by
these slower, contained local runs. Fixtures do not prove live-backend persistence.

At 17:41 Europe/Copenhagen, read-only before/after observations confirm the
installed Runner binary, configuration and launchd plist hashes are unchanged,
its existing PID 66004 remains running, and the deployed FlowStack container
identity/image are unchanged. No pilot task, source adoption, handover or
publication is performed. Final cleanup confirms all 17 recorded owned check
containers are absent. A passing complete gate remains an explicit qualification
requirement before fresh quality and pilot launch.

**Approved-budget candidate:** tree `72a671d2eda74383aa50de435951f2406d0fc951`
adds only the single `test.setTimeout(60_000)` and its explanatory comment to the
GTK candidate. A reconstruction check again confirms the five affected specs
preserve every baseline assertion and other step apart from the documented
native gestures and approved budget. The long journey passes all four projects
with two workers in 52.9s while the original six-case selection runs concurrently
in a second isolated check and passes in 57.2s. There are no failures, skips,
flaky outcomes or retries. Run IDs are
`c0e4d5ad-89b7-4c7c-957e-b71ccb482fc2` and
`ffb464e8-6cd7-46fa-896b-df6c58468db0`; both receipts bind fingerprint
`1b93395ed7c3dbe3a80cdc99b24de92eb6b0f59a2bf5ea94cbce3c8f1c4d698e`.
Both actual Runner checks pass with intact original/disposable inventories and
absent containers (172.99s / 173.97s driver time). Observed memory peaks are
3,350,368,256 / 3,447,787,520 bytes respectively, with zero observed OOM events.
These bounded selections prove concurrent behavior; they do not replace the
fresh complete gate now running on this final source and the final prepared image.

**Complete-gate finding — paired-helper geometry:** the fresh gate passes all
4,084 unit/component tests and 154 tooling tests, then a desktop Chrome assertion
reports a chip overlapping authored text in
`tiptap-migrated/transfers/email-inline.spec.ts:609`. It fails after 9.207s, not
at a deadline, and is separate from the approved loop-journey budget. Stop the
owned browser lifecycle and retain the failed/incomplete report. Before editing,
inspect the measured rectangles, screenshot and source, then run one unchanged
focused reproduction under the same boundary. Determine whether it measures
chip-owned text incorrectly, observes unsettled layout, or reveals actual product
overlap. Preserve geometry and source assertions; do not increase tolerances,
hide content, remove coverage or infer that the earlier focused passes qualify
the whole product. A product redesign or unrelated feature repair needs a new
bounded brief; this finding does not authorize it.

The unchanged isolated trace reproduces identical rectangles (run
`a5563fd5-117b-429f-ad7f-ec0140d0e296`); this is a stable authoring-layout defect,
not load, missing writes or a timeout. The interrupted complete run retains
378 passes/one failure/two skips after 40m03.72s; its prefix passes 4,084
unit/component tests (369.19s) and 154 tooling tests (157.342s). Runner records
unavailable/143 with intact original/disposable inventories and absent container;
build does not run. The two recorded skips are the desktop project's mobile-only
layout case and the opt-in local-backend persistence case.

**Bounded geometry correction, before implementation:** private measurement
keeps every original assertion and restores the original style before assertions.
It finds two overlaps with the inherited 14.95px chip line box, and none when
chips use the font's native `normal` line height. Raising numeric row floors also
removes overlap, but a native chip line box avoids another guessed clearance
constant. Correct only the injected, editor-only chip `line-height` from `inherit`
to `normal`, preserving the existing authored-row minimum, font size, labels,
caret/history behavior and serialized email styles/source. This satisfies the
existing paired-helper geometry contract; it adds no feature or test exception.
Keep the 0.5px overlap and 2px alignment assertions unchanged. Extend no suite:
run native affected unit/component checks and the existing geometry, long
edit/history/reopen and rich-clipboard/undo journeys across all four projects,
then run a fresh complete gate on the resulting source. The earlier failed gates
remain failed and cannot be renewed into an accepted baseline.

The geometry correction has tree `46280253cd3a471fdbb3706f99273246b0759e07`.
Native affected selection passes 607 tests in 20 unit/component suites. The
three existing browser journeys pass **12/12** across both engines/layouts in
93.437s, zero failures/skips/flaky outcomes/retries, run
`bc6392d7-54a7-484c-8c59-25d2dd3938cb`, fingerprint
`9ceb79f3e1b0a0892d0ec2612d67c88ff5ed576749037052037dac9404622c31`.
The original 0.5px overlap/2px alignment assertions pass in resting and editing
views. Retained desktop Chrome resting and mobile WebKit editing screenshots
were inspected; no new wrapping or clipping was observed in this geometry
fixture. The long journey takes 32.879s in desktop WebKit, demonstrating why its
approved whole-test budget is needed; every assertion retains 15 seconds.
The actual check passes in 314.75s, with intact original/disposable inventories
and absent container. Complete diff review confirms only one injected CSS
property and its comment changed since the approved-budget candidate. A fresh
locked-install/complete gate is now running on this source; these focused results
do not waive the interrupted gate or establish complete readiness.

**Complete-gate finding — mobile inspector visibility, before diagnosis:** the
fresh geometry-corrected gate passes its lower layers and the formerly failing
geometry case, then mobile Chrome fails
`tiptap-migrated/authoring/email-inline.spec.ts:291`: the selected-chip action
section has viewport intersection ratio `0.992290318…`, below the existing full
visibility requirement. The test has not reached typing or Save. Do not describe
this as missing writes or extend its timeout. Stop the owned known-failing run,
retain its partial report and reproduce this exact case alone with a trace.
Inspect the toolbar bounds and overflow ownership before any change. Preserve
`toBeInViewport({ ratio: 1 })`, control availability, source and history proof;
only a demonstrated minimum layout/scroll correction is within this email-check
bundle, not a toolbar redesign or unrelated feature work.

The unchanged isolated trace reproduces the same visibility ratio, run
`70c12fc0-e644-4226-812e-26d68400845c`. The failed complete gate is interrupted
after 57m17.97s; source/index/control verification passes, Runner retains
unavailable/143, the container is absent, and build does not run. The test fails
before typing or Save, not at a persistence assertion.

**Bounded inspector correction, before implementation:** private native
IntersectionObserver measurements show a 366.828px chip-action group clipped to
364px. Its 96px identity label cannot shrink. Capping the group to its parent's
content width and permitting only that already-truncated label to shrink produces
a 356px fully visible group (ratio 1), an 85.172px label and the same row height.
Apply only `max-w-full` to the group and replace the identity's `shrink-0` with
`min-w-0`; preserve the identity text/title, every action, fixed toolbar height,
remaining horizontal scrolling and focus/source/history behavior. No new
component, scroll watcher or test exception is warranted. Run native affected
component checks and both existing full-visibility authoring/profile-chip journeys
in all four projects before a fresh complete gate. Keep all assertions and
deadlines, apart from the separately approved loop-journey budget, unchanged.

**Bounded test-design correction, before implementation:** on inspector-corrected
tree `cf51c5fc263502b9ab1327378ae0e683c2d6e3a0`, native affected selection passes
612 tests in 21 suites. The eight-case browser selection passes six cases, but
both WebKit authoring journeys reach their existing 120-second whole-test limit.
They pass full inspector visibility, the first verified Save/reopen and the
replacement Save/readback; they expire later at the unsaved-footer/logout tail.
Preview screenshots occur around 78–82s. These outcomes remain failed, not proof
of missing writes or permission to raise another deadline.

The journey has a natural state reset at its verified Save and `page.reload()`.
Split its independent post-reload lifecycle into one additional case in the
same owning file, reusing `openAuthoringJourney` without a new helper/framework.
Preserve the proof as follows:

| Existing obligation | Ownership after the split |
| --- | --- |
| Selection/composition, toolbar/overlay focus and geometry, active typing, image changes, isolated preview, exact payload/source/metadata and one verified Save/reopen | The existing authoring journey, ending after its first verified reopened content. |
| Protected attribute deletion refusal, no-op preview, exact attributed replacement with verified Save, unsaved navigation cancellation, logout/picker retirement without another write | A separate lifecycle case from the same saved rich fixture, which already contains the protected attributes and opaque template. It selects the unchanged footer by its original text; earlier editing history is not part of this post-reload contract. |
| Cross-feature undo/redo across formatting, blocks and images | The pre-existing shared-history journey remains unchanged. |

Move the existing tail, not a copy. Retain every source, visibility, control,
history, metadata and cleanup expectation; adjust only write counts to the new
independent session (zero before replacement, one after its verified Save).
Monitor unexpected requests/errors in both cases. Keep 120 seconds per journey,
all existing assertion waits, two browser workers and zero retries. This adds one case
per project (1,112 total complete browser selections) rather than relaxing the
failed case. Run both split journeys and the existing profile-chip inspector
journey in all four projects, then the fresh complete gate. No old failed gate
becomes an accepted baseline.

Before retrying the expensive complete gate, qualify the remaining four
authoring-suite cases (shared cross-feature history, scalar/date conditions and
canonical variable spellings) in all four projects. This closes the concrete
GTK coverage gap exposed by the 120-second authoring failure. Use one native
16-case selection on the same final source; do not repeat the twelve already
selected split/inspector cases or treat these selections as a complete baseline.

**Source before the additional owner-approved budgets:** tree
`7d58eae28ba99ed51a9d8b881498234d2b6adc92` passes lint and all twelve split/
inspector journeys, run `767c58ab-06d6-4c7a-a8b8-81ddf1a94da4`, fingerprint
`01807b82b862a14591395f237e3c61fcfa6bc13823d02a921110d9cba28de773`.
The longest authoring case takes 117.239s against its unchanged 120s cap. The
separate guard lifecycle takes 45.553s at most. All original actions/assertions
reconstruct the baseline exactly after undoing only the documented case boundary,
fresh-session counter and footer-locator adaptations.

The remaining native selection finishes **12 passed/four whole-test timeouts**,
zero skips/flaky outcomes/retries, run `b7b7caca-8180-48b2-8220-0fefcc732f15`.
Both WebKit shared-history cases exhaust 120s: desktop image setup finishes at
116.895s before the remaining undo/redo/save cycle; mobile reaches redo after
undoing to baseline. Desktop scalar/date cases exhaust their existing 90s caps,
respectively during post-save reopen and final pre-save branch work. Both mobile
condition variants and all canonical-spelling variants pass. Actual checks retain
failures, intact original/disposable inventories and absent containers; these
results do not establish a complete baseline.

The owner approved these named whole-journey budgets on 2026-10-18:

| Named journey | Previous whole-test cap | Approved cap | Reason |
| --- | --- | --- | --- |
| Shared formatting/block/image undo/redo and verified save | 120s | 240s | One complete shared-history cycle must stay together; native desktop setup alone consumes 116.895s. |
| Authoring, current-input preview and verified save/reopen | 120s | 180s | The split is adequate in scope, but the observed 117.239s leaves insufficient load headroom. |
| Parameterized scalar/date condition authoring and persistence | 90s | 150s | Desktop WebKit reaches the current whole-test boundary during late branch/reopen steps. |

This changes three existing timeout declarations only, in all four projects.
Default 15s assertion waits and all existing explicit readiness waits (including
45s waits), proof steps, workers, retries and containment remain unchanged.
The new guard lifecycle, canonical-spelling journey and other tests keep their
existing budgets. The separate previously approved 60s loop journey remains as
approved. The exact approved patch is retained privately as
`proposed-journey-budgets.patch` under the qualification evidence root. Apply
these three declarations, run the named affected journeys concurrently, then a
fresh locked-install/complete gate on the final source. Publication, installation
and pilot launch remain outside this bundle.

**Approved-budget qualification:** the three declarations produce tree
`5f2b1bbea631537a77886857017be5b424fccaf3`. Native run
`900fb2f2-f886-4a11-8acb-06725c44c8f9` passes all sixteen affected journeys in
671.754s, zero failures/skips/flaky outcomes/retries. Fingerprint
`681879d571a7f8b60d83d08b72362e1ed3968a4abfbfdecfece5df531295b81b`
binds the frozen source. Desktop/mobile WebKit shared-history takes
201.386/156.971s; scalar conditions take 104.916/75.049s. These completed
workflows exceed their former caps without changing an assertion or bypassing
any step. The actual Runner check passes, original/disposable source and index
remain intact, and its container is absent. A source-preparation attempt first
failed before launch because the private caller's image-id file was placed one
directory too high; its log is retained, then the private path was corrected.
This did not execute a browser or change the product candidate.

After reviewing the complete fifteen-file diff and confirming that this delta
contains exactly the three approved declarations, start the fresh locked-install
and native `validate:complete` gate on the same immutable source. Its result,
including any failure, remains required before qualification is complete.

**Additional library-journey finding and proposed verification brief:** on the
same tree/fingerprint, fresh `npm ci` and `validate:complete` pass lint,
architecture, types, all 4,084 unit/component tests and all 154 verification-tool
tests. Browser run `36bf7403-2fde-47c3-83ee-46dc35b66853` records 692 passes,
one failure and five skips before a deliberate stop. Desktop WebKit's existing
30-second HTML-template save/archive/restore/reuse case expires during its final
library archive/readback, after the new email Save and exact-source readback have
already passed. Its snapshot shows the saved email and a pending library change.
The actual check is unavailable/143 after cancellation, with intact inventories
and absent container; build was not reached. This is not an accepted baseline.

The unchanged eight-case traced selection, run
`126f4545-0084-43e0-aa34-0871f2109d60`, passes all four Chromium cases and times
out in both WebKit variants of the HTML-template and visual saved-block journeys.
The HTML cases expire at different late steps (new-email navigation and final
reload). The visual cases finish actual template/block creation, then expire at
library insertion or verified Save after undo/redo. Traces show cumulative native
browser setup, navigation and successful actions rather than one 15-second
assertion consuming its deadline. Failed outcomes and traces remain retained.

Before another complete run, qualify the remaining `email-editing.spec.ts`
journeys in both WebKit projects with native selection, two workers and zero
retries, excluding these two known failures. This closes the concrete coverage
gap exposed by the Linux GTK path without re-running already passing Chrome.
Also run the existing single-owner native cut/history case explicitly as desktop
WebKit: its unchanged test declaration normally selects desktop Chrome and skips
the other projects. Preserve that ownership selection and report every skip.

The reviewable proposal changes only two existing whole-test declarations:

| Journey | Previous cap | Approved cap |
| --- | --- | --- |
| HTML template save, archive, restore and independent exact-source reuse | 30s | 60s |
| Visual template/saved-block creation, actual insertion, shared history, Save/reopen and library archive | 30s | 90s |

Keep these producer/consumer journeys together: a split with pre-seeded saved
content would omit reuse of the data actually produced by the UI. Retain every
action, assertion and existing assertion deadline, two workers, zero retries and
the four-GiB/two-CPU sandbox. Do not change other budgets. The exact approved
patch is `proposed-template-budgets.patch` in the private evidence root. The
owner explicitly approved these two declarations on 2026-10-18. Apply that patch,
run both affected journeys concurrently in all four projects and the remaining
WebKit editor selection, then the fresh locked-install/complete gate on the
final source. No publication, installation change or pilot launch is authorized
by this decision.

The approved two-line change produces tree
`a757954e7689e1389a4c688d839ddb558b840d8a`. Run
`8aeef5cd-d61f-4e95-91dd-f7b2703f2d88` passes all eight affected cases in
110.602s, zero failures/skips/flaky outcomes/retries, fingerprint
`4f3eaf31bc5bfaffbb600fd94cccca2dcf80efb928e164e51df6bc02d0c6c212`.
Desktop WebKit takes 33.216s for HTML and 36.973s for visual reuse; mobile takes
24.780/32.154s. The actual Runner check passes, inventories are intact and the
container is absent.

Before retrying the full product gate, close the remaining GTK coverage gap
with native file selections: finish the remaining editor journeys; then check
the other email adapters/previews/integration and unchanged fixture files, and
the migrated transfer/recovery files in separate bounded selections. Reuse the
already adequate changed-fixture and authoring proofs. Run both WebKit projects,
two workers, zero retries, unchanged assertion and unapproved journey deadlines.
Collect failures across these selections before proposing further policy
changes; do not start another complete gate with a known failure. These are
development checks, not a complete baseline or a replacement for the final gate.

The remaining editor selection, run `6da6b3ad-3f55-4df5-94e7-13211f8fe05c`,
finishes in 1,318.250s with **69 passes/seven 30-second whole-journey timeouts**,
zero skips/flaky outcomes/retries. Both inventories are intact and the container
is absent; the actual check remains failed/1. The failures are:

| Existing workflow | Failing project(s) and last reached obligation |
| --- | --- |
| Independent visual-email copy | Desktop: second Save and exact payload checks pass; the deadline interrupts final reopen readiness. |
| Cropped-image upload/history/save | Desktop: one upload, one verified Save, exact stored HTML/MJML/metadata checks pass; final reopen readiness is interrupted. |
| Parameterized stock-provider selection/history/save/reopen/copy | Mobile, all three providers: original Save/reopen and stored-preview assertions pass; the deadline interrupts the independent copy's editor entry. Desktop variants pass later with the other worker idle, which does not waive the concurrent failures. |
| Parameterized HTML/MJML feed snapshots and preview/test-send recovery | Both MJML projects: persisted snapshots, template and refresh protection pass; the deadline interrupts profile preview or the final test-send form. HTML variants pass. |

All seven expire at their whole-test cap, rather than exhausting an individual
assertion deadline. No new budget change is applied. The concrete four-line
proposal, `proposed-editor-followup-budgets.patch`, gives copy, cropped-image and
the three-provider family 60s each, and the two-format feed family 90s. Keep all
producer/consumer, write-count, source, history, preview and recovery proof, all
assertion deadlines, workers, retries and containment unchanged. Finish the
remaining GTK selections before requesting one consolidated owner decision;
additional failures are findings, not authority to relax the policy.

**Consolidated GTK finding and next verification boundary, 2026-10-19:** the
other adapters/previews/integration and unchanged fixture selection, run
`cbbc2cc9-3af9-4f33-a1cc-f70766198100`, finishes **173 passed/one 30s timeout**
in 1,222.421s, zero skips/flaky outcomes/retries. The timeout is desktop's
condition-move round trip after undo/redo, during its second native drag. Mobile
passes in 13.713s with the other worker idle; that does not waive the failure.

The transfer/recovery selection, run `092aab50-8e9d-48d1-8fc8-c90496466fce`,
finishes **50 passed/one 30s timeout/one intentional skip** in 1,056.031s, zero
flaky outcomes/retries. Desktop's passive-selection case expires during the
last whole-block condition-form checks, after inline selection, apply/cancel
and undo/redo assertions pass. Mobile passes in 20.679s. The existing native-cut
test explicitly uses `FLOWSTACK_NATIVE_CLIPBOARD_PROJECT=desktop-webkit-email`:
desktop passes in 58.747s with every cut/undo/redo/source assertion; mobile skips
under its existing ownership rule. Both actual checks remain failed/1, with
intact source/index inventories and absent containers.

The unchanged, concurrent two-project condition-drag trace, run
`8877936b-403a-4bd9-8034-981cae40eb3f`, retains two 30s timeouts at different
steps. It shows successful actions and geometry reads before the deadline:
desktop reaches Undo/Redo after its first drag; mobile reads the empty origin's
box successfully, then the next read is cut off by context teardown after 58ms.
There is no demonstrated disappearing paragraph or single long assertion wait.
The earlier missing-locator error cannot by itself establish a product bug.
Both inventories are intact and the container is absent.

The consolidated, reviewable patch is `proposed-final-journey-budgets.patch` in
the private evidence root. It adds only these six whole-test declarations:

| Existing journey/family | Previous cap | Approved cap |
| --- | --- | --- |
| Independent visual-email copy | 30s | 60s |
| Cropped-image upload/history/save/reopen | 30s | 60s |
| Stock selection/history/save/reopen/independent copy, all three providers | 30s | 60s |
| Feed snapshot/template/preview/test-send recovery, both HTML/MJML formats | 30s | 90s |
| Passive selection, inline and whole-block condition forms, apply/cancel/history | 30s | 60s |
| Condition move out and back through native drags and undo/redo | 30s | 60s |

This covers nine parameter-expanded journeys in their existing suites. Preserve
all steps and assertions, their existing 15s/45s deadlines, two workers, zero
retries, unchanged global defaults and four-GiB/two-CPU isolation. The owner
explicitly approved this exact six-line patch on 2026-10-19. Apply it and run the
thirty-six affected project/case combinations in
one native selection on the final source, review the complete diff, then run a
fresh locked install and `validate:complete`. The existing default native-cut
ownership explains three extra complete-matrix skips in addition to the two
layout and two real-backend skips; retain all reasons and the explicit WebKit
supplement. No failed or interrupted run becomes an accepted baseline.

#### WebKit timeouts and full-gate limit: implementation brief

Owner authorization, 2026-10-19: diagnose and fix the eight retained desktop
WebKit timeouts and the full-gate limit in the existing isolated worktrees.
Publishing, the running installation and pilot launch remain out of scope.

Supported result: the eight owning journeys and their shared browser/runtime
setup work reliably with their existing behavior and proof. The full native
`npm ci` / `npm run validate:complete` sequence must finish inside an explicit,
credible outer Check deadline. The previous 120-minute envelope stops with
161 cases unfinished and cannot serve as an accepted baseline.

- Start from tree `7cbe534820fdf541fa7b8f829835209539963dcc`; retain all prior
  failures. Confirm no owned check is active before diagnosis. Use one bounded
  native traced selection of the eight cases with two workers and zero retries.
  Inspect action timings, individual waits, frame rendering/focus and CPU/memory
  pressure to distinguish cumulative work, browser setup, stalled interaction
  and application failures. A timeout is an observation, not a diagnosis.
- Preserve every behavior, source, selection/caret, history, write-count,
  validation, rendering and save/reopen assertion. Preserve the existing 15s
  assertion and explicit 45s readiness deadlines, two workers, zero retries and
  four-GiB/two-CPU boundary during the initial diagnosis. Do not replace native
  clipboard or keyboard proof, force interactions, add sleeps, skip cases or
  introduce a new test framework. Change the demonstrated owning surface only.
- Prefer a shared browser/setup fix when measurements demonstrate one. Split an
  oversized journey only at a real verified lifecycle boundary, retain all proof
  in the owning suite, and explicitly account for fixture/write-count changes.
  A whole-journey budget change requires measured cumulative cost, not a way to
  hide an unresolved wait. Keep the decision and impact assessment here.
- The outer qualification deadline is separate from browser assertions. Size it
  from the retained complete execution and remaining matrix, with bounded
  headroom; update only the private Check caller/qualification configuration and
  its driver watchdog. Runner production code, installed configuration and
  hosted CI policy are unchanged. Retain the selected deadline and provenance.
- Extend existing owning unit/tooling coverage only when the fix introduces a
  credible regression risk. Run applicable lower-layer checks first, then batch
  the eight cases across all four projects plus representative overlapping
  editor/fixture journeys. Include the existing explicit WebKit native-cut proof
  if browser startup or display behavior changes. Inspect the full diff, then
  run a fresh locked install and native complete gate on the final candidate.
  A failed/interrupted execution cannot be renewed into a passing baseline.

Finish line: retained candidate-bound focused and complete evidence, all actual
outcomes/skips, integrity/owned-process cleanup and a fresh installation audit.
Keep known unproved backend/hosted-platform coverage explicit. Stop before any
publication, installation change or pilot launch.

**Eight-timeout diagnosis and repair decision, 2026-10-19:** unchanged traced
run `e0f65b76-1509-4f77-a8a2-def0d28d2abf` selects all eight cases together with
two workers: **one passed/seven whole-test timeouts**, zero skips/flaky
outcomes/retries. The last nested-condition case passes after the other worker
becomes idle. The fixed sandbox reaches its four-GiB boundary without an OOM
kill. Completed individual waits remain within their existing limits; the
longest is 8.359s of initial readiness. Median completed clicks range from about
0.6 to 1.5 seconds across these traces, with input dispatch accounting for a
material part of that time. The fallback journey completes 151 browser operations
and reaches final Save confirmation near its 120-second boundary. The short
journeys make successful progress until their thirty-second cap interrupts
later proof. This supports cumulative journey cost under the real GTK/sandbox
configuration; it does not support deleting proof or repairing persistence.

The minimum repair preserves each coherent lifecycle and every existing action
and assertion: fallback **120s -> 180s**; each of the other seven journeys
**30s -> 60s**. No blanket/global timeout, assertion deadline, readiness bound,
worker, retry, clipboard or capacity change is made. Existing owning suites need
no new cases or helper framework for these declarations. Verification must prove
the complete original workflows under concurrency; an extended diagnostic alone
is not passing proof of the repaired candidate.

The private caller uses **180m** for a complete Check, a **185m** settle watchdog
and a **190m** Go-test watchdog. The previous attempt consumed 120m with 161
mobile WebKit cases unfinished plus build still outstanding; the three-hour
budget supplies bounded headroom for that measured remainder. The binary is
compiled from unchanged Runner `b0bdb01860eadf6095e73bad730c64d701b84f23`, with only
the two private test-driver duration constants changed. Production Runner source,
installed quality configuration, the check image and hosted CI policy remain
unchanged. The repaired FlowStack tree is
`68182062e4a935e6d222ffafbfd1bc60c3ebb12b`; its fresh forty-case native selection
covers the eight journeys in all four projects, the long shared-history journey
in all four, and the explicit desktop WebKit native-cut journey (three other
project ownership skips). Fresh native complete verification follows only after
that selection passes.

**Repaired-candidate focused proof:** run
`96876001-8601-4f32-a99b-98caf7a110c1`, fingerprint
`4e504cfdc62e71ebd8506340961b8bb96089c48e39b3d5a4cb31869da2f344b0`, finishes
**37 passed/three intentional clipboard-ownership skips** in 793.097s, zero
failures/flaky outcomes/retries. The eight journeys pass in all four projects;
all four shared-history journeys and explicit desktop WebKit native cut pass.
Desktop fallback takes 119.583s; sidebar chip/word cases 32.100s/31.511s and nested
movement 30.016s demonstrate why their previous caps had insufficient margin.
The existing desktop shared-history journey takes 231.591s within its unchanged
240s cap. Native cut takes 58.042s. Every original workflow assertion remains.
Source digests match before/after, integrity errors are empty and ownership is
released. This is focused proof; a fresh complete gate is now running.

One source-export preflight fails before any container or browser starts because
Git's default tar directory mode is group-writable 0775. The rejected export is
retained; the next immutable export uses per-invocation `tar.umask=0022`, preserving
Git content/executable bits while making directories 0755 and files 0644/0755.
It passes Runner's existing source guard. The corrected archive SHA-256 is
`1a66dc4edddc87f8ffd18e695a1a0a2b8a04d5622ab0a5b5c44fe37488ab7e59`.
No guard or installation setting changes.

**Fresh complete attempt and expanded diagnostic boundary:** run
`5c66c351-ca79-4acf-84a7-fb04c71e8bc8`, actual Check
`complete-1853722351`, passes fresh locked installation, lint, architecture,
types, all 4,084 unit/component checks and all 154 tooling checks. The browser
matrix reaches 772 passes/thirteen additional whole-test timeouts/five existing
skips before a controlled stop at approximately 95 minutes; 322 remain
unfinished (two active/320 unstarted), and build is not reached. Actual outcome
is unavailable/143; native metadata remain running and are not a completed
receipt. Source digests match and owned-process cleanup passes.

The additional failures span short native editing/clipboard interactions,
upload/sign-out recovery, two-document adapter mounting and three existing
120-second workspace journeys. No additional assertion, deadline or production
code change is made. Before proposing any further repair, run these thirteen
cases together with trace enabled, two workers, zero retries and their original
caps in the same immutable candidate. Inspect native action timings and setup,
plus runtime/process resource data, to distinguish a shared slowdown from
separate defects. Full-run CPU samples show approximately 1.94–2.00 CPUs in use
during both low and high host load; host load spikes above 100 are recorded but
do not establish CPU starvation as the cause. The sandbox reports no OOM kill
and negligible memory pressure. No thermal/performance warning is recorded by
the host. Do not turn this partial execution into an accepted baseline or
increase additional budgets without measured justification.

**Intermediate owning-journey repair brief and decision (superseded below):** the unchanged thirteen-
case trace run `a4a0bc1e-43a8-4f3c-bb59-d9f9ad050aab` finishes eight passes/five
whole-test timeouts, zero skips/flaky outcomes/retries, with integrity and owned-
process cleanup intact. Host load is around four to six during this reproduction.
The completed assertion waits stay within their existing deadlines (maximum
8.006s, initial readiness); median native clicks are approximately 0.8–1.4s.
The workspace-properties journey completes 535 browser protocol operations before
its 120s cap, at a later workflow step than the full-run failure. Opaque-rule,
loop, source-preserving typing/history and chip-cut cases likewise make successful
progress to their thirty-second cap. Eight other journeys pass unchanged, several
with only a few seconds of whole-test margin: upload/sign-out 28.410s, completion
29.202s, selected-word loop 26.935s, typed-condition selection 27.927s. The full
run has already demonstrated their inadequate margin under the same resource
boundary. No shared stuck assertion, missing write, OOM, or justified browser
flag/runtime change is identified.

Extend the same measured owning-journey rule to the thirteen full-gate findings,
without changing any proof, fixture, expectation/readiness deadline, global
budget, worker, retry or resource bound:

| Existing owning suite | Whole-journey changes |
| --- | --- |
| `email-editing.spec.ts` | Unknown-upload/sign-out and opaque-rule feedback 30s -> 60s; workspace properties, canvas move/duplicate/delete and tenant Files recovery 120s -> 180s |
| `email-editor-adapter.spec.ts` | Two supported documents' mount/edit/move/reopen and resource isolation 30s -> 60s |
| `tiptap-candidate/boundaries/email-inline.spec.ts` | Delete/Backspace pair unwrap 30s -> 60s |
| `tiptap-candidate/email-editing.spec.ts` | Completion/cancel, unnamed-loop protection, selected-word loop, and typed-condition selection 30s -> 60s each |
| `tiptap-candidate/email-inline.spec.ts` | Source-preserving typing/history/reopen and native chip cut/paste/block history 30s -> 60s each |

These are ten additional 60s declarations and three 120s -> 180s changes. They
retain coherent lifecycle coverage in its existing owning suites; no new tests,
helpers, application feature or browser flags are needed. Reinspect the complete
diff, then run the **entire two-project WebKit matrix**, including explicit
desktop WebKit native-cut ownership, before another fresh complete gate. This
addresses the now-demonstrated broader WebKit timing concern before spending
another complete run. The private WebKit-only Check uses a bounded 120m envelope,
125m settle watchdog and 130m driver watchdog; it is not a complete baseline.
The fresh complete gate still uses 180m/185m/190m and must include all four
projects, the lower layers and build. Only a successful actual Check with a
complete native receipt, source integrity and cleanup qualifies the candidate.
Keep prior failures and known backend/hosted-platform limitations. No publication,
installation change or pilot launch is authorized by this extension.

**Accepted owner quality policy, 2026-10-19: Linux GTK WebKit default.** The
expanded full WebKit matrix exposes another unmodified thirty-second journey
(`chips stay visible outside editing and clicking an imported helper selects
its exact pair`). Continuing to add a declaration for each such journey is
less clear than assigning the demonstrated platform a suitable whole-test
budget. The brief preserves defaults, so this broader policy was prepared as a concrete
unapplied patch. The owner explicitly approved: "Approve Linux GTK WebKit
default of 60 seconds (Recommended)". It is now applied as tree
`1b10f251b6c2bc486d7d486fbc92b37821a2391d`.

The accepted policy sets **60s only for the two Linux GTK WebKit projects**.
Chromium and macOS defaults remain 30s; all assertion/readiness deadlines, every
action/proof step, existing named long-journey budgets, two workers, zero retries
and the four-GiB/two-CPU sandbox remain unchanged. Remove the seventeen new 60s
per-journey declarations from this request (seven original and ten additional),
retaining the four measured 180s long journeys. This reduces the repair to two
project defaults plus those four long-journey changes relative to the preceding
approved candidate; no new tests, helper framework or browser flags are added.
The exact approved patch is retained privately as
`proposed-linux-webkit-budget.patch`. A failure of an individual fifteen-second
assertion remains a failure. A stalled action without its own bound may now
consume up to the additional thirty seconds; the matrix and complete gate must
still succeed with zero retries. Keep this tradeoff explicit instead of
implicitly changing every future journey by scattered edits.
The intermediate `ed8c73f` WebKit matrix is deliberately stopped after 66
passes/one timeout/zero skips with 345 unfinished; its actual Check is
unavailable/143, source integrity passes and the owned container is absent. A
fresh entire WebKit matrix now verifies the accepted source, followed by fresh
native complete verification. No intermediate execution is relabeled as proof
of the accepted candidate.

**Remaining named-journey diagnosis after platform approval:** retain the whole
412-case matrix before choosing a further repair. Its 120m outer deadline stops
with eleven whole-test timeouts and one failed individual assertion. The nineteen
unfinished cases subsequently pass in their separate exact selection (eighteen
passes/one expected skip). Findings reach explicit 90s visual-template, 120s
image/group-rule, 120s combined-transfer, 180s mobile workspace-properties, 60s
stock-provider family, 240s shared-history, 180s authoring/preview/save and 150s
scalar/date budgets; provider variants and scalar/date each share a declaration.
The PEXELS error snapshot contains the expected
attribution after the cap fires, so that observation does not establish a missing
attribution write. Keep individual assertions and all source/write/history proof.

After the matrix settles, select its remaining failed journeys in both WebKit
projects. Where a whole-test cap prevents observing complete workflow cost, use
one separate immutable diagnostic export with a bounded five-minute observation
budget for those selected journeys only. The later mobile shared-history finding
reaches its 240s cap during Undo traversal, before the complete Redo and persistence
proof; allow eight minutes for that diagnostic alone to observe the whole replay.
These are observation bounds, not proposed accepted budgets. Preserve every action and individual
15s/45s deadline, two workers, zero retries, image and sandbox. Keep the assigned
worktree's approved named budgets unchanged during this experiment. Inspect the
complete action timeline, especially the combined native-drag placeholder and
later history/image/save proof: a persistent individual assertion failure calls
for a responsible product/test fix. Completed steady work supports a measured
owning-journey budget proposal with explicit margin. The diagnostic export is
observation evidence, not an accepted candidate or complete baseline. Present any
remaining named-budget patch for the owner's decision before changing the
approved working tree, then verify the accepted candidate and a fresh native
complete gate. Recalibrate the private outer Check envelope from the measured
matrix plus earlier lower-layer/Chromium execution, separately from all assertion
and journey requirements. Publication, installation changes and pilot launch
remain outside this boundary.

**Diagnostic timing control:** the initial 24-case all-traced export
`fe1483da01d0d8410a7a459f6ad173d5d102b316` is deliberately stopped after two
completed cases (run `a2fcb4b2-2e68-475f-bff9-6cdf93adb084`); native metadata stay
running with twenty-two unfinished. Actual outcome is unavailable/143; source
integrity and owned-process cleanup pass. The visual-library journey completes
in 108.896s and image/group rules also complete, preserving their assertions.
The traced shared-history case is substantially slower than its earlier
normal-mode evidence. Compare the exact selection without tracing before using
those timings to size accepted budgets. First isolate the protected-profile
reload assertion in a four-case trace with the workspace-properties journey,
covering both WebKit projects under the same concurrent/resource boundary.
The fifteen-second reload assertion stays unchanged. Neither diagnostic mode
is accepted full verification or permission to change the assigned budgets.

The focused trace run `ab31d32a-99ae-4da7-b428-57b1b1e04ae6` passes all four
cases with source integrity and cleanup intact. The protected-profile post-reload
readiness wait completes in **12.361s desktop / 7.298s mobile**, retaining the
15s assertion and all write/source/reopen checks. Each reload records 180 module
and fixture requests; desktop fixture reads complete successfully before the
editor dependencies and mount finish. This does not reproduce the earlier
failure or establish a missing write; it also does not prove that readiness has
ample margin under every load. Keep that retained finding explicit and require
the unchanged assertion in normal-mode diagnosis and fresh complete verification.
Normal-mode run `3439ecc5-78b1-4cae-a8c7-356a9166ac7f` passes the exact
24-case selection on the same diagnostic source, with tracing at its repository
default: **24 passed, zero failures/skips/flaky outcomes/retries**. Actual Check
passes/0; source integrity and cleanup pass. Its browser fingerprint matches the
focused trace, `264b6143f8e45d6d6ade1c3536f2f3ca45005409dd02bf9d7b375b50b37eef65`.
All native-drag, history, delayed-image, source, write-count and reopen proof
completes. This is diagnostic evidence; accepted worktree `1b10f251` remains
unchanged, and no complete baseline exists.

**Consolidated named-budget decision: approved and applied.** The earlier owner
approval preserved named long-journey budgets. On 2026-10-19 the owner explicitly
replied "Approve the consolidated named budgets (Recommended)" to the reviewable
nine-declaration patch. It is now applied as tree `dec81715` under that decision.
Keep all 15s assertions, explicit 45s readiness waits, actions, two workers,
zero retries and sandbox bounds unchanged. The normal-mode measurements are not
the worst full-run cost: the retained matrix reaches existing caps at later
workflow steps, and shared-history still has complete Redo and persistence proof
remaining. Give those coherent journeys explicit headroom rather than remove
proof, add sleeps or introduce a timeout helper/framework.

| Existing journey | Prior cap | Accepted cap | Maximum normal diagnostic |
| --- | --- | --- | --- |
| Visual template and saved block | 90s | 150s | 58.682s |
| Image and grouped-column rules | 120s | 180s | 66.606s |
| Contextual workspace properties | 180s | 240s | 110.187s |
| Three stock-provider variants | 60s | 120s | 45.178s |
| Shared authoring history | 240s | 360s | 221.477s |
| Authoring, preview and save | 180s | 240s | 107.185s |
| Scalar/date family | 150s | 240s | 124.715s |
| Protected-profile journey | 120s | 180s | 79.845s |
| Combined native transfer/history | 120s | 240s | 112.474s |

The protected-profile row does **not** relax or repair its 15s reload assertion.
That earlier failure ends at 119.886s with further chip/reopen proof remaining;
the accepted whole-test allowance prevents a secondary 120s cap from cutting
that proof short. The strict readiness finding stays retained and must pass on
the final source. A recurrence requires diagnosis, not another assertion limit.
As with existing named budgets, these nine declarations apply to all their
projects. Defaults remain Linux GTK WebKit 60s / other projects 30s. An action
without its own bound may take longer to fail under the new whole-test cap; this
is the explicit quality tradeoff accepted by the owner.

The reviewable private patch is `proposed-named-journey-budgets.patch`, SHA-256
`92b532846d2ecdafff2ed6f325bccdb95534ea106b7d897835776d9abe193f28`.
It changes nine declarations in three existing suites and documents the already
approved Linux default in README. Applied tree
`dec81715fb61f44f786035cdd4780be660a9e105` is the final verification candidate. It needs no
new tests for constants: the existing owning workflows provide the meaningful
regression proof. Patch applicability, complete-diff review and whitespace checks
pass; the assigned staged tree matches the exact approved patch.

The approved patch is applied and the final immutable candidate exported.
Run the twelve diagnostic titles in all four projects plus the explicit WebKit
native-cut proof in one native selection: **52 cases, 49 expected passes and
three clipboard-ownership skips**, zero retries. Then run fresh `npm ci` and
`npm run validate:complete`, once and sequentially on that final source, with the
private 240m Check / 245m settle / 250m driver envelope. Retain all outcomes,
candidate identities, original failures, integrity and owned-process cleanup.
Stop for any real failure; an interrupted/failed gate remains no baseline.
Finish with read-only installation verification and refreshed retained records.
No publication, installation change or pilot launch is authorized.

**Final-candidate focused proof:** run
`ca998f11-bfb9-4470-a779-403c4cb5849f`, browser fingerprint
`ab04e8164378b7218fe20d9ab5e688f5c756844d607132fbacf841e91099dfc9`,
passes **49/52 cases with three expected clipboard-ownership skips** in 23.951
Check minutes. All thirteen selected titles appear in all four projects; the
explicit native-cut case passes in desktop WebKit and owns the three skips.
Every protected-profile 15s reload assertion passes. Actual Check passes/0,
complete native selection receipt/execution/run agree, every attempt is initial
with retry zero, source digests match and owned-process cleanup passes. This is
focused proof, separate from the complete gate below.

**Final-candidate complete proof:** actual Check `complete-58472347` passes/0
in **144.326 minutes** after fresh locked installation and sequential
`npm run validate:complete`. Lint, architecture, types, all **4,084
units/components, 154 tooling tests, 1,105 browser cases and production build
pass**. Native browser run `5ac15549-cd08-4da1-b430-1b1a3feb3665` completes all
1,112 selected cases with exactly seven expected skips, zero failures/flaky
outcomes/retries and the same final fingerprint. The skips are two opt-in real
backend cases, two viewport-specific exclusions and three native-clipboard
ownership exclusions; their exact project/title identities match the retained
expectation. All four protected-profile reload assertions pass unchanged.
All fifteen summary audit flags pass, including complete receipt/execution/run
agreement, source integrity and released container ownership. Before/after
source digest is
`dd7e4deefc070f44ce4fa82656aa8c70c8813a6c335630524c46ce09c10cde4d`.
This is an accepted complete baseline for the final immutable candidate only.

The final read-only installation audit at **2026-10-19 19:51 CEST** confirms
unchanged binary/configuration/plist hashes, service PID `66004`, deployment
container/image and absence of all **54** recorded owned check containers.
The refreshed thin evidence archive contains **733 files**, SHA-256
`a4643ea56d2a86dbc4899c944867e24b53541ced6eaea905657b7ff0c2ec75c5`;
the 698-file pre-approval snapshot remains separately retained. Raw traces and
screenshots remain in their original private run directories. The local index
and `named-complete-outcome.json` retain the accepted candidate, exact skips,
timing and audit results. Real backend persistence remains opt-in/unproved,
and hosted CI's existing 20-minute allowance is not qualified by this local
container execution. No pilot, publication or installation change occurred.

**Private complete-envelope recalibration:** the retained full attempt starts
its first WebKit case 69.399 minutes after Check launch. The accepted-policy
WebKit matrix alone exceeds 104 minutes before its final journeys. A 180-minute
complete envelope therefore has insufficient credible margin for the remaining
work, build and ordinary timing variation. The next private caller uses a
**240m Check / 245m settle / 250m driver** envelope. Focused long-journey
diagnosis and affected proof use **60m / 65m / 70m**. These are private
qualification-driver bounds compiled from unchanged Runner `b0bdb018`; the
active earlier caller is untouched. Record the matrix's final duration and the
resulting complete outcome rather than treating this allowance as proof. Browser
assertions, accepted journey budgets, capacity, installed policy and hosted CI
remain unchanged.

**Approved patch qualification, 2026-10-19:** the six additions are exactly
four declarations in `tests/e2e/email-editing.spec.ts`, one in
`tests/e2e/tiptap-integration/email-inline.spec.ts`, and one in
`tests/e2e/tiptap-migrated/recovery-layout/email-inline.spec.ts`. No assertion or
proof step changed. The archived source is bound to browser fingerprint
`92660f07052a681c54f1788f04bc76d81e3299d22f344a71ff9883a05b9fb3a4`.

- Initial affected run `9cf8ed65-0749-4507-9a75-7886ef81fae3`: **33 passed/three
  failures**, zero skips/flaky outcomes/retries. Desktop WebKit hits the existing
  ten-second product startup fallback before cropped-image upload, reaches the
  whole-test deadline at the stock PIXABAY copy-entry form, and reaches the
  passive-selection deadline while awaiting the next whole-block form action.
  These outcomes do not establish missing writes or a lost form interaction.
- Diagnostic selection `b16e3cac-c563-4bf4-b740-2d6cc17925e5` runs no tests because
  of an incorrect project name; its failed check and cleanup are retained.
  Corrected unchanged trace `40dc28b5-c7fc-40b4-b34b-557cdc9648bb` passes **3/3**:
  image 56.257s, passive selection 56.482s and PIXABAY 32.183s. Both form clicks
  complete, and upload/history/save/reopen proof completes. Tracing reaches the
  four-GiB memory boundary without an OOM kill. Host load is lower than during
  the initial failure; correlation is not causal proof.
- Fresh untraced affected run `3ab1a0fe-ed0a-4fa4-bc23-ef4966cc1952` passes
  **36/36**, zero skips/flaky outcomes/retries. Both source inventories and
  container cleanup pass. No source or policy change separates these runs.
- Fresh complete attempt `bba8748b-30a0-49a6-a7bb-9a3e18c0d7c3`, actual Check
  `complete-2729070886`, passes all lower layers before the browser findings
  below. It stops at the existing **120-minute Check deadline**. Its last report
  is **937 passed/eight timed out/six skipped**, with 161 unfinished; it is not a
  completed browser receipt or a passing complete gate. Build is not reached.

All eight individual failures are desktop WebKit whole-test timeouts in
previously unchanged journeys, not new assertion-deadline decisions:

| Existing journey | Whole-test cap | Last retained boundary |
| --- | --- | --- |
| Literal variable fallback authoring/history/reopen | 120s | First save/readback and reopen pass; Cancel after removal/Undo is interrupted before the second verified save. Mobile WebKit later passes unchanged. |
| Feed-template ownership, collision rebind and independent save | 30s | Awaiting verified Save confirmation. |
| Sidebar chip condition, edit from endif, history/save/reopen | 30s | Final reopened chip inspection. |
| Keyboard-selected word through sidebar focus/cancel | 30s | Final Undo action. |
| Loop field types and invalid-draft protection | 30s | Whole-test deadline; exact action requires tracing. |
| Selected-text condition with new hotel/car fields | 30s | Whole-test deadline; exact action requires tracing. |
| Selected chip/formatted-text movement and pending typing | 30s | History/source assertion boundary. |
| Complete nested-condition movement with both branches | 30s | Awaiting verified Save confirmation after Redo. |

At that earlier boundary, these failures were unresolved. A whole-test timeout alone does not establish
whether time was spent cumulatively or an interaction stalled. Diagnose the
owning cases before changing their structure or budget. The complete check's
runtime boundary also remains unresolved: all of desktop Chromium, mobile
Chromium and desktop WebKit finish, but most of mobile WebKit remains unfinished.
The six observed skips are the two layout exclusions, two opt-in live-backend
cases, and two existing native-cut ownership exclusions. The third native-cut
exclusion is in the unvisited remainder. No real-backend proof is claimed.

At 09:46 Europe/Copenhagen on 2026-10-19, a read-only audit at that boundary confirms the
installed Runner binary/configuration/plist hashes, service PID 66004 and deployed
FlowStack container/image still match the original observations. All **42**
recorded owned qualification containers are absent. Actual complete-check source
inventories match before/after, integrity errors are empty and ownership is
released. The native browser metadata remains incomplete; the actual stopped
Check result takes precedence. Full diff and exact six-line-delta checks pass.
Publishing, the running installation and pilot launch remain untouched.

At **15:11 Europe/Copenhagen on 2026-10-19**, the latest read-only audit confirms
those binary/configuration/plist hashes, service PID and deployed container/image
still match. All **52 recorded owned qualification containers are absent**.
At that audit the assigned source remains `1b10f251` and the nine-declaration
proposal is unapplied; the subsequent approval/application is recorded above. No publishing, installation change or pilot launch has occurred.

The latest thin-record archive, `qualification-records.tar.gz`, contains **698
files / 28,935,852 bytes**, SHA-256
`f98c804f5bc8a6bad3090743ff08c0208dbd94d6c87d1e08eb2ea9f9b836f68f`.
It retains actual Check results, incomplete native metadata from interrupted
runs, successful tail and diagnostic receipts, logs, timings, the unapplied
proposal metadata and the latest installation/cleanup audit. The earlier
517-file / 23,182,963-byte snapshot, SHA-256
`827ea2a6b085b73ab25ee6d95dedb46cc913681d010e9c53fd909b144f64a9dc`,
is preserved as `qualification-records-20261019-before-webkit-timeout-repair.tar.gz`.
Raw traces, screenshots and error snapshots remain with their original runs.
No failed, interrupted or focused result is relabeled as a complete baseline.

At 00:52 Europe/Copenhagen on 2026-10-19, a fresh read-only audit confirms the
installed Runner binary/configuration/plist hashes, service PID 66004 and deployed
FlowStack container/image still match the original observations. All 37 recorded
owned check containers are absent. Thin source-bound reports, receipts,
process/containment observations and logs are consolidated in the local
`qualification-records.tar.gz` (448 files, 20,209,085 bytes, SHA-256
`3447706c2a8b98ca3fc736862aa60073b20b9a6558931c316c049ced35edba2e`).
The previous archive is retained separately; traces and screenshots remain with
their original runs. At that boundary, the full source diff and six-line proposal
had been reviewed and passed whitespace/applicability checks. No failure,
cancellation or focused pass has been relabeled as a complete baseline.

Private source, wrapper/image inputs, check observations, traces, diagnostic
timings and receipts are retained locally at
`/Users/dan/.codex/runner-qualification/flowstack-email-reliability-20261018/`
and on `always-on` at
`/Users/dan/.local/share/flowstack/runner-email-reliability-20261018/`.

### GitHub Issues bundle qualification

Implemented on `codex/runner-github-issues`, from main merge `0d19a9a`. Store format
18 adds one unique source mapping and connection health; format 17 is preserved
and rejected rather than migrated. No execution-role instructions, harness
semantics, dependencies, hosted merge policy or installed skills changed.

- `GOTOOLCHAIN=go1.26.9 go test -race -timeout 15m ./...` passed. Subsequent
  presentation refinements and the provider-bound configuration simplification
  received fresh owning engine/CLI/adapter race checks; unchanged lifecycle and
  low-level results are reused rather than repeating the expensive whole suite.
  Final `go vet ./...`, formatting and complete diff review passed. Separate
  source-observation/publication interfaces contain no GitHub configuration;
  the built-in Issues adapter binds its provider settings at CLI composition.
- Real CLI used private qualification request
  [#6](https://github.com/cortexium-io/runner-delivery-qualification/issues/6) and
  dedicated overview [#5](https://github.com/cortexium-io/runner-delivery-qualification/issues/5).
  Capture remained Draft with zero invocations. Explicit owner approval produced
  Ready without starting work. Editing and closing the source then reimporting
  preserved the exact task/approval/history. Closing the overview failed visibly
  without changing that task; reopening it restored synchronization. Stopped work
  remained separate from completion. No role or model invocation was made.
- Linux arm64 focused race checks passed in a disposable, network-disabled
  `golang:1.26.9-bookworm` container, image
  `sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c`,
  kernel `7.0.14-linuxkit`. This qualifies the changed Go/SQLite/CLI paths inside
  Docker Desktop; it does not repeat native harness qualification. The container
  was removed. Existing source and module-cache mounts were read-only.
  The final provider-bound interfaces also passed fresh Linux engine/CLI/adapter
  race checks after their simplification.
- The operator guide and repository-local operator reference now describe the
  intake/owner/view journey. The real executable smoke and four no-CGO platform
  archives passed; these are local candidate artifacts, not a published release.
- Read-only FlowStack observation at approximately 10:43 Europe/Copenhagen still
  showed two active, two waiting and two included tasks, with no blockers and no
  delivery. No new Runner was installed or dispatched there. The remaining pilot
  needs the safe handover decision above, not a second owner for the same work.

Evidence is retained privately at
`/Users/dan/.codex/runner-qualification/github-issues-20261018/`: native/focused
test logs, Linux log, CLI snapshots before/after owner acceptance and connection
failure/recovery, public overview bodies and platform artifacts. This bundle
does not qualify autonomous intake, remote approvals, Projects/Trello, request
auto-closure, an unbounded board or FlowStack product completion.
GitHub transport follows its [REST Issues contract](https://docs.github.com/en/rest/issues/issues):
only inbox/request reads and the configured overview body's update are added.

### GitHub Issues review fixes, 2026-10-18

The owning regression cases reproduced and now cover four recovery/ownership
defects: an uncertain old overview write could overtake newer progress; read-only
inbox used unapplied file configuration; fatal view cleanup delayed cancellation
of unrelated workers; and concurrent import decoded into caller-owned source
memory. The fixes retain one pending view body before dispatch, observe its
receipt before newer publication, bind inbox to applied settings, cancel all work
before waiting for cleanup, and decode the captured task into a separate value.
The existing public-output case also covers a trailing backslash in a source title.
No new scheduler, retry policy, dependency or test harness was added.

Focused native race cases and the Linux arm64 owning suites passed. The existing
concurrent-source test additionally checks input ownership and passed ten native
race repetitions. Linux initially exposed its data race; that failed log is
retained. The incomplete full gate was stopped for the store fix; it is not a
passing result. A fresh full gate runs against the corrected source, with its
terminal result recorded on the PR. Vet, executable smoke and four local platform
archives were renewed after the fix.

The real CLI also exercised overview #5 using a qualification-only transport
wrapper that forwarded one successful GitHub PATCH but discarded its response.
Runner retained the pending body; the next sync observed its exact receipt and
sent no second PATCH. The task, source, history and allowances stayed unchanged,
with zero model invocations. This complements the controlled delayed-server case;
it does not claim to reproduce GitHub's server scheduling. If an uncertain write
never appears, newer view writes remain held for operator reconciliation.

Review evidence is private at
`/Users/dan/.codex/runner-qualification/github-issues-review-20261018/`:
regressions before/after fixes, full-gate output, Linux logs, real transport trace
and task snapshots, executable and platform archives. No release, installation
change, request auto-closure or FlowStack handover was performed.

## Accepted direction

1. Build a fresh core around one durable local source of truth and Runner-owned task IDs.
2. Preserve the event-and-action coordinator, the three execution contracts,
   dependency-aware concurrency, independent review and serialized integration.
3. Make the default journey complete and understandable before adding integrations.
4. Expose one execution policy with explicit retry and escalation semantics.
5. Introduce GitHub Projects as an optional projection, then prove the integration
   boundary with a second board provider. Defer externally loaded plugins.
6. Define test scope and strategy before implementation, and review test quality
   and consolidation as part of accepting every change.

Success means a user can set up Runner, submit and approve work, follow its
progress, resolve an interruption and reach a reviewed candidate without a
GitHub Project. GitHub code delivery can still require GitHub. The first
connected delivery milestone remains an accepted pull request and confirmed merge.

## First release scope

The first replacement release includes the complete local Plan/Ready process,
one repository/base per configured Runner instance, exact approval, independent
review, persisted recovery, configurable models/reasoning/escalation and
parallelism, an optional per-task harness-invocation ceiling, and readable local
status. It also includes GitHub PR/check/merge delivery, human merge by default,
explicit opt-in automatic merge policy, backup/restore, and a documented breaking
cutover.
The existing [macOS/Linux amd64/arm64 builds](../scripts/build-release.sh)
remain the packaging target.

Use sandboxed Codex CLI as the first qualified harness. This selects a bounded
delivery scope, not a preferred model. The execution boundary remains capable
of other native harnesses; Claude and Pi qualification follow separately. Setup
must reject unqualified harness choices rather than silently substitute Codex.

Defer board views until after the core release. Also defer autonomous issue
intake, additional repository references, user-authored workflow composition,
time/token/cost admission ceilings, multi-host execution, a web dashboard and
externally loaded plugins. Preserve usage reporting, with unavailable usage
shown as unknown. These omissions are explicit release limits, not features
that agents may approximate. Add a deferred capability only with its own scoped
brief and acceptance evidence.

## Rebuild the core without inheriting its constraints

A fresh core is the recommended construction approach. The change in authority
from GitHub Projects to local state affects work identity, approval, scheduling,
recovery, configuration and tests. Extracting abstractions from the old design
first risks preserving those assumptions behind new interfaces. Build from the
accepted process and contracts, consulting the current code when a concrete
behavior, failure case or boundary needs investigation.

The old implementation remains valuable evidence. Carry forward required
invariants and known failure scenarios, then express them against the new
boundaries. Do not copy whole packages or the test suite merely to preserve
behavior parity. Evaluate a self-contained low-level component for reuse only
when its responsibility and contract fit directly and its complexity is justified.
Rewriting a sound subprocess or filesystem primitive brings no automatic benefit;
retaining one must not force the old work model into the new core.

Keep the rebuild incremental and runnable. Establish a complete local journey
early, then add the remaining required policies and integrations. A fresh start
does not require recreating every existing feature before anything works. It
does require maintaining authority, integrity and recovery boundaries in every
slice that can execute real work. Select the first release's capabilities
explicitly; omitted advanced features are documented, not silently approximated.

Backward compatibility is not a design requirement. New configuration, IDs,
state, commands and integration contracts may break compatibility. Provide an
upgrade utility only where conversion is small, deterministic and verifiable.
Otherwise publish a clearly breaking release with fresh setup and explicit
resubmission of remaining work. Do not build a compatibility layer, maintain two
active backends, or port historical approvals and recovery journals to avoid
that break. Retain old artifacts safely for inspection; breaking compatibility
does not authorize deleting existing work.

## Lessons and their disposition

The two assessments are [Assess source control article](codex://threads/01a13967-44ab-71f1-b689-ee91f0ea967d)
and [Review software-factory lessons](codex://threads/01a11fb1-f1c6-7a52-b8a2-f35fe00ece4f).
They provide design input, not evidence that either alternative has better
operational reliability than Runner. The software-factory references below use
the revision assessed in those chats.

| Learning and source | Decision for Runner |
| --- | --- |
| software-factory keeps its work locally and makes GitHub delivery optional. [Overview][factory] | Own work independently of external boards. Keep work management and code delivery separate. |
| Its quick start follows setup, specification, approval, execution and delivery. [Overview][factory] | Offer one recommended first-change journey. Preview and apply setup in one session. |
| Repository preparation establishes quality requirements and verification prerequisites. [Setup skill][factory-setup] | Agree authoritative checks and explicit baseline exceptions at setup; distinguish Runner readiness, repository readiness and integration availability. |
| Conversational operation explains running work, waits and escalations. [Dispatch skill][factory-dispatch-skill] | Add an operator skill over the same deterministic commands and local status. |
| Its status operation can also enforce worker limits. [Dispatcher][factory-dispatch] | Keep Runner inspection read-only; scheduling and recovery belong to explicit actions and the coordinator. |
| Plan review presents consequential decisions together. [Plan review][factory-plan] | Show outcome, scope, dependencies, assumptions and authorization in one approval preview. |
| The floor guard detects weakened checks, while behavioral cases test pressure to take shortcuts. [Guard][factory-guard], [cases][factory-cases] | First add paired evaluations for weakened tests and legitimate test refactors. Add narrow enforcement only if a demonstrated gap remains. |
| Visual QA produces evidence a fresh reviewer can inspect. [QA skill][factory-qa] | Retain suitable screenshots or recordings for interaction claims, bound to the candidate and checked for relevance. Avoid recording every task by default. |
| Carrying review through rebase based on an unchanged patch can miss interactions with the new base. [Dispatcher][factory-dispatch] | Preserve explicit base and candidate validation. An unchanged patch is insufficient authority to reuse acceptance. |
| Missing executable checks can remain gaps in the external check runner. [Check runner][factory-check] | Required missing proof blocks acceptance; process exit success alone does not establish completion. |
| Anthropic reports delivery infrastructure becoming the bottleneck as code production accelerates. [Article][anthropic] | Measure the complete Ready-to-merged path, integration waits and repeated review before changing scheduling. |
| Anthropic exposes metadata queries without requiring workspaces. [Article][anthropic] | Keep observation cheap; create a workspace only when an action needs files. Retain Git for Runner's workload. |
| Anthropic describes both excessive defensive agent review and useful simulations and fuzzing. [Article][anthropic] | Require concrete, reachable review findings. Invest in interruption, concurrency and publication tests instead of speculative machinery. |
| The earlier Runner assessment found competing recovery advice for moving a card to Ready and retrying its recorded stage. [Status rendering](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/cmd/cortexium-runner/status.go) | Present the appropriate recovery first, including preserved work and the stage that repeats. |
| The earlier Runner had candidate-bound evidence, focused follow-up review and private recurring-failure drafts. [Evaluation guidance](model-profile-evaluation.md) | Preserve evidence integrity and economical review. The replacement retains task evidence but retired the guidance-draft command; review actual incidents before changing instructions, without adding a learning service. |

Keep Runner's continuous reconciliation, interchangeable harness boundary and
typed internal actions. Do not adopt file-overlap locks as a substitute for actual
resource conflicts, mandatory extra reviewer roles, or weaker pre-merge proof. Anthropic's
post-merge emphasis reflects its unusual scale and does not justify relaxing
Runner's integration contract. Worktrees also do not isolate ports, databases
or test fixtures: declare or allocate those resources when a repository needs them.

## Product and skill reassessment

**Owner clarification, 2026-10-16.** Build the simplest complete Runner around
the accepted local process. The legacy capability comparison is an inventory
of possible needs, not a restoration checklist. Existing code, skills and
research must earn inclusion through a current user outcome or concrete risk.
R1–R5 qualified their frozen scope; they did not establish legacy feature parity
or completion of every product goal.

### Local process and optional connections

The target journey is setup, Plan or Ready, approval, execution, independent
review, meaningful completion and recovery using local authority. A code host,
issue tracker or board must not be required to complete that local journey,
including real dependencies. Required local Git objects, a usable configured
harness and repository verification tools remain prerequisites. A cloud harness
can still require its provider; optional integrations do not imply offline models.

GitHub is the first and most likely code-delivery integration. GitHub Projects
is the likely first board view; Trello or another provider should follow a real
use case. Keep intake, views and delivery separate. A board mirrors bounded
progress and links; it neither owns approvals nor makes a failed task successful.
An unavailable board delays its synchronization, while a code-host outage delays
the affected remote delivery. Neither creates another scheduler or cancels
unrelated eligible local work. Prove the boundary with actual adapters before
considering any public plugin framework.

The remaining design questions are about the new process:

| Need | Current evidence and design boundary |
| --- | --- |
| Complete local work without GitHub | Local mode currently retains a reviewed candidate in Awaiting delivery. The [new brief](#local-completion-and-combined-plan-verification-brief) defines result-branch or configured-branch completion, human/automatic merge permission and durable local proof. These remain unimplemented. A status edit or an agent's claim cannot substitute for completion proof. |
| Establish the requested Plan outcome | Child acceptance alone does not establish the combined result. The new brief defines a shared approved outcome, private child integration and combined verification before final delivery. The [legacy plan-delivery decision][legacy-plan-delivery] is evidence, not an implementation to port; its amendments and Project machinery are not adopted. The legacy flow itself still awaits live rollout qualification. |
| Make ordinary repository checks usable | The [pilot preflight](#real-project-pilots-before-merge) demonstrates toolchain discovery, dependency preparation and loopback gaps. Readiness must exercise the actual execution boundary and distinguish missing capability from a product defect. Add only capabilities needed by supported checks, with clear ownership of ports, processes and data; do not solve failures by blanket host access or baseline exceptions. |
| Make progress and recovery understandable | Keep attention-first status, explicit limits and stage-specific recovery. Assess automatic remote observation, feedback and an operator skill against the connected journey, without duplicating coordinator decisions. Local operation must remain useful without any of those integrations. |

This reassessment does not reopen every deferred feature. Additional harnesses,
budgets, workflow composition and services require their own demonstrated need.
Retain the existing pilot boundary and release evidence. Before another build
instruction, refine the owning brief here with representative outcomes, existing
coverage and a clear stopping point. Do not create a parity backlog or copy the
old runtime to reconcile the branch with `main`.

### Skills and instructions assessed

The 2026-10-16 review read all four installed Runner skills, the replacement's
[embedded role instructions](../internal/runner/execution/codex.go), its
[paired behavioral cases](../internal/runner/execution/behavior_eval_test.go),
and the relevant `start-work`, `pr-ready`, `genny-cli` and `techbase-operations`
skills. It also inspected the interaction evaluation and Genny verification
references and the Techbase handoff guidance. Unrelated personal skills are
outside Runner's instruction surface.

The installed planner, reviewer and interaction-design skill files matched
legacy commit `14a86d2c26acae4eefe8c499eae4e0cdc4ebede7`; the installed implementer
did not. Its SHA-256 was
`a965d1f8b85daf6a0d0c760c3b9cfdcae40a9c505f27c09c3ad6cc6057c36f90`.
This is a source assessment, not a live evaluation or installation change.

| Instruction surface | Retain or adapt | Remove or qualify before use in the replacement |
| --- | --- | --- |
| [Planner][legacy-planner] | Coherent outcomes, inspected contracts, actual dependencies, explicit consequential questions and economical verification. | The installed skill delegates proof technique to the implementer and assumes outline/details stages and per-card profile selection. The new brief must define expected behavior, evidence boundary and inspected coverage before coding; use only the supported planning schema and configured profile policy. |
| Installed implementer | Complete the scoped outcome, diagnose faithful failures, preserve required assertions, extend/consolidate owning tests and report actual results and limitations. | Do not import its `repair_needed`/`test_requested` protocol, specialist handoff, whole-plan gate or host-only receipt assumptions. Replacement assignments follow the approved verification brief and current result/capability contract. A skill grants neither another role nor more time/access. |
| [Reviewer][legacy-reviewer] | Independent judgment, concrete reachable findings, distinction between defect/missing proof/decision, test sensitivity and valid evidence reuse. | Adapt legacy status names and member-evidence/whole-plan assumptions to implemented boundaries. The new reviewer checks the agreed test strategy; it must not silently return all evidence-method decisions to the implementer or require speculative tests. |
| [Interaction design][legacy-interaction] and its evaluation reference | User goal, states, adjacent journeys, accessible interaction and evidence suited to the claim. Distinguish requirement, preference and unobserved behavior. | Keep it optional for relevant UI work. It cannot assume a browser grant or mandatory visual pass for backend tasks. Candidate-bound screenshots establish only what they show; the current harness has no browser capability. |
| Embedded role instructions and behavioral evaluations | These are the replacement's active, already-qualified contracts: explicit briefs, independent audit, focused verification, bounded repair and honest evidence. Existing cases pair weakened assertions with legitimate consolidation and fabricated claims with real evidence. | Reassess omissions against incidents and current requirements. Extend the owning cases only for distinct risks; do not replace concise contracts with the installed legacy handbooks or treat prompt wording tests as behavioral proof. |
| Operator interface and `start-work` / `pr-ready` | Current repository authority, status inspection, existing commands, scoped recovery and accurate handoff evidence. | There is no dedicated replacement operator skill yet. A future one should explain and invoke the same deterministic commands, never edit private state, reset limits, operate a second queue or grant its own approval. Generic task/PR skills remain human-session tools, not implicit worker permissions. |
| `genny-cli` | Target-project source ownership, the selected generator version and verification through real entrypoints. | Local installation/update instructions and absolute reference paths do not become agent capabilities. Bind the approved toolchain and checks to the pilot environment; do not update the user's Genny installation as setup for a Runner task. |
| `techbase-operations` and handoff guidance | Project ownership, explicit delivery authority, observed evidence and required project-specific checks. | It still says GitHub Project state owns work, uses legacy `add plan`/`add ready`, and assumes Runner installs role skills. Those consumers need a separately scoped cutover update. Techbase's external policy is not Runner's core architecture or a requirement for every repository. |

Keep three execution contracts: planner, implementer and reviewer. Preparation,
audit and focused verification are supported stages, not reasons to add roles.
Keep runtime guarantees in code and task-specific requirements in the approved
brief. Optional references should supply relevant reasoning, not repeat the
entire process in each assignment. If instructions change, qualify their actual
behavior with the smallest existing paired cases that cover the change before
using them for new work. Do not turn this documentation review into global skill
discovery, automatic skill installation or a new skill/plugin framework.

### Carry research into the owning behavior

The [source-control assessment](codex://threads/01a13967-44ab-71f1-b689-ee91f0ea967d)
and [software-factory assessment](codex://threads/01a11fb1-f1c6-7a52-b8a2-f35fe00ece4f)
were reread for this pass. Their source observations remain distinct from their
assistant recommendations and from Runner's qualification results. The existing
[lessons table](#lessons-and-their-disposition) remains the source mapping.

Apply the software-factory onboarding and conversational-operation lessons to
repository readiness and the operator interface: one recommended journey,
visible waits, concrete next actions, and recovery that says what is retained
and repeated. Keep inspection read-only. Its [setup][factory-setup] and
[dispatch skill][factory-dispatch-skill] are references, not imported workflow.

Apply [Anthropic's lessons][anthropic] to complete-journey measurement, cheap
observation and concrete review findings. Measure time to useful completion,
integration waits, manual interventions and repeated verification with its
cause. Keep local Git and required pre-integration proof; their scale and
post-merge emphasis do not establish Runner's needs.

For each later change, state the user need, source or observed incident, affected
contract and smallest sufficient proof in its existing brief. Preserve the
testing policy's ownership: repository policy defines quality; author/planner
defines the verification brief; implementer extends or consolidates tests;
reviewer checks adequacy and unnecessary complexity. A repeated model claim is
not a new rule, a successful check is not proof of every claim, and a useful
lesson does not require importing the mechanism that originally expressed it.

## Original constraints to replace (historical assessment)

The [engine](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/internal/engine/engine.go) owns a concrete `github.Project`;
work items, authorization and several process locks also belong to the GitHub
package. Each observation cycle loads Project lifecycle items. The
[configuration validator](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/internal/config/project.go) requires a Project and
its lifecycle fields, while repository and merge settings sit under
`github_project`. This is deeper than an interchangeable board client.

The current design has useful protections for exact-content approval, complete
plan release, retained workspaces and interrupted publication. Some recovery
complexity compensates for multi-step remote writes. Local transactions can
remove those particular partial-write states. They cannot make Git operations,
model calls and remote publication atomic with a database transaction.

Configuration already supports role profiles, reasoning, an implementer ladder,
parallelism and budgets. However, QA limits live in workflow actions, the ladder
advances from Project QA failures, and provider retries use a fixed schedule
with in-memory state. [Roles](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/internal/config/roles.go),
[workflow](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/internal/config/workflow.go), [automatic retries](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/internal/engine/automatic_retry.go).
The redesign should expose their meaning coherently and preserve limits across
restarts.

## The complete user journey

### Set up and prove readiness

Start with a local repository and one supported, authenticated harness. Guided
setup selects the repository and base, role profiles, parallelism, recovery
limits and delivery policy. It previews the resolved configuration once, then
applies it. External boards are optional subsequent connections.

Readiness has three separate results:

- Runner can invoke its configured profiles and protect the intended boundaries.
- The repository has known verification commands and the tools, services and
  inputs needed for the requested work.
- Each configured integration is available, unavailable or not configured.

Use existing repository commands and instructions. Missing requirements should
produce a concrete action; setup must not invent production credentials or
silently change the repository's quality requirements. Static inspection remains
non-billable. Live harness checks and repository commands have explicit scope.
One unavailable optional board does not make the whole setup unusable.

The guide ends with a small authorized change, its review evidence and, when
delivery is connected, its PR. It should not end at a successful configuration check.

### Repository quality agreement

Setup records the repository owner's definition of done and the instructions
and commands that establish it. Reuse existing repository policy; do not create
mandatory duplicate `DONE.md`, constraints files or a new testing framework.
Keep a versioned resolved agreement in private Runner state with references to
its authoritative repository sources. A task records the agreement revision
used for its verification brief.

For each required check, record its command, working directory, prerequisites
and permitted environment, plus the observed baseline revision and result.
Distinguish a passing check, a specific pre-existing failure and a check that
could not run. A missing tool or credential is a prerequisite gap, not a pass.
Static setup does not run arbitrary repository commands; preview the commands
and obtain authorization as part of setup before running the baseline.

The default is that required checks must pass. The owner may explicitly accept
a bounded, identified baseline exception with its scope and rationale. It cannot
cover a new or changed failure, excuse missing proof of the requested outcome,
or permit editing assertions until a gate turns green. Agents cannot grant or
broaden exceptions. Setup reports unresolved required gaps as blockers for work
that needs them, while unrelated inspection remains available.

Changes to authoritative commands, requirements or exceptions require an explicit
owner-applied agreement revision. Merely observing edited policy files cannot
relax a running task's contract. New tasks use the applied revision; existing
tasks keep their pinned agreement unless the owner explicitly selects them for
adoption. Adoption creates a new unapproved task version and revokes further
old-version actions. A weaker agreement cannot retroactively authorize a pass.
Test consolidation remains permitted when the same required guarantee is still
established.

### Submit and clarify work

Keep the two entry points:

- **Plan:** “I know the outcome; help me define the work.”
- **Ready:** “The task is sufficiently specified; implement it.”

A local Ready command is explicit authorization of the displayed task version.
Imported requests are untrusted input until an operator or configured trust
policy authorizes them. An external label or column name alone is not authority.
Retain the existing trusted issue-intake policy as an optional GitHub intake
capability; do not generalize private-repository trust to every external service.

The planner remains the ambiguity and decomposition boundary. It proposes
cohesive cards with acceptance criteria, proof obligations, non-goals and real
dependencies. No target number of cards, fixed file count or separate triage
agent is required. Open decisions remain questions on the request and prevent
release of an executable plan. Distinguish planning immediately from submitting
a planning request to the running coordinator.

### Approve the exact work

The preview leads with the intended outcome, proposed cards and dependencies,
material assumptions, expected verification and what approval permits. Full
task content remains inspectable. A complete batch is released atomically in
local state after its displayed version is revalidated.

Changing execution-defining requirements or dependencies invalidates the
affected authority and evidence. Presentation changes do not. A changed task
version must be approved before new execution; it cannot silently alter an
already running assignment. Revocation prevents subsequent privileged actions
and requests cancellation of affected active work while preserving its artifacts.
External completion already in progress is reconciled rather than assumed undone.

### Admit and execute safe actions

Keep one coordinator that observes state, derives safe actions and admits as
many as configuration allows. A poll is an observation boundary, not a batch.
Dependency outcomes and resource ownership determine safety; capacity and
budgets determine admission. Deterministic reconciliation does not consume
agent capacity or wait for unrelated agents.

Status and dispatch use the same eligibility calculation. A waiting task names
the dependency, occupied resource, capacity limit, budget, provider or decision
that prevents it from starting. Reserve capacity and record an attempt before
dispatch. Multiple CLI operations and the service share these bounds.

Implementation uses an isolated workspace and the exact authorized task.
Preserve useful work, feedback and valid saved results on recovery. Reports
identify commands, settings, outcomes and material limits; a model's claim that
a check passed is not independent proof that it did. The user sees the actual
role, model, reasoning, current stage, elapsed time and remaining recovery allowance.

### Review with sufficient evidence

Keep the independent reviewer and its existing evidence audit followed by
focused dynamic verification when necessary. Assess the approved outcome and
affected contracts as well as listed proof obligations. Review findings must
describe a concrete failure, its evidence and the relevant requirement.
Hypothetical improvements do not automatically become rejection criteria.

Distinguish accepted work, confirmed defects, a missing human decision and
incomplete evidence. A decision request takes priority over automatic repair.
An unavailable test environment is not a code defect. Preserve valid prior
findings and evidence; rerun checks for concrete gaps or invalidating changes.
Candidate bindings establish provenance, not the truth of every reported result.

Changing a base or candidate requires reassessing affected proof. Reuse exact
accepted results for interrupted deterministic publication only under the
existing integrity conditions. A missing required check cannot become a pass
because a review budget is exhausted.

### Publish and integrate

Store the reviewed candidate and evidence before remote publication. Publication
and integration use the code-host adapter, independently of any board adapter.
After a timeout or restart, inspect remote identity and outcome before repeating
a write. Recover an existing PR instead of creating a duplicate.

Serialize integration per repository and base. Refresh against the latest base,
resolve conflicts through bounded repair, and obtain the required verification
and review before merge. Preserve protection against base movement between
verification and the merge request. Human merge is the default; automatic merge
requires explicit policy and all the same correctness conditions.

Publication, CI, human review and integration waits remain distinguishable.
Missing remote observations never establish success. Closed-without-merge PRs
do not satisfy dependencies. A reviewed local candidate awaiting delivery is
useful progress, but it is not a merged implementation.

### Finish, stop and recover

A code task succeeds when its intended integration is confirmed. A planning
operation can finish while the original request remains incomplete; the request
finishes only after all required children succeed. Board updates, issue closure
and other housekeeping can retry independently of that recorded outcome.

Keep graceful drain: stop new admission and let admitted actions finish.
Immediate cancellation is a separate explicit operation. Restart reconciles
unfinished attempts and retained artifacts before admitting conflicting work;
it does not blindly rerun every stage marked active. Loss of the coordinator's
ownership must not allow a replacement to race surviving harness processes.

Every actionable failure presents its cause, preserved work, required action,
resume stage, expected repeated work and private evidence location. Recovery
does not require manually arranging board columns. The recommended action is
specific; alternatives explain their different effects.

## State and progress that users can understand

Separate the task's lifecycle stage from its waiting reason and current attempt.
“Waiting for provider” is not another workflow lane. “Needs your decision” is
different from “retry limit reached.” Board column names are display mappings.
The default display vocabulary is:

| Stage | Meaning |
| --- | --- |
| Draft | A request is captured but its planning or implementation is not yet authorized. |
| Planning | Runner is clarifying or decomposing the request. |
| Awaiting approval | A complete proposal needs authorization. |
| Ready | Work is authorized; a stated dependency or admission limit may still prevent starting. |
| Implementing | An implementation or repair attempt is active. |
| Reviewing | Independent assessment or required verification is active. |
| Awaiting delivery | A reviewed candidate needs publication, checks, human review or integration. |
| Completed | The intended successful outcome is confirmed. |
| Stopped | Work was cancelled or abandoned; this does not satisfy dependencies. |

Attention and wait reasons decorate those stages instead of creating a second
workflow graph. For example, an interrupted review remains Reviewing with a
specific recovery reason. Later user-authored workflows may name additional
states, but must provide the same stage, wait and next-action explanations.

The local status view leads with decisions and recovery actions, then active
work, ordinary waits and recent completions. Inspection is read-only. A
continuous display refreshes observations without granting execution authority.
Machine-readable status supports the operator skill and any future local UI.
Remote views show their last successful synchronization time and stale state.

Illustrative output, not an implemented CLI contract:

```text
Needs you
  R-42  Reviewing — browser capability unavailable
        Candidate and implementation evidence retained; no code rejection.
        Next: repair the browser capability, then resume review.

Working
  R-43  Implementing — repair 1 of 2
        Profile: implementer-careful; reasoning: high
        Reason: confirmed review defect; next: independent review

Waiting
  R-44  Ready — dependency R-43 has not merged
  R-45  Awaiting delivery — GitHub unavailable; reviewed candidate retained

Integrations
  GitHub Projects — stale; local work continues
```

The operator skill should answer “what needs me?”, explain a wait, preview a
policy change and perform an authorized recovery through these same commands.
It adds no alternative approval rules, direct database editing or scheduler.

## Core contracts

These are semantic contracts, not a requirement for one table or interface per
row. The engine decides transitions; the store atomically checks their expected
revisions and writes the resulting state. No transaction spans a model call,
Git operation or network request.

| Record | Required identity and meaning |
| --- | --- |
| Repository | Runner-owned identity bound to the validated local Git repository and configured base ref. A display path or remote board ID is not identity. |
| Task version | Stable local task ID plus monotonically increasing version and digest of execution-defining content: outcome, scope, constraints, acceptance/proof brief, dependencies, repository/base policy and quality-agreement revision. Store immutable versions; changing the title alone need not invalidate authority unless it changes the task's meaning. |
| Approval | Exact task ID/version/digest, authorization scope and recorded operator or explicit trusted-intake decision. Approval and revocation have a generation checked by every privileged action. A digest identifies content; possessing it is not authorization. |
| Attempt | Unique ID, task/version, approval generation, stage, pinned policy/profile, resolved base, owned resources, reserved allowances and invocation/result state. Preserve prior attempts; a retry is not an overwrite. |
| Candidate and review | Repository, task/version, base commit, candidate commit/tree, workspace identity, quality revision and evidence references. Review acceptance is valid only for this binding, after Runner validates the structured result and integrity checks. |
| External effect | Stable operation/object identity, authorized intent and observed outcome. Written before publication; ambiguous outcomes are reconciled, never assumed absent. This is needed from phase 3, not an empty framework in phase 1. |

Approval and admission use compare-and-set checks in short transactions. Repeating
approval of the same still-current version returns the existing approval; it does
not reset attempts or create another execution. A stale preview is rejected.
Requirements edits create an unapproved version and revoke future actions under
the old one. Display-only edits must not change the version's delegated content.

At admission, recheck authority, required dependencies, readiness, capacity and
resource ownership, then reserve the attempt and its allowances together. At
result commit, recheck the attempt, approval generation and candidate binding.
Results arriving after revocation are retained as historical evidence but cannot
advance work. A single task may have only one unfinished execution owner; a
resource cannot be released merely because a wall-clock lease expired.

| Transition | Guard and committed effect |
| --- | --- |
| Draft to Ready | Approve the exact complete task version. No harness is launched by approval itself. |
| Ready to Implementing | Reserve one eligible implementation attempt and its resources before launch. |
| Implementing to Reviewing | Validate and durably record the completed result, construct/validate its candidate, then make independent review eligible. A model's success string is insufficient. |
| Reviewing to Awaiting delivery | Required proof is sufficient, independent review accepts, and candidate/authority checks still match. No dependency is satisfied yet. |
| Reviewing to repair | A confirmed defect and remaining repair allowance permit another implementation attempt. Missing proof or a decision stays Reviewing with its own attention reason. |
| Awaiting delivery to Completed | Confirm the configured delivery outcome: a verified local result receipt, an authorized local base fast-forward, or a confirmed GitHub merge. PR creation alone cannot complete connected delivery. |
| Any nonterminal stage to Stopped | Record revocation, prevent new actions, request cancellation and retain artifacts. Show cancellation pending until owned processes are known to have stopped. |

Plan adds a planning operation and an awaiting-approval proposal in phase 2.
Release all approved children and dependency edges in one transaction; reject
cycles, missing targets and stale proposal content. Planning success alone does
not complete the request. Failed, stopped or closed-unmerged dependencies do not
count as success.

Stage, waiting reason and attempt state remain separate. Recovery clears a
specific wait only after its prerequisite is satisfied; it cannot manufacture
approval, a passing review or unused allowance. Configured repository/base
identity changes require explicit setup and new task authority, not a hot switch
for retained work.

## Configuration and bounded recovery

### One visible execution policy

Keep named role profiles for harness, model, reasoning, timeout and supported
capabilities. An escalation step references a complete implementer profile; it
can change the model, reasoning, or both. Runner never infers capability or
price order from a model name. Offer supported choices for the installed harness
and reject unsupported combinations instead of silently substituting them.

The default workflow should be generated from one documented policy. Later
user-authored typed workflows must compile into the same coordinator and obey
the same limits. A setting must have one owner: do not retain competing
QA limits in workflow actions and recovery settings.

These are the first-release controls and pilot defaults. The first slice supports
the smaller explicit subset below; unsupported settings must fail validation.
Tune later revisions against observed rework, waiting and operator intervention:

| Control | Pilot default and meaning |
| --- | --- |
| Planner, implementer and reviewer profiles | Explicit profiles selected during setup; no new universal model default. Show resolved model, reasoning, timeout and access. |
| Implementer escalation | Disabled initially. An ordered list of approved profiles advances after a confirmed reviewer rejection, before the next permitted repair. |
| Automatic repair limit | Two additional implementation passes per approved task version. Includes QA repair, candidate correction, CI rework and conflict repair. Initial implementation is not a repair. |
| Transient retry limit | Three additional attempts for a recognized transient failure in the same stage recovery episode: 30 seconds before retry 1, two minutes before retry 2, then five minutes before each later retry up to the configured limit. Zero disables automatic retry. |
| Parallelism | One concurrent harness invocation initially; configurable from 1 to 16 in phase 2. Includes planning, implementation and review across service and CLI. |
| Role timeouts | Explicit per-profile values; use current generated values as initial tuning references and record setup choices. |
| Admission budget | Optional harness-invocation ceiling per approved task version; absent means no such ceiling. Reserve before launch and count planning preparation, audit, focused review, retries and repairs. A definitely unlaunched reservation may be released; uncertain launch counts until reconciled. Report time/tokens/cost where available; their admission ceilings are deferred. |
| Delivery | Human merge by default. Auto-merge is an explicit setting independent of board configuration. |
| Base freshness | Refresh before implementation by default. Optional use of an available recorded base during a code-host outage; latest-base verification remains mandatory for integration. |
| External views | None required. Each connection has its own destination and publication scope. |

Two repair passes preserve the default allowance of initial implementation plus
two QA-driven corrections, but deliberately also bound automatic paths that
currently do not count as QA rejections. Keep QA rejection counts as quality
evidence and the escalation trigger; stop using that count as the only general
repair bound. A candidate-correction call consumes a repair even when made inside
the same engine action. This prevents hidden extra model work.

A transient retry of a repair resumes that reserved repair pass; it does not
consume another repair allowance. All harness invocations still count toward
admission budgets and usage. Both repair and transient counters are reserved
before dispatch, so interruption cannot manufacture an extra attempt. A repair
limit of zero permits initial implementation and review but no automatic repair.

Example: profiles `implementer` and `implementer-careful`, with two repairs,
start on `implementer`. A confirmed rejected review permits repair 1 on
`implementer-careful`. A second rejection permits repair 2 on that final profile.
Another rejection stops for an operator decision. A provider outage repeats the
same stage/profile within its transient allowance and does not advance the ladder.
CI rework and conflicts consume repairs but do not imply a stronger model is needed.
Planner-selected starting profiles must come from the configured allowlist; show
the resulting reachable escalation sequence before release.

### Failure and retry rules

| Event | Automatic behavior | What the user sees |
| --- | --- | --- |
| Recognized transient provider or browser-startup failure | Retry the same stage/profile within its transient limit. | Cause, attempt count, next retry time and remaining allowance. |
| Confirmed reviewer defect | Repair within the task's repair allowance; advance the configured ladder. | Findings, selected next profile and repair count. |
| Candidate correction, terminal CI failure or integration conflict | Bounded repair with retained context; no automatic model escalation. | Concrete reason, retained branch/candidate and remaining repairs. |
| Required evidence unavailable, missing decision, invalid configuration or unknown harness failure | Stop for the specific operator action. | Exact missing input or capability and the stage that can resume. |
| Authority or integrity mismatch | Prevent further privileged work and retain evidence. | The changed binding and explicit recovery requirements. |
| Interrupted deterministic post-processing | Reconcile the existing effect; reuse an exact valid saved result. | Which operation resumes and whether any new harness call is needed. |
| Optional board unavailable | Retain pending synchronization and continue unrelated work. | Integration health and synchronization age, separate from task failure. |
| Admission limit reached | Wait without cancelling admitted work or weakening QA. | The limiting capacity or budget and when it can be reevaluated. |

Persist counters, reasons and retry deadlines. Restart and ordinary inspection
do not reset them. A successful stage ends its transient recovery episode;
changing error text does not. An explicit manual retry does not silently grant
a fresh repair budget. Additional allowance requires a visible operator choice
and records its scope. Changed requirements create a newly approved task version
while preserving prior attempts and outcomes.

Clean base refreshes can still cause repeated review without implementation.
Bound automatic refresh-and-review cycles separately, initially to two per
approved task version, then request operator intervention. Ordinary initial QA
does not consume this allowance; each base movement that requires another review
does. Returning through repair or restarting does not reset it. This advanced
integration limit prevents endless verification when the base keeps moving. Existing
bounded transport retries remain internal adapter behavior, with their total
calls visible; they must not multiply into independent unbounded retry loops.

### Make policy changes predictable

Provide an interactive editor, a non-interactive equivalent, validation and a
resolved-policy explanation. The preview says, for example, “two repairs remain;
the next confirmed rejection selects this model and reasoning.” Include warnings
for unreachable escalation steps and budgets that cannot admit the selected work.
Routine changes should not require editing nested workflow JSON.

Validate and atomically apply the whole change. Pin the effective configuration
revision to every attempt. Model, reasoning and recovery-policy edits affect the
next admitted attempt after operator application; active attempts retain their
recorded configuration. A pending transient retry retains its recorded profile
unless the change preview explicitly selects it for a profile change. Counters
carry forward. Lowering parallelism stops new
admission until active work fits; it does not cancel active agents. Lowering a
limit below consumed allowance stops the next automatic action.

Permission expansion, changed delivery authority and changed task scope require
their own explicit authorization. Escalation alone must not widen filesystem,
network or tool access. Switching reviewer profiles does not carry acceptance
across a changed candidate or base. Keep a compact history of applied policy
revisions so recovery and metrics can explain which settings governed each action.
The phase 3 [merge policy](#using-automatic-merge-policy) is live permission:
disabling it prevents unsent automatic merges, including active admission.

### First slice configuration contract

Keep JSON and strict decoding. This is the minimal new configuration shape;
`SELECT_SUPPORTED_MODEL` and absolute paths are setup inputs, not working
defaults. The format marker deliberately distinguishes it from legacy v5.
There is no Project, workflow-lane or credential field.

```json
{
  "format": "runner-local-v1",
  "repository": {
    "path": "/absolute/repository",
    "base_ref": "refs/heads/main",
    "base_freshness": "recorded"
  },
  "state_dir": "/absolute/private-runner-state",
  "worktree_dir": "/absolute/runner-worktrees",
  "profiles": {
    "build": {
      "harness": "codex",
      "model": "SELECT_SUPPORTED_MODEL",
      "reasoning": "high",
      "timeout": "20m"
    },
    "review": {
      "harness": "codex",
      "model": "SELECT_SUPPORTED_MODEL",
      "reasoning": "high",
      "timeout": "20m"
    }
  },
  "roles": {"implementer": "build", "reviewer": "review"},
  "policy": {
    "max_parallelism": 1,
    "max_repairs": 0,
    "max_transient_retries": 0,
    "max_base_refreshes": 2,
    "implementation_ladder": []
  },
  "delivery": {"mode": "local"}
}
```

Validate unknown keys, missing role/profile references, unsupported harness or
model/reasoning combinations, nonpositive timeouts, unresolvable base refs and
unsafe/overlapping paths. State must be outside the repository and workspaces;
use canonical paths without symlink components and preserve configuration
provenance protections.
Profiles do not grant access: the native sandbox and role permission ceilings
remain Runner-owned. The two profiles may use the same model; review still uses
a separate invocation and context.

Phase 1 accepted local delivery and recorded base mode, with all automatic
allowances set to zero. The example now includes the phase 3 refresh allowance. At approval, pin an available local base commit; that milestone never
fetches, pushes or merges. A later base movement does not alter that candidate's
recorded starting base or turn local review into current-base delivery proof.

Phase 2 enables the documented limits, a planner profile and
`max_invocations_per_task` (absent or positive integer). The implementation ladder
lists successor profile names after the configured starting implementer; empty
disables escalation. Exhausting the list keeps the last profile while repairs
remain. The same profile cannot recur in the ladder. Phase 3 adds GitHub delivery
configuration and, when its full delivery journey lands, requires refresh before
implementation by default; `recorded` remains explicit opt-in. The first two
phase 3 bundles retain explicit recorded starting-base mode and verify after
draft publication. All integration must verify the latest base.

Setup previews a complete resolved policy, then atomically applies it with a
revision. Phase 1 requires a drained coordinator before replacement of that
policy; phase 2 adds the live-change semantics above. No unsupported setting is
silently ignored. Model IDs and reasoning choices are validated against the
installed harness during implementation, not frozen to article examples.

## Architecture and ownership

```mermaid
flowchart LR
    U[CLI and operator skill] --> C[Runner commands and coordinator]
    I[Optional intake adapters] --> C
    C --> S[(Private local state)]
    C --> H[Harnesses and Git workspaces]
    C --> D[Code delivery adapter]
    C --> V[Optional board projections]
```

### Package boundaries

Start with these responsibilities. Package names are illustrative; create a
separate package only where it owns a cohesive contract and has a clear reason
to change. Avoid generic `manager`, `service`, `common` or `utils` layers that
only forward calls.

| Boundary | Owns | Must not own |
| --- | --- | --- |
| `cmd` | CLI parsing, composition and presentation of application results. | Scheduling rules, provider-specific workflow or direct state edits. |
| `config` | Configuration decoding, validation and resolved execution policy. | Provider calls or duplicate workflow decisions. |
| `core` | Task/version identities, approvals, dependencies, outcomes and small invariant functions. | I/O, provider fields, database rows or a generic plugin framework. |
| `engine` | Application commands, transitions, admission, recovery and the one coordinator. Consumer-defined interfaces for required effects. | GitHub/Trello transport, SQL or harness command syntax. |
| `store` | Transactions, durable reservations, queries, history and local recovery records. | Deciding whether work is authorized or which action should run next. |
| `execution` | Role assignments, native harness adapters, structured results and invocation boundaries. | Work queue ownership, approval or merge policy. |
| `workspace` | Git workspaces, candidate construction and validated artifact handling. | Task lifecycle or external board state. |
| Integration adapters | Provider-specific intake, view or code-delivery operations. | Core scheduling, approvals or alternate completion semantics. |

The composition root wires concrete dependencies. Core types do not import the
engine or adapters; the engine depends on narrow contracts rather than concrete
integrations. Adapters can share core value types without importing each other.
Keep interfaces at actual substitution boundaries, defined by their consumer;
do not create an interface for every struct or a generic repository API that
leaks storage operations into workflow code. Telemetry, process execution and
filesystem protection remain focused support responsibilities where needed.

Design a board-view contract around publishing Runner progress. GitHub Projects
and Trello each translate that contract into their own representation. Separate
intake and code delivery are additional capabilities, not mandatory methods on
one universal plugin interface. Built-in adapters are enough to establish
interchangeability. A public plugin loader, SDK and permission system require
their own demonstrated need.

The practical architecture check is whether a core eligibility or retry test
can run without constructing a GitHub payload, launching Git or pretending to
be a harness process. Conversely, provider transport and workspace tests should
exercise their actual boundaries. If a small rule needs a simulation of the
whole application, revisit the production boundary before adding more helpers.

### Local authority

Use one transactional local SQLite store. Qualify the selected driver against
the existing no-CGO packaging targets in the first slice. Store task versions,
dependencies, approvals, plan releases, attempt reservations, recovery counters, integration
ownership and external-object mappings there. Keep evidence files and Git objects
outside the database when appropriate, with validated references and identities.

The store is private Runner state outside agent-writable workspaces. Editable
requirements and exported plans are inputs; importing them does not import
execution authority. Existing sandbox and trusted-host limitations still apply:
local file permissions do not contain deliberately unrestricted host execution.

Use the existing single-coordinator ownership model and short transactions for
CLI mutations and admission. Do not add a second scheduler, distributed queue or
full event-sourcing platform. Minimal transition history belongs alongside the
canonical state; telemetry remains diagnostic and is not reconstructed as authority.
Budget admission must continue to account for durable unfinished reservations.

Transactions cover local changes only. Record the stable identity and intent of
an external effect before issuing it, then reconcile the result. Where a provider
cannot establish whether creation succeeded, stop ambiguous replay rather than
promise exactly-once behavior. Missing or corrupt authoritative state stops new
work; a remote board cannot silently recreate approvals.

Provide consistent backup/export and tested restore. Restoring onto a host
requires exclusive ownership, reconciliation of external effects and validation
of retained workspace identities before execution. A copied database is not
permission to run a second coordinator. Several independently active Runner
hosts sharing work are outside the first release.

### Integration boundaries

Define work identity, task versions, authority, outcomes and resource keys in
the new provider-neutral core contract. The engine owns
process decisions; the store owns durable changes; GitHub transport owns API
requests. Repository/base/merge policy is independent of Project configuration.
Use the current execution, workspace and subprocess code as reference for
necessary guarantees; assess any reuse against the new boundaries.

Define integrations by the capability they actually supply:

- **Intake:** discover a request, provide its source identity and observed version,
  and return acknowledgement or questions. Core policy decides authorization.
- **View:** publish a bounded task summary, progress, attention reason and links.
  It has no permission to approve work or set successful outcomes.
- **Code delivery:** publish/inspect a candidate and PR, observe checks and merges,
  and perform an authorized merge under the core integration contract.

Start with built-in adapters. Each declares required credentials, operations,
stable identity, retry behavior and supported capabilities. A Trello view does
not implement GitHub merge methods. No adapter receives arbitrary authority to
change workflow rules or private approval records.

Initial views are one-way projections. Reconcile the newest committed task
version using durable external mappings; coalesce superseded presentation
updates. Prevent out-of-order writes from reverting a newer projection. Retry
only affected connections. Do not publish raw transcripts, private diagnostics,
credentials or unrestricted evidence merely because a board is connected.

Later editable integrations may submit versioned commands through the core.
Runner validates source authorization, rejects stale/conflicting edits and
acknowledges acceptance. An offline board edit is pending, including a requested
stop; it cannot promise immediate cancellation. Do not add bidirectional field
merging or infer completion from a moved card.

Externally loaded plugins, arbitrary scripts, workflow expressions, additional
role contracts and a full web dashboard remain deferred. A second actual
provider should validate the adapter boundary before a public plugin API is frozen.

## Outages and recovery boundaries

| Unavailable component | Work that can continue | Work that waits |
| --- | --- | --- |
| GitHub Projects or another board | Local intake, planning, implementation, review and available code delivery. | That board's synchronization and incoming requests. |
| GitHub code hosting | Local status and work with available repository objects under the selected base-freshness policy. | Remote fetches, publication, hosted checks and confirmed integration. |
| A harness provider | Other admissible profiles and deterministic reconciliation. | Affected model calls within the configured retry policy; no implicit cross-provider fallback. |
| Repository test service | Unrelated tasks and evidence checks that do not need it. | Acceptance requiring that service's proof. |
| Authoritative local store | Diagnostic access and preservation of retained artifacts where possible. | New claims, approval changes and privileged publication until state is safely restored. |

Removing Projects alone does not create code-host independence. The current
implementation [fetches the base before implementation](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/internal/engine/engine.go).
The new core must make that dependency explicit and exercise the optional
recorded-base policy. Missing local objects remain a real blocker. A locally
reviewed candidate must refresh and regain required evidence before remote
integration; offline progress never means offline confirmation of a merge.

## Construction and release plan

Implement these slices only after an explicit instruction to begin implementation.
Build the new core directly; the existing application stays available as reference
and as the last supported release until replacement. There is no requirement to
refactor it into a transitional backend. Keep the new code isolated during work
and preserve unrelated local changes.

| Phase and dependency | Concrete scope | Completion evidence |
| --- | --- | --- |
| 0. Record the process and proof before construction | Accepted decision, release scope, contracts, quality agreement and first-slice brief. Extract essential invariants from current contracts and incidents. | This plan and ADR 0005 establish the build direction. Detail later slice briefs here before their implementation, using what preceding slices establish. |
| 1. Build one complete local journey after 0 | New core identities, private store, commands, coordinator and one harness path. Support local Ready intake, exact authorization, isolated implementation, independent review, retained results and local status. | One reviewed candidate without Projects; small core tests and actual store/workspace boundary tests establish the required safety. A controlled restart preserves authority and work. |
| 2. Complete the core process after 1 | Add Plan and atomic release, dependencies, bounded repairs/retries, configured escalation and reasoning, capacity, invocation budgets, configuration editing, full quality-agreement changes, attention-first progress and stage-specific recovery. | Both entry paths reach reviewed candidates for eligible work; dependencies wait honestly for integration. Core fixtures prove success/release rules; phase 3 proves the merged dependency journey. Policy previews, restart limits and test reuse hold without a board. |
| 3. Connect delivery after 2 | GitHub PR/check/merge adapter, serialized current-base integration, bounded refresh and ambiguous-write recovery. | A complete authorized change reaches confirmed merge. Outages, base movement, conflicts, closed PRs and interrupted publication have correct bounded outcomes. |
| 4. Qualify and release after 3 | Run selected live and operator journeys, backup/restore and package/platform checks. Use fresh setup for the breaking cutover; the frozen checklist excludes an importer. | Core reliability and usability gates pass. A selected installation can switch safely without dual ownership; difficult historical-state conversion is not a release prerequisite. |
| 5. Add optional board views after 4 | GitHub Projects projection, then Trello against the same capability contract. Include synchronization health, stable mapping and bounded publication. | Disconnect/reconnect and reordered updates do not affect work authority. The second provider needs no changes to core scheduling or approval. |

Phases 1 and 2 must produce useful complete journeys, not a collection of
unconnected packages. Configuration and user-facing progress are core work.
Do not postpone test design until a final testing phase. Optional integrations
must wait when core scenarios, test quality or the operator journey remain weak.

### Local completion and combined-plan verification brief

**Status: implemented and qualified on 2026-10-16 within the boundary below.**
This brief follows the owner's local-core clarification and decision that local
delivery and permission to merge should be configurable. It supersedes the
earlier terminal behavior of local mode and child-count-only Plan completion
in this implementation. R1–R5 remain evidence for their earlier, bounded behavior.

#### Outcome and boundary

A user can finish a Ready task or a dependent Plan using local Git, the configured
harness and the repository's verification environment, with no GitHub account,
remote, `gh`, issue tracker or board. Completion identifies the actual reviewed
result, its evidence and its chosen destination. A Plan must establish the original
combined outcome, not merely report that its children passed.

Use the existing coordinator, private store, workspaces, three role contracts,
quality agreement and recovery policy. A single task and a Plan both produce one
final candidate; the selected delivery capability determines how it completes.
Coding agents never gain permission to commit, merge, publish or edit authority.

The implementation boundary includes the necessary existing GitHub consumer
adjustment: a released Plan delivers its final parent candidate through the same
draft/base/CI/merge path as Ready, rather than publishing children. Keep one Plan
process across destinations. Reuse GitHub's existing delivery contract without
adding polling, board views, new harnesses, installed skills, host access or a
plugin framework.
Pilot toolchain/loopback fixes remain a separate brief; missing capabilities
continue to block required proof rather than weaken it.

#### Local destination and merge permission

Keep `delivery.mode=local`. Add one optional `delivery.local_target`, default
`result`; `base` selects the existing `repository.base_ref` as the destination.
Do not introduce a second independently configurable base branch. Reuse
`policy.merge=human|automatic`, with `human` the default.

| Configuration | Successful result and authorization |
| --- | --- |
| Local target `result` | Create a dedicated `runner/result/<top-level-task-id>-v<version>` branch at the fully accepted candidate. Its verified creation plus the local receipt completes the task/Plan. Approval/release authorizes this result; there is no further merge gate. The operator's checkout and configured base stay untouched. |
| Local target `base`, merge `human` | Retain the accepted result branch, then wait in Awaiting delivery. `task merge ID` explicitly requests one guarded local integration into the configured branch. Completion requires confirmation of that exact integration. |
| Local target `base`, merge `automatic` | After acceptance, the coordinator admits the same guarded integration under the currently applied policy. No extra approval is required per task, but task/release authority and every verification requirement still apply. |
| GitHub delivery | Keep the existing host protection, CI and ordinary-merge receipt requirements. Local acceptance alone does not complete a task whose approved destination is GitHub. `local_target` is invalid for this mode. |

Reject `automatic` with local target `result`: that target does not merge a base.
Show the distinction in setup, approval/release previews and `config explain`.
Destination is part of fixed repository/delivery authority, selected at setup;
changing it requires fresh setup and new work approval, not reinterpretation of
in-flight work. Merge permission remains editable through the existing policy
preview/apply path. Enabling automatic merging names the branch and explains its
effect on existing approved work; disabling it fences unsent automatic effects.
An explicit `task merge` request remains separate authority, as it does today.

Local `base` integration advances the branch to the exact accepted candidate by
fast-forward. The candidate must contain the freshly observed target base; clean
refresh and bounded conflict repair prepare it when necessary. No force update,
squash or unrelated history rewrite is permitted. To keep checkout handling
small and safe, the initial contract supports only targets not checked out in
any worktree, even if clean. Setup reports that prerequisite; dispatch checks it
again. Operators must not concurrently check out or administratively alter the
reserved target. Expected-old-ref checks protect concurrent ref changes. Runner
never resets, stashes or switches an operator's files to make a merge possible.
Result mode remains usable while the source checkout is dirty or on the base;
only committed, approved source is input.

For a human merge, prefer the same Runner command and guarded path. Explicit
`task integrate ID` may confirm a manual fast-forward only when the target tip
is exactly the accepted candidate, all local proof/authority still matches, and
the candidate contains the last verified base. Other unrecorded merge shapes or
later descendants require attention; do not infer success from ancestry alone.
Reconciliation of Runner's own recorded integration can recognize its exact
commit in later target ancestry without repeating the write.

#### Ready and Plan process

Ready retains its current approval, optional verification preparation,
implementation and independent review. After acceptance, Runner automatically
observes the current **local** configured base and completes required refresh,
checks and review before creating the result branch. It then completes in
`result` mode or follows the selected merge policy in `base` mode. No GitHub
adapter is constructed or called on this path.

Plan adds one shared Outcome/Scope/Acceptance/Verification body to the existing
proposal, alongside its cards and dependency graph. Preserve the original request
as provenance. The shared body states the requested overall result, constraints,
cross-card behavior and final evidence boundary, grounded in inspected tests.
It cannot silently omit requested behavior; material scope choices appear in
the release preview for explicit approval.
The release preview binds this body, exact children/versions, quality revision,
destination and authority generation. Missing outcome proof or consequential
questions prevent release. No separate final-QA card, manifest service or new
approval stage is needed.

The released Plan then follows these steps:

1. Create a Runner-owned private integration ref/workspace from the approved
   base. Each child still has its own worktree. Independent children may run
   concurrently; a child starts from an observed plan head containing all its
   exact prerequisite results, not the original base copied to every child.
2. Independently review each child against its scoped brief and shared context.
   Serialize inclusion into the plan head. If that head moved, refresh against
   it and regain affected checks/review through the existing bounded mechanism.
   Record inclusion only for the accepted resulting commit/tree and exact release.
   Same-file work alone is not a scheduling dependency.
3. A durable inclusion receipt can release a dependency **inside that Plan**.
   It is not final delivery and cannot satisfy an external dependency. Show an
   included child as Awaiting delivery, “included in Plan; final verification
   pending”; do not mark it Completed or publish a child PR/result branch.
4. Once all exact released members are included, freeze that combined candidate,
   refresh it against the current destination base and run final verification.
   The existing reviewer assesses the shared outcome, cross-card interactions
   and required child guarantees on this combined source. A one-child Plan still
   receives this outcome check; Ready does not gain a redundant parent review.
5. Deliver the accepted parent candidate to its configured destination. Commit
   parent completion and children’s final completion together, retaining their
   original inclusion/evidence history. Each child's completion references its
   inclusion and the parent receipt; do not fabricate individual deliveries.
   No child count, planner success string
   or individual acceptance record can replace the final parent receipt.

Private inclusion is authorized by Plan release and does not wait for the
external merge policy. Only final integration into the configured branch/host
uses human or automatic merge permission. Plan internals remain private until
the accepted final result is delivered. Existing public GitHub delivery uses
one final parent PR, with the same remote guards as Ready.
Child delivery/merge/integration commands identify the parent and refuse a
separate delivery. A stopped member blocks completion of the original Plan;
it cannot be silently removed from the released outcome.

Final review receives the original request, released shared body and child briefs,
the exact combined source and comparison, and identified child evidence through
the existing read-only source/evidence boundary. A historical child report is
labelled with its original candidate and attempt; never relabel it as current
combined proof or execute captured report content. No new evidence-bundle service
or mandatory review role is needed.

Released membership, scope and quality remain immutable in this bundle. Reject
replacement or quality adoption of a released child as well as its parent;
stop and submit a new request for a consequential contract change. This replaces
the current behavior that permits child replacement but strands the original
Plan. Preserve retained work and evidence; never transfer its authority silently.

#### Verification and bounded repair

Repository policy owns when checks run. Extend the existing named quality check
with `at: task|completion|both`, default `both`; do not create a second catalog.
`task` covers child implementation/repair and inclusion; `completion` covers the
combined Plan result. Standalone Ready must satisfy **all** applicable checks,
running a `both` check once when source and relevant conditions have not changed.
Plan release requires at least one completion check. Expensive final suites can
be placed at completion only through an explicit quality agreement, never by the
planner or implementer. Timing participates in the quality revision and previews;
baseline qualification still runs every declared check.

Child briefs identify focused evidence and inspected coverage to extend. The
parent brief identifies meaningful combined behavior and the existing final
entrypoints. New permanent tests require a credible uncovered failure; combine
or extend owning tests where useful. The parent reviewer must assess the approved
outcome beyond test counts, including omitted behavior and accidentally weakened
child guarantees, without inventing new product requirements.

Runner runs the pinned completion checks on the combined candidate before final
audit, then uses focused verification only for specific remaining gaps. Required
failed or unavailable checks prevent acceptance regardless of model prose.
An observed failing check may be audited to establish a concrete in-scope defect;
that uses the ordinary reviewer, not a separate failure-classification role.
A known unavailable capability stops before speculative repair or another model
call intended merely to bypass that capability.
Retained child evidence may support unchanged claims with an explicit relevance
assessment; it is not a substitute for required combined-candidate checks.
Resume can reuse valid saved results for the same candidate/conditions. Changed
source or base requires fresh affected proof; do not rerun passing checks solely
to obtain another report. Parent repairs or final base refresh run task checks
as well as completion checks, because they can affect prior child guarantees.

A confirmed final-review defect permits the existing implementer to repair the
retained combined candidate within the **released shared scope**. Reuse the
parent's repair limit, implementation ladder and reviewer; then rerun the affected
proof and combined review. Do not requeue completed child attempts, create repair
cards or add an ownership-classification agent. Original child receipts remain
historical evidence, not certification of subsequently changed code. Missing
proof uses focused verification; missing capabilities or product choices require
attention. Neither automatically authorizes a code repair.

Planning and final review/verification/repair calls consume the parent's existing
invocation ceiling; child calls consume their respective task ceilings. Show both
per-task consumption and the Plan total. Parent defects spend parent repairs,
child defects spend child repairs, and ref operations consume no model calls.
Refreshes and text-conflict repairs retain the existing limits and escalation
semantics. A child starting on the current admitted plan head is not a refresh;
movement after that binding is. Every allowance survives restart and policy edits.
No unbounded final review loop, hidden budget reset or new global budget is added.

Local quality must be satisfiable locally. Reject hosted `required_ci` for local
delivery with guidance to establish a suitable explicit quality agreement or
choose connected delivery. Do not silently drop a hosted obligation or use a
baseline exception for an unavailable environment. Required browser/database or
toolchain capabilities remain blockers until separately qualified.

#### Completion proof, ownership and recovery

Separate provider-neutral completion evidence from the existing GitHub-specific
receipt. Reuse candidate, base-proof, approval and quality identities; local
success must not fabricate a PR, CI assessment or remote merge. The local receipt
binds the task/version/generation, release digest when present, destination,
observed base, accepted commit/tree, verification/review identities and exact
observed ref result. Plan inclusion additionally binds its child version and
expected previous plan head. These are records in the existing private store,
not agent-editable files or a second event journal.

`core` owns these identities and success/dependency rules; `engine` derives the
actions; `store` enforces atomic claims and receipt commits; `workspace` owns Git
effects. `execution` supplies the appropriate shared context and result schema,
and `cli`/`config` expose the choices. Replace the current child-count completion
shortcut in `Store.CompletePlans` with the verified-outcome path; do not put the
new scheduling process in SQL or introduce a separate local-delivery engine.

Record owned intent before a Git effect, use expected-old-ref/expected-absence
updates, observe actual objects/ref state, then commit the receipt and lifecycle
change transactionally. Serialize plan-head writes per Plan and final base writes
per physical repository/ref. Recheck authority and current policy at dispatch
and receipt commit. Long checks run as owned asynchronous work; status, stop and
unrelated reconciliation must remain responsive.

For result creation, verify that the source base still equals the freshly checked
base when creating the result ref. For base integration, its expected old value
must be that verified base. Movement invalidates the pending intent and uses the
remaining refresh allowance; it never becomes an unconditional ref update. Create
the result ref once after full acceptance; retain its pinned commit as evidence.
If a base-mode task later needs a refreshed candidate, retain the earlier result
and create a separately identified revision, rather than rewriting a branch the
operator may already be inspecting. Status identifies the current accepted result.

On restart, a known undispatched intent may proceed only with still-valid proof
and authority. A possibly dispatched write requires inspection: its exact applied
effect is recorded without repeating it; a missing or changed ref blocks rather
than assuming the write never happened. A definitely rejected compare-and-set
can enter bounded refresh with fresh intent, preserving its failed history.
A coincidentally matching ref without Runner-owned intent does not prove an
authorized Runner write. A failed compare-and-set or an uncertain process never
releases a dependent. Stop prevents unsent effects, cancels owned work and retains
artifacts; a dispatched effect may remain as historical evidence after revocation
but cannot complete stopped work. Do not roll back operator history to simulate
cancellation. Later base movement does not reopen a completed result or imply that
its historical evidence qualifies the new base.

Backups must include new intents, plan heads, result refs, candidate/evidence
bindings and owned workspaces. Extend existing drained/freshness/restore checks;
an unresolved local write or live verification process prevents a resumable backup.
Use a new store format so older binaries cannot ignore the new completion rules.
Fresh isolated setup is sufficient; no compatibility runtime or authority importer.

#### Operator contract

Keep `task add`, `approve`, `release`, `show`, `status`, `run`, `stop`,
`verify-base`, `delivery --reconcile`, `merge` and `integrate`. Extend their
existing local behavior rather than add a second completion CLI. Approval/release
shows the destination, whether later merging is automatic, the combined proof
and relevant limits. Status explains child dependency/inclusion, final checks,
repair or refresh allowance, human merge wait, checked-out target and result
branch/commit. Inspection remains read-only.

For example, a local Plan with two dependent changes should read:
“First change included; second change implementing”, then “Checking combined
outcome”, then either “Completed — result branch …; base unchanged” or “Awaiting
delivery — reviewed result …; merge into … requires your action”. No GitHub setup
step or unexplained Awaiting delivery remains in the result-mode journey.
Update help, setup examples and the operator guide with the implementation.
Keep installed legacy skills and the running service unchanged.

#### Verification brief and finish line

Extend existing owning suites; the rows below are distinct guarantees, not a
required new test file or test per table cell. Use real temporary Git and SQLite
at their boundaries, explicit interruption signals, and small model/host adapters
only where their protocol is the subject of the test.

| Claim and representative expected result | Existing coverage to extend |
| --- | --- |
| Ready completes locally without remotes or `gh`; result mode preserves a dirty operator checkout/base; base mode waits for human permission or performs one authorized fast-forward. A checked-out target is refused without touching files. | [CLI journey](../internal/runner/cli/cli_test.go), [integration journeys](../internal/runner/engine/integration_test.go), [workspace delivery](../internal/runner/workspace/delivery_test.go). Fail the fixture if a GitHub adapter is called. |
| A Plan A → B plus independent C admits safe concurrent work; B reads A's included code. Missing, replaced, unreviewed or revoked versions cannot release B. All children passing with a missing combined consumer behavior still fails parent review. | [Planning rules](../internal/runner/core/planning_test.go), [store release/claim races](../internal/runner/store/planning_test.go), [Plan journeys](../internal/runner/engine/planning_test.go). |
| Parent completion requires actual combined checks/review and destination proof; parent and children settle once. Parent repair preserves sound sibling changes, spends its allowance and fixes the combined defect; missing tools spend no repair or escalation. | Extend/replace the old child-count completion expectation in [integration tests](../internal/runner/engine/integration_test.go); reuse [quality](../internal/runner/engine/quality_test.go) and [policy](../internal/runner/engine/policy_test.go) fixtures. |
| Source/base movement invalidates pending proof; a clean combination is reverified, a text conflict uses the bounded path, and exhaustion stays visible. A valid equivalent test consolidation is accepted while a weakened assertion is rejected. | [Refresh/conflict tests](../internal/runner/engine/refresh_test.go) and existing [paired role evaluations](../internal/runner/execution/behavior_eval_test.go). |
| Crash before/after a ref write, competing completion, stop at dispatch and edited/deleted refs cannot duplicate work or forge success. Disabling automatic merging fences unsent actions; another task and status still progress during slow checks. | [Integration](../internal/runner/engine/integration_test.go), [merge policy](../internal/runner/engine/merge_test.go), [store](../internal/runner/store/store_test.go) and [policy concurrency](../internal/runner/engine/policy_test.go) suites. |
| `task`/`completion`/`both` select the agreed checks; Ready does not omit completion checks; Plan does not repeat completion-only suites per child. Changed parent source invalidates affected evidence; hosted-only requirements cannot pass in local mode. | [Quality checks](../internal/runner/config/quality_test.go), [quality journeys](../internal/runner/engine/quality_test.go) and [execution checks](../internal/runner/execution/check_test.go). |
| Drained backup/restore retains a partially included Plan and completed local results; stale restore cannot revive writes. GitHub Ready retains its gates; a GitHub Plan emits one parent draft only after combined acceptance and completes only after its confirmed merge. | [Backup round trips](../internal/runner/backup/backup_test.go), [delivery](../internal/runner/engine/delivery_test.go), [CI](../internal/runner/engine/ci_test.go), [integration](../internal/runner/engine/integration_test.go) and [merge](../internal/runner/engine/merge_test.go) fixtures. |

Implementation order is local completion/permission and receipts, then shared
Plan outcome/inclusion/final verification, then consumer/help updates and complete
qualification. These are internal construction steps, not permission to declare
the brief complete after Ready alone. Preserve GitHub's existing guarantees while
adapting its Plan consumer; do not retain an alternate child-PR Plan workflow.

Required implementation gates are `go test -race -timeout 15m ./...`,
`go vet ./...` and a CLI build. Qualify changed planner/reviewer behavior with
the existing opt-in native evaluation using `gpt-6.1-sol / high`: one Plan with
actual dependent changes and combined proof, plus paired combined-outcome defect
and correct-control cases. Reuse existing test-quality cases unless the changed
instructions or an unresolved concern invalidate them. Record actual calls,
limits, commands, outcomes and environment failures separately.

Also exercise real CLI Ready and Plan journeys in disposable local repositories,
covering result delivery and human/automatic base integration, restart and
inspection of the delivered code. No GitHub credentials or remote are available
to those journeys. Controlled GitHub adapter journeys qualify the consumer
adjustment here; renewed real-GitHub qualification remains a pre-release gate
before claiming the changed connected Plan flow is live-qualified. No production
repository, running installation or global skill change belongs to these checks.

Finish only when both local entry paths reach their selected terminal outcome,
the combined-outcome failure cannot pass, recovery preserves authority and work,
the configurable merge policy is demonstrated, and help/evidence name the actual
limits. Stop before pilot implementation/environment expansion, board views,
remote polling, arbitrary Plan amendments, additional hosted merge methods,
publishing or installation changes. The following qualification record distinguishes
implemented behavior from earlier release evidence.

#### Qualification record, 2026-10-16

Implemented in `codex/runner-local-core` under this brief. Local Ready and Plan
share the existing coordinator and delivery lifecycle. The change adds owned Git
ref intents, inclusion/completion receipts, the shared Plan brief and check timing
to the existing packages; it removes the child-count completion shortcut and
child-PR delivery. No additional scheduler, review role or plugin framework was
introduced. The operator guide describes configuration, result inspection,
permission, recovery and the checked-out-base restriction.

Store format **16** deliberately rejects earlier state. Preserve the old binary,
state and backups; use fresh configuration/state/workspace paths and fresh
approvals. This bundle does not migrate the running installation.

Verification uses the existing integration, planning, quality, CLI, backup and
native role-evaluation suites. The owning journeys cover actual temporary Git
and SQLite, concurrent A → B plus independent C, refresh before inclusion/final
proof, parent repair, missing capabilities, focused proof gaps, failed required
checks despite passing review prose, manual/human/automatic local delivery,
checked-out targets, consumed-write recovery and revocation. The CLI round trip
now retains completed Ready/Plan results; backup additionally retains a partially
included Plan and rejects an unresolved local write. GitHub's controlled journey
proves one parent publication, pinned CI and atomic parent/child completion.

Native qualification uses `gpt-6.1-sol / high` with the real sandboxed Codex
harness in disposable repositories under `/private/tmp`. The completed operator
journeys use a six-call per-task ceiling, one repair, no transient retries, two
base refreshes and parallelism two; each invocation has a four-minute timeout.

| Native observation | Actual result |
| --- | --- |
| Two dependent cards: normalize the greeting, then consume it in `example.py`; local result destination | Completed in six model calls: planner, two child implementations/reviews and final parent review. Existing unittest assertions pass; the delivered example prints exactly `Hello, Ada!` and a newline. Base and uncommitted operator work are unchanged. |
| Ready, local base, human permission | Completed in two calls. Base stayed unchanged until `task merge`; one exact fast-forward receipt then completed the task. |
| Ready, local base, automatic permission | Completed in two calls with one recorded fast-forward and no per-task merge request. |
| Paired combined outcome, latest parent reviewer instructions | Two calls: missing consumer behavior was rejected as a defect despite passing unit checks; the correct control was accepted. |
| Existing fabricated-evidence control, latest reviewer instructions | One call: missing command execution evidence remained a proof gap, not acceptance or an invented source defect. |
| Final-binary coordinator restart and read-only inspection of the three completed journeys | Passed with unchanged task receipts, candidates and invocation counts; no model calls repeated. |
| Ready result with `gh` absent from PATH, empty GitHub configuration and no remote | Completed in two calls; delivered assertions pass and the original base/operator files remain unchanged. |
| Dependent Plan with separate task/completion checks, latest child instructions, and the same unavailable GitHub boundary | Completed in six calls. Each child ran its task check; the parent ran the completion check once. Combined example output and existing assertions pass. No repairs, escalation or refreshes were consumed. |

Qualification exposed and fixed missing read access to historical child command
logs, a missing evidence directory for no-op refresh backup, and disabled-policy
reconciliation of already dispatched local writes. Human permission remains bound
to its exact base proof. Required combined checks cannot be overridden by model
prose. Restoring a missing check capability resumes deterministic checks without
spending a repair or escalation; the subsequent ordinary review uses one call.

Keep failures distinct from passing evidence: the first native Plan used six
calls and stopped with a historical-command-evidence gap before the read-root fix.
An initial pair launched beneath `.codex` failed while loading instructions before
model evaluation; moving disposable fixtures to `/private/tmp` fixed the sandbox
path issue without broadening permissions. The earlier successful combined pair
used two calls; the latest three-call evaluation above includes the additional
fabricated-evidence control. Interrupted superseded gate runs are not passes.

Final gates passed: `go test -race -timeout 15m ./...` (recorded with `-json`;
engine suite 704 seconds), `go vet ./...` and
`go build -o /private/tmp/runner-local-completion-final ./cmd/cortexium-runner`.
The complete diff was reviewed; whitespace, documentation links and code fences
were checked. Native command logs, attempts, summaries and binary identities are indexed
at `/Users/dan/.codex/runner-qualification/local-completion-20261016/summary.json`.
The original native fixtures remain at their recorded `/private/tmp` paths;
evidence is local and is not published with the repository.

The expanded connected Plan flow is **controlled-adapter qualified only**; renewed
real-GitHub qualification remains a pre-release gate. This bundle does not renew
Linux release qualification or qualify the pilot toolchain/loopback boundary.
Real-project pilot implementation, boards, publication, installation and cutover
remain separate work. No running service, global skill or production repository
was changed.

### Frozen first-release checklist

**Scope frozen on 2026-10-15, after `aedc8bd`.** The nine phase 3 bundles close
delivery feature construction for the first release. The five bundles below are the
remaining sequence; this section is the release checklist, not a second backlog.
R1–R5 completed on 2026-10-16 within their recorded qualification boundaries.
The owner subsequently added the [real-project pilot gate](#real-project-pilots-before-merge)
before merge; it is not complete.
Publication and installation remain separate authorized actions. The checklist
alone authorizes no implementation, remote mutation, publication or installation
change.
When a bundle is authorized, refine its verification brief here before code and
stop at its exit condition.

Keep the implemented Plan/Ready process, quality agreements, independent review,
bounded recovery, configuration, local status and GitHub delivery. Backup/restore
and replacing the shipped runtime remain required release work. A concrete defect
in this supported contract must be fixed and verified in its owning bundle;
feature requests wait until after release unless the owner explicitly reopens
scope. A failed acceptance check is not a reason to weaken the check or silently
drop a promised capability.

The following limits are deliberate and do not block this release:

- One local Runner instance owns one repository/base. Sandboxed Codex is the
  qualified harness. Local implementation uses the recorded base; connected
  delivery verifies the current base before integration.
- Humans mark PRs ready and satisfy host reviews. CI and integration reassessment
  use the existing explicit commands after the initial automatic observations.
  Human merge is the default; `task merge` or applied automatic policy uses the
  existing protected ordinary-merge contract. Automatic policy is not a promise
  of unattended progress through every external wait.
- No ready-state writes, remote polling/events, GitHub auto-merge requests, merge
  queues, ruleset-only Runner-owned admission, squash/rebase receipts or automatic
  artifact cleanup. Ruleset-only repositories can still use human integration
  when the ordinary-merge receipt requirements are met.
- GitHub Projects/Trello views, other harness qualification, autonomous intake,
  extra repository references, workflow composition, multi-host execution, new
  budget types, dashboards and externally loaded plugins remain post-release.
- Fresh setup is the cutover path. No legacy importer, configuration aliases,
  historical approval transfer or conversion of in-flight authority is required.

#### Remaining bundles

Execute R1–R5 in order. A bundle is done only when its evidence and unresolved
limits are recorded here; running out of time or adding scaffolding is not an
exit condition. Splitting an unexpectedly large bundle must preserve its scope
and acceptance conditions rather than add another feature milestone.

| Bundle | Bounded work | Required evidence and exit condition |
| --- | --- | --- |
| **R1 — Qualify frozen delivery** | Close the live-host gap using an explicitly authorized test repository with the supported protection configuration. Exercise human integration and Runner-owned ordinary merge, including the automatic-policy trigger. Fix only defects in the frozen contract. | A real accepted change reaches draft, base verification, pinned CI, ready state and exact confirmed merge. Human/default and automatic paths retain correct authority and dependency completion. Extend the existing delivery/integration/merge and GitHub protocol suites only for discovered gaps; reuse their drift, outage, revocation and ambiguous-write cases. Record repository/PR identities, configuration, commit, commands and receipts. A mocked merge or read-only schema query cannot close this gate. Stop before backup/restore. |
| **R2 — Consistent backup and safe restore** | Add the smallest explicit backup/restore path for a quiescent single-host installation, matching binary/store format and original paths. Include configuration, private state/evidence and the referenced Git/workspace data needed to recover retained work; document what must be preserved. Online backup, relocation and cross-version migration are excluded. | Real SQLite/filesystem/Git round-trip preserves tasks, quality/approval bindings, counters, saved results and candidates. Reject incomplete/corrupt/incompatible material and competing ownership without overwriting unrelated data. Restore starts drained and cannot replay uncertain external effects or regain write permission lost after the snapshot. Stale snapshots whose later actions/counters cannot be established require explicit recovery, not automatic continuation. Verify interruption, missing artifacts and an external effect after backup. Stop with a documented, tested recovery procedure. |
| **R3 — Qualify the complete operator journey** | Use the current CLI and one concise setup/operator guide from a fresh installation. Update the replacement operator reference through delivery and restore. Fix observed supported-flow problems; do not add a dashboard or a second configuration workflow. | Follow the guide through Plan and Ready, a question, a dependency wait, interrupted review, configuration preview/save/apply, model/reasoning/escalation and retry/parallelism limits, drain/restart, delivered-result inspection and R2 recovery. Show that users can identify the cause and next action without undocumented help. Reuse valid live role evidence; rerun paired behavioral cases only if role instructions or execution behavior change. Record elapsed time, interventions, repeated verification and all actual calls; unavailable cost/usage stays unknown. Stop with no unresolved blocker to the documented journey. |
| **R4 — Promote and package the replacement** | Promote the replacement to `cortexium-runner`; retire the legacy entry point, obsolete consumers/tests and the temporary `runner-next` name. Update architecture, README, setup/examples, help, operator/role references and install/release tooling. Reuse useful low-level code and tests by current requirement. | The packaged binary exposes the replacement commands and version, requires no Project, and its docs/installed instructions match its behavior. Extend the existing packaging, installer and release-security checks; replace the legacy Project smoke journey. Build all four no-CGO targets and verify archives/checksums. Update CI/readiness commands to the qualified package timeout. No compatibility runtime, general updater redesign or new plugin layer. Stop at a reviewable release candidate, without publishing or switching a real installation. |
| **R5 — Final qualification and cutover rehearsal** | Qualify one reviewed candidate and rehearse the documented breaking cutover on a selected test installation. Check backup, old-owner shutdown, fresh setup, explicit resubmission and the prohibition on restarting stale authority after new remote effects. | Complete the gates below on the candidate; reuse R1–R3 evidence only where subsequent changes have not invalidated it. Exercise the packaged entry point on macOS and Linux and record native sandbox/runtime evidence separately from cross-builds. Finish with tested artifacts, evidence, release notes and the exact installation/cutover procedure. Missing infrastructure or unverified safety leaves this gate open. Stop release-ready; publishing and changing the user's running installation require their own explicit instruction. |

R1–R5 are complete with the evidence below. For any later requalification, if a
needed test host, repository or credential is unavailable, name the specific blocked evidence and
continue independent preparation; do not substitute a simulated success. Live
model qualification uses the already selected `gpt-6.1-sol / high` where a model
call is needed. Use only an identified, authorized test destination for remote
writes; never enable automatic policy on a production installation or alter
production protection as a side effect of qualification.

#### Real-project pilots before merge

**Authorized 2026-10-16: setup qualification and briefs only.** The owner added
two real-project pilots as a gate before merging the redesign. R1–R5 remain
historical qualification of their recorded candidate; they do not establish
compatibility with arbitrary repositories. Use the packaged R5 candidate,
isolated committed source copies and fresh drained state. Do not run a planner
or implementer, approve tasks or quality agreements, publish project changes,
alter existing installation/configuration, or change protection during this pass.
Keep evidence here rather than creating a second work ledger.

Qualification must identify the actual target base, required local commands,
tools/dependencies, existing coverage, and delivery prerequisites. Compare
representative commands in an ordinary isolated development copy with the same
commands through packaged Runner quality checks. Separate missing tools,
dependency preparation, sandbox restrictions and source defects. Reproduce
loopback access independently of dependency downloads. Do not hide infrastructure
failures behind baseline exceptions or relax a sandbox to report success.

**Pilot A — api-mcp, proposed Ready brief.** Reject unknown configuration fields
with a useful error instead of silently ignoring misspelled settings. File
loading, embedded loading and embedding preparation must agree. Valid existing
configuration, defaults, relative spec resolution and arbitrary keys in declared
maps such as `headers` must retain their meaning. Reject unknown nested struct
fields as well as top-level fields; report the offending field without printing
credential values or the entire configuration. No live API/Keychain calls,
authentication redesign, new configuration format, dependency upgrade or general
validation framework. Define this deliberate compatibility tightening in the
issue and user documentation before implementation.

The author owns these outcomes; the implementer extends
`internal/apimcp/config_test.go`, consolidating loader cases where useful. Prove
one top-level typo, one nested typo, valid map keys, valid file/embedded parity
and absence of secret values in errors. Reuse existing path/default/credential
tests; do not duplicate those scenarios or add a new test framework. Required
local gates are the repository's formatting, vet, race suite and build command.
Its Linux/macOS CI and vulnerability check remain delivery gates. Setup
qualification alone cannot establish correctness of this future change.

**Pilot B — Genny, proposed Plan brief.** Shape the smallest dependent delivery
for [issue #575](https://github.com/cortexium-io/genny/issues/575): resolve the
supported server-stats enrichment verification contract and provide a bounded,
reproducible adoption path for ShowIP. Reuse the existing bounded-default fix
from #494. Proposed sequence: first settle and implement the generator-owned
hook/test contract; then verify and document its adoption in a disposable app
fixture against the accepted predecessor. The planner may combine work if no
meaningful dependency remains; do not manufacture cards to exercise scheduling.

Before implementation, explicitly settle the supported customization of final
path/route labels and whether adoption uses current generation or a scoped
backport. Default normalization, privacy, exclusions, storage and authorization
remain distinct guarantees. Test the default at its owning boundary and supported
customization through the real hook; do not preserve an application-specific
post-hook equality assertion by inventing special routes or detecting tests.
Extend the existing server-stats scaffold/CLI suites and generated fixture;
preserve credible assertions and avoid duplicating the generated suite. Require
real-router cardinality/privacy evidence for the chosen adoption fixture and
the applicable backend/Release Gate checks. Do not edit ShowIP's working tree,
historical generator checkout, generated files, database or deployment. Source
and regeneration evidence precede any separately authorized ShowIP adoption.

For both future pilots, use `gpt-6.1-sol / high`, human merge, parallelism one,
one repair, one transient retry and one base refresh initially. Agree a bounded
invocation cap with the final task/plan; do not reset counters to continue.
Confirm no existing Runner owns the selected work before scheduling. Connected
delivery must use an explicitly selected repository/base and observed CI names
and app identities. Local result delivery now supports dependent Plans and combined
verification without GitHub. A connected pilot completes after its confirmed
ordinary parent merge; human merge remains the policy for these project pilots. Measure setup interventions, status
clarity, calls/retries, proof reuse and unnecessary test growth as well as the
result. The end condition is reviewed useful changes plus an honest account of
operator friction, followed by redesign/main reconciliation and verification of
the integrated source. Publishing and operational cutover stay separate.

##### Setup qualification — pilots blocked, 2026-10-16

The preflight is complete; neither project is approved to execute. Packaged
Runner from `eee0791bc6ec9f1278c160f0aa4023bdd7d46e72` was invoked directly;
its binary SHA-256 is
`49eedf9a83b068af697c947d7f3fd5d0ee332f4f439f371cdc62f426e785b1c6`.
Fresh clones contain committed source only, with independent config/state/worktree
roots. Both `init --apply` calls succeed and leave drained, empty, unconfigured
authority. No coordinator, model, task approval or remote write was started.
These disposable setups are evidence, not installations to adopt automatically.

| Project and pinned target | Ordinary local control | Packaged Runner checks |
| --- | --- | --- |
| api-mcp `main`, `7aced1601ed0b60f55ea777f2d047102db4fd415` | Configuration tests, formatting, uncached race suite, vet and `scripts/build` pass. With isolated Go 1.27.2, race/vet/build pass again and `govulncheck@v1.7.0` reports no reachable vulnerabilities. | Bare `go`/`gofmt` cannot be located inside containment despite host prerequisite detection. The build script also encounters the disposable copy's unborn Git HEAD. A real loopback test is denied after compiler/dependency setup is removed from the experiment. |
| Genny `dev`, `d298ff57fb848380a9f15ef2831899892c696746` | With isolated Go 1.26.9, fast root tests, the complete scaffold suite, all `TestCLIStatsAddServer` cases (including generated backend tests), root vet and pinned Staticcheck across all four modules pass. | Fast tests fail before package enumeration; focused scaffold/CLI and vet are unavailable because `go` cannot be located. The loopback probe is denied too. |

The actual local control commands were:

```sh
# api-mcp: Go 1.27.2 in the private qualification cache
go test -race -count=1 -timeout 10m ./...
go vet ./...
VERSION=pilot-preflight OUTPUT=.runner-output/api-mcp-go1.27.2 ./scripts/build
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...

# Genny: Go 1.26.9 in the private qualification cache
scripts/test-fast -count=1 -timeout 10m
go test ./internal/scaffold -count=1 -timeout 20m
go test ./internal/cli -run '^TestCLIStatsAddServer' -count=1 -v -timeout 10m
go vet ./...
scripts/staticcheck
```

Controls use explicit, credential-free environments and private dependency/build
caches. Genny's temporary directory must be outside an enclosing Git repository:
the first fast-suite run inherited the surrounding `.codex` repository and failed
the missing-Git-metadata fixture; rerunning in `/private/tmp` passes without any
source or assertion change. Genny's pinned Staticcheck v0.7.0 cannot read Go 1.27
export data; it passes on its declared Go 1.26.7 and the selected patched 1.26.9.
This establishes a need for per-repository toolchain selection. api-mcp's first
vulnerability check found eight reachable standard-library advisories on the
installed Go 1.27.1; its repeated check on private Go 1.27.2 passes. The installed
toolchains and binaries were not replaced. api-mcp's CI still selects Go 1.27.0
from `go.mod`; align that baseline separately before relying on future green CI.

Containment diagnosis separated three concerns:

- **Tool access:** the captured PATH is correct. Metadata lookup of
  `/usr/local/go/bin/go` is denied, while direct execution of that absolute path
  prints its version. An absolute Go test command then fails with `no such tool
  "vet"`. This is an incomplete usable toolchain boundary, not proof Go is absent
  from the host. Native checks also expose Apple tool-launcher cache denials.
- **Loopback:** a diagnostic fixture carries an unmodified, precompiled api-mcp
  test binary with its source commit and hash. Both selected tests pass outside
  Runner. Inside Runner, `TestNormalizeConfigUsesBuildVersionByDefault` passes;
  `TestExecuteOperationExtractsProvenPaginationMetadata` fails at the real
  `httptest` listener with `bind: operation not permitted`. A separate IPv4 socket
  probe also fails. This evidence is independent of downloads/compiler access;
  it is not acceptance evidence for a future candidate.
- **Check definitions and preparation:** `test -z "$(gofmt -l .)"` can falsely
  pass when `gofmt` is missing. The draft instead uses
  `files=$(gofmt -l .) && test -z "$files"`; its native diagnostic correctly
  reports unavailable. The api-mcp build smoke can use the script's existing
  explicit `COMMIT=qualification BUILD_DATE=qualification` inputs rather than
  assume copied Git history; this is a build check, not release metadata.
  Private HOME/TMPDIR and no network also require deliberate dependency/cache
  preparation; host caches and downloads are not automatically available.

Both native project reports produce blocked agreement previews. No exceptions
were requested. A successful report command is not a passing suite, and a socket
panic classified as `failed` is still an environment incompatibility, not an
implementer defect or an acceptable baseline exception.

Read-only delivery inspection found ordinary merges enabled and strict required
checks defined through rulesets on both targets. Human integration fits this
shape; Runner-owned merge is not qualified against ruleset-only protection.
The draft agreements pin `kind: check`, observed GitHub Actions app ID `15368`,
and these exact names:

| Target | Required CI |
| --- | --- |
| api-mcp `main` | `Test (ubuntu-latest)`, `Test (macos-15)`, `Vulnerability check` |
| Genny `dev` | `Non-secret release checks` |

api-mcp's historical head checks are green; Genny's identity comes from merged
PR #586's head `b140975358829020087844629c1aa84e33d7fef1`. Neither establishes
future candidate CI. Full Genny CLI, generated frontend build/test/audit and any
additional adoption-specific gates remain to be selected and run on the shaped
candidate; they were not claimed from the focused server-stats controls. The
template change is subject to the repository's path-aware Release Gate. No
Docker/Postgres, browser, live-service or Linux pilot qualification occurred.

**Required before pilot execution:** authorize a bounded Runner check-environment
brief covering usable toolchains, explicit dependency preparation and an
appropriately isolated loopback-fixture capability. Keep default containment;
do not silently grant unrestricted host/network access. Qualify those changes
against these unchanged checks, with isolation and missing-prerequisite failures
covered by the owning execution suite. Agree how Genny's existing issue/Project
and legacy host-access policy applies to this isolated local-core pilot, confirm
exclusive ownership, settle #575's hook/adoption decisions, and approve the final
task/plan and quality agreement. No such implementation is authorized by this
preflight. External views and other deferred features remain out of scope.

Raw evidence, exact environments, valid but unapplied `quality-draft.json` files,
CLI reports, hashes and command logs are retained privately at
`/Users/dan/.codex/runner-qualification/pilots-20261016`.
`summary.json` indexes the observations; `probes/` retains the controlled native
comparison. Production project working trees, live data, services and the
installed Runner/Genny binaries remain unchanged. This documentation pass adds
no maintained tests or production code; the diagnostic fixture stays outside
the repository. New paid model calls: **0**. Remote writes: **0**.

#### Remaining qualification implementation brief, 2026-10-16

**Authorized:** finish documentation and harness-learning reconciliation, usable
contained checks, the api-mcp pilot and Genny verification/adoption pilot, renewed
connected Plan and platform qualification, main reconciliation and PR #106.
Publication, installation/cutover, project merges, board integrations and new
model-harness adapters remain separate. The earlier setup-only authorization
and evidence above are historical, not current scheduling authority.

**Supported check environment:** retain native Codex containment as the default.
An owner may instead pin a local Docker image by exact `sha256:` image ID in the
quality agreement. Prepare that image explicitly with its toolchain and offline
dependencies; Runner neither pulls images nor downloads dependencies. The image
identity participates in quality previews, approvals and retained evidence.
Each check gets a fresh container, the existing disposable source copy, private
HOME/scratch, a read-only container root, unprivileged user, dropped capabilities,
no host credentials, no Docker socket, no host network and no published ports.
`--network none` permits fixture loopback only within that container. It does not
grant access to host loopback or external services. Image preparation is an
operator action outside repository/model execution. No generic resource service,
second scheduler or arbitrary permission profile is added.

A durable process record must include container ownership before launch.
Cancellation must remove the owned container and confirm absence before releasing
its reservation. Recovery and backup must retain ownership if either its process
group or container remains, or the Docker daemon cannot establish absence.
Missing Docker/image/tool, unsupported image configuration and setup failure are
unavailable proof, never a source defect or baseline waiver. Checks retain source
integrity verification, output bounds and their existing deadline.

**Representative results and coverage:** extend `config/quality_test.go` for
immutable image selection and approval identity; extend the execution check and
process suites for native-default preservation, actual offline tool execution,
container-only loopback, denied host/outbound access, missing prerequisites,
source integrity, cancellation/absence and unknown ownership. Use the existing
engine quality/backup suites for propagation and recovery; do not create a second
end-to-end test framework. Qualify unchanged api-mcp and Genny commands through
the exact environment before authorizing model work. The normal race and vet
gates and native check-boundary qualification remain required.

**Harness learnings:** apply the observations in
[model and role evaluation](model-profile-evaluation.md#local-harness-benchmark-learnings)
to existing boundaries. Require valid structured proof and a terminal successful
protocol result; exit status zero alone is insufficient. Preserve invocation and
repair/retry bounds across interruptions. Keep backend/capability failure distinct
from a defect. Context/compaction settings and requested versus observed reasoning
belong to a qualified profile; do not assume one backend implements another's
protocol. Codex remains the supported adapter. Pi/OpenCode adapters require their
own future brief and native contract qualification; do not copy benchmark code.

**Pilots:** api-mcp retains the Ready behavior and owning coverage above. Its
patched Go baseline must agree with CI before claiming that gate. Use human
integration and retain candidate-bound local and remote evidence. The owner
selected an isolated verification/adoption pilot of existing Genny PR #576
(`24665db0e196bfe440d865dff4cbdc3eecf17f2c`); #575's implementation and ShowIP
PR #19 already exist. Reproduce the approved historical patch/output contract,
run applicable existing generated/backend tests and address only demonstrated
gaps. Do not create duplicate implementation, manufacture dependencies, edit
ShowIP, replace PR #576 or change its ownership. Record if no source fix is needed.
Use `gpt-6.1-sol / high`, human merge, parallelism one, one repair/retry/refresh,
explicit invocation/time limits and fresh isolated authority. The owner's current
instruction authorizes these pilot approvals within these briefs.

**Finish line:** renew live GitHub qualification of one combined parent draft and
confirmed ordinary merge in the dedicated qualification repository; review incoming
`main` changes individually, retaining applicable shared safety fixes and retiring
legacy-only consumers. Update the Go security baseline and CI/release safeguards.
Run required full gates, native Mac/Linux and package checks on the resulting
candidate, then update draft PR #106 with honest evidence and breaking changes.
Real-project changes remain available for review under human merge policy. No
release/tag, publisher dispatch, production merge or running installation change.

##### Remaining qualification progress

The documentation contradictions are reconciled. The optional `check_image`
contract, durable container ownership and terminal Codex result validation are
implemented in the existing config/execution/engine boundaries. Native default
containment and the container boundary both pass. The container control compiles
and executes a Go program, proves private loopback, rejects host/outbound access,
checks missing tools and proves cancellation/absence and durable launch gating.
The existing engine quality contract journey and backup refusal suite pass;
one actual `gpt-6.1-sol / high` supplied-evidence audit passes with terminal
protocol validation enabled. Full integrated gates and both project baselines
remain pending at this checkpoint.

The first project baseline exposed non-executable Docker scratch and automatic
VCS stamping against source-only Git inventories. The corrected boundary uses a bounded executable Linux tmpfs at container
`/tmp`, separate from source/Git discovery; the explicitly prepared
images select `GOFLAGS=-buildvcs=false` and existing explicit build metadata.
An affected Genny baseline was cancelled and its owned container was confirmed
absent before restarting. No failure was waived. Corrected baseline runs use new
pinned image identities. Private evidence and prepared image inputs live at
`/Users/dan/.codex/runner-qualification/check-environment-20261016`.

##### Main reconciliation record

Reconciled `main` at `14a86d2c26acae4eefe8c499eae4e0cdc4ebede7` into the
replacement. The 109 incoming commits were assessed against the new boundaries.
Retained the Go 1.26.9 security minimum, workload/platform/toolchain-specific
refreshed caches, reviewed-source release safeguards, nonblocking no-follow FIFO
protection, bounded subprocess pipe drainage/teardown, safe common-config
recapture and verified loose/packed Git ref transitions. The shared filesystem,
subprocess, workspace and release-script owning suites pass with race detection.
Final integrated gate results are recorded in the continued qualification below.

Legacy Project runtime additions, bundled skill installers, legacy Pi/browser
lanes, migration/rebase delivery and the host/global heavy verification service
are retired with their old consumers. Their unused helper APIs were not added to
the new core. Main's independently numbered old ADR 0005 remains available through
the existing immutable historical references; local-core ADR 0005 is the current
accepted decision. Existing role/test/evidence learnings are represented by the
replacement contracts and qualification cases rather than duplicated old suites.
The private `main-resolution.json` records the reviewed path dispositions.

The api-mcp unchanged format/race/vet/build baseline passes inside its offline
image. Genny preparation exposed missing transitive generated-app dependencies
and a host-shared scratch filesystem incompatibility in Git fixtures. Prepare the
full declared dependency graph explicitly, qualify real Linux executable scratch,
and rerun the unchanged owning checks; no assertion or baseline exception is
changed to accommodate setup. The corrected image baseline and pilot results
are recorded in the continued qualification below.

##### Pilot finding: recovery after a failed required check (approved)

The api-mcp worker completed its scoped source/test change honestly, with native
Go/gofmt unavailable. Runner's race, vet and build checks passed, but its format
check correctly failed; the existing rule blocks review without an automatic
repair. The current operator route replaces the task version, which restarts
per-version allowances and repeats already completed source work. That is poor
recovery for a confirmed formatting defect under the unchanged approved scope.
No check or assertion was waived. The pilot was drained with its original attempt,
source and counters retained before the owner approved recovery.

The owner approved this bounded correction on 2026-10-16: an explicit operator
request may
classify a complete failed-check observation as a confirmed in-scope source
defect and request the existing implementation-repair continuation. Require the
exact task/attempt, no surviving execution/container, unchanged task/quality and
source binding, actual failed (not unavailable) check evidence, and a concrete
operator reason. Spend the existing repair and invocation allowances, preserve
all counters, profiles, source and evidence, and refuse duplicate requests. Do not
add another repair budget, auto-interpret arbitrary logs, change required checks,
retry unavailable infrastructure or bypass review. A repair reruns required checks
and ordinary independent review; exhausted allowances remain exhausted.

Representative proof: formatting failure can receive one explicitly authorized
source repair, preserving the failed receipt and current source; missing tools,
stale/revoked authority, modified source, surviving process/container, repeated
request and exhausted limits cannot obtain a new write. Extend existing operator,
policy and quality fixtures. Qualify the real pilot's recovery, and make native
formatter/tool availability explicit before further model work. The explicit owner
confirmation authorizes this correction to the frozen contract.

The check-image ownership record and new repair continuation change persisted
execution semantics. Fence the replacement with store format 17 so earlier
private format-16 binaries reject it before ignoring container ownership or
repair admission. Preserve prior stores and their matching binaries for evidence;
fresh setup remains the migration path. Extend the existing previous-store
rejection coverage and qualify an earlier binary refusing a fresh format-17 store.
No compatibility runtime or data import is added.

The pilot also qualifies the source-to-review handoff: a succeeded implementer
result may retain honest proof gaps when Runner completes all pinned checks.
Construct the bound candidate and pass those gaps and actual check receipts to
independent audit; gaps do not constitute acceptance. Defect/needs-input or a
blocked result still stops. Existing audit and focused verification must establish
every obligation before delivery. Extend the existing proof-gap quality fixture
to prove this transition and that unproved delivery remains impossible. Apply the
same handoff rule to resolved conflict repairs: deterministic marker/source checks
must run before quality checks and independent audit; unresolved markers and a
missing decision still stop before checks. Extend the existing conflict journey
and unresolved-source cases, without another fixture or allowance.

##### Continued real-project qualification

The bounded check-repair implementation is `f1b92f6`. Its owning race-enabled
fixtures pass: failed-check recovery preserves task/quality/source/history,
idempotent requests consume no extra write, admission enforces existing limits,
changed source/evidence and unresolved ownership refuse repair, and a proof gap
cannot bypass independent audit/focused verification. Retry/resume retains the
original operator assignment. Native project qualification and final platform
checks remain separately recorded below; focused fixtures are not release gates.

- **api-mcp local pilot passed.** Task `aaa14807f622d32c60352e879aadeee3`,
  unchanged version 1, completed to a local result branch at
  `ec0535108486c9a7277a514c2a2aa46585d3cf4d`. The initial worker changed only
  configuration source, its owning test suite, README and the approved Go patch
  baseline. Runner's format check found one whitespace alignment defect while
  race/vet/build passed. One owner-confirmed repair used a read-only formatter
  diagnostic; the worker made the correction, all four fresh checks passed, and
  independent audit accepted the candidate. Total: three `gpt-6.1-sol / high`
  role invocations, one repair, zero escalations, no counter reset. Native
  Go/gofmt stayed unavailable to the model; no container socket or host access
  was granted.
  The owner explicitly enabled pull requests for this repository, and
  [pilot draft #6](https://github.com/cortexium-io/api-mcp/pull/6) remains unmerged.
  Linux/macOS race/vet/build/format and vulnerability CI passed on the exact
  published head ([run](https://github.com/cortexium-io/api-mcp/actions/runs/38922327516)).
- **Genny verification/adoption pilot passed.** The isolated existing
  [PR #576](https://github.com/cortexium-io/genny/pull/576) source at
  `24665db0e196bfe440d865dff4cbdc3eecf17f2c` needed no correction. Task
  `5b8ef643f7387a729cbbe18693425121` completed locally at
  `686fb642d9204927256bac1a2256a9f88a192289`, with the same source tree as that
  approved snapshot. Fast/scaffold/stats-CLI tests, vet, four-module Staticcheck
  and adoption checks passed; independent audit accepted the owning assertions
  without adding tests. The adoption helper rebuilt historical Genny `7e66bf8`
  plus the candidate's compatibility patch, reproduced both ShowIP outputs twice
  from unchanged metadata, and passed the consuming backend and race-enabled
  stats/privacy/cardinality cases. Its private modfile changes only local
  replacement paths. Image preparation added root/auth checksum entries to the
  historical fixture; all other archived bytes, both `go.mod` files and all
  ShowIP fixture files were verified unchanged. No dependency versions changed.
  Original PR ownership, application source and installation remain untouched.
  Version 1 spent three role executions and correctly stopped because the brief
  omitted the adoption helper and consuming assertion source. The normal
  clarification flow supplied immutable snapshots/provenance, retained the old
  version/history and created version 2. Before approval, the invocation cap was
  lowered from eight to the five remaining; the original two-hour deadline was
  retained. Version 2 spent two executions: **five aggregate**, no repair or
  escalation. Per-version counters restart on replacement; this explicit lower
  cap conserved the original overall allowance. The pilot used the matching
  format-16 `f1b92f6` binary; its unchanged execution path remains qualified for
  `f9a3c6c`, while the new format boundary has separate native proof below.
  A separate overlapping probe hit the consuming test's existing three-second
  asynchronous wait. Serial baseline and both candidate passes retained that
  assertion and timeout. This qualifies the isolated serial fixture, without a
  deployment or contention-reliability claim. No new Runner feature was needed
  to supply the missing brief context. Evidence: `genny-pilot-qualified.json`,
  the versioned briefs/attempts and `image-evidence/provenance.json` under the
  private qualification root named above. Both project coordinators are drained
  and idle; matching binaries, stores, source and receipts remain retained.
- **Real combined GitHub Plan passed.** Parent
  `19ee70dedefb5831a309314e54847f8b` released two dependent cards, included
  their exact candidates privately, passed combined checks and independent
  outcome audit, and published only
  [qualification PR #4](https://github.com/cortexium-io/runner-delivery-qualification/pull/4).
  The pinned Actions check passed on `bb1341ef8f376d83af9458d57259c33d02313607`.
  A normal protected operator merge produced
  `0630ae05a7c312f6084bba96cf7d80e29d89f4fc`; exact integration inspection
  completed parent and both children together. Six Codex role invocations total,
  no repair, retry or escalation. This used `9608782` and the dedicated test
  repository; no project pilot or Runner PR was merged.
- **Final code qualification passed.** On immutable `f9a3c6c`, the
  [PR race/vet gate](https://github.com/cortexium-io/runner/actions/runs/38926057506)
  and [full platform/release workflow](https://github.com/cortexium-io/runner/actions/runs/38926238688)
  passed: native Linux/macOS tests and vet, an uncached full Linux race run,
  vulnerability/module checks, packaged CLI smoke and all four no-CGO
  archives/checksums. Native Linux arm64 also passed all 13 race-enabled packages,
  vet and the actual Codex sandbox boundary on `f1b92f6`; native macOS sandbox
  containment and CLI smoke passed. These boundaries are unchanged by `f9a3c6c`.
  The new handoff and store-fence owning suites passed race detection on macOS.
  A fresh native format-17 store accepts current commands; the prior format-16
  binary refuses both inspection and execution without changing its database.
  The private `format17-native-fence.json` records the hashes and observations.
  Earlier local macOS failures are retained: one engine race run hit its
  15-minute limit under contention; a launcher run overlapping source edits
  produced invalid mixed compilation and its CLI journey hit its own deadline.
  Neither is claimed as a final pass. No timeout or assertion was relaxed.

#### R1 implementation and verification brief

**2026-10-16, before qualification or code changes.** Qualify the frozen delivery
path against the owner-authorized private repository
`cortexium-io/runner-delivery-qualification`. The authorization covers creating
that fixture, strict classic protection, real Actions checks and two synthetic
PRs with ordinary merges. Existing production repositories and their policy stay
outside this exercise. Use `gpt-6.1-sol / high` for the native Codex calls.

- Build the actual `runner-next` entry point. Use fresh private state and a tiny
  repository with `alpha.txt`, `beta.txt` and one existing shell check. Baseline
  checks accept the documented before/ready states; each task's separate proof
  requires its own file to contain exactly `ready`. No new test framework.
- Through normal commands, configure local and pinned hosted quality checks,
  approve a Plan proposing exactly two bounded tasks, then inspect and release
  its exact proposal. Alpha precedes beta. Confirm beta cannot start before alpha
  is integrated, and the parent cannot complete after only one child.
- Keep merge policy human for alpha. Run real implementation/review, draft
  publication, current-base verification and passing pinned CI. Mark ready and
  perform a normal operator merge through GitHub, then request integration.
  Validate the exact merge parents/tree/ancestry, receipt and dependency release.
- Explicitly apply automatic policy for beta. Exercise current-base refresh as
  required after alpha's merge. Observe the draft waiting; mark it ready through
  GitHub, request the explicit CI/integration observations needed, and let Runner
  perform the protected merge. Require live eligible host admission, the durable
  automatic-policy dispatch marker, exact receipt and parent completion. A
  command's success or remotely merged status alone is insufficient.
- Use enforced classic protection with strict required Actions checks and no
  force-push, deletion, bypass or merge-queue allowances. Pin the observed check
  name and producing app. Never weaken protection merely to obtain admission.
  Preserve repository/PR links and private configuration, native-call evidence,
  status/receipt snapshots and version information for reproduction.

Fix only demonstrated defects in this supported path. Extend the owning suites
(`internal/runner/engine/{delivery,integration,merge,planning}_test.go`, the
GitHub protocol tests, or the relevant workspace/execution suite) for a concrete
regression; reuse the established negative, restart and one-write evidence.
Do not implement ready-state writes, polling, new host support, backup/restore
or another qualification framework. Record blocked infrastructure honestly.
Run the required race/vet/build gates and review the complete diff; rerun the
live evidence affected by any fix. R1 closes only after both real merge routes,
exact dependency/parent completion and required checks pass. Stop before R2.

**R1 discovered-defect brief, before the adapter fix.** The real host retained
alpha's original `baseRefOid` after its base branch advanced; `baseRef.target.oid`
and the generated test-merge parents identified the new base. CI, open integration
and merge admission must read the actual base ref target, never use the retained
PR base OID as current state. Missing live base data must refuse open-PR progress;
merged receipts still require fetched Git ancestry and exact parents/tree. Extend
the existing GitHub CI/integration/merge protocol tables with stale PR OIDs and
missing live targets, then repeat alpha's CI and both live integration routes.
No store format, authority, scheduler or retry change is needed.

#### R1 qualification — complete

**2026-10-16, source `346ae6b`.** Both live delivery routes passed in the
private [qualification repository](https://github.com/cortexium-io/runner-delivery-qualification).
The actual `runner-next` ran fresh local state, native Codex `gpt-6.1-sol / high`,
independent review, Git workspaces and real GitHub Actions. No Project was used.
The only Runner code change reads `baseRef.target.oid` for CI, open integration
and merge admission. This corrects the observed stale `baseRefOid`; existing
GitHub protocol tables now cover stale historical OIDs, missing live targets and
merged inspection after ref deletion. No new suite, dependency or store format.
GitHub documents the [PR ref](https://docs.github.com/en/graphql/reference/pulls#pullrequest)
and its [current target](https://docs.github.com/en/graphql/reference/git#ref);
the retained live response demonstrates why the distinction matters here.

| Route | Reviewed candidate | Verified base | Confirmed ordinary merge |
| --- | --- | --- | --- |
| Human/default, [alpha PR #1](https://github.com/cortexium-io/runner-delivery-qualification/pull/1) | `fd37076ce129a084e0d7c153f40f333073bc5d73` | `7284b842ab6c2b90461a2ea95ee440fb5136dd71` | `b61747ceea99d9f700c57e6fa5fa5ba3f41180b8` |
| Automatic, [beta PR #3](https://github.com/cortexium-io/runner-delivery-qualification/pull/3) | `29491aaf8d4afcbb7996c3eeb0f6ac0e245efbe3` | `b61747ceea99d9f700c57e6fa5fa5ba3f41180b8` | `5c05a8b14cd710d2e7eb36cb1b15ed15991e9463` |

Both receipts passed independent Git checks: exactly the verified base and
candidate as the two merge parents, identical candidate/merge trees and merge
ancestry in the fetched base. Both final files contain exactly `ready` plus one
newline. Beta had zero invocations while alpha awaited delivery; its first
attempt began after alpha's confirmed receipt. The parent remained incomplete
after alpha and completed only after beta's receipt. Each child consumed one
base refresh, three model calls (implementation, audit, refreshed audit), and
no repair or escalation.

The host enforced strict classic `main` protection, including administrators,
with required Actions check `fixture`, app ID `15368`, and no force-push,
deletion, bypass or queue allowance. Live admission reported
`viewerCanMergeAsAdmin=false`. Human policy produced no Runner merge dispatch.
For beta, the operator previewed, saved and applied `merge-policy=automatic`;
a passing draft stayed open without a dispatch. After external ready marking
and `task integrate`, Runner recorded eligible protection
`BPR_kwDOVL46U84FD75U`, policy origin
`4b3b749456fde40f57e4228c3ae19a7d281b83b5563d4b56535fb72b1d7f3b04`,
one consumed dispatch and the exact confirmed receipt. No `task merge` command
or external merge command was used for beta. Protection remained enforced at
completion. The fixture is drained, with no coordinator or occupied reservation.

**Reproduction and interventions.** Use the retained configuration and quality
specification: `init --apply`; `quality preview/check` and agreement
`preview/apply`; `task add --plan`, exact-version approval, `run`, inspect and
release the proposal. Inspect `task show --json` through review, base verification
and publication. Reassess settled CI explicitly when needed; mark ready with
`gh pr ready`. For alpha use ordinary `gh pr merge --merge --match-head-commit`
and `task integrate`; for beta apply automatic policy through
`config edit/explain/apply`, then request `task integrate` after ready marking.
Never use `--admin` or weaken protection.

- The first planner proposal mixed delivery duties into coding acceptance and
  assumed beta would start on an advanced base. It was rejected before release.
  `task replace` and a second native plan produced two concise file-only cards;
  Runner retained responsibility for refresh and dependency/parent completion.
- The initial fixture triggered the same check on push and PR events. Runner
  correctly refused duplicate check identities. One additional
  [fixture setup PR #2](https://github.com/cortexium-io/runner-delivery-qualification/pull/2)
  changed the workflow to push-only and merged under the existing protection.
  Alpha refreshed onto that base; beta later refreshed onto alpha's merge.
- Both synchronization pushes briefly exposed different branch and PR heads.
  Runner stopped safely, and one explicit `task delivery --reconcile` per child
  confirmed the settled head. No polling or retry behavior was added.
- The stale PR base OID blocked alpha's CI. After the narrow adapter fix,
  drain/rebuild/restart and `task assess-ci` reused its accepted candidate and
  review. Both integration routes then ran on the corrected adapter. No paid
  role call was repeated for this adapter-only fix.

**Retained evidence and checks.** Private evidence is under
`/Users/dan/.codex/runner-qualification/r1-20261016`: configuration, quality and
Plan inputs, the fixture Git repository, private state/attempt logs and an
`evidence/` directory with proposal/status snapshots, protection, PR identities,
exact receipts, native calls, command results and build provenance. The qualified
binary SHA-256 is
`925038dd6011992eb7d81cbf96acf3fca3b963690eb4483d9f2fc81a846f7cb2`;
`build-provenance.json` binds the source commit and retained adapter patch.
Parent ID is `b8abd0e08caea775764c698b2e876327`; alpha is
`ddb856ca2f0e2b002b3c81a886b82788`, beta is
`da118c16d37ea1579f7b7933c7d9b844`.

Eight actual native calls ran (two plans, two implementations, four audits),
including the unreleased first proposal. Planning started at 06:39:41 UTC and
both integrations were confirmed by 06:56:51 UTC; this elapsed time includes
fixture correction, diagnosis and operator work. Provider-reported token usage
is retained per call; monetary cost is unknown. Environment: macOS 27.0 arm64,
Go 1.27.1, Apple Git 2.54.0, gh 2.94.0 and Codex CLI 0.162.1.

Passed focused GitHub race tests, `go test -race -timeout 15m ./...` (22 packages),
`go vet ./...`, building `cmd/runner-next` into the retained fixture binary,
formatting and `git diff --check`.
The full gates were repeated after the adapter fix; Go reused valid unaffected
package results, including the earlier 585.698-second engine suite. Complete
diff review found no extra architecture or test machinery.

This closes R1 for the frozen strict-classic-protection contract. Readiness is
still external; settled CI and remote reconciliation can require explicit
commands. Duplicate required identities remain unavailable. Queues, ruleset-only
Runner-owned admission, squash/rebase, polling and ready-state writes remain
unsupported. R2–R5 retain their own backup/restore, complete operator journey,
packaging and native-platform obligations. Stop here before R2.

#### R2 implementation and verification brief

**2026-10-16, before implementation.** Add explicit `backup --to DIRECTORY` and
`restore --from DIRECTORY [--apply]` commands to `runner-next`. Restore previews
by default; apply restores only to the original paths, all of which must be
absent. Never overwrite an existing installation or unrelated files. Stop before
R3's complete operator journey.

- Back up only a drained installation with no coordinator, active command,
  occupying reservation or surviving recorded process. Hold an exclusive
  maintenance lock while capturing a consistent SQLite snapshot, configuration,
  private evidence, repository/Git administration, task workspaces and retained
  role inputs. Keep a private directory bundle with a versioned manifest and
  hashes; publish the manifest last. No archive framework, online snapshots,
  relocation, cloud storage or cross-version conversion.
- Require the same binary, store format, host, user and original paths. Validate
  the complete bundle, SQLite integrity and referenced artifacts before install.
  Restore staged data without replacement and expose authority last. Interrupted
  installation must stay unusable and preserve enough information to inspect and
  retry without deleting unrelated data.
- Keep one small maintenance/freshness guard beside the state directory, outside
  the backup. Normal database users hold shared ownership; writable access and
  configuration saves durably invalidate its snapshot token before any effect.
  Backup/restore take exclusive ownership. This is a rollback fence, not another
  state store or action ledger. Preserve this original guard separately; copying
  it from an old backup cannot establish freshness.
- A matching untouched guard permits a drained restore of the same authority.
  If the guard is missing, replaced or invalidated, restore only for inspection:
  all writes, execution, policy changes and task recovery remain disabled. There
  is no override that guesses later consumption or remote effects. Explicit
  recovery then means reconcile retained/remote evidence and initialize fresh
  authority at distinct paths, with newly specified and approved work. Keep the
  historical counters and approvals visible; never resume them as current.
- Restored Git directories have new filesystem identities. Validate their full
  copied contents and retained workspace bindings, then record the new physical
  binding while preserving the logical repository identity used by task, quality
  and approval records. An ordinary checkout replacement must still fail identity
  validation. A new store format prevents older binaries ignoring recovery holds;
  no migration of previous experimental stores is included.

Representative evidence: a real Git/SQLite round-trip preserves tasks, versions,
quality history, approvals, counters, saved results, candidates and consumed
publication/merge markers; read-only inspection preserves freshness; a subsequent
mutation invalidates it. Stale restore after a recorded external effect or
revocation stays inspection-only and cannot repeat a write or regain permission.
Reject running ownership, incomplete/corrupt or mismatched bundles, missing
candidate/evidence files and occupied destinations. Exercise interruption before
activation and verify retry/cleanup boundaries. Extend the owning store,
workspace and CLI suites; use one focused filesystem bundle suite for the new
boundary. Reuse delivery ambiguity/revocation tests rather than copy them. No
paid role calls are required because role prompts and model execution do not
change. Run race/vet/build gates, review the complete diff and document the exact
recovery procedure and retained limitations here.

#### R2 backup and recovery procedure

Use the **same Runner binary**, host, OS user and absolute configuration path
for creation and restore. This bundle uses store format **14**. Preserve older
experimental stores with their matching binaries; no format conversion is
included. The R1 fixture and its format-13 evidence remain unchanged.

1. Finish active commands, stop `status --watch`, and drain the coordinator:

   ```sh
   runner-next --config /absolute/path/runner.json drain
   runner-next --config /absolute/path/runner.json status
   runner-next --config /absolute/path/runner.json backup --to /absolute/path/new-backup
   ```

   Keep editors, Git tools and other programs from writing these roots while
   copying. The command refuses active ownership, unresolved process groups or
   reservations, missing recorded inputs and invalid accepted candidates. A
   failed launch that never produced an output does not invent a required file.
   The destination must be new and outside all source roots.

2. Retain the complete private backup directory and the exact binary separately.
   `manifest.json` is written last; an absent, truncated or mismatched manifest
   is an incomplete backup. The manifest binds file contents, file/directory permissions and literal
   symlink targets (link permissions are platform-dependent). The bundle contains the consistent SQLite image, exact config
   file (including unapplied edits), state/evidence, repository and Git
   administration, workspaces and recorded role inputs. Host credential stores
   and installed tools are not collected; files already inside captured roots are
   copied as-is. Preserve required tool installations
   separately.
   Symlinks are copied without following them; special files and Git object
   alternates are unsupported. The manifest has a 64 MiB limit. This is a private
   local copy, not an encrypted or
   signed portable archive.

3. Keep the original `STATE_DIR.guard` file **in place**, beside the state directory.
   It is intentionally excluded from the backup. Read-only inspection preserves
   its snapshot token. A writable command, config save, newer backup or restore
   invalidates/supersedes that token before it can change authority. A failed
   writable command may conservatively make a snapshot inspection-only too.
   Never copy an old guard back to claim freshness. Losing the guard, including
   losing the whole disk, permits historical inspection only.

4. Inspect the restore preview, including every original destination and blocker:

   ```sh
   runner-next --config /absolute/path/runner.json restore --from /absolute/path/new-backup
   ```

   The config file may be absent; restore reads it from the bundle. Existing paths
   are listed as blockers. With the installation stopped, preserve any current
   files by moving each listed root aside to a distinct name. Do not move or
   replace the guard. **Every listed destination must be absent**, including
   retained temporary execution inputs; their parent directories must exist.
   Restore never removes old data or creates missing mount/parent hierarchies.

5. Repeat the preview, then apply only to those original paths:

   ```sh
   runner-next --config /absolute/path/runner.json restore --from /absolute/path/new-backup --apply
   runner-next --config /absolute/path/runner.json status
   runner-next --config /absolute/path/runner.json task show TASK_ID
   ```

   An unchanged original guard gives `inspection_only: false`. Restore validates
   contents, installs without replacement, checks native Git/workspace bindings
   and starts **drained**. It preserves task versions, quality and approval
   identities, counters, saved results and consumed delivery writes. Physical Git
   identity is rebound to the restored copy. Inspect pending configuration and
   retained work before explicitly issuing `run`; ordinary delivery recovery
   still inspects uncertain writes and never repeats a consumed merge.

   A missing, replaced or invalidated guard gives `inspection_only: true`.
   Historical approval and counters remain visible, but `run`, task mutations,
   config saves/applies and recovery cannot execute. There is no force/resume
   override. Reconcile the historical work with current remote/local evidence,
   then initialize fresh authority at distinct paths and specify/approve only
   work that still remains. A new backup of an inspection-only installation
   cannot remove its hold.

6. If applying is interrupted, inspect the listed destinations and any sibling
   `.runner-restore-*` directories. Automatic cleanup removes only this command's
   staging directories on a normal error; process death may leave them behind.
   Installed roots remain for inspection. Authority is installed last with a
   recovery hold, which is released only after all checks succeed. Move partial
   roots aside explicitly before retrying; nothing is overwritten. Once apply
   begins, the old snapshot's freshness token is consumed, so a retry remains
   inspection-only. An incomplete backup must be recreated from a valid drained
   installation; do not patch its manifest.

This deliberately bounded path does not provide online backup, relocation,
cross-version migration, automatic rollback, restoration of host credentials or
reconciliation of unknown later history. R3 will exercise it within the complete
operator guide; R4/R5 retain packaging and native-platform qualification.

#### R2 qualification

**Completed 2026-10-16 in `codex/runner-local-core`, after the brief above.**
Implementation and initial evidence are committed in `f561a5c`.
One offline backup package owns the directory bundle and restore operation;
the existing store owns SQLite snapshots and maintenance ownership, and the
existing workspace adapter validates Git. No scheduler, public plugin API,
archive dependency, role prompt or delivery mechanism was added.

| Evidence | Result |
| --- | --- |
| Real SQLite/Git round-trip | Preserves complete task/approval/counter/result history, quality revisions/reports, accepted candidate identity and consumed publication/merge records. Accepted work stays pinned to its original agreement after a newer default. The restored physical Git binding changes while logical authority remains stable. Ordinary checkout replacement still fails validation. |
| Freshness and ownership | Read-only inspection preserves the guard; writable opens and config saves invalidate it. Commands and maintenance exclude each other. A simulated external effect after backup plus task revocation leaves the restored historical approval inspectable but unusable. Missing/copied guards also force inspection-only recovery. |
| Failure boundaries | Rejects active commands/coordinator ownership, unresolved process groups, reservations, undrained state, missing candidate/evidence, incomplete/corrupt/incompatible bundles and existing destinations. Controlled interruption before authority installation and before activation cannot expose usable authority. A retry never overwrites partial copies or regains freshness. Native exclusive rename also refuses an existing empty directory. |
| Existing CLI journey extended | Recovers the same Plan/Ready, interrupted-review, configuration, dependency and agreement history already covered by the owning journey. Status/show retain records but remove approval/execution advice under a hold. Run, task recovery/mutation, config apply and config save cannot bypass it. The fake harness now creates its evidence directory, as the real adapter does. |
| Built binary, real native checks | Final rehearsal completed 30 commands in 3.80 seconds: initialization, a native baseline check, approval, preview, fresh drained restore, quality-report inspection and configuration application, followed by revocation and a stale inspection-only restore. The configured profile was `gpt-6.1-sol / high`; capability validation and sandboxed shell checks invoked no model. No GitHub writes were made. |

`go test -race -timeout 15m ./...` passed (replacement engine 608.920 seconds).
Final review added explicit snapshot
file syncing and exclusive ownership for restoration writes, corrected backup
freshness reporting, bounded the manifest consistently with its reader and made
symlink handling independent of platform link permissions. The affected backup,
CLI and store race suites passed again on the final source (backup about 57
seconds; CLI about 17 seconds). Final `go vet ./...`,
`go build -o /tmp/runner-next-r2 ./cmd/runner-next` and `git diff --check` passed.
The complete source/test/documentation diff was reviewed for scope, recovery
safety and unnecessary machinery. Existing delivery tests continue to own
uncertain-write reconciliation and revocation; those journeys were not copied.

Environment: Go 1.27.1, Git 2.54.0 (Apple Git-157), macOS arm64. Final native
commands, snapshots, matching binary and summary are retained privately at
`/Users/dan/.codex/runner-qualification/r2-20261016-release-boundary`;
logs are `/tmp/runner-r2-race.log`, `/tmp/runner-r2-boundary-final.log`,
`/tmp/runner-r2-vet-final.log` and `/tmp/runner-r2-native-release.log`.
Earlier rehearsals remain in the neighboring R2 directories. One temporary
driver used the wrong JSON field (`preview` instead of `apply_preview`) and was
corrected; this was not a Runner failure. Rehearsals after boundary fixes used
fresh fixtures and their matching binaries. Every rehearsal made zero model
calls and zero GitHub writes. The temporary driver is not shipped.

R2 is complete only for this same-host, same-binary, original-path contract.
Native Linux/runtime packaging qualification remains R5 work; no cross-platform
runtime success is claimed here. R1's store/binary and remote fixture are
preserved. Stop before R3's complete operator journey and R4 runtime promotion.

#### R3 implementation and verification brief

**Authorized 2026-10-16; define this boundary before qualification and fixes.**
Qualify the existing `runner-next` operator contract from fresh, separate local
paths. Publish one concise replacement guide covering setup, repository quality,
Plan/Ready approval, daily operation, delivery and recovery. Link it from the
README and the legacy operator reference without promoting or renaming the
runtime; that remains R4. The guide must distinguish a passing local candidate
from confirmed integration and show the cause and next action at each wait.

Representative expected results and evidence:

- Fresh build/configuration and native sandbox baseline establish drained local
  authority without a Project. The guide's command syntax, preview fields and
  required tools work with the current binary. Record a fresh native rehearsal
  and retain its exact binary, inputs, outputs and elapsed time.
- Extend the existing CLI journey only where observable advice lacks coverage:
  Ready approval; Plan question answered by versioned replacement and approval;
  proposal inspection/release; dependency wait; interrupted audit resumed with
  explicit invocation permission; preview/save/apply of model, reasoning,
  escalation, retry and parallelism limits; drain/restart; fresh and stale restore.
  Use its controlled harness for deterministic questions and interruptions, with
  real Git/SQLite/filesystem state. Do not present this as new live model evidence
  or build a second journey framework.
- Reuse unchanged live role qualification and R1's real GitHub ordinary-merge
  receipts for delivered-result inspection, dependency/parent completion and
  automatic policy. Inspect retained results with their matching binary; no new
  remote mutation or paid role call is necessary unless a discovered change
  invalidates that evidence. Record reused call counts separately from new calls.
- The guide makes testing ownership concrete: repository policy defines required
  checks, author/planner supplies expected behavior and the verification brief,
  implementer extends sufficient existing coverage, independent review checks
  adequacy. Explain focused verification, when no new test is needed and why a
  repeated check or invocation occurs. Unknown cost/usage stays unknown.
- R2 recovery is exercised from the guide, including occupied destinations,
  drained continuation and an inspection-only stale snapshot. Preserve existing
  roots and the original guard; no overwrite, migration or recovery override.

Fix only concrete supported-flow defects found during this pass and add the
smallest regression in the owning suite. Run focused CLI/affected checks, the
full race gate with the qualified 15-minute timeout, vet, build and diff review.
Record commands, elapsed time, interventions, repeated verification and actual
model/remote calls here. Stop when the documented journey has no unresolved
blocker; no package promotion, legacy retirement, installation change or R4 work.

#### R3 qualification

**Completed 2026-10-16, `codex/runner-local-core`, based on `59515c5`.**
The guide, owning test changes and qualification are committed in `ef94956`. The
[replacement operator guide](operator-reference.md) is the single current setup
and command reference through delivery and restore. README points there directly;
the repository-local operator reference now links to it and preserves the
authorization boundaries. Its obsolete claim that integration was unsupported
is removed. Historical bundle notes and the legacy runtime remain separate.

No production code, role prompt, execution behavior or store format changed.
The owning CLI journey gained small assertions for visible apply advice,
configured limits, audit recovery permission/candidate retention, local delivery's
incomplete status, and the named question/dependency recovery paths. No new suite,
fixture framework or test dependency was introduced.

| Journey evidence | Observed result |
| --- | --- |
| Fresh native setup and quality | Built the current binary, copied the guide's JSON/task examples with fresh absolute paths, initialized drained state and ran the real Codex sandbox baseline. The quality preview/check/report/apply sequence used the printed fields successfully. |
| Native intake and policy | Ready and Plan intake/approval were inspected, then stopped before model execution. The documented editor preview/save/explain/apply worked for parallelism, repair/retry/refresh/invocation limits and the named escalation profile. Saved policy remained visibly unapplied until application. |
| Complete controlled CLI journey | Existing real Git/SQLite/filesystem journey passed in 15.24 seconds under race detection: Ready implementation; interrupted audit; pinned-profile resume without reimplementation; Plan question, versioned answer and release; exact prerequisite wait; policy changes during review; quality adoption; fresh/stale restore. Five controlled role calls (one implementation, two audits, two plans) exercised these deliberate outcomes, not live model behavior. |
| Watch, drain and recovery | Native watch was interrupted independently. Foreground run/drain completed before and after a fresh restore. Preview identified occupied destinations; moving only those fixture roots aside permitted drained recovery with history intact. Later work invalidated another snapshot; restoring it visibly blocked execution and mutation as inspection-only. |
| Delivered result inspection | Read R1's parent, alpha and beta with its matching format-13 binary, using only status/show. Both children displayed Completed, exact confirmed merge receipts and their PRs; the parent displayed all original children integrated. Human/default and applied automatic-policy receipts remain valid. The new format-14 binary did not open or migrate R1's state. |

The native rehearsal took **5.94 seconds**, including receipt inspection: **60
Runner commands** (53 current-binary commands and seven R1 read-only commands),
plus six Git setup/provenance commands. It used zero new model calls and zero
GitHub requests/writes. R1's eight live `gpt-6.1-sol / high` calls (two plans,
two implementations, four audits) are reused; their original 17-minute-10-second
journey, interventions and reported token usage remain recorded above. Monetary
cost is unknown. This is composed qualification with fresh native operations,
controlled failure cases and retained live delivery evidence, not a claim that
every event was reproduced in one new live session or a human usability study.

Interventions were the documented setup/approval/policy decisions, explicit audit
repeat permission, clarification and release, interruption of the read-only
watch, and preservation of listed roots during restore. Native tasks were stopped
deliberately before execution to reuse existing live evidence. No database edits,
counter resets, manual lifecycle transitions or undocumented rescue were needed.
The controlled audit repeated once because its first invocation was interrupted;
implementation did not repeat. R1's repeated audits were caused by changed-base
candidates. No paid behavioral evaluation was repeated because instructions and
execution were unchanged.

Private evidence is retained at
`/Users/dan/.codex/runner-qualification/r3-20261016`: exact binary, guide snapshot,
configuration, quality/task inputs, full command outputs, summary, version facts,
restored/moved fixture roots, backups and delivered-result text. The matching
binary SHA-256 is
`1dcb3c847c5bb666b3ea4b8874204eff5a9b86831da83fd1a1f2e084d08c19f1`.
Environment: macOS 27.0 arm64, Go 1.27.1, Apple Git 2.54.0 and Codex CLI 0.162.1.
The native driver is `/tmp/runner-r3-qualify.py`; it is qualification material,
not a shipped second workflow. Logs are `/tmp/runner-r3-native.log`,
`/tmp/runner-r3-cli.log`, `/tmp/runner-r3-race.log` and `/tmp/runner-r3-vet.log`.

Passed `go test -race -v ./internal/runner/cli` (18.074 seconds),
`go test -race -timeout 15m ./...` (replacement engine 661.286 seconds),
`go vet ./...`, `go build -o /tmp/runner-next-r3 ./cmd/runner-next`, guide shell
syntax, relative file links and `git diff --check`. The complete diff was reviewed
for accuracy, scope, redundant tests and unnecessary machinery. The full gate
repeated the focused CLI suite as required; there was no failure-driven rerun.
No unresolved blocker remains in the documented R3 journey. Stop before R4.
Runtime promotion, packaging and native Linux/final cutover qualification remain
R4/R5 obligations. No production installation or remote repository was changed.

#### R4 implementation and verification brief

**Authorized 2026-10-16; define this boundary before promotion or retirement.**
Make `cmd/cortexium-runner` the single entry point for the qualified local engine.
Retire `cmd/runner-next`, the Project runtime and its obsolete commands, adapters,
setup/skill installation, updater integration and tests. Preserve the useful
Git, process, sandbox-path and private-filesystem primitives with their owning
coverage. Remove the unused repository-reference helper and old publication journal,
merge-method/refresh and task-cleanup APIs that pull legacy configuration or
retired lifecycle behavior into the replacement's dependency graph. Do not
reorganize the qualified coordinator/store or introduce a compatibility wrapper.

Supported behavior and representative expected results:

- The built and packaged `cortexium-runner` reports its injected version, shows
  local command help without configuration, and routes explicit configuration to
  the existing CLI. Legacy commands/configuration fail without creating authority.
  Remove temporary command/branch/ref names from current runtime surfaces. Use a
  new store format for this deliberately breaking namespace boundary; preserve
  experimental stores under their matching binaries, with no in-flight import.
- README, architecture, the consolidated operator guide, example configuration,
  role reference and contributor/security instructions describe only the current
  supported runtime. Preserve accepted decisions and historical qualification as
  history, identifying superseded paths rather than advertising removed commands.
  The qualified role prompts stay in the execution adapter; remove obsolete
  installed skill machinery rather than create another prompt authority.
- Release archives retain one executable and the license, with the current guide
  and example available offline. Extend the existing archive/installer tests for
  that exact content and command contract. Keep HTTPS/checksum/version validation
  and the reviewed-source, immutable-artifact and isolated-publish gates. No new
  updater design, service management or automatic installation migration.
- Replace the Project smoke fixture with a small fresh local CLI/package journey:
  version/help, strict setup preview, rejection of legacy input, and read-only
  missing-state inspection. Exercise the real built entry point and verified
  archive contents; reuse the owning CLI journey for model execution, questions,
  recovery and delivery rather than building another fake harness framework.
- CI/readiness run race tests with the qualified 15-minute package timeout and
  adequate job time. Remove obsolete Doctor/live-probe options; native sandbox,
  model and GitHub qualification remain explicit R5 evidence, never implied by
  cross-builds. Update the opt-in behavioral evaluation launcher for the existing
  replacement evaluations without adding or running paid cases unnecessarily.

Verification: focused entry-point, workspace and packaging/installer/security
tests; `go test -race -timeout 15m ./...`; `go vet ./...`; module verification;
format/shell/diff checks; the new native smoke journey; four `CGO_ENABLED=0`
archives (`darwin/amd64`, `darwin/arm64`, `linux/amd64`, `linux/arm64`) with an exact
asset/checksum set and inspected documentation. Installer tests use private
temporary destinations only, never the user's installation. Record source,
commands, artifacts and any limits here. Review the entire removal/change diff
for remaining consumers and lost guarantees. Stop at a reviewable local release
candidate before R5: no tags, publishing, remote mutation, installation changes
or claim of final native Linux/cutover qualification.

#### R4 qualification

**Completed 2026-10-16 in `codex/runner-local-core`, based on `5897f4c`.**
The single entry point is now `cmd/cortexium-runner`. Help/version need no config;
the existing local CLI owns the workflow. Current branch/ref names use `runner/`
and `refs/runner/`. Store format 15 rejects earlier authority without importing
approvals. Existing installations, binaries, state and qualification fixtures
were not changed.

Retirement review and retained guarantees:

| Retired surface | Current owner or explicit release limit |
| --- | --- |
| Project CLI, configuration, setup, coordinator and adapters | `internal/runner/{cli,config,core,store,engine,github}` and their owning suites. Exact approval, reservations, revocation and uncertain remote effects remain covered. |
| Legacy execution and bundled installed role skills | Qualified `internal/runner/execution` contracts and paired behavior cases. Prompts and sandbox policy are unchanged. Additional harnesses remain deferred. |
| Workspace publication journal and old refresh/merge-method APIs | Transactional publication bindings and engine delivery/refresh/merge tests, including changed-candidate refusal and lost-response recovery. Ordinary candidate construction and low-level identity/index/object protections remain. |
| Old task cleanup/resource API, reference helper, metrics/guidance, service/updater and automatic intake paths | No consumer in the frozen local runtime. Task artifacts remain retained; additional references, automatic intake, services and a learning subsystem are outside this release. Disposable review cleanup and process/private-filesystem tests remain. |

The CLI journey now rejects unknown/retired commands before opening writable
authority. The previous-store test includes format 14. Existing archive and
installer suites cover offline files, executable permissions and rejection of
unexpected entries; release-security tests retain reviewed-source and isolated
publication checks. No new orchestration fixture or test framework was added.
An initial race run exposed a missed format-14 SQL initialization value; the
schema/insert were corrected and the store suite passed before restarting the
full gate. The failed run is retained as evidence, not counted as qualification.

Verification on macOS arm64 with Go 1.27.1, Git 2.54.0 (Apple Git-157) and
Codex CLI 0.162.1:

- `sh scripts/test-release-readiness.sh` passes: uncached
  `go test -count=1 -race -timeout 15m ./...` (engine 653.823s, backup 61.519s,
  CLI 20.277s), `go vet ./...`, pinned `govulncheck@v1.6.0`,
  `go mod verify`, smoke and four no-CGO builds/checksums. Govulncheck reports
  zero reachable findings; it also lists advisories in one imported package and
  twelve required modules whose vulnerable symbols Runner does not appear to call.
- Focused store, packaging/installer/security and retained workspace suites pass.
  Final workspace test-helper cleanup and archive test consolidation received
  focused race runs; expensive engine verification was not repeated for those edits.
- Four retained `v0.0.0-r4` archives pass checksums, exact five-file content,
  byte-for-byte guide/example comparison, executable/architecture checks and
  `CGO_ENABLED=0` metadata inspection. The extracted macOS arm64 executable
  passes version/help, local setup preview, legacy-input/command refusal and
  read-only missing-state inspection.
- Format, shell syntax, relative documentation links and complete diff/dependency
  review pass. No legacy Project/config/skill package remains in the binary graph.
  The live-evaluation wrapper's help, syntax and dirty-source refusal were checked
  without model calls.

Evidence and archives are retained under
`/Users/dan/.codex/runner-qualification/r4-20261016`: `readiness.log`,
`readiness-initial-failed.log`, focused logs, `archive-inspection.log`,
`packaged-smoke.log`, `source.txt`, `source-manifest.json` and
`release/SHA256SUMS`. `source.txt` records the final local commit.
The readiness build is disposable; the separately retained build has the R4
candidate version and inspected offline documents.

No paid model invocation, GitHub mutation, publication, tag, service or operator
installation change occurred. Installer tests wrote only temporary destinations.
R1–R3 evidence remains historical support for unchanged role and delivery behavior;
R5 must assess its applicability to the promoted candidate, run native Linux and
packaged journey checks, and rehearse cutover. Stop before R5.

#### R5 implementation and verification brief

**Authorized 2026-10-16; baseline candidate `0a2668d`.** Qualify the promoted
replacement and rehearse fresh cutover only in isolated test roots. The owner
selected a disposable Docker Linux environment; record the actual Linux kernel,
image, architecture, user and sandbox/container settings rather than treating a
cross-build as native execution. Keep the frozen feature scope. Stop release-ready;
no production installation, service, publishing, tag or remote protection change.

Supported outcomes and verification boundary:

- Identify one source candidate and the four checksum-verified release archives.
  Reuse R4's macOS race/vet/module/vulnerability evidence where source identity is
  unchanged; run the native Linux race/vet gates and actual packaged commands.
  Rebuild/recheck artifacts or rerun invalidated evidence if a production fix is needed.
- Extend the existing opt-in native quality-check test for explicit denied
  outside-root read/write, denied loopback network access and bounded sandbox
  timeout. Existing process-group interruption tests run on both operating
  systems. A missing native capability blocks qualification; never replace the
  Codex sandbox with a stub or disable its containment to obtain a pass.
- Verify actual installation into dedicated test-only binary directories.
  Exercise fresh setup, quality preview/check/apply, Ready/Plan intake and exact
  approval, policy preview/save/apply, run/drain/restart, status and backup/restore
  using the packaged entry point and guide. Reuse the owning controlled CLI
  journey for deliberate question/dependency/interrupted-review cases, and the
  unchanged live role evidence instead of buying repeated behavioral matrices.
- Rehearse an old matching-binary owner shutting down, a consistent preserved
  backup, fresh format-15 setup at distinct paths, and explicit resubmission with
  new approval. Do not copy authoritative state or reactivate the old owner.
  A newer mutation/effect must leave an older snapshot inspection-only; verify
  refusal to run or mutate it. Preserve the original guard and all moved roots.
- Review R1's live human/automatic delivery receipts against current contracts;
  only repeat live delivery if a subsequent change invalidates that proof. Any
  needed model invocation uses `gpt-6.1-sol / high`; any remote write is limited
  to the already authorized private delivery-qualification repository.
- Finish with concise release notes and exact backup/shutdown/install/fresh-setup/
  resubmission/verification instructions. Identify remaining unsupported modes,
  irreversible remote effects and why old authority must stay stopped. Record
  actual calls, elapsed time, operator interventions and evidence reuse. Unknown
  cost remains unknown.

The existing CLI, store/backup, workspace, execution/process, archive/installer
and release-security suites own these guarantees. Add no new workflow framework
or duplicate test registry. The native additions address concrete containment
evidence missing from the existing opt-in check. Review the entire final diff
and candidate/artifact identity before closing the R5 box. Missing Linux
infrastructure or an unresolved required behavior leaves R5 open even when all
independent work is complete.

#### R5 qualification — complete

**2026-10-16.** The frozen replacement is release-ready within the boundaries
below. No production code change was needed. The owning opt-in execution suite
now proves host filesystem containment, network isolation and actual deadline
cleanup; the operator guide includes release notes and the ordered
[breaking cutover procedure](operator-reference.md#8-breaking-release-and-cutover).
No new package, dependency, workflow framework or test suite was introduced.

**Candidate and artifacts.** Packages were built from `5446dbe`, whose production
code is byte-identical to R4 `0a2668d`. The closing change only strengthens the
native deadline assertion and records qualification; both native suites were
rerun, and executable/package inputs remain identical. Retained source manifests
and artifact metadata bind those inputs to the final tree. The private evidence
root is `/Users/dan/.codex/runner-qualification/r5-20261016`.
`v0.0.0` is a local qualification label, not a published release or Git tag.

| Archive target | SHA-256 |
| --- | --- |
| `darwin-amd64` | `29fc74f1f6ca387651b1205af76ce4a179d1177de92fdfe4b3486964d9ad172a` |
| `darwin-arm64` | `8387cc7519ae39d162008d1f678ead9536d4e80afe081ed4b21acfbabc1c97a3` |
| `linux-amd64` | `5fba30975ea4f14086fb29b10fb1afa72b8340092e9fe227ed610689fe41a320` |
| `linux-arm64` | `595dcffd89b54739102bbd6162c25135394c2e7e3673cb0e7ea9f4f1306694b7` |

`release/SHA256SUMS`, `artifact-inspection.json` and the install summaries verify
four target formats, `CGO_ENABLED=0`, exact five-file archive membership and
byte-identical packaged instructions. Both native arm64 binaries report
`cortexium-runner v0.0.0`; Linux amd64 also passes a translated version probe.
macOS amd64 is cross-built only: this host has no x86 translation runtime.
There is no claim of native amd64 qualification on either OS.

**Native environments and gates.** macOS 27.0 arm64 used Go 1.27.1, Apple Git
2.54.0 and Codex CLI 0.163.0. Linux used Ubuntu 24.04 arm64, kernel
`7.0.14-linuxkit`, Go 1.27.1, Git 2.43.0 and Codex CLI 0.163.0 installed through
its pinned npm package. The owner-selected disposable Docker environment ran
as UID 1001 with `--init --security-opt seccomp=unconfined`, default capabilities,
no privileged mode, no host mounts and no copied credentials. Only the non-secret
model catalogue was copied for capability validation. The image identities,
Dockerfile and exact container settings are retained. This qualifies actual
Linux processes/kernel behavior inside Docker Desktop, not arbitrary bare-metal
hosts or Docker's default seccomp profile. Both test containers and their custom
image were removed after evidence capture.

Passed macOS `go test -race -timeout 15m ./...` and Linux
`go test -count=1 -race -timeout 15m ./...`, vet and module verification.
The current native execution suite passed with `RUNNER_LOCAL_LIVE_CHECKS=1`
on both platforms, including process-group interruption and pre-launch ownership.
Linux's complete engine suite took 74.781 seconds; macOS took 712.736 seconds.
R4's uncached macOS and pinned vulnerability evidence remains valid for unchanged
production inputs. A fresh Linux `govulncheck@v1.6.0` found zero reachable
vulnerabilities; it reported one imported-package and twelve required-module
advisories whose vulnerable symbols are not called. Formatting, shell syntax,
archive/installer/publication safeguards, CLI smoke and final diff checks passed.

The first Linux probe correctly failed under Docker's default namespace policy.
A container without an init process retained a terminated child and Runner
refused to release ownership; adding the init reaper resolved that environment
limitation. Linux can hide ungranted host paths behind private scratch files:
the initial syscall-only write assertion was too strict. The final test checks
both inability to read the host fixture and absence of any host modification or
creation, while an in-workspace write succeeds. It also proves the loopback
fixture works outside containment and is inaccessible inside. No Codex sandbox
policy was weakened. Early failures and final native process/result logs remain
in the evidence root; four native executions per platform reflect these fixture
and assertion checks, not repeated paid evaluations.

**Packaged journey and cutover.** The real HTTPS/checksum installer fetched the
actual archives from a loopback TLS fixture into isolated binary directories on
both OSs, followed by the packaged CLI smoke. The guide then passed fresh init,
quality preview/check/apply, Ready and Plan approval, policy preview/save/apply,
status/watch, run/drain/restart, fresh restore and stale inspection-only restore.
The old format-14 matching binary completed 58 recorded commands in 5.12 seconds;
the packaged macOS journey used 58 in 6.85 seconds and Linux 60 in 2.31 seconds.
Installer runs took 1.57 and 1.16 seconds respectively. These are local scripted
qualification timings, not Ready-to-merged throughput claims.

The old test owner exited before fresh setup; its exact binary, consistent
backups, moved roots and guard are retained. The same unfinished human brief was
resubmitted under a new task identity and exact approval in separate format-15
roots. The new binary refused the old format-14 store. All test authorities
were drained and deliberately left inspection-only: a later mutation invalidated
the snapshot, and restored `run` and task mutation were refused. This rehearses
old local-core to fresh local-core cutover; it does not claim that an actual
legacy Project service was switched. Independent old installations are not
automatically revoked by new roots; the documented old-owner shutdown remains
essential. The R2 owning regressions, rerun on both OSs, cover a recorded remote
effect after backup as well as local mutation.

**Live evidence reuse and interventions.** R1's role instructions, Codex execution
contract and GitHub adapter are unchanged. R2 adds guarded recovery and R4 changes
branch/ref names; the current owning suites cover those changes. Three read-only
GitHub requests reconfirmed alpha PR #1, beta PR #3 and strict required-check
protection in the authorized private fixture. Their candidate and merge SHAs
still match the exact R1 Git receipts. R1's eight actual `gpt-6.1-sol / high`
model calls remain the live role/delivery proof; R3's controlled owning journey
and the current CLI suite cover questions, dependencies and interrupted review.
Native submitted tasks were stopped before model execution. R5 made **zero new
model calls and zero remote writes**; monetary cost was not measured.

Operator interventions were fresh paths/Git identity, returned approval previews,
explicit stopping before paid work, interruption of read-only watch, moving only
listed restore destinations while retaining the guard, and deliberately adding
later work to exercise the stale hold. Qualification drivers and command results
are retained outside the repository; no parallel task system was added. Normal
local operation needs no `gh` or Project calls, also demonstrated in the Linux
fixture without `gh` installed. No unsupported feature was added to finish R5.

All five release boxes are closed. Stop release-ready: there was no push, tag,
publication, production install, service change or change to remote protection.
Publication must choose its real version and use the existing reviewed-main,
immutable-artifact release workflow; the local `v0.0.0` fixtures are not a
published version. Installation/cutover remains a separate authorized action.

#### Required verification

Each implementation bundle uses the existing owning suites and the repository's
brief-first testing policy. Add a regression only for a concrete uncovered defect;
consolidate overlapping coverage while retaining distinct guarantees. Review the
complete diff for unnecessary architecture and test machinery. Run focused checks
while editing, then `go test -race -timeout 15m ./...` and `go vet ./...` at the
bundle boundary. The fifteen-minute package timeout is the measured budget already
qualified above; it changes neither assertions nor operation deadlines.

R4 updates the existing [release-readiness script](../scripts/test-release-readiness.sh),
[smoke test](../scripts/release-smoke-test.sh), [build script](../scripts/build-release.sh)
and [CI workflow](../.github/workflows/ci.yml) to exercise the promoted local
runtime. R5 runs the updated checks against the exact reviewed candidate:

- Formatting, shell syntax and `git diff --check`; uncached
  `go test -count=1 -race -timeout 15m ./...`, `go vet ./...`, `go mod verify`,
  and the readiness script's pinned `govulncheck` invocation.
- The native macOS/Linux test and packaged-runtime matrix, including the actual
  containment and interruption guarantees. Cross-compilation alone proves no
  native runtime behavior. Record OS/architecture, Go/Git/Codex versions and any
  limitations; missing required platform evidence is a release blocker.
- Four archives (`darwin/amd64`, `darwin/arm64`, `linux/amd64`, `linux/arm64`) with
  `CGO_ENABLED=0`, correct version output, exact asset/checksum set and verified
  installation. Retain existing archive/checksum and
  [reviewed-source publication safeguards](../.github/workflows/release.yml).
- Current live delivery/operator evidence, successful restore/cutover rehearsal,
  and confirmation that normal local operation makes no Project calls.

Run expensive passing checks again only when the candidate or unresolved evidence
requires it. Record the commit, command, environment, result and retained evidence
location here; do not create a new test registry. A provider outage is an
uncompleted live check, not a code failure or a pass. Do not repeat paid calls
merely to populate a matrix when valid evidence already covers the claim.

#### Release finish line

The first replacement is **release-ready** when every item below is checked with
evidence. R1–R5 are now closed above. Construction completion alone did not
imply release qualification; the recorded R5 gates establish this finish line.

- [x] R1: real human and Runner-owned protected merges have exact receipts; frozen
  manual handoffs and unsupported host configurations are documented.
- [x] R2: consistent backup/restore and stale-snapshot handling are verified;
  recovery cannot duplicate ownership, reset known consumption or replay writes.
- [x] R3: the full documented operator journey works without undocumented rescue;
  configuration, test ownership, interruption and recovery are understandable.
- [x] R4: one replacement binary, accurate instructions and verified packages
  remain; obsolete runtime paths and their superseded tests are retired.
- [x] R5: the reviewed candidate passes the required gates and cutover rehearsal;
  artifacts and evidence identify that candidate, with no unresolved defect in
  a required safety, supported-behavior or operator acceptance condition.

At that point feature construction is finished. Publication/installation is a
separate authorized action using those verified artifacts, not another feature
bundle. Deferred features are not release blockers and stay deferred after a
successful rehearsal. If qualification finds a defect, repair it in the owning
bundle and rerun the invalidated evidence; do not restart the redesign or expand
scope. A change to the frozen contract requires an explicit owner decision here.

### First implementation slice

**Outcome:** on a local repository, a user configures Runner, supplies and
approves a Ready task, follows its implementation and independent review, and
inspects a retained reviewed candidate after restart. The final stage is
**Awaiting delivery — local review complete; delivery is not configured**.
This is a useful internal milestone, not the first public release or successful
integration. It must make no GitHub/board calls and require no `gh` installation.

The prerequisite is this accepted plan. The slice owns the new local command
path, minimal config/setup, identities, store, coordinator, one Codex harness
path, isolated Git workspaces, replacement implementer/reviewer instructions and
the evidence below. Use one invocation at a time, no automatic repairs/retries,
no planner, no dependencies and no remote delivery. Reject unsupported controls.
Require a supplied verification brief and a passing repository baseline; baseline
exceptions and changes to the quality agreement are phase 2 work.

#### Construction boundaries

Use a dedicated rebuild branch/worktree when implementation begins; preserve
the current checkout's unrelated work. Build an isolated `cmd/runner-next`
entry point with new responsibilities under `internal/runner/` following the
package map above. Keep the existing binary available until cutover. This is
build-time separation, not a runtime backend switch or compatibility layer.
At phase 4, retire the old entry point and promote the replacement to
`cortexium-runner`; do not retain a `next` alias or duplicate runtime.

Create only packages the working journey uses. In particular, do not scaffold
board adapters, a generic event bus, an empty plugin registry or every later
database table. Existing workspace/subprocess primitives may be reused after
checking their contracts. Do not import `internal/github` or the old coordinator
into the new core. Own replacement instructions separately from the currently
shipped `skills/runner-*` prompts until the old binary is retired.

Choose the SQLite Go driver as an ordinary implementation decision. First prove
transactions and reopen behavior with a real temporary database and compile the
new entry point with `CGO_ENABLED=0` for the four existing release targets. If
that fails, resolve the driver or packaging decision before building more on it;
do not quietly substitute a JSON journal. This is a bounded step inside the
slice, not a separate research project.

#### Command and task contract

The new `runner-next` entry point uses the explicit `--config PATH` selection.
Commands below are implemented there; the shipped `cortexium-runner` binary
continues to use its existing commands.

| Command | Required behavior |
| --- | --- |
| `init` | Guide or consume the new configuration, preview paths/profiles/quality checks, then apply setup on explicit confirmation. Preserve unrelated files and prior applied revisions. Reconfigure a compatible setup only while drained; reject incompatible state rather than migrate it. Run only authorized, needed baseline checks. |
| `config explain` | Read and explain the applied revision, actual profiles, local base policy and zero automatic allowances. No writes or model calls. |
| `task add --file TASK.md` | Capture an unapproved draft. Require Outcome, Scope, Acceptance and Verification sections; Verification contains the behavior, expected results, boundary and reuse choice. No dependencies in this slice. Structural validation does not replace the author's responsibility for a complete task. |
| `task show ID [--json]` | Show exact content/version/digest, quality revision, observed local base, authority, attempts and evidence. Supply a separate approval-preview digest. |
| `task approve ID --version N --preview DIGEST` | Revalidate and approve only the displayed version and preview binding. Pin the local base commit. Return the existing still-valid approval for an exact repeat, without a new attempt. A changed preview requires inspection again. |
| `task replace ID --version N --file TASK.md` | Compare the expected version, create an unapproved replacement and revoke subsequent old-version actions. Request cancellation of old active work; retain its artifacts and pending cancellation status. |
| `run` / `drain` | Acquire exclusive coordinator ownership and reconcile/start eligible work; drain records a durable admission stop and waits for active work to settle before the owner exits. A later explicit `run` clears drain after recovery. |
| `status [--json]` | Read-only view of attention, active work, waits and retained results, including whether the coordinator is running or drained. Show actual model/reasoning, stage, cause and next action. Never launch, retry, migrate or repair state. |
| `task stop ID` | Revoke authority before requesting cancellation. Return promptly with cancellation pending if work has not stopped. Never delete retained branches or evidence. |
| `task resume ID --attempt ATTEMPT [--allow-new-invocation]` | Target the displayed interrupted attempt and preview its stage, preserved artifacts and repeated work. Request reconciliation of valid saved results without another model call. A new call requires the explicit flag and valid authority, profile and prerequisites; it cannot repair a rejected candidate in this slice. Repeating a request for the same attempt cannot queue another invocation. |

Keep the immutable task content digest distinct from the approval-preview digest.
The latter binds task ID/version/content digest, repository/base-policy identity,
displayed quality revision and observed base commit. An unchanged requirements
file is not enough when those inputs changed. An already committed exact preview
can return its approval while that authority remains valid; it grants nothing
new. Capture imported text once. Editing its source file later has no effect
until an explicit replacement. Init applies only the supported configuration
contract; it cannot install new tools or invent credentials.

Task commands commit state or recovery intent. Only the owning `run` process
performs mutating task Git operations and task harness calls; a CLI must not bypass admission
rules. If the coordinator is absent, show that the request is pending and give
the command to start it. Setup's separately authorized baseline checks run in a
disposable copy of the observed base, preserving the user's checkout. Preview
that uncommitted checkout changes are not part of the pinned base.

JSON status/show expose the same facts as human output: task ID/version, stage,
wait/attention reason, active attempt/profile, base/candidate identities,
preserved evidence, cancellation state and allowed next action. Report missing
values as absent/unknown, never as successful zero values. Inspection can explain
a damaged or missing store but must not create a replacement authoritative store.

#### Execution and recovery

1. Setup validates repository, private state and workspace locations, native
   harness capabilities and the quality agreement. Use safe native sandbox
   profiles; unrestricted host execution is not part of this slice.
2. Approval and admission follow the core contracts. Record the attempt and
   resources before launch. Keep subprocess work outside transactions and off
   the coordinator's reconciliation path. A second owner cannot run, and a
   second reservation cannot claim the same task/workspace.
3. The implementer receives the approved task, verification brief, quality
   revision and pinned base. Preserve the user's checkout. Runner records a
   validated completed harness result durably before candidate post-processing,
   with the workspace/content binding needed for safe reuse.
4. Construct an immutable candidate and launch independent evidence audit.
   When a concrete proof gap requires dynamic verification, use the existing
   focused-verification contract in a disposable candidate copy. The reviewer
   cannot edit canonical source or weaken the task's checks. Both calls use
   ordinary capacity and record their actual profile and result.
5. Commit acceptance only after required proof and authority/integrity checks
   pass. Confirmed defects, missing proof and missing decisions remain distinct
   attention reasons. With zero repairs, a confirmed defect waits for an
   operator decision; `resume` cannot secretly rerun implementation as repair.
6. Drain and restart reconcile reserved/running attempts before admitting more
   work. The following boundaries determine whether a model call may repeat.

| Interrupted boundary | Required recovery |
| --- | --- |
| Approval/admission transaction | Either the whole local change exists or none does. Repeated approval/admission cannot create duplicate ownership. |
| Reserved or launched attempt without a durable completed result | Reconcile the owned process group and process identity, not a PID or elapsed timeout alone. Do not release resources while a surviving process may mutate them. If launch/completion remains uncertain, retain the attempt and request explicit recovery; no automatic replay. |
| Valid completed result before candidate or transition commit | Verify task, authority, base and workspace/content binding; resume deterministic post-processing without another implementation call. |
| Valid review acceptance before transition commit | Revalidate the exact candidate/quality/authority binding and finish the transition without another reviewer call. A mismatch blocks reuse. |

If the runner cannot prove an orphan is stopped, it reports the unresolved
ownership instead of killing an unrelated process or reclaiming its workspace.
Stopped or superseded attempts may retain late output, but that output cannot
advance the task. Manual recovery is explicit and recorded; it is not a reset
of authority or attempt history.

#### Verification brief

The task author owns these expectations. The implementer selects local test
syntax; the independent reviewer checks sensitivity, missing coverage and setup
complexity. Use ordinary Go tests, actual temporary SQLite databases and Git
repositories, and small execution-adapter doubles controlled by explicit
signals. Do not simulate GitHub payloads or reproduce the engine in a fake.

| Case | Expected result and proof boundary |
| --- | --- |
| S1 Setup and happy journey | Through real commands, create/approve one task, implement, independently review and reach Awaiting delivery. Verify durable candidate/evidence after reopen, untouched original checkout, and no fetch/push/merge/board calls. Use controlled harness output here; qualify the real adapter separately. |
| S2 Stale approval and competing claims | Change version or base observation after preview: approval fails without a launch. Exact repeat returns the same approval. A default quality change preserves existing task pins; explicit adoption invalidates the old version. Competing store connections cannot reserve two owners. Exercise real transactions, not only mocked return values. |
| S3 Responsive coordinator | Hold an implementation on a signal while status, stop/drain and unrelated deterministic reconciliation remain responsive. A second coordinator is rejected. No arbitrary sleeps or global harness lock around the loop. |
| S4 Interruption and saved results | Fault at each recovery-table boundary; reopen state. Valid implementation/review results avoid another call. Unknown or surviving execution blocks conflicting claims. Counters/history are not discarded. |
| S5 Authority and candidate integrity | Revoke or replace while a call is active; late output cannot advance. Change candidate bytes, base binding or repository/workspace identity; saved acceptance cannot be reused. Use real Git/filesystem fixtures for the integrity claims. |
| S6 Review sufficiency and isolation | Accepted complete evidence progresses; confirmed defect, missing proof, human decision and malformed result do not. Generated verification output is allowed only in the disposable copy; source/test/lockfile changes invalidate the result. Exercise core classification and actual copy boundaries separately. |
| S7 Honest configuration and recovery | Invalid paths/profiles, unknown keys and unsupported nonzero limits fail before launch. Missing/corrupt state blocks execution instead of creating a new store. Status/show leave control state unchanged. Resume explains reused versus repeated work, requires explicit authorization for a new invocation and cannot replay the same recovery request twice. Rejected review cannot obtain a hidden repair. |

Reuse knowledge from the following inspected tests. These are references, not
an instruction to port their Project scaffolding:

- [Engine recovery tests](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/internal/engine/engine_test.go):
  `TestImplementationResumesCompletedHarnessAfterPostProcessingFailure` and
  `TestAgentQAResumesExactAcceptanceWithoutAnotherReviewerRun` establish S4's
  no-repeat requirement. Express it against the new store/engine boundary.
- [Continuous coordinator](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/internal/engine/continuous_coordinator_test.go):
  `TestRunLoopReconcilesIndependentPullRequestWhileHarnessActionIsRunning`
  supplies the signal-based interleaving pattern for S3; substitute a local
  deterministic action, not a fake PR service.
- [Resource tests](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/internal/engine/resources_test.go) establish task/workspace
  conflicts. Replace provider identities with core values for S2/S3.
- [Workspace publication tests](../internal/workspace/publication_test.go) and
  [reviewer verification tests](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/internal/execution/reviewer_verification_test.go)
  establish candidate-bound acceptance and disposable-copy isolation. If the
  owning primitive is reused unchanged, reuse its coverage; extend it only for
  a distinct uncovered case. Otherwise replace the obsolete representation's
  tests with equivalent guarantees at the new owning boundary.

There is no new store suite to extend yet: create one focused owning suite for
transactions/reopen behavior. Keep command smoke tests few and reserve exhaustive
variations for their owning core/store/adapter tests. A test must name the
incorrect behavior it distinguishes; duplicate journey tests are not extra proof.

#### Completion and implementation order

Build in this order within the slice: storage/package feasibility and
version-bound commands; isolated implementation with durable result capture;
independent review; recovery/status; then the complete acceptance journey.
These are construction steps, not separately complete deliverables. Keep the
working path runnable as it grows; do not report a package skeleton as the slice.

Completion requires S1–S7, the configuration example validated by the new parser
with setup-selected paths/model inputs, `go test -race ./...`, `go vet ./...`, and the new entry point's four
no-CGO target builds. Then run one scoped live Codex journey on a disposable
repository with operator-selected model/reasoning, including drain/restart and
inspection of the reviewed result. Record actual calls and candidate-bound
evidence; unavailable credentials or capabilities leave that qualification open.
No remote publication or production data is part of this proof.

Review the complete diff for coupling to old Project types, unsupported settings,
hidden retries, generic infrastructure, duplicated tests and unrelated changes.
Update this slice with evidence and actual remaining limits. Before starting
phase 2, write its bounded brief here using the contracts and suites that now
exist; do not create a second test framework or parallel work plan.

#### Using the implemented local slice

Build the internal milestone with `go build -o /tmp/runner-next ./cmd/runner-next`.
Create a configuration using the [first-slice example](#first-slice-configuration-contract),
select installed Codex model/reasoning values, and use three separate canonical
absolute paths. There is no tool installation or credential setup inside Runner.

Preview setup before applying it. Replace the check placeholder with the
repository's existing validation command, and name quality documents that exist
at the committed base. Repeat `--check` and `--quality-source` as needed:

```sh
/tmp/runner-next --config /absolute/runner.json init \
  --check 'REPOSITORY_CHECK_COMMAND' --quality-source AGENTS.md
/tmp/runner-next --config /absolute/runner.json init \
  --check 'REPOSITORY_CHECK_COMMAND' --quality-source AGENTS.md --apply
/tmp/runner-next --config /absolute/runner.json task add --file /absolute/task.md
/tmp/runner-next --config /absolute/runner.json task show TASK_ID
/tmp/runner-next --config /absolute/runner.json task approve TASK_ID \
  --version 1 --preview DISPLAYED_APPROVAL_PREVIEW
/tmp/runner-next --config /absolute/runner.json run
```

Use `status`, `task show TASK_ID --json` and `config explain` from another
terminal. `drain` records the admission stop and waits for the owning coordinator
to settle; the next explicit `run` recovers reservations and clears drain.
For profile, timeout and recovery-limit edits, edit the same configuration file,
run `config explain`, inspect the task effects and active pinned attempts, then
run `config apply --preview DISPLAYED_APPLY_PREVIEW`. Application is atomic and
can happen while the coordinator runs. Already admitted attempts retain their
actual settings; queued actions use the applied policy. Pending transient retries
and explicit interrupted resumes retain their original profile, including after
that named profile is edited or removed. This bundle does not offer a retry-profile
override. The [operator bundle](#using-the-operator-interface) adds interactive and
named-setting editing over the same preview/apply boundary. `init` handles initial setup only.
Changing repository identity or roots requires fresh setup. Use the
[quality workflow](#using-quality-agreements) to revise the default agreement and
explicitly adopt it on existing work. Editing a task file has no effect until
`task replace`; replacement preserves that task's pinned quality revision.

The runtime consists of [core values](../internal/runner/core/types.go),
[strict configuration](../internal/runner/config/config.go),
[SQLite transactions](../internal/runner/store/store.go),
[application commands and coordinator](../internal/runner/engine/),
[Codex/process execution](../internal/runner/execution/),
[Git and disposable-copy operations](../internal/runner/workspace/) and
[CLI/setup presentation](../internal/runner/cli/). The coordinator consumes one
model execution interface and a small native-check interface. Git protection reuses the existing workspace and subprocess
primitives; the replacement imports no old engine, execution or GitHub package.
The schema contains settings/history, task versions, attempt reservations and
immutable quality agreements/baseline reports.

#### Phase 1 evidence

Implementation commit: `d6d0ddb`. Verification date: 2026-10-14. The accepted
planning documents were carried into the dedicated worktree with this change;
unrelated edits in the original checkout were preserved.

| Proof | Owning evidence and observed result |
| --- | --- |
| S1 | [CLI journey](../internal/runner/cli/cli_test.go) exercises setup preview/apply, a committed-base baseline despite an uncommitted checkout edit, add/show/approve/run, active status, drain, retained acceptance and read-only reopen. Real SQLite and Git; one small controlled harness. |
| S2 | [Store transactions](../internal/runner/store/store_test.go) reject competing connections and roll back the losing claim. [Core/engine tests](../internal/runner/engine/engine_test.go) reject changed base/version previews and preserve exact repeated approval. The third phase 2 bundle replaces the original global quality invalidation with explicit adoption and immutable task pins. |
| S3 | The engine suite holds execution on a signal while status, unrelated draft creation and stop remain responsive. A second coordinator is rejected; cancellation and drain settle without sleep-based synchronization. |
| S4 | The engine suite reopens saved implementation results before and after candidate construction, and saved audit results before acceptance; exactly two attempts remain. Reserved work requires explicit recovery; surviving/unknown process groups keep ownership. [Process tests](../internal/runner/execution/process_test.go) prove a failed durable launch callback cannot execute the command and cancellation targets the owned group. |
| S5 | Engine tests reject modified candidate bytes and discard stopped/replaced authority while retaining late evidence. Existing workspace identity/candidate tests continue to cover the reused Git boundary. |
| S6 | Core/engine suites distinguish complete evidence, a focused gap, confirmed defect, missing decision and malformed output. Focused verification receives only unresolved proof and cannot alter source. [Copy tests](../internal/runner/workspace/workspace_test.go) permit generated output and reject source changes/external links; existing verification-index protection is reused. |
| S7 | [Config tests](../internal/runner/config/config_test.go) parse this document's actual example and reject unsupported controls, unknown keys and unsafe paths. Store tests preserve missing/corrupt authority. CLI inspection leaves database bytes unchanged. Engine recovery requires explicit new-call authorization, preserves its parent attempt and cannot queue a duplicate invocation from a recovery request or repair a rejected candidate. |
| Repository gates | `go test -race ./...` and `go vet ./...` passed with Go 1.27.1. |
| Packaging | `modernc.org/sqlite v1.60.1` passed real transaction/reopen tests and `CGO_ENABLED=0` builds of `cmd/runner-next` for macOS/Linux, amd64/arm64. |

The final disposable live journey used Codex CLI 0.162.1 with the operator-selected
`gpt-6.1-sol` / `high` for both roles. Task `ec4bb865e3139495377522319a0ea3f8`
approved base `708b8476c93b031179d41c318848d40f6eb9321f` and produced candidate
`88454b6a033e204fcff76d0490c62b6fb7eef823`, tree
`d3c4dc1515aa5f63b2416a7fbe413652832f76ed`.

- Implementation attempt `4bc502da92124193345c1cc5f124c153` changed one Python
  function and extended its existing unittest suite with three distinct cases.
  All four tests passed. No dependencies or test framework were added.
- Runner drained during implementation, retained the completed candidate, exited,
  and admitted audit only after an explicit restart.
- Audit attempt `8880a42b0e52d465e13381b11a0cadb7` independently inspected the exact
  candidate, test adequacy/scope and the retained implementation event log's
  actual test result. Both proofs passed without another test run or focused call.
- The task reached **Awaiting delivery**. Subsequent drain/restart preserved the
  exact task and two attempts. The final platform binary also passed fresh native
  baseline setup and this restart check. The original checkout remained clean.

An earlier qualification task retained three attempts: one launcher-startup
failure before a model session, followed by explicit recovery and two successful
calls. That exposed and fixed the installed Codex package's sandbox read paths;
it was not an automatic retry or a code rejection. Across both tasks there were
five harness launches and four completed model calls. No fetch, push, merge,
board call or `gh` dependency was involved.

Local disposable evidence is retained at
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-s1-live-7cz16ttl`:
`final-status.json`, private `state/attempts/` prompts/events/results and the
candidate worktrees. These are temporary qualification artifacts, not a portable
backup or a release retention guarantee.

At the phase 1 checkpoint, the deliberate limits were one invocation, local Ready only, zero automatic
allowances, no delivery, no quality exceptions, and no board/plugin scaffolding.
Native tools have no network access; unavailable dependencies or toolchain access
remain explicit blockers. Baselines use bounded committed-base archives and
reject link/special-file entries. Verification copies have source inventory but
no Git history/remotes; `.runner-output/` is available for generated reports and
caches. Completed candidates/evidence and disposable diagnostic copies are
retained; automated cleanup/backup/restore remain release work. Native execution
was qualified on macOS arm64; the other three targets were compile-qualified.

### First phase 2 bundle — Ready admission and recovery

The authorized first phase 2 bundle makes the existing **Ready** journey
honor configurable capacity and persisted recovery policy. Its required outcome
is two independent Ready tasks progressing under configured limits, with every repeated
call explainable from a durable allowance and actual profile. Keep Plan,
dependency release, quality-agreement revisions, delivery and views outside this
first phase 2 bundle; write their next bounded briefs here before implementing
them. Phase 2 is complete only when the full phase-table scope is established.

Extend the current core/config/store/engine and owning tests:

1. Replace the single-slot index with explicit task/workspace claims and global
   admission accounting. Keep one coordinator. Prove capacity changes and
   cancellation with signal-controlled adapters and real competing transactions.
2. Add typed transient, proof-gap, defect and decision outcomes. Persist retry
   and repair consumption before each launch; preserve it across crash, stop,
   replacement and policy edits according to the existing contracts. A transport
   error cannot become a code defect or trigger escalation.
3. Enable configured model/reasoning profiles, a successor implementation ladder
   and the optional invocation ceiling. Only an authorized repair following a
   confirmed defect advances the ladder. A proof gap remains reviewer work.
4. Preview effective policy changes and their effect on queued versus active
   attempts. Pin active profiles. Show remaining allowances, next eligible
   action and the specific limiting resource in the existing status/show views.
5. Extend S2–S7 at their owning boundaries. Include paired behavioral cases for
   weakened assertions versus legitimate test consolidation, fabricated evidence
   versus valid supplied evidence, and useful findings versus speculative review.
   Use a small selected live evaluation only after deterministic proofs pass.

This bundle uses **store format 2 and fresh state**. The configuration marker stays
`runner-local-v1`; its supported policy values are expanded below. Format-1 stores
are rejected with recovery guidance and left untouched. Drain and stop the previous
coordinator, preserve its state and evidence, then initialize a new private state
and worktree directory and resubmit work for approval. There is no historical
workflow conversion, second scheduler, new test framework, plugin registry or
remote integration. Zero repair/retry allowances remain supported and tested.

#### Using the Ready policy

Keep the same explicit implementer/reviewer profiles and local recorded-base
configuration. `max_parallelism` accepts 1–16, repair/retry limits are nonnegative,
and `max_invocations_per_task` is absent or a positive integer. Base refresh stays
zero. The ladder lists distinct named successor profiles after the starting
implementer; the starting name cannot recur. An empty ladder disables escalation.
For example, after defining a complete `careful` profile:

```json
{
  "max_parallelism": 2,
  "max_repairs": 2,
  "max_transient_retries": 3,
  "max_base_refreshes": 0,
  "implementation_ladder": ["careful"],
  "max_invocations_per_task": 8
}
```

Use that object for `policy`, then preview/apply the complete configuration. Profile
selection never expands role permissions. The preview shows consumed allowances,
before/after eligibility, active pinned settings, and warnings for unreachable
ladder steps or a ceiling below initial implementation plus audit. The preview
digest binds the old and proposed policy revisions; task effects are observations
at preview time, not a lock on tasks while the user reads them.

Each model attempt reserves an invocation, its task/workspace ownership and any new
repair/retry allowance in the same SQLite transaction. Independent tasks share
capacity. Lowering parallelism prevents new admission while occupancy meets or
exceeds the new limit; it does not cancel active work. Unknown surviving processes
retain both their resource and capacity claims. Deterministic candidate/result
reconciliation continues while unrelated harness invocations are active.

A confirmed independent review defect schedules repair; the next admitted repair
consumes one pass and advances the configured ladder, retaining the last profile
when it is exhausted. Its findings and working branch are retained. A transient
retry repeats the stage and reserved repair pass without another repair or ladder
advance. Retry deadlines are durable: 30 seconds, two minutes, then five minutes.
Success ends the stage's transient episode. A missing decision outranks a defect;
a proof gap stays in review and can never become repair or acceptance because a
budget is exhausted.

The native adapter recognizes terminal Codex `turn.failed` messages with the
anchored HTTP status form for 429, 500, 502, 503 or 504. Codex's
[exec event definitions](https://github.com/openai/codex/blob/main/codex-rs/exec/src/exec_events.rs)
expose an error message rather than a structured provider code. Other failures,
timeouts, missing prerequisites and malformed proof stop for inspection; arbitrary
stderr, tool output and model text never trigger retries or escalation. This bundle
has no browser capability. Native transport attempts remain visible in retained
`events.jsonl`; the ceiling counts harness invocations, not HTTP requests.

Restart, stop and policy edits retain consumption. Raising a limit is an explicit
operator policy change; lowering it below consumption blocks the next automatic
action. `task resume --attempt ID --allow-new-invocation` explicitly repeats one
interrupted invocation with its recorded profile and repair pass. It neither resets
counters nor bypasses the invocation ceiling. Lowering the repair limit below
already consumed passes blocks subsequent automatic invocations, including a
pending retry; explicit interrupted resume can repeat its already reserved pass.
A reserved but interrupted attempt continues to count; this bundle conservatively performs no reservation refunds.
Replacing requirements creates an unapproved version with new per-version
allowances; prior attempts retain their original numbers and evidence. Saved valid
results still resume deterministically without a model call.

`status` and `task show` use the dispatch eligibility calculation. They show actual
profiles, elapsed time, reserved invocation/repair/retry numbers, remaining
allowances, retry deadlines and the specific ownership, capacity, budget or
operator action preventing admission. Candidate acceptance still ends at
**Awaiting delivery**.

#### First phase 2 qualification

Verification date: 2026-10-15. The existing owning suites were extended:

| Boundary | Evidence and credible failure detected |
| --- | --- |
| S2 store | Real independent connections compete for the final capacity slot, task and workspace; failed transactions leave no task/counter change. Reopen preserves the winner. Previous-format and corrupt stores are not modified. |
| S3 coordinator | Signal-controlled invocations exercise capacity 1 → 2 → 1, pinned active settings, cancellation of one task, continued reconciliation of another, new reviewer settings and graceful drain. |
| S4/S5 recovery | Real Git/SQLite journeys exercise reserved repairs, restart, manual repetition, durable 30s/2m/5m backoff, changing transient messages, budget exhaustion, policy edits and new-version authority. No hidden extra repair or ladder advance. |
| S6 review | Existing proof-gap, decision, malformed-result and candidate-integrity cases remain. Zero repair is still enforced. Replacement role instructions distinguish sensitive assertions, equivalent consolidation, supplied evidence and concrete findings. |
| S7 configuration/CLI | Strict limits/ladder validation, live policy preview/apply with a stale-preview rejection, actual pinned profile reporting and read-only inspection. |

Required gates: `go test -race ./...` and `go vet ./...` passed. The selected native
review evaluation used **Codex 0.162.1, gpt-6.1-sol / high** on macOS arm64:

| Live pair | Observed outcomes |
| --- | --- |
| Weakened assertions / equivalent subtest consolidation | Defect / accepted |
| Unsupported claim of a passing check / actual supplied command evidence | Proof gap / accepted |
| Demonstrated whitespace-normalization defect / speculative feature suggestion | Defect / accepted |

All six native audit calls completed once, in 154 seconds overall; no evaluation
retry was needed and the source fixtures remained unchanged. These small cases
qualify the selected Ready reviewer behavior, not general model reliability or
other platforms. Private prompts, events, results, process identities and outcome
summaries are retained locally under
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-ready-eval-358039781`.
Reproduce deliberately with:

```sh
RUNNER_LOCAL_LIVE_MODEL=gpt-6.1-sol RUNNER_LOCAL_LIVE_REASONING=high \
  go test ./internal/runner/execution -run '^TestLiveReadyReviewBehavior$' \
  -count=1 -v -timeout=22m
```

It is opt-in in the owning execution suite, with no new evaluator service or
legacy Project test fixtures. The required gates skip these paid calls unless
explicit model/reasoning inputs are provided.

### Second phase 2 bundle — Plan and verification preparation

Authorized continuation: 2026-10-15. Build one complete Plan-to-approved-children
journey and the missing-verification path for Ready. Continue using the same
coordinator, transactional store, profiles, invocation ceiling and recovery rules.
Quality-agreement changes, delivery, external views and the interactive policy
editor remain outside this bundle.

Commands and authority:

1. Configure an explicit optional `roles.planner` profile through the existing
   policy preview/apply commands. Complete Ready tasks still need only the
   implementer/reviewer roles.
2. `task add --plan --file REQUEST.md` creates a draft planning request. A nonempty
   request can describe an outcome without already defining executable cards.
   `task approve ID --version N --preview DIGEST` authorizes planning only.
3. The planner reads a disposable copy of the approved base and the pinned quality
   agreement. It proposes cards with local keys, complete Outcome/Scope/Acceptance/
   Verification sections, and explicit dependency keys within that proposal.
   There is no target card count. Questions remain visible and prevent release.
4. `task show ID` displays the complete proposal and a separate release preview.
   `task release ID --version N --preview RELEASE_DIGEST` approves the exact full
   proposal. All children, their pinned dependency versions, approvals and the
   parent's release receipt are committed together. Repeated release returns the
   same children; stale versions, moved bases, missing targets, cycles, incomplete
   briefs or open decisions cannot release any subset.
5. Independent children can start; dependents require confirmed completion of the
   exact prerequisite version. Awaiting delivery, stopped and replaced prerequisites
   never satisfy an edge. This bundle provides no command to claim integration.
   A released request stays incomplete while its children await delivery/integration.
6. `task stop PLAN_ID` atomically revokes the released children and requests ordinary
   coordinator cancellation. A released proposal is otherwise immutable: create a
   new planning request to change the batch. Individual child replacements retain
   their prerequisite bindings; a changed prerequisite requires a new plan.

Ready preparation uses the same configured planner. A Ready brief still requires
Outcome, Scope and Acceptance; a missing Verification section schedules a visible
`prepare` attempt after approval. Its output can add only a verification brief,
not rewrite requirements or decompose the task. Store that supplement separately
from the immutable approved version, bind it to its preparation attempt and use it
for implementation/review. It requires no second approval. Questions stop work.
Preparation consumes the Ready task's invocation allowance; a ceiling of one can
permit preparation while leaving implementation visibly waiting for allowance.

Plan proposals, release receipts and dependency bindings extend the existing task
records; no planning service or second scheduler is needed. Store format 3 prevents
an older coordinator from ignoring the new authority/dependency fields. Preserve
and drain old state, then use fresh state/worktree roots. No historical conversion.

Verification brief before coding: extend core tests for malformed/incomplete plans,
cycles, missing targets and exact-version dependency success; store tests for real
competing releases, rollback and atomic revocation; engine tests for saved planner
results after restart, stale/revoked approval, shared admission, dependent waits,
Ready preparation with unchanged scope and bounded calls. Exercise the new CLI
commands and read-only proposal display. Use the existing opt-in native evaluation
pattern for one concrete plan and one Ready preparation with `gpt-6.1-sol / high`
after deterministic tests pass. Run `go test -race ./...` and `go vet ./...`.

#### Using Plan and Ready preparation

Use fresh state and worktree roots with this version. Formats 1 and 2 are rejected
unchanged; drain and preserve an older coordinator's state before resubmitting
work. The configuration marker remains `runner-local-v1`. Add `"planner": "build"`
to `roles` to reuse an explicitly selected profile, or define a separate complete
profile and name it there. Apply the change with `config explain` and
`config apply --preview DIGEST`. Setup also displays the configured planner.

```sh
/tmp/runner-next --config /absolute/runner.json task add --plan --file /absolute/request.md
/tmp/runner-next --config /absolute/runner.json task show PLAN_ID
/tmp/runner-next --config /absolute/runner.json task approve PLAN_ID \
  --version 1 --preview DISPLAYED_APPROVAL_PREVIEW
/tmp/runner-next --config /absolute/runner.json run
```

From another terminal, inspect `task show PLAN_ID`. When it reaches **Awaiting
approval**, read the full proposal, scope, verification briefs and prerequisites.
The approval above authorizes only planning. Release is the separate authorization
to execute the exact proposed batch:

```sh
/tmp/runner-next --config /absolute/runner.json task release PLAN_ID \
  --version 1 --preview DISPLAYED_RELEASE_PREVIEW
```

The running coordinator can then admit independent children under the same global
capacity. Each child displays its parent and pinned prerequisites. Repeating the
same release returns the same children. The parent stays **Ready** with its release
receipt; it is not complete. Children needing integration wait visibly because
this milestone stops at **Awaiting delivery** and has no completion command.

Questions require an explicit replacement request and fresh approval. A moved base
requires replanning before release. Released proposals cannot be replaced; use
`task stop PLAN_ID` to revoke the entire batch, then add a new request. Stop retains
evidence and process claims until cancellation settles. Editing one child's body
does not remove its prerequisite bindings or restore a stopped parent's authority.

For a Ready task, `task add --file TASK.md` still requires Outcome, Scope and
Acceptance. Supply Verification directly to avoid a planning call. If it is absent
or empty, approval authorizes one `prepare` action before implementation. Its
verification supplement is displayed separately with its attempt ID; the approved
body/version/preview remain unchanged. Preparation cannot create child tasks and
needs no second approval. A straightforward prepared Ready journey uses three
calls: prepare, implement and audit. The ordinary invocation ceiling, transient
retry rules, pinned profiles and explicit interrupted recovery all apply.

#### Second phase 2 qualification

Verified on 2026-10-15 with Go 1.27.1 on macOS arm64. `go test -race ./...` and
`go vet ./...` passed. The complete diff was reviewed for authority gaps,
accidental legacy coupling and unnecessary test infrastructure. No provider or
platform dependency was added; release/platform qualification remains a later gate.

| Guarantee | Owning evidence |
| --- | --- |
| Complete, reviewable proposal and exact release | Core validates complete briefs and dependency graphs. Engine reopens saved planning output without another call; rejects stale previews, changed bases, questions, invalid graphs and superseded results; exact repeated release preserves child identities. The existing CLI journey covers the new commands and read-only, human-readable proposal inspection. |
| Atomic batch authority | Real SQLite connections compete to release one parent; one wins. A later child-insert failure rolls back earlier children and the receipt. Parent stop revokes all child approvals together while retaining an active claim and late evidence. |
| Dependency safety | Core requires the exact prerequisite version in Completed state. Engine keeps reviewed children at Awaiting delivery and their dependents waiting; replacement invalidates the old edge. Store admission rechecks a prerequisite changed after scheduling and rolls back counters. Confirmed integration and parent completion remain phase 3 work. |
| Ready verification ownership | Engine proves an absent planner cannot approve incomplete work; preparation preserves the approved body, digest and approval, saves provenance, and supplies the brief to both implementation and audit. A one-call ceiling permits preparation but blocks implementation until explicitly raised. |
| Recovery reuse | The existing interruption test now covers both implementation and planning. Saved Plan/preparation results resume deterministically after reopen. The existing coordinator/capacity suites continue to own shared scheduling and cancellation behavior. |
| Fresh-state boundary | Both prior store formats are rejected without modifying their bytes. |

Exactly two opt-in live calls used native Codex CLI 0.162.1 with
`gpt-6.1-sol / high`: one Plan and one Ready preparation. Both succeeded without
retry. The planner chose one cohesive card for greeting normalization, its example
and existing test coverage. Preparation returned only the missing brief and no
cards/questions. Both inspected the existing `greet.py`/`test_greet.py`; saved events
show only listing/reading commands, no repository tests/programs or source changes.
The briefs identify the public function boundary, concrete expected strings and
which existing assertions to reuse or extend. These two small outcomes qualify the
selected role instructions, not general model reliability.

Private prompts, schemas, events, results and process identities remain under
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-planning-eval-3235820790`.
Reproduce deliberately with:

```sh
RUNNER_LOCAL_LIVE_MODEL=gpt-6.1-sol RUNNER_LOCAL_LIVE_REASONING=high \
  go test ./internal/runner/execution -run '^TestLivePlanningBehavior$' \
  -count=1 -v -timeout=8m
```

This bundle stopped before quality-agreement and operator-interface changes, now
covered by the third and fourth briefs below. Delivery, confirmed integration and
external views retain their later phase boundaries.

### Third phase 2 bundle — quality revisions and baseline exceptions

Authorized on 2026-10-15. Keep one coordinator and private store. Add an explicit
quality workflow and task adoption; stop before delivery, the interactive policy
editor and broader operator presentation work.

The supported journey and authority are:

1. `quality preview --file QUALITY.json` resolves a definition, repository-relative
   policy sources and named checks against the observed committed base. Each check
   specifies a shell command, relative working directory, executable prerequisites
   and timeout. Checks use a fixed isolated environment: PATH plus private HOME/
   TMPDIR and a fixed locale, no inherited credentials or arbitrary environment
   configuration. The preview shows the exact commands and environment contract.
2. `quality check --file QUALITY.json --preview DIGEST` explicitly authorizes those
   checks in disposable base copies. Record pass, observed failure, or unavailable
   separately, with exit code, exact output digest and private evidence. Capture
   source identity and reject commands that alter source or Git controls. A failed
   baseline is retained as evidence, never silently applied. Initial `init --apply`
   may establish repository state without an agreement; approval/execution waits
   for an applied agreement. Existing concise `init --check` setup uses this same
   check machinery and applies only a completely passing baseline.
3. `quality preview --report ID [--exceptions EXCEPTIONS.json]` displays the proposed
   resolved agreement, blockers and apply digest. An exception names one failed
   check, a nonempty rationale and exact existing source/test paths identifying the
   affected behavior. Runner pins those files and that failure's exit/output digest
   from the baseline. No globs, entire-command ignore switch, unavailable checks,
   missing tools, timeouts or unresolved process ownership can be excepted.
4. `quality apply --report ID [--exceptions EXCEPTIONS.json] --preview DIGEST` saves
   the immutable agreement and makes it the default for new work. Reject changed
   reports/specifications, moved bases and stale previews; recheck the previous
   default transactionally. Do not reset counters, cancel work, or change existing
   task approvals merely because the default changes. Read-only `quality show`
   exposes the active agreement; `--revision DIGEST` inspects retained history.
5. Every task, attempt, planning release, candidate check and reviewer uses its
   pinned agreement from history, including after restart. Source-policy edits
   cannot weaken it. `task adopt-quality ID --version N --preview DIGEST` explicitly
   selects the current default shown by `task show`, creates an unapproved version
   with the same body/dependencies, and revokes old actions. Clear prepared proof,
   proposals and candidate acceptance; retain old versions, attempts and evidence.
   Repeating the exact adoption is harmless. Released plans remain immutable;
   stop and create a new plan to change their batch. Individual children may adopt,
   with the ordinary dependency-version rules still applying.
6. Runner executes pinned quality checks on a disposable implementation copy before
   allowing candidate review. This work runs in the existing worker/reservation,
   with durable process identity and normal cancellation, without a model call or
   extra scheduler. Save results on the attempt and supply them to review. Passing
   checks pass normally; an exception permits only the same failure with all its
   scoped files unchanged. New/changed failures and missing proof block progress.
   Exceptions cannot waive task acceptance or permit weakened assertions. Saved
   complete results can advance after restart without rerunning checks; interrupted
   work uses the existing explicit recovery rule. No check is silently replayed.

Use store format 4 and fresh private roots; no conversion of old approvals or
in-flight state. Add only immutable agreement/report tables and check evidence on
attempts. Keep execution-policy changes separate from agreement changes. Reuse
native containment, disposable-copy integrity, admission and cancellation helpers.

Verification brief before coding: extend the existing real-store suites for
competing agreement applies, immutable history and stale default rejection; core/
config tests for exception bounds and unavailable-versus-failed results; engine
tests for pinned old/new tasks, explicit adoption and late results, saved checked
results after reopen, matching exceptions versus changed failures or scoped files,
and unchanged task-proof requirements. Extend the CLI journey for preview/check/
apply/show and read-only inspection. Qualify the actual native command boundary
with a disposable failed baseline, matching/changed failures and unavailable
prerequisites. If role instructions change, run a small selected live reviewer
pair with `gpt-6.1-sol / high`. Run `go test -race ./...`, `go vet ./...` and build
`cmd/runner-next`; platform/release qualification remains at release.

#### Using quality agreements

Use fresh private state/workspace roots for format 4. Older formats are rejected
without conversion or mutation. `init --apply` without `--check` creates a drained
setup; submitting work waits for an applied quality agreement. The concise
`init --check` route remains available for entirely passing baselines, with checks
named `check-1`, `check-2`, etc. It uses a 20-minute timeout and `sh` prerequisite.
Use a specification for descriptive names, tool prerequisites and per-check limits:

```json
{
  "definition": "Satisfy the approved outcome; preserve existing behavior and reuse the owning test suite.",
  "sources": ["AGENTS.md"],
  "checks": [
    {
      "id": "unit",
      "command": "python3 -B -m unittest",
      "directory": ".",
      "prerequisites": ["python3"],
      "timeout": "2m"
    }
  ]
}
```

Replace this example with the target repository's established checks and policy
files. Source documents come from the committed base, excluding checkout edits.
Runner records PATH at preview and uses it for baseline and candidate commands.
Each command gets a fresh source copy, private HOME/TMPDIR, C locale and no
inherited credentials or network access. Keep generated output under
`.runner-output/`; modifying source or Git controls makes the result unavailable.
Declare required executable names explicitly. A check must use exit 0 for success,
1–124 for observed failures and 125–127 for prerequisites it cannot satisfy;
signals, timeouts, output overflow and unresolved process ownership are unavailable.
Runner cannot infer an undeclared prerequisite failure from arbitrary error text.

```sh
/tmp/runner-next --config /absolute/runner.json quality preview --file /absolute/quality.json
/tmp/runner-next --config /absolute/runner.json quality check \
  --file /absolute/quality.json --preview DISPLAYED_BASELINE_PREVIEW
/tmp/runner-next --config /absolute/runner.json quality preview --report REPORT_ID
/tmp/runner-next --config /absolute/runner.json quality apply \
  --report REPORT_ID --preview DISPLAYED_AGREEMENT_PREVIEW
/tmp/runner-next --config /absolute/runner.json quality show
```

`quality check` returns an immutable report, including logs, even when checks fail;
it does not apply the agreement. To accept a known baseline defect, create an
exception request such as this, after inspecting its actual failure and scope:

```json
[
  {
    "check_id": "unit",
    "rationale": "The existing legacy parser defect is outside this work; retain its failing assertion.",
    "scope": ["legacy/parser.py", "tests/test_legacy_parser.py"]
  }
]
```

Pass `--exceptions /absolute/exceptions.json` to both the report preview and apply.
The preview shows the exact scoped hashes and observed exit/output digest. A later
candidate must pass the check or reproduce that exact failure with every scoped
file unchanged. Output matching is byte-exact: timestamps, timing differences and
changed diagnostics invalidate the exception. Choose focused, stable checks rather
than normalizing away new failures or weakening assertions. Scope and rationale
remain an owner decision; the reviewer still checks task acceptance independently.

Applying changes only the default for new tasks. Existing Ready tasks, Plan
proposals, released children, retries and saved results retain their agreement.
`quality show --revision DIGEST` inspects that history. The revision covers the
whole resolved agreement, including baseline evidence, so recollecting a baseline
can produce a new revision even with unchanged specification text.

To move an existing task to the new default, inspect `task show TASK_ID`, then run
`task adopt-quality TASK_ID --version N --preview DISPLAYED_ADOPTION_PREVIEW`.
This creates the next unapproved version and cancels old authority, retaining its
attempts and logs. Inspect and approve the new version separately. Prepared briefs,
proposals and candidates must be recreated. Released plans cannot adopt; their
children can adopt individually, but dependents still require their exact approved
prerequisite version. Replacing task text alone preserves its quality revision.

Quality checks run after implementation inside its existing reservation, without
another model call. Complete saved observations survive restart without rerunning
commands. A failed/unavailable check blocks review with retained evidence and no
automatic repair or escalation. For an operator-confirmed in-scope source defect,
`task repair-checks ID --attempt ATTEMPT_ID --reason "CONCRETE_DEFECT"` queues one
repair using the existing allowances and pinned profile, followed by all required
checks and independent review. Unavailable/incomplete checks are ineligible.
Correct requirements or explicitly adopt a corrected agreement when needed.
Interrupted checks use the existing explicit recovery command; Runner never
silently repeats them.

#### Third phase 2 qualification

Verified on 2026-10-15 with Go 1.27.1 on macOS arm64. `go test -race ./...`,
`go vet ./...` and the `cmd/runner-next` build passed. The complete diff was reviewed
for authority gaps, accidental changes and unnecessary complexity. The dependency
graph still contains no old engine, execution or GitHub package.

The implementation uses two private immutable tables, one native-check interface
and the existing worker, process and disposable-copy boundaries. There is no second
scheduler, test framework or delivery adapter. Platform/release checks remain a
later gate.

| Guarantee | Owning evidence |
| --- | --- |
| Immutable default changes and history | [Store quality tests](../internal/runner/store/quality_test.go) use competing SQLite connections: one apply wins, both retained revisions remain readable and execution policy is preserved. Engine rejects stale default previews and moved bases. |
| Explicit adoption and retained authority | [Engine quality tests](../internal/runner/engine/quality_test.go) cover new-task defaults, stale/repeated adoption and superseded late results. Existing approval/reopen and Plan-release tests now prove that the previous agreement survives a default change. |
| Exact exceptions and unchanged task obligations | [Config tests](../internal/runner/config/quality_test.go) reject invalid scopes and unavailable evidence. The engine's candidate table covers matching and changed output, changed scope, unavailable checks and missing task proof. Only the matching complete case reaches Awaiting delivery. |
| Evidence tied to actual source | The same engine table rejects changes to the canonical workspace while checks run and commands that edit their disposable copy. Saved complete checks advance after reopen with a check runner that fails if called again. Existing process/recovery suites own cancellation and retained reservations. |
| Operator commands and fresh state | The existing [CLI journey](../internal/runner/cli/cli_test.go) covers preview/check/apply/history/adoption and proves inspection does not alter database bytes. Store tests reject formats 1–3 without mutation. |

On 2026-10-15, the native Codex 0.162.1 command qualification passed on macOS arm64:
three actual sandbox commands produced the known failure, matching failure and
changed failure; a missing declared tool produced unavailable without launching a
command. The commands verified that an ambient secret variable was absent.
This used zero model calls. Private evidence remains at
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-quality-checks-825654352`.

Exactly two live reviewer calls used `gpt-6.1-sol / high`, with no retries. The
correct candidate with a scoped existing failure was accepted; a candidate with a
task defect was rejected despite the same exception. Both retained their source
unchanged. This qualifies the selected exception instructions, not general model
reliability. Prompts, events and results remain at
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-ready-eval-2780415215`.
These temporary local artifacts are not a portable backup. Reproduce deliberately:

```sh
RUNNER_LOCAL_LIVE_CHECKS=1 go test ./internal/runner/execution \
  -run '^TestLiveQualityCheckBoundary$' -count=1 -v -timeout=3m
RUNNER_LOCAL_LIVE_MODEL=gpt-6.1-sol RUNNER_LOCAL_LIVE_REASONING=high \
  go test ./internal/runner/execution \
  -run '^TestLiveReadyReviewBehavior/(known_baseline_exception|exception_cannot_waive_task_defect)$' \
  -count=1 -v -timeout=8m
```

This bundle stopped before operator-interface work, now covered by the fourth
brief below. Delivery/integration, external views, cleanup/backup and
platform/release qualification retain their later boundaries.

### Fourth phase 2 bundle — operator status and configuration

Authorized on 2026-10-15. Complete the local operator interface on the existing
commands and coordinator. Stop at Awaiting delivery; do not add delivery, a web
UI, another scheduler or a new store format.

The supported behavior and expected results are:

1. Human `status` leads with **Needs you**, then **Working**, **Waiting** and
   **Retained**. Show each task once, with its outcome, lifecycle stage, specific
   cause and next action. Draft/proposal approval, decisions, exhausted limits and
   interrupted/uncertain execution need attention. Ordinary dependencies, capacity
   and retry deadlines are waits. Awaiting delivery is a retained reviewed
   candidate, never completed work. An absent coordinator does not imply that a
   surviving reservation is safe to replay.
2. Keep status compact. Show the relevant actual attempt's role, model, reasoning,
   elapsed time and retained evidence, plus remaining allowances. `task show`
   provides full content/history, exact approval/release commands, candidate and
   recovery details. Recovery names the role that repeats, what remains and whether
   another invocation needs authorization. A planning question is answered by
   replacing and approving the clarified request; there is no second conversation
   store. Human and JSON output use the same derived facts and dispatch eligibility.
3. `status --watch DURATION` refreshes read-only observations until interrupted;
   `--json` emits one complete JSON object per line. No inspection creates state,
   reconciles processes, starts work or modifies counters. Refresh current applied
   settings and coordinator state on every observation. Use a minimum one-second
   interval, no terminal dependency or screen-control framework.
4. `config explain` becomes a readable resolved-policy preview; `--json` retains
   machine access. Show role profiles, escalation order, limits, active pinned
   attempts, affected queued work and warnings. Include concrete next actions when
   budgets cannot admit the selected work. No prompt/model changes are required.
5. `config edit` offers a small line-oriented editor over named settings. Repeated
   `--set KEY=VALUE` provides the same noninteractive changes: parallelism, repairs,
   transient retries, invocation ceiling, roles, named model/reasoning/timeout
   profiles and escalation order. Repository paths, quality and delivery authority
   are outside this editor. Validate the complete proposal and installed harness
   capabilities, show its effects, then ask to save. Noninteractive saves require
   `--save --preview DIGEST`; preview alone writes nothing. Cancellation/EOF,
   invalid input and stale previews leave file and store untouched. Reuse secure
   conditional file replacement to reject concurrent edits and preserve provenance.
6. Saving edits only the explicit configuration file. Applying remains the existing
   separate `config apply --preview DIGEST` transaction. This avoids pretending
   that the file and database share one transaction. Explain pending file changes,
   preserve active/retry profiles and all consumption counters, and never start
   the coordinator as a side effect. Keep the existing manual file-edit route.
7. Add a replacement-specific operator skill/reference over these commands, with
   exact setup, approval, quality and recovery boundaries. Do not change or install
   the legacy bundled execution skills. Qualify the local operator journey through
   configuration, Plan/Ready, a decision, a dependency wait, interrupted review,
   live parallelism changes, drain/restart and retained candidate inspection.
   Delivered-result inspection remains phase 3/4 evidence.

Verification brief before coding: extend the existing CLI journey and owning
engine policy tests, reusing real SQLite/Git and controlled harness calls. Add
small table cases only for wrong grouping/recovery advice and editor validation
or lost-update risks. Prove preview/cancel/watch are read-only, JSON and human
views identify the same action, a live policy edit preserves active profiles,
and recovery repeats only the interrupted review. Exercise actual terminal input
and the built binary on disposable state; inspect its output as an operator.
Reuse existing role/live containment evidence since no harness instructions change.
Run `go test -race ./...`, `go vet ./...`, and build `cmd/runner-next`. Record the
walkthrough, remaining release boundaries and any usability limitations here.

#### Using the operator interface

Use the existing format-4 state; this bundle needs no migration or reset.
`runner-next help` lists the local commands without requiring setup. All commands
below use the same `--config /absolute/runner.json` prefix. The repository-local
[operator skill](runner-operator/SKILL.md) explains the authorization boundaries;
it is not installed automatically and is separate from the legacy bundled skills.
The consolidated [operator guide](operator-reference.md) now covers the complete
current journey through integration and restore; the bundle notes below retain
their historical qualification context.

`status` groups tasks by what needs attention, rather than printing every attempt.
It shows actual attempt settings, elapsed time, evidence, remaining allowances
and the same next action available in JSON. **Retained** includes stopped work and
reviewed candidates awaiting delivery; neither means integrated success.
`task show ID` retains the complete request, proposal/questions, history, proof
and candidate identities. Copy its exact approval or release command only after
reviewing the content. Recovery explains the affected role, retained work and
what repeats; a saved result does not authorize another model call.

```sh
/tmp/runner-next --config /absolute/runner.json status
/tmp/runner-next --config /absolute/runner.json status --watch 2s
/tmp/runner-next --config /absolute/runner.json status --watch 2s --json
/tmp/runner-next --config /absolute/runner.json task show TASK_ID
```

Watch appends complete timestamped observations; JSON watch emits one object per
line. Ctrl-C stops inspection without stopping work. Missing or unreadable
authority is reported as an error, never initialized by inspection. An unreadable
coordinator lock is unknown ownership, not an absent coordinator. Observations
may span concurrent progress; every mutation still revalidates its own authority.

For routine settings, run `config edit`. It shows the current file proposal and
available setting names. Enter a setting, its value, and repeat; `done` previews
the complete proposal. Confirming saves the file only. `cancel`, EOF, Ctrl-C, a
negative save response or invalid input leaves the proposal unapplied. Input
errors discard that edit session; the original file stays available.

| Setting | Value |
| --- | --- |
| `merge-policy` | `human` (default) or `automatic` for GitHub delivery; [applying it authorizes existing and future approved work](#using-automatic-merge-policy). |
| `parallelism` | Integer 1–16. Lowering it does not cancel active work. |
| `repairs`, `transient-retries` | Nonnegative integer; consumed allowances are retained. |
| `invocations` | Positive per-task ceiling or `unlimited`. |
| `implementer`, `reviewer`, `planner` | Existing profile name; `planner=none` removes optional planning configuration. |
| `profile.NAME` | `MODEL REASONING TIMEOUT`, e.g. `gpt-6.1-sol high 30m`; `remove` deletes an unreferenced profile. |
| `escalation` | Comma-separated successor profiles, or `none`. The starting implementer is not repeated. |

The editor starts from a valid configuration file; correct malformed manual edits
before opening it. The whole proposal must validate, including the installed Codex catalogue.
Profiles and their references can change together. Repository identity, roots,
quality agreements and delivery destination are not editor settings. Initial setup
still consumes the strict configuration example above; this editor handles
execution and merge policy changes to an initialized local setup.

The noninteractive equivalent previews first, then saves the exact proposal:

```sh
/tmp/runner-next --config /absolute/runner.json config edit \
  --set parallelism=2 --set repairs=1 \
  --set 'profile.careful=gpt-6.1-sol high 30m' --set escalation=careful
/tmp/runner-next --config /absolute/runner.json config edit \
  --set parallelism=2 --set repairs=1 \
  --set 'profile.careful=gpt-6.1-sol high 30m' --set escalation=careful \
  --save --preview DISPLAYED_SAVE_PREVIEW
/tmp/runner-next --config /absolute/runner.json config explain
/tmp/runner-next --config /absolute/runner.json config apply --preview DISPLAYED_APPLY_PREVIEW
```

Saving uses conditional replacement: concurrent file edits are never overwritten.
The separate `config apply` transaction changes running policy. `config explain`
shows resolved profiles, escalation order, allowances, pinned attempts and queued
effects; `--json` supplies the structured form. It warns when a missing-brief Ready
journey needs three calls but its budget cannot admit them. Existing manual file
editing remains supported. No setting change starts the coordinator, resets
consumption or changes active/retry profiles.

#### Fourth phase 2 qualification

The complete local operator journey is owned by the extended existing
[CLI journey](../internal/runner/cli/cli_test.go), using real Git and SQLite with
one small controlled harness. It now covers:

- Committed-base setup, Ready approval and actual active-review status.
- Editor preview/save/apply during review, including an escalation profile and
  a parallelism change while the active reviewer retains its original reasoning.
- Drain, interrupted review, a rejected unauthorized repeat, explicit resume and
  restart. Exactly one implementation and two audit attempts retain the same
  candidate; the repeated audit keeps its original profile.
- A Plan question, clarified replacement and approval, complete proposal release
  and an exact-version dependency wait. Human and JSON status give the same
  causes/actions in attention-first order.
- Retained candidate/evidence inspection, plan stop, quality adoption and read-only
  reopen. No board, publication, merge or false completion is involved.

The small [editor/watch suite](../internal/runner/cli/operator_test.go) proves
preview, cancellation, EOF and idle-prompt interruption write nothing; invalid
settings, stale save previews and concurrent file changes cannot replace the
configuration; saving does not apply. Watch observes a policy change on its next
snapshot without writing the store. [Operator advice cases](../internal/runner/engine/operator_test.go)
cover actionable limits versus ordinary waits, active/absent ownership and saved
versus interrupted recovery. Existing process tests now also distinguish an
absent, held, released and unreadable coordinator lock. No new test framework,
store schema or execution role was added.

On 2026-10-15, the built binary was exercised in a real macOS arm64 terminal on a
disposable repository: native baseline setup, interactive profile/escalation and
parallelism editing, separate policy application, task intake, human/JSON status,
watch and Ctrl-C at both watch and an idle edit prompt. The walkthrough removed
an unnecessary second preview digest from editing; apply guidance now belongs to
`config explain`. Final inspection left configuration and database hashes unchanged.
The actual installed Codex catalogue accepted `gpt-6.1-sol / high` and rejected an
unsupported model without changing the file. No model calls
were needed because role prompts and harness execution contracts were unchanged.
Existing live role evidence remains applicable to those contracts.

Local walkthrough files and observations are retained at
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-operator-live-lbedxh4q`.
These temporary artifacts are not a portable backup. This is an exercised local
journey, not a study of independent users or evidence of delivered-result usability.
The editor is deliberately line-oriented, without completion or a full-screen UI.

`go test -race ./...`, `go vet ./...` and the `cmd/runner-next` build passed with
Go 1.27.1 on macOS arm64. The complete diff was reviewed for accidental changes,
misleading recovery advice and unnecessary complexity. The replacement still
imports no legacy engine, execution or GitHub package.

This completed the phase 2 local interface boundary. At that boundary, code-host delivery,
confirmed integration and delivered-result inspection remained unimplemented;
release/platform, backup/restore and installation qualification remain phase 4.

### Phase 3, first bundle: durable draft publication

**Historical implementation brief (2026-10-15).** Publish the exact independently accepted
candidate as one GitHub draft PR, with private durable intent and recoverable
remote identity. This bundle stops before integration. A draft is an inspectable
handoff artifact, not current-base delivery proof or task completion.

Supported behavior and boundary:

- Opt in at initialization with `delivery: {"mode":"github-draft",
  "repository":"OWNER/REPO"}`. Local mode remains available. The destination and
  base are immutable setup authority, displayed before initialization and task
  approval; changing them needs fresh state. No Projects configuration is involved.
- This intermediate mode requires explicit `base_freshness: "recorded"` and
  `max_base_refreshes: 0`. It never fetches or claims freshness. The next integration
  bundle owns default refresh, bounded refresh/review, CI, merge policy and
  confirmed completion. It must replace this intermediate mode when ready.
- The existing coordinator admits a deterministic publication worker after local
  acceptance, under shared parallelism and task ownership. Publication does not
  consume model, repair or escalation allowance. Slow hosting must not block
  local reconciliation. Drain admits nothing new; stop/replacement revokes future
  writes, while already-sent effects and their identities remain inspectable.
- Before remote writes, retain task/version/generation, approved base, candidate,
  quality/review binding, destination and a stable unique branch/operation identity.
  Revalidate local authority and candidate immediately before each write. Push an
  exact commit with an absent-ref lease; never overwrite a conflicting branch.
- Observe before writing. Recover only the exact destination/head/base and Runner
  marker. Persist PR-create intent before sending it. After interruption or any
  uncertain create outcome, observations may recover an existing PR; absence
  cannot authorize another create. Duplicate/mismatched identities stop safely.
- No automatic transport retry loop. One bounded reconciliation reports its
  result; `task delivery ID --reconcile` explicitly requests another safe inspection
  or continuation. Known branch publication is reusable; an uncertain PR create
  is inspection-only thereafter. No new model call is implied.
- Human and JSON inspection show publication state, remote link, last observation
  and specific recovery advice. Inspection itself is offline/read-only. Observed
  closed, merged, missing or externally changed PRs never complete tasks or release
  dependencies. Runner never marks ready, merges, deletes, closes or enables
  auto-merge in this bundle.
- A consumer-defined delivery interface and one built-in GitHub adapter separate
  provider transport from coordinator rules. Reuse config-free Git/subprocess
  protections, not legacy Project workflow. Credentials come from the operator's
  GitHub CLI configuration, outside harness environments. No plugin loader.
- Store format 5 adds private publication records and durable reservations. Older
  formats are rejected untouched; no in-flight migration or compatibility layer.

Verification brief, established before implementation:

| Credible failure | Required evidence and owning boundary |
| --- | --- |
| Publication runs without exact acceptance or after revocation | Extend engine journey coverage: stored review/candidate bindings, stop/replacement, no model calls and no false completion. |
| Restart duplicates a PR or overwrites someone else's branch | Engine recovery cases cover lost create response and absent observation; adapter/Git cases assert exact identity, draft flag and absent-ref lease. |
| Slow remote work blocks local work or exceeds capacity | Existing coordinator/store suites extend shared reservations, drain and concurrent progress using explicit channels. |
| Provider output is missing, malformed or names another object | Small adapter fixtures at the real gh request boundary; no full GitHub simulator. |
| Operator cannot understand the handoff/recovery boundary | Built CLI walkthrough of configuration, retained candidate, delivery status and reconciliation; extend existing presentation tests where necessary. |

Reuse existing candidate integrity, sandbox, native model, quality and dependency
coverage. No new model behavior or role prompts are planned, so another live model
run is unnecessary unless implementation changes those contracts. Run targeted
checks, `go test -race ./...`, `go vet ./...`, build `cmd/runner-next`, and review the
complete diff. Verify native Git/gh transport locally without publishing into an
unapproved repository; report any live GitHub write verification limitation.
GitHub's [PR API](https://docs.github.com/en/rest/pulls/pulls) and
[gh API input contract](https://cli.github.com/manual/gh_api) are transport references,
not workflow authority.

#### Using draft publication

Initialize fresh private state and workspace roots with the existing local config,
changing only delivery to:

```json
"delivery": {"mode": "github-draft", "repository": "OWNER/REPO"}
```

Keep `repository.base_ref` as the intended local `refs/heads/BRANCH`, explicitly
select `base_freshness: "recorded"`, and set `max_base_refreshes: 2` for bounded
post-publication verification. The first bundle itself supported only 0.
`init` and `config explain` display the destination and boundary. This intermediate
mode authorizes draft publication, current-base verification and synchronization
after task acceptance, not integration. The third bundle advances the same draft
only from its exact confirmed previous commit. Changing
an initialized destination or base is rejected by execution-policy apply. Local
mode still runs without GitHub. Store formats 1–8 are rejected untouched: preserve
old state/workspaces and initialize fresh roots; no old approvals are imported.

Use an operator-owned `gh auth login --hostname github.com` configuration with
repository contents and pull-request write access. The adapter uses github.com
only, with HTTPS and that same credential configuration for both Git and API
operations. Ambient token, host, proxy and debug overrides are not the credential
contract. Repository remotes, Git hooks and executable Git configuration cannot
redirect publication. A missing login or unavailable host leaves local acceptance
retained and reports the operation needing attention.

After normal Ready approval or Plan release and local acceptance, `run` queues
publication automatically. It pushes the exact accepted commit to the recorded
unique `runner-next/OPERATION_ID` branch and opens one draft PR. The PR includes a
readable task title, task/version identity, initial candidate/base/quality identities
and an explicit pending-integration statement. The description labels this initial
snapshot as historical when the branch later advances; human edits are preserved.
Private evidence files stay local.

- `status` and `task show ID` show the current publication and its last successful
  remote observation. `--json` includes all retained publication records. These
  commands never contact GitHub; a retained link is not a live health check.
- `task delivery ID --reconcile` queues one remote inspection and safe continuation.
  If the coordinator is absent or drained, use `run`. This consumes no model call
  or repair allowance. Missing remote observations are never success.
- Once PR creation has been requested, reconciliation can recover that exact PR
  but cannot send another creation request. If GitHub cannot reveal its outcome,
  inspect the displayed branch/operation identity manually. This conservative
  boundary can leave an uncreated PR requiring operator resolution; there is no
  force-create switch in this bundle.
- Stop or replacement prevents subsequent authorized writes. Already-sent effects
  can settle after revocation, and their records remain in `task show`. Runner does
  not close PRs, remove branches or undo another actor's remote changes.

The coordinator shares `max_parallelism` between model attempts and publication
workers, including reservations recovered after restart. Drain waits for admitted
workers. Publication is independent of model invocation/repair counters and does
not add a second scheduler or an integration lane. Tasks and parents cannot reach
Completed in this bundle, even if a human merges the remote draft externally.

#### Draft-publication qualification

The engine suite extends the existing real-store/real-workspace journey rather
than introducing a workflow simulator. It proves durable intent before POST,
recovery after a lost response, inspection-only restart after uncertain creation,
retained identity when a previously known PR disappears, unchanged model counters,
revocation and changed-candidate rejection, honest dependency waits and no completion
from an observed merge. A channel-controlled slow host demonstrates that local
execution continues within shared capacity and drain settles admitted work.

The small GitHub adapter suite checks literal request fields, draft-only creation,
exact head/base/repository/marker identity and unavailable, malformed or duplicate
observations. A native Git test pushes to a disposable bare repository through a
controlled transport substitution; the production Git argument/environment boundary
is retained. It proves exact-object publication, the absent-ref lease, unchanged
conflicting remote branches and rejection of object redirection. Existing privileged
Git, candidate integrity, sandbox and quality suites remain the owners of their
broader guarantees. The subprocess suite checks the restricted gh environment.

On 2026-10-15, a read-only call through the production GitHub adapter and native
`gh` successfully observed the actual repository and absence of a random branch.
No remote writes occurred. The built `runner-next` binary was also exercised against
a disposable accepted task and controlled remote observation: status, JSON, task
inspection, policy explanation, reconciliation and stop. Inspection left the
SQLite file hash unchanged; reconciliation preserved create intent; stop retained
the remote link. Its logs are at
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-draft-publication-walkthrough-2859547228`.
The controlled URL in those logs is fixture evidence, not a real PR. Temporary
walkthrough harness files were removed from the repository.

`go test -race ./...`, `go vet ./...` and the `cmd/runner-next` build passed
with Go 1.27.1 on macOS arm64. The final transport-boundary cleanup and operator
advice were checked with targeted race tests; vet and build passed again.
The complete diff was reviewed for authority gaps, accidental changes and
unnecessary complexity. The replacement still imports no legacy engine,
execution or GitHub package. This bundle adds no dependency, model role or board
connection. Stop here; the next bundle must establish current-base integration
proof and its bounded recovery before any merge or task-completion behavior.

Real GitHub branch/PR writes have not been exercised in this bundle. Model roles,
prompts and native harness permissions are unchanged, so existing live Codex
qualification remains applicable and no new model call was required. Platform,
release, backup/restore and the full merge journey remain later work.

### Phase 3, second bundle: current-base verification and bounded refresh

**Implementation and verification brief (2026-10-15, before code).** After draft
publication, verify the accepted candidate against the configured delivery base.
Keep the original approval and publication immutable. A clean refresh produces a
new retained workspace/candidate, reruns the pinned native checks and independent
review, and observes the base again after review. A proof names the exact base,
candidate, quality agreement, review and observation time; it is not a promise
that the base cannot move later.

- Reuse the coordinator's durable attempt/reservation and process ownership for
  a deterministic `refresh` action. Fetch/check work shares capacity with model
  and publication work, without consuming model invocations or escalating models.
  Review uses the existing audit/verification roles and invocation allowance.
- GitHub draft mode fetches the explicit destination/base through protected Git
  transport. Local mode can explicitly verify its local base without GitHub.
  `task verify-base ID` queues inspection/recovery; status/show remain offline.
  GitHub verification starts automatically after the first confirmed draft.
- Each changed base reserves one refresh before workspace construction. Persist
  its target and count so restart/retry cannot reset the allowance or charge the
  same interrupted cycle twice. `max_base_refreshes` becomes nonnegative, with 2
  in the example policy; 0 permits inspection but no changed-base rebuild.
  Exhaustion requires an explicit policy allowance change and verification request.
- Changed source/base invalidates acceptance. Use a separate workspace per cycle,
  preserving earlier candidates, attempts and draft identity. Reuse sufficient
  evidence only for the exact unchanged candidate/base. Pinned quality-source drift,
  merge conflicts and failed native checks stop with actionable attention; this
  bundle does not invent a conflict-resolution model workflow. Ordinary review
  defects retain the existing bounded repair policy.
- A refreshed accepted candidate is retained locally. The original remote draft
  remains explicitly labelled as an earlier candidate. No second PR or branch
  overwrite is authorized. Remote draft synchronization, CI, serialized merge,
  conflict repair, default pre-implementation fetching and task/parent completion
  belong to subsequent delivery work. Recorded starting-base mode remains explicit.
- Store format 6 rejects earlier formats untouched; no compatibility layer.

Extend the existing real-store/real-Git engine journey for unchanged base, clean
movement, movement during review, restart/retry and exhausted allowance; prove
stale acceptance cannot qualify, counters persist, stopped authority cannot advance,
and existing publication identity survives. Extend native workspace tests for
protected literal fetch. The engine journey uses native Git for clean/conflicting
merges and immutable earlier workspaces, avoiding a duplicate workspace fixture.
Extend the owning configuration/operator suites for the one new limit and command.
Reuse shared reservation, sandbox and review suites rather than duplicate them.
Run targeted tests, `go test -race ./...`, `go vet ./...`, build `runner-next`, and
review the complete diff. The clean merge uses the documented
[Git merge-tree contract](https://git-scm.com/docs/git-merge-tree), accepting only
a successful exit status before constructing a candidate. Exercise the built CLI and a live refreshed-candidate
audit with the authorized `gpt-6.1-sol` / `high` profile. Remote writes are outside
this bundle; report any transport or platform qualification limits accurately.

#### Current-base verification qualification

Implemented in the same `codex/runner-local-core` worktree. The coordinator uses
the existing attempt reservation for deterministic fetch/check work; no scheduler,
table, dependency, model role or public plugin framework was added. Approval stays
at its original base. Each changed-base cycle has a distinct retained workspace;
its target and consumed allowance are committed before construction. The store
checks the current allowance in that transaction, including a concurrent policy
change. Repeated inspection of an unchanged base spends neither a refresh nor a
model invocation. A changed candidate clears review and needs the existing audit.

The real-store/real-Git engine journey covers unchanged-base evidence reuse, two
successive base movements (including one after review starts), exhaustion and
explicit additional allowance, failed native checks, interruption before result
persistence, merge conflicts, stopped authority and candidate tampering. It checks
that the original workspace and draft identity survive, each reserved cycle is
charged once, and no refresh can complete a task. Existing shared-capacity tests
now also race a deterministic refresh claim against a model claim. The native Git
transport test verifies the literal fetch destination, private ref and unchanged
checkout/FETCH_HEAD; the configuration and operator suites cover the new allowance
and recovery advice. Existing quality, sandbox, review and publication suites
remain responsible for their broader contracts.

On 2026-10-15, a refreshed fixture candidate passed native sandboxed checks and a
real independent Codex audit using `gpt-6.1-sol` / `high`; initial fixture acceptance
was controlled, so this is qualification of the new refresh/review path, not a new
full implementation journey. The built CLI exercised human/JSON inspection,
configuration explanation, `task verify-base`, coordinator admission and drain.
Inspection preserved the database hash, and re-observation preserved all consumed
allowances. Evidence and the disposable repository/state are retained at
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-current-base-qualification-2503460638`.
A read-only production Git fetch of `cortexium-io/runner`'s `main` into that
disposable repository also passed. No GitHub write occurred. Temporary qualification
harnesses were removed.

`go test -race ./...`, `go vet ./...` and the `runner-next` build passed on macOS
arm64, Go 1.27.1 and Git 2.54.0. Final presentation and crash-recovery assertions
received focused race checks; vet/build passed again. The complete diff was reviewed for authority,
retained-work recovery, stale evidence, unnecessary complexity and accidental
changes. Replacement packages still import no legacy engine, execution or GitHub
package.

Stop at this boundary. Base proof is an exact, timestamped observation. The next
delivery bundle must synchronize the refreshed candidate to the existing draft
with an exact remote lease, then establish CI and serialized integration authority
with another current-base check before merge. Default pre-implementation fetching,
bounded conflict repair, confirmed task/parent completion and platform/release
qualification remain pending. There is no automatic polling after proof is retained;
`task verify-base ID` explicitly requests another observation. Formats 1–5 require
fresh private state; earlier evidence/workspaces are preserved, not migrated.

### Phase 3, third bundle: remote draft synchronization

**Implementation and verification brief (2026-10-15, before code).** Advance an
existing Runner draft to the exact refreshed, independently accepted candidate.
Keep the same repository, base branch, head branch, PR number/URL and original
Runner marker. Stop before CI assessment, marking ready, merge and completion.

- Reuse publication workers, shared admission and task ownership. Record each
  update as an immutable publication intent linked to its confirmed predecessor;
  retain the original creation record and the last observation for each operation.
  No new scheduler, table, model role, external configuration or plugin framework.
- Queue synchronization after the exact candidate has retained current-base proof.
  Pin its candidate, quality/review binding, observed base/time, expected previous
  commit and known PR identity before any write. Revalidate task authority and
  candidate integrity immediately before dispatch. A timestamped base observation
  remains historical evidence; integration must check the latest base again.
- Observe the exact open draft and marker before pushing. Update only the exact
  expected remote commit using an explicit Git lease. Missing branches/PRs,
  changed identities, external commits, closed/merged PRs and ready PRs require
  attention. Never recreate a branch, open another PR or overwrite an external
  change to recover a synchronization operation.
- Observe after the write, including a failed/lost response. The target commit on
  the exact draft confirms success; the previous commit permits another leased
  attempt only through explicit reconciliation or restart of an interrupted
  reservation. Other observations stop safely. There is no automatic retry/poll
  loop. `task delivery ID --reconcile` remains the recovery command.
- Settle an outstanding publication before requesting another base refresh. Check
  this dependency in the store transaction as well as operator guidance, so a
  racing refresh cannot discard an uncertain branch update. Stop/replacement
  still revoke authority and retain already-sent effects and links.
- Synchronize the branch; preserve the PR title/body and human edits. Initial PR
  descriptions explicitly label their candidate/base details as the initial
  snapshot, which can become historical when Runner advances the branch. The local
  task view retains exact proof and synchronization history. No metadata PATCH or
  bot comment is needed for this bundle.
- Human/JSON status distinguish a current confirmed draft, an earlier draft and
  synchronization needing attention. Synchronization consumes no model, repair,
  escalation or base-refresh allowance and cannot complete a task or dependency.
- Store format 7 permits the immutable predecessor chain. Earlier formats are
  rejected untouched; initialize fresh state rather than migrate live authority.

Extend the owning engine delivery journey with real store/workspaces for a clean
refresh and two successive synchronizations, uncertain writes and restart, exact
identity conflicts, revocation, changed candidates and unchanged counters. Extend
the existing native Git transport fixture to prove expected-old leases refuse both
external changes and deleted branches. Extend the GitHub adapter fixtures for
previous/target head observations and known PR identity; reuse existing publication,
refresh, shared admission and sandbox coverage. Test the store race between queuing
synchronization and requesting refresh. Keep helpers small and specific.

Run targeted checks, `go test -race ./...`, `go vet ./...`, build `runner-next`,
review the complete diff, and exercise the built CLI's synchronization/recovery
presentation. Use a disposable bare Git remote for write verification and a live
read-only GitHub observation where useful. No live remote write destination is
authorized by this repository implementation request. Model contracts are unchanged;
reuse the previous bundle's live Codex qualification unless implementation changes
those contracts. Record evidence and any remaining verification limits here.

#### Using synchronized drafts

After a changed base passes pinned checks, independent review and base
re-observation, `run` automatically queues the refreshed candidate for the same
draft. `task show ID` displays each retained operation, its previous/target commits,
known PR and timestamped base proof. `status --json` exposes those same records;
inspection does not contact GitHub. Earlier records describe historical candidates,
while the summary identifies the latest confirmation or required action.

If synchronization needs attention, inspect the reason, then use
`task delivery ID --reconcile` and `run` if the coordinator is absent or drained.
The target commit confirms a successful earlier push without another write. The
expected previous commit permits the same leased update. A missing or changed
remote identity requires attention and cannot be force-created or overwritten.
Settle the operation before requesting another `task verify-base ID`. Stop and
replacement retain prior operations and remote effects. No new configuration,
model invocation or allowance is required for synchronization.

#### Draft-synchronization qualification

The existing engine delivery suite now proves two successive real Git refreshes
and synchronizations with one retained branch, PR and original marker. It checks
exact base/review binding before queueing, unchanged counters, restart of an
interrupted reservation, lost push/observation responses, explicit leased retry,
remote identity/state conflicts, stop and candidate tampering. Controlled model
and host adapters isolate these coordinator obligations. Existing refresh, review,
quality, shared-capacity and sandbox suites remain responsible for their broader
contracts; no second workflow simulator or generic test framework was added.

The native Git transport fixture proves an exact expected-old update succeeds,
external commits survive rejected updates, and deleted branches are not recreated.
GitHub fixtures accept previous/target heads only on the known draft and original
marker, reject inconsistent branch observations and cannot create a PR from a sync
intent. Independent store connections race refresh against both new synchronization
and explicit reconciliation; exactly one operation may queue. Immutable update
intents and old-format rejection are covered in the owning suites.

On 2026-10-15, the built CLI exercised human/JSON status, full task history,
configuration explanation, attention recovery and stop against a disposable task.
Inspection preserved the database hash; reconciliation confirmed the same draft;
stop retained its link. The fixture used real Git/store state and controlled
acceptance/remote observations. Its evidence is retained at
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-draft-sync-qualification-636930304`.
The URL in that fixture is controlled evidence, not a real PR. A separate read-only
call through the production GitHub adapter and native `gh` successfully observed
`cortexium-io/runner` and absence of a random branch. Temporary qualification
harnesses were removed. No GitHub branch or PR write was made.

`go test -race ./...`, `go vet ./...` and the `cmd/runner-next` build passed
on macOS arm64 with Go 1.27.1 and Git 2.54.0. The complete diff was reviewed for
authority, uncertain-write recovery, accidental changes and unnecessary complexity;
`git diff --check` passed. The replacement still imports no legacy engine,
execution or GitHub package. There are no new dependencies, model roles or
configuration fields. Gate logs are retained with the CLI qualification evidence.

Git leases atomically protect the expected branch commit; they cannot atomically
lock GitHub PR metadata. Runner observes the draft before and after its one push.
If another actor closes or marks it ready during that window, reconciliation
requires attention and retains any already-sent effect; Runner never marks ready,
merges or completes the task. Current-base proof remains a timestamped observation.
CI assessment and serialized integration with another current-base check are the
next delivery boundary. Platform/release and live GitHub write qualification remain
pending. Model contracts are unchanged, so the preceding bundle's live Codex
qualification remains applicable without another invocation. Store format 7
requires fresh state; earlier state and workspaces are preserved untouched.

### Phase 3, fourth bundle: CI assessment

**Implementation and verification brief (2026-10-15, before code).** Assess the
owner's required hosted checks for an exactly confirmed draft and current-base
proof. Preserve the existing publication, refresh and review contracts. Stop before
marking ready, merge, task completion, automated CI reruns or CI repair.

- Extend the existing quality agreement with optional `required_ci` entries:
  exact `kind` (`check` or legacy `status`), `name`, and positive `app_id` for check
  runs (zero/omitted for status contexts). Names and producer identity are explicit;
  agents and observed green jobs cannot invent or weaken requirements. The existing
  preview/check/apply and task adoption flows own changes. Local baseline exceptions
  do not excuse hosted CI failures. An absent list is unconfigured, never passed.
- Add one narrow read-only checks capability. Keep provider JSON in the GitHub
  adapter and assessment policy in core. Reuse publication reservations and global
  capacity; no new scheduler, table, model role, queue or plugin framework.
- Automatically request one assessment once a current confirmed draft has exact
  base proof. `task assess-ci ID` explicitly requests another observation after
  pending/missing/failing checks or an outage. Inspection commands remain local and
  read-only. No polling loop, CI rerun, provider mutation or model invocation.
- Retain the latest assessment per publication, bound to its immutable candidate,
  quality/review identity and exact base proof, with observation time, selected
  commit/source, required-check results and actionable reason. Reassessment clears
  current success while queued or running. Interrupted reads may repeat safely;
  stop/replacement and refresh invalidate current applicability, preserving history.
- Observe the known repository, branch, PR number/URL, marker, open draft state and
  exact head. Require the remote base to equal retained base proof. Assess the test
  merge commit when it has checks, otherwise the candidate head; test merge parents
  must be exactly the observed base and candidate. Unknown, incomplete, truncated,
  inconsistent or ambiguous observations cannot pass. Bound one observation to
  100 contexts per commit; larger responses require attention, not partial success.
- Evaluate every pinned requirement. Missing jobs remain missing; recognized active
  states remain pending; failures/cancellation/timeouts/action-required/stale outcomes
  fail. Successful, skipped and neutral completed check runs satisfy this hosted
  check contract, with the exact conclusion visible. Legacy statuses require success.
  Unknown outcomes and duplicate matching jobs are unavailable. Unrelated checks
  neither satisfy a required job nor cause an automatic code rejection.
- Human/JSON status distinguish unconfigured, missing, pending, failed, unavailable,
  stale and passed observations. Passing means the pinned CI agreement was observed
  satisfied, not that every live GitHub protection, required review, workflow trigger,
  merge queue or merge condition has been verified. Future integration must recheck
  current remote policy, base, candidate and CI; this evidence grants no merge authority.
- Failed CI requests operator inspection and explicit reassessment; bounded code
  repair is a later delivery bundle and cannot silently consume repair/escalation
  allowance here. Neither CI success nor a remotely merged PR completes dependencies.
- Store format 8 rejects prior formats untouched. Preserve earlier state/workspaces;
  initialize fresh state instead of migrating live authority.

Extend existing quality agreement tests for validation/pinning/adoption. Use a small
pure assessment table for missing/pending/failed/unknown/duplicate/wrong-producer
results. Extend GitHub fixtures for complete head/test-merge observations and exact
identity, truncation and base changes. Extend the real-store engine delivery journey
for automatic assessment, explicit retry, interrupted read recovery, invalidation,
unchanged counters and shared capacity. Keep helpers small and reuse existing
publication, refresh and operator coverage. Exercise the built CLI and a production
read-only GitHub query; no remote writes or new model qualification are needed.
Run `go test -race ./...`, `go vet ./...`, build `runner-next`, review the complete
diff and record qualification/limits here before stopping.

Provider semantics were checked against GitHub's [required-check guidance](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-required-status-checks)
and [GraphQL reference](https://docs.github.com/en/graphql/reference/commits).
The adapter follows head/test-merge selection; Runner's pinned agreement remains
separate from the live branch-protection decision reserved for integration.

#### Using CI assessment

Add hosted requirements to the same `QUALITY.json` used by `quality preview`,
`quality check` and `quality apply`, preferably before authorizing a Ready task.
For example, this fragment selects a named check from one specific app and a legacy
status context (replace both names and the app ID with the repository's requirements):

```json
"required_ci": [
  {"kind": "check", "name": "test", "app_id": 15368},
  {"kind": "status", "name": "external-validation"}
]
```

Keep the existing definition, sources and local checks in the full specification.
The existing quality preview displays the exact requirements; baseline execution
still runs local checks only. Hosted CI is assessed on the published candidate.
`quality show` exposes the applied/pinned list. A changed list creates a new quality
revision; existing work adopts only through the displayed task adoption command,
which creates an unapproved version. An empty list never silently passes CI.

After publication/synchronization and exact base proof, `run` admits one CI read.
`status` identifies waiting jobs, required attention or retained passing evidence;
`task show ID` lists the required results, exact conclusions, safe detail links,
selected commit/source and observation time. The JSON forms include the complete
bounded observation. Inspection makes no network call and does not start work.

Use `task assess-ci ID` after pending/missing jobs settle, a provider outage clears
or a failure is resolved, before any repair admission; then `run` if absent or drained. This requests one read,
not a CI rerun or code repair. A refreshed candidate needs publication confirmation
and its own assessment. A new base observation invalidates the previous assessment;
old publication records remain inspectable. A passed result is timestamped evidence
for the pinned list, not permission to merge or a claim that live branch protection
has been evaluated. The human status never reports completion or releases dependencies.
The fifth bundle below adds automatic bounded repair for eligible failures.

#### CI-assessment qualification

The pure core table covers missing jobs, unrelated green results, wrong producers,
duplicate matching jobs, active states, terminal conclusions and legacy statuses.
Quality tests cover explicit validation, revision changes and adoption. The existing
real-store/Git delivery fixtures prove automatic first assessment, explicit repeated
reads, interrupted reservation recovery, stale requests after refresh, task pinning
across default-quality changes, remote identity/base changes and candidate tampering.
A channel-controlled read demonstrates shared capacity, concurrent unrelated local
work and historical-only results after stop. No copied legacy suite, polling test
framework or new dependency was introduced.

GitHub fixtures cover complete head/test-merge selection, exact parents and check
commit provenance, required producer identity, absent checks, unknown context types,
truncation and omitted fields, partial API errors, unsafe detail links and changed
PR identity/state. Existing publication fixtures remain the owner of shared marker,
repository, base branch and duplicate-PR validation. Native workspace, sandbox,
quality and refresh suites retain their existing responsibilities.

On 2026-10-15, the built CLI exercised pending/passed status, JSON, required-check
details, quality inspection, explicit reassessment and stop against a disposable
confirmed draft. Read-only inspection preserved the database hash; queued reads
removed current success; stop retained the draft link and historical evidence.
The fixture used real Git/store state and controlled acceptance/provider results.
Evidence is retained at
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-ci-qualification-2699375809`.
A production read-only query decoded four hosted checks on historical Runner PR
105's exact head. Full assessment correctly refused that merged, non-Runner-marked
PR. This qualifies the live query/decoder, not a live end-to-end accepted draft.
No GitHub writes occurred, and temporary qualification harnesses were removed.
Model invocation contracts and sandbox permissions are unchanged; preceding live
Codex qualification remains applicable without another invocation.

`go test -race ./...`, `go vet ./...` and the `runner-next` build passed on
Go 1.27.1, macOS arm64, Git 2.54.0. The final historical-result and interrupted-read
wording changes passed the focused operator race test and rebuilt CLI walkthrough;
the stopped-task inspection again preserved the database hash. Gate logs are kept
with the qualification evidence above. The complete diff was reviewed and
`git diff --check` passed. The replacement still imports no legacy engine,
execution or GitHub packages; model prompts and release packaging are unchanged.

Stop before merge. Integration must still verify current host protections and
required reviews, check applicability (including workflow events/merge queues),
current base/candidate/CI, and the authorized merge outcome. Automatic observation
polling, CI reruns and bounded CI repair are not implemented. More than 100 contexts
on either observed commit require attention rather than partial assessment. Store
format 8 requires fresh private state; prior state/workspaces are preserved.
Platform/release qualification and a live complete merge journey remain pending.

### Phase 3, fifth bundle: bounded CI repair

**Implementation and verification brief (2026-10-15, before code).** Extend the
existing repair journey to current, complete failed CI evidence. Stop before merge,
marking ready, task completion, workflow reruns, automatic polling or conflict repair.

- Derive one implementer repair from the latest confirmed publication only when
  its assessment still matches the accepted candidate, quality, review and base
  proof. Every required result must be terminal and understood: passed, or explicit
  check/status `FAILURE`. Pending/missing/unknown/duplicate results, cancellation,
  timeouts, startup errors, action-required results and stale evidence cannot
  automatically diagnose a code defect. They retain inspection/reassessment advice.
- Reuse `max_repairs`, invocation limits, capacity, drain and task ownership. Reserve
  one repair and invocation atomically before dispatch; recheck the exact assessment
  in that transaction so concurrent reassessment, refresh or stop wins safely.
  Zero/exhausted allowance waits visibly; policy edits retain consumed counters.
  CI repair keeps the currently selected implementer escalation level. Only a
  subsequent independent reviewer defect advances the configured ladder.
- Derive eligibility without changing authoritative state during inspection or
  while blocked on a limit. Keep `task assess-ci` available until repair is admitted.
  After admission, ordinary retry/resume reuses the reserved pass and pinned profile;
  interruption cannot obtain a fresh repair. No new command, queue, table or scheduler.
- Bind the failed assessment and publication identity to the task/attempt. Preserve
  the published candidate in its existing workspace and seed a separate repair
  workspace from its exact commit. Never reset partial repair work on retry/resume.
  Clear current acceptance/base proof when admitting repair. Preserve approval,
  quality requirements, original scope and consumed base-refresh allowance.
- Supply bounded CI results as untrusted diagnostic context to implementation and
  review, not as revised requirements or permission to fetch links. The implementer
  must reproduce/diagnose locally, reuse sufficient tests, and report missing evidence
  or a decision when the failure cannot be explained. This bundle does not download
  arbitrary hosted logs or grant network access. Unchanged source is not a repair.
- A changed candidate must pass pinned local checks and independent audit/focused
  verification, then current-base verification and existing leased synchronization
  to the same draft. Only that confirmed candidate receives a new CI assessment.
  A subsequent eligible failure consumes another remaining repair; success remains
  Awaiting delivery and never satisfies dependencies. Old observations stay historical.
- Keep human/JSON status explicit about CI repair, its cause, actual profile and
  remaining allowances. Store format 9 rejects older state untouched; preserve
  earlier state/workspaces and initialize fresh private state.

Extend the existing CI policy table for repair admission, the real-store/Git CI
journey for limits, assessment races, retained workspace, retry/restart, fresh review,
base verification and same-draft synchronization, and existing operator coverage.
Reuse the established capacity, review-repair/escalation, stop and quality suites;
do not copy them. Qualify the changed role context with a small opt-in live Codex
pair: a reproducible defect repaired using the owning tests, and insufficient CI
evidence reported honestly without speculative edits. Use the already selected
`gpt-6.1-sol` / `high`. Exercise the built CLI; run `go test -race ./...`,
`go vet ./...` and the build; review the complete diff and record evidence here.
No remote writes or new GitHub API behavior are required for this bundle.

#### Using bounded CI repair

Keep using the existing execution policy: `repairs` bounds all additional
implementation passes together; `invocations` and `parallelism` still apply.
No CI-specific budget or model profile is needed. With a configured allowance,
`run` derives a repair from current complete explicit failure evidence. It keeps
the current implementer escalation level; a later independent review rejection
uses the existing escalation rule. A failure does not prove that code is at fault:
the implementer must diagnose locally and report a gap/decision when it cannot.

`status` and `task show ID` explain the CI cause and limiting allowance. The detailed
view shows the prospective profile before admission, the actual attempt afterward,
and the source publication/candidate. Inspection and blocked eligibility consume
nothing. To reassess a potentially transient job failure before repair starts,
use `task assess-ci ID`; the reservation checks that this newer request wins over
an older repair observation. Drain before editing allowances when you want admission
to remain paused. Raising a limit never erases consumed work.

Admitted repairs use a separate workspace seeded from the failed commit. A retry
or explicit interrupted-attempt resume keeps partial edits and the same reserved
pass. The old draft stays at its confirmed commit until the new candidate passes
native checks, independent review and base verification. Synchronization updates
that same draft under the existing exact commit lease, then assesses CI once.
Further eligible failures spend remaining repair allowance. Unchanged source,
missing diagnostics, integrity failures and exhausted limits remain explicit
attention. Success still stops at **Awaiting delivery**.

#### CI-repair qualification

The core assessment suite now checks repair admission for explicit failures versus
mixed pending/missing/unknown results, infrastructure outcomes, changed proof,
queued reassessment and stop. The existing real Git/SQLite CI journey now covers
zero/exhausted allowance, invocation ceilings, same-transaction reassessment races,
unchanged escalation level, partial edits across transient retry/restart, manual
resume, stop, retained original workspace, fresh review and base proof, leased
same-draft synchronization and reassessment of the new candidate. Focused cases
check that unchanged source (including permission-only changes outside Git's tree)
or externally changed source cannot progress to publication. A no-op is an explicit
proof gap, not a saved-result retry that would repeatedly fail.
Existing capacity, reviewer-defect escalation, quality/adoption and refresh suites
retain their responsibilities; no separate test framework or copied legacy suite.

On 2026-10-15, the opt-in native Codex pair passed using `gpt-6.1-sol` with `high`
reasoning. The reproducible case fixed whitespace normalization in `greet.py`,
kept the sufficient owning unittest suite unchanged, and passed its exact-output
checks. The insufficient-diagnostics case kept source/tests unchanged and reported
that the Windows-only hosted failure could not be diagnosed from available evidence.
Prompts, native invocation logs, results and fixture source are retained at
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-ci-repair-eval-1232309559`.
This qualifies the added diagnostic context on two bounded cases, not general
model reliability or a complete live hosted repair/review journey. Previous review
qualifications still apply; sandbox and provider interfaces are unchanged.
Final review made the stage boundary explicit in the added CI context: local
reproduction belongs to implementation, while audit only inspects supplied proof.
A third live case accepted a repaired candidate with actual local check evidence,
kept source/tests unchanged and used only file inspection commands; it did not
treat the historical failed CI as a defect in the new candidate. That evidence is
retained at
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-ci-repair-eval-3549975306`.

The built CLI walkthrough covered limited/eligible repair, prospective profile,
JSON status, policy explanation, explicit reassessment before admission, reservation,
stop and historical evidence. Read-only commands preserved the database hash.
It used disposable real Git/store state with controlled provider/acceptance results;
logs are retained at
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-ci-repair-cli-777190604`.
The temporary qualification test was removed. No GitHub writes occurred.

`go test -race ./...`, `go vet ./...` and the `runner-next` build passed on
Go 1.27.1, macOS arm64, Git 2.54.0. After final review, the clarified review prompt
passed the execution race suite and live audit above; the final no-op boundary
passed its real-store/Git cases under race detection. Vet and the build were rerun
after those changes. Gate logs are retained with the CLI evidence. The complete
diff was reviewed, `git diff --check` passed, and the replacement still imports no
legacy engine, execution or GitHub packages. No release packaging changed.

At the fifth bundle boundary, merge, marking ready, workflow reruns, automatic
CI polling, conflict repair and confirmed task/dependency completion remain
unimplemented. Integration still needs current host protections/reviews, check
applicability and current base/candidate/CI
verification. Store format 9 requires fresh private state; prior state and retained
workspaces are left untouched. Platform/release qualification remains pending.

### Phase 3, sixth bundle: bounded conflict repair

**Implementation and verification brief (2026-10-15, before code).** Extend
current-base refresh with bounded repair of ordinary text content conflicts.
Stop before merge, marking ready, task/dependency completion or new host operations.

- Keep the accepted source candidate untouched. For supported conflicts, record the
  exact source workspace, reserved target base/cycle, conflict tree and paths from
  Git's machine-readable
  merge result. Exit status and conflict types decide eligibility; a tree or missing
  markers alone do not prove a clean merge. Binary, rename/delete, mode, submodule
  and unknown conflict types require attention. A missing product decision must
  remain a question, even when the conflict is mechanically a text conflict.
- Queue an ordinary implementer continuation only for supported content conflicts.
  Admission atomically consumes one shared `repairs` pass and one invocation,
  checks current authority/policy/ownership, and switches to the already reserved
  base. No additional refresh cycle and no model escalation. Zero/exhausted limits
  wait visibly; policy changes retain counters. No new budget, command or scheduler.
- Seed a separate workspace from the recorded conflict tree, retaining the source
  commit as HEAD until Runner constructs the resolved candidate. Do not commit
  conflict markers as a baseline. Completed seeding is recognized before any model
  runs; retries/resumes preserve partial edits and their reserved pass/profile.
  Interrupted or altered partial initialization fails closed for inspection instead
  of resetting unknown content. Stop/replacement revokes admission and saved results.
- Give implementation and review the exact source/target and conflicting paths as
  diagnostic data, without permission to fetch, change Git administration, broaden
  scope or weaken tests. Preserve required behavior from both sides. Unresolved
  markers cannot pass to checks/candidate construction. Existing sufficient tests
  remain sufficient; new tests need a concrete behavior/regression requirement.
- Invalidate acceptance and base proof on admission. Require pinned local checks,
  independent review, another current-base observation, and existing leased sync
  to the same draft before fresh CI assessment. Further base movement uses the
  remaining refresh allowance; further defects use the remaining repair allowance.
  Keep CI/conflict evidence historical when later repair types replace its context.
- Status/show explain conflict cause, target, paths, profile, limits and retained
  work. Unsupported conflicts advise inspection and revised work instead of a
  blind refresh loop. Store format 10 rejects older private state untouched.

Extend the owning real Git/SQLite refresh suite for retained source, exact target,
zero/exhausted allowances, non-escalation, partial edits across retry/restart,
revocation, unresolved markers, fresh review/base proof and same-draft sync.
Extend the same Git journey for actual text and unsupported conflicts and
interrupted initialization, and the workspace suite for conflict-record parsing;
reuse existing shared admission, repair/escalation, quality, CI, publication-lease
and stop suites. Qualify changed role context using
a small native Codex pair: a resolvable conflict with sufficient owning tests,
and a missing product choice reported honestly. Use `gpt-6.1-sol` / `high`.
Exercise the built CLI, run `go test -race ./...`, `go vet ./...` and the build,
then review the complete diff. Record actual evidence here before committing.

Git parsing follows the documented exit status and NUL-delimited conflict records
in the [official merge-tree contract](https://git-scm.com/docs/git-merge-tree).
Only known ordinary content conflicts enter automatic repair in this bundle;
additional conflict classes need their own safe resolution contract and evidence.

#### Using bounded conflict repair

Keep the existing configuration: `base-refreshes` bounds changes of target base;
`repairs` bounds all additional implementation passes, including review, CI and
conflict repairs together. `invocations`, `parallelism`, drain and ownership still
apply. No conflict-specific profile or allowance is needed. A conflict keeps the
current implementer escalation level. Only an independent reviewer defect moves
up the implementation ladder.

After publication, current-base verification runs as before. `task verify-base ID`
also requests it explicitly for an accepted candidate. A supported text conflict
records its source candidate, target base and conflicting paths, then waits for
repair admission. `status` and `task show ID` explain the cause, actual/prospective
profile and limiting allowance. Use `config edit`, `config explain` and
`config apply` to change limits; consumed work is never reset. Inspection does not
launch a repair or spend allowance.

An admitted repair gets a separate workspace containing both sides of the text
conflict. The published source stays untouched. Retries and explicit interrupted
resumes preserve partial work and the reserved pass/profile. If initialization
itself was interrupted, unknown contents remain for inspection; Runner does not
reset them or silently repeat a model call. A missing product choice remains
`needs_input`, without executing project checks against unresolved source. Revise
and approve the task when resolving a product question or an unsupported conflict.

Resolution must remove markers and preserve required behavior from both sides.
Pinned checks and fresh independent review precede current-base verification and
leased synchronization to the same draft; CI is assessed again for that candidate.
Further base movement consumes another refresh cycle. Exhausted limits, failed
checks or missing proof remain visible; no conflict is accepted just because a
model says it is resolved. Successful delivery remains **Awaiting delivery**.

Automatic repair supports ordinary regular-file text content conflicts. Binary,
symlink, rename/delete, mode, submodule and unknown conflict classes require
inspection and revised work. This boundary avoids silently selecting one side of
a conflict without an established resolution contract. Store format 10 requires
fresh private state; older databases and workspaces are preserved unchanged.

#### Conflict-repair qualification

The owning real Git/SQLite refresh suite covers exact source/target retention,
zero/exhausted repair allowance, invocation limits, unchanged escalation level,
partial edits across transient retry and restart, and lost-result recovery after
constructing a candidate with both parents. It checks fresh review/current-base
proof, preservation of clean base changes, same-draft synchronization and fresh
CI assessment, another conflicting base within the remaining allowance, and stop.
Separate cases reject unresolved markers and partial initialization, retain an
honest product question without running project checks, and reject revoked results.
The earlier unsupported-conflict case now uses an actual binary conflict and
checks that status advises inspection/replacement, never automatic replay.
The workspace parser suite checks NUL-delimited filenames and rejects malformed,
unknown or incomplete conflict records. Existing shared policy, reviewer-defect
escalation, quality, explicit resume and publication-lease suites remain authoritative.

On 2026-10-15, native Codex qualification passed with `gpt-6.1-sol` / `high`.
The implementer combined the base's `Welcome` greeting with the candidate's
whitespace normalization, reused the sufficient exact-output unittest suite
unchanged, and passed its checks. An independent evidence audit accepted the
resolution using actual implementation events and file inspection only. In the
paired missing-choice case, the implementer left source unchanged and asked which
greeting was intended. Three native calls, results, source and event logs are
retained at
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-conflict-repair-eval-2531439474`.
The source-only evaluation has no Git checkout: attempted read-only Git diff
commands failed and were not used as acceptance evidence. These bounded cases
qualify the changed role context, not general model reliability or a complete
live GitHub conflict/CI journey.

The built CLI walkthrough exercised limited/eligible conflict repair, human and
JSON inspection, policy edit/save/explain/apply, actual selected profile,
reservation and stop. Eleven commands completed; read-only commands preserved
the database hash. It used disposable real Git/store fixtures with controlled
provider/acceptance results; logs are retained at
`/private/var/folders/q8/ctsgn2bx3t11cyfdts0q_8h80000gn/T/runner-conflict-cli-3694101951`.
The temporary qualification test was removed. No GitHub writes occurred.

`go test -race ./...`, `go vet ./...` and the `runner-next` build passed on
Go 1.27.1, macOS arm64 and Git 2.54.0. Final review tightened conflict-record
termination and replaced text-matched recovery advice with an explicit unsupported
conflict category. The affected parser/real-store/Git cases then passed under race
detection; vet and the build passed again. The CLI help was inspected after its
wording update. Gate logs are retained alongside the CLI evidence. The complete
diff was reviewed and `git diff --check` passed. The replacement still imports no
legacy engine, execution or GitHub packages; no dependencies or release packaging
changed.

At this sixth bundle's boundary, merge, marking ready, workflow reruns, automatic
CI polling and confirmed task/dependency completion remained unimplemented. The
seventh bundle below adds confirmed human integration; Runner-owned merge writes
still need current host protections/reviews and current base/candidate/CI proof.
Platform/release qualification remains pending.

### Phase 3, seventh bundle: confirmed human integration

**Implementation and verification brief (2026-10-15, before code).** Complete the
first human-merge observation journey. Runner still publishes drafts; a human
uses GitHub's review/protection workflow and performs the merge. This bundle
records an already-observed integration, not permission to merge or an attestation
that a human did not bypass host rules. Runner-owned ready/merge/auto-merge writes,
pre-merge host-policy admission, queues, squash/rebase receipts, polling and
pre-implementation fetching remain outside this boundary.

- Add `task integrate ID` to request one read-only inspection of the known PR.
  Require current approval, independently accepted exact candidate, current-base
  proof and passed pinned CI. Inspect once automatically after a new passed CI
  assessment; open/closed/unavailable observations do not spin or invoke a model.
  The operator explicitly requests another observation after human action.
- Reuse publication reservations and the coordinator. Serialize integration
  inspection per repository/base in the store, sharing capacity, drain and task
  ownership with other work. Bind requests to exact proof/CI, and recheck them on
  admission and completion. Restart may repeat this read; it cannot replay writes.
- Read the PR by its known number and verify URL, repository, head/base names,
  candidate head and original publication marker. Deleted source branches must
  not prevent confirmation. Changed identities, closed-unmerged PRs, missing
  fields, unknown states and unavailable reads never establish success.
- For a merged PR, require its ordinary two-parent merge commit: first parent is
  the verified base, second is the accepted candidate, and its tree is exactly
  the reviewed candidate tree. Fetch the configured base through the existing
  protected adapter and confirm this merge is in its ancestry using real Git
  objects. Base movement between proof and actual merge therefore cannot silently
  qualify. Squash/rebase and unsupported topology remain explicit attention.
- Re-read the exact previously selected CI commit and its complete required
  results. Preserve producer and test-merge parent bindings; changed, missing or
  failed checks cannot become success from an older green snapshot. This receipt
  establishes Runner's pinned proof, not historical compliance with host policy.
- Commit the integration receipt, task completion and released reservation in one
  transaction, checking stop/replacement, current approval, dependency and parent
  authority again. A lost completion response is idempotent. Preserve evidence;
  completed tasks are immutable and stop is harmless. Only confirmed exact-version
  completion releases dependencies. Finish a released parent only after every
  original child has a valid integration receipt; stopped/replaced/unmerged work
  cannot finish it. Stopping a plan preserves already completed child receipts.
- Status/show distinguish queued inspection, human merge wait, closed-unmerged,
  unavailable/stale proof and confirmed completion, including merge SHA/link/time.
  Local delivery remains Awaiting delivery. No board, new model prompt, new budget
  or separate scheduler. Store format 11 requires fresh private state.

Extend the owning delivery/CI adapter suites for exact PR observations, deleted
head refs, incomplete data and CI binding; extend real Git/SQLite delivery and
planning journeys for merged ancestry/tree/base proof, atomic completion,
stop/replacement races, serialized admission, restart, dependency/parent outcomes
and ordinary work continuing during a slow integration read. Reuse existing CI
classification, refresh/repair, capacity and publication tests. Test expectations
come from the receipt contract above, not mocked success or test count.
Run the built CLI through inspection/recovery/completion with controlled host data;
qualify new GraphQL fields with a read-only real-host query where available.
No live writes or new paid model evaluation are needed because role behavior is
unchanged. Run `go test -race ./...`, `go vet ./...` and the build, review the full
diff and record actual evidence here. Stop at confirmed human integration.

### Using confirmed human integration

Keep the PR as a draft through current-base verification, synchronization and CI
assessment. Configure `required_ci` in the quality agreement before approving the
task. A new passing CI assessment queues one integration inspection. An open PR
then waits for the human; neither `status` nor the coordinator polls GitHub.

Use GitHub's normal review and protection workflow to mark the PR ready and merge
it with **Create a merge commit**. Runner does not perform those writes. The
verified base must still be the first parent of the merge, the accepted candidate
must be its second parent, and the merge tree must equal the reviewed tree. If the
base moves while the PR is open, return it to draft as needed and use
`task verify-base ID`; Runner's existing refresh, checks, review, synchronization
and CI path must finish before human integration. A green CI observation is not
permission to bypass host requirements.

After the human merge, request confirmation:

```sh
runner-next --config /absolute/path/runner.json task integrate TASK_ID
runner-next --config /absolute/path/runner.json run # if absent or drained
runner-next --config /absolute/path/runner.json task show TASK_ID
runner-next --config /absolute/path/runner.json status
```

`task integrate` queues a read. With the default human policy it cannot lead to a
Runner-owned write; with [automatic policy](#using-automatic-merge-policy), a ready
result may trigger guarded merge admission. It requires the known
PR, current accepted candidate/base proof and passing pinned CI snapshot. The
worker re-reads the selected CI commit, imports the configured base and verifies
the actual merge objects. A deleted source branch is supported. Later base commits
are allowed when the verified merge remains in that base's ancestry. GitHub's
[PR schema](https://docs.github.com/en/graphql/reference/pulls#pullrequest)
retains head name/OID after ref deletion and exposes the actual merge commit and
time; Runner does not use the disappearing potential-merge field as a receipt.

`task show` retains the last assessment and, on success, the immutable merge
receipt with PR, commit, observed base and observation time. Completion releases
exact-version dependents. A released plan completes only when all its original
children are integrated. A replaced child cannot complete the original plan.
Stopping an unfinished plan revokes its unfinished children and preserves completed
receipts. Completed tasks accept repeated inspection/stop harmlessly; create a new
request for further changes.

| Observation | Result and next action |
| --- | --- |
| Open draft or ready PR | Human merge pending; follow host requirements, then `task integrate ID` again. |
| Open PR base moved | `task verify-base ID` through fresh review/synchronization/CI before merging. |
| Closed without merge | No completion or dependency release. Inspect the host and revise or stop the task. |
| Host unavailable, changed identity, missing or failing CI | Retained evidence and explicit diagnostic; resolve and request inspection again. No automatic repair or polling. |
| Squash/rebase, changed merge tree/base or merge absent from fetched base | Cannot confirm this integration. This bundle cannot translate those outcomes into a receipt. |
| Confirmed ordinary merge | Task completes atomically with receipt and reservation release; original dependent work becomes eligible. |

One integration read per repository/base may run at a time, within existing
`parallelism` and drain controls. Other eligible implementation/review work can
continue. An interrupted read may repeat after restart; no write is replayed.
Local delivery mode still retains candidates at Awaiting delivery. There is no
new model profile, escalation policy, repair budget or integration configuration.
Store format 11 requires fresh state; preserve older state for inspection with
its matching binary. This bundle adds no compatibility conversion.

### Human integration qualification

Implemented on 2026-10-15. Verification extends the existing delivery, CI,
planning and adapter fixtures rather than introducing another harness framework.

| Evidence | Result |
| --- | --- |
| Real Git and SQLite receipt journey | Open/ready/closed/outage, wrong PR, fresh CI failure, changed tree/base, squash and non-ancestor merge cannot complete. A correct merge followed by later base history completes; restart, repeated receipt, stop and replacement preserve authority. |
| Coordinator and store ownership | A second integration for the same repository/base waits even with spare global capacity. Unrelated implementation starts during a blocked host read. Drain prevents admission; stop keeps late evidence historical and releases the claim. |
| Planning journey | Real implementation, audit, refresh, draft synchronization, CI and merge receipts release the second child and finish the parent. Additional cases cover stopping after the first merge and a replaced child that cannot complete the original release. |
| Built binary journey | Nine CLI commands exercised request, JSON inspection, human wait, interrupted-read advice, completed receipt/status and idempotent confirmation/stop. Core execution used controlled host observations and real Git/store; no live merge was performed. |
| Read-only GitHub qualification | The production GraphQL query decoded historical Runner PR 105's merge fields; its missing Runner publication marker was correctly rejected. That PR's source ref still exists; deleted-ref behavior is covered by the protocol fixture and documented schema. No remote write was made. |
| Required gates | `go test -race -timeout 15m ./...`, `go vet ./...` and the `runner-next` build passed on Go 1.27.1, macOS arm64, Git 2.54.0. |

The initial `go test -race ./...` reached Go's ten-minute package timeout during
an existing conflict-repair case, without an assertion failure or race report.
The complete rerun passed with a fifteen-minute package budget (the replacement
engine took about 650 seconds); assertions and individual operation deadlines
were unchanged. Final review also revoked old execution-attempt authority once a
task is Completed, preventing recovery from reactivating integrated work. The
affected integration/parent journeys passed again under race detection, as did
final CLI/store checks; vet and the build passed after the last code change.
The complete diff was reviewed and `git diff --check` passed. No dependencies,
role prompts or release packaging changed.

The CLI transcript and qualification logs were retained in the local temporary
verification directory (`/tmp/runner-integration-*.log`). The temporary qualification
drivers are not shipped. Role prompts did not change, so this bundle required no
new paid model evaluation. Live remote merge execution and host protection
admission were not exercised or claimed.

The seventh bundle stopped here. The eighth bundle below defines explicit
Runner-owned admission, ordinary merge execution and ambiguous-write recovery.
Squash/rebase support, automatic polling and release qualification each require
their own explicit boundary; none is implied by this
receipt path.

### Phase 3, eighth bundle: explicit Runner-owned merge

**Implementation and verification brief (2026-10-15, before code).** Add one
operator-authorized ordinary merge of an already-ready PR. Human merge remains
the default; `task integrate` remains read-only. `task merge ID` pins one request
to the accepted candidate, current-base proof and passed CI. This is explicit
per-task authority, not an automatic merge policy or a new configuration system.

- Reuse integration requests, the repository/base reservation and coordinator.
  Admission shares capacity, drain and task ownership. Recheck current approval,
  dependencies and parent authority immediately before dispatch. Stop/replacement
  revokes unsent work; an already-sent remote effect cannot be recalled.
- Support only a known, open, ready PR with exact publication identity and head.
  Require GitHub's clean, mergeable state, satisfied review decision, ordinary
  merge enabled, no auto-merge request, no merge queue and no viewer bypass.
  Require a matching classic branch protection rule with strict required checks
  (at least one), admin enforcement, no deletion/force-push allowances and no PR
  bypass allowances. Ruleset-only protection is outside this first contract;
  additional host rules remain enforced by GitHub's normal merge operation.
- Read the selected pinned CI again, fetch the configured base and require exact
  equality with the accepted base proof. Re-read host admission after the fetch,
  then check local authority immediately before one `mergePullRequest` mutation
  with `expectedHeadOid` and `mergeMethod: MERGE`. Never enable auto-merge, enqueue,
  bypass protections, push directly to the base, or update repository rules.
- GitHub's merge API has an expected-head condition but no expected-base condition.
  The execution contract therefore depends on enforced strict up-to-date checks
  to reject competing base advancement, with host protections remaining enabled
  through the operation. It is not a base compare-and-swap API. Keep the exact
  actual-parent/tree/ancestry receipt check: a different actual base cannot
  silently complete a task even after a successful host response. Concurrent
  administrative weakening of protections or PR retargeting is outside Runner's
  authority; host policy and destination must remain stable during dispatch.
- Durably mark dispatch before sending. One publication may send at most one merge
  mutation; timeout, malformed response, process loss or rejection consumes that
  permission. Inspect once after dispatch and on restart; never replay the write.
  An open or unavailable result is explicit unresolved attention with `task
  integrate` as recovery. A blocked pre-dispatch request may be explicitly retried
  after resolution. Neither outcome polls or consumes a model/repair budget.
- Confirm completion only through the existing exact merge receipt transaction.
  A success response alone cannot complete work. Preserve the admission and write
  marker in show/JSON and distinguish queued merge, blocked admission, uncertain
  dispatch and confirmed completion. Format 12 requires fresh private state.

Extend the existing integration Git/SQLite fixture with sequential admission,
base/CI changes, one conditional dispatch, failed/lost response, restart before
and after dispatch, and stop before the write. Reuse existing integration tests
for reservations, concurrent local work, completion races and dependencies. Extend
the GitHub protocol suite for complete live policy decoding, denied/unknown or
missing fields, exact conditional mutation and authorization at dispatch. Keep
all remote writes controlled; qualify the new production query/schema read-only
against GitHub and exercise the built CLI. No role prompt or model behavior changes.
Run race tests (15-minute package budget, as qualified above), vet and build;
review the complete diff and record evidence here. Stop after explicit guarded
merge and receipt recovery. Ready writes, automatic policy/polling, queue support,
squash/rebase, ruleset-only guards, cleanup and release remain separate bundles.

### Using explicit Runner-owned merge

Complete current-base verification, draft synchronization and pinned CI as before.
A human marks the PR ready and obtains any host reviews. Inspect `task show ID`,
then explicitly authorize one merge:

```sh
runner-next --config /absolute/path/runner.json task merge TASK_ID
runner-next --config /absolute/path/runner.json run # if absent or drained
runner-next --config /absolute/path/runner.json task show TASK_ID
```

The command queues admission; it does not promise that GitHub will accept a merge.
Runner uses the same coordinator, capacity, drain and repository/base ownership
as integration inspection. Human merging remains available. Under the default
human policy, `task integrate` only observes; the later
[automatic policy](#using-automatic-merge-policy) can act on its ready result.
Explicit merging needs no model profile or retry budget.
The delivery mode retains its `github-draft` name because publication
still creates drafts; this command separately authorizes integration.

The first execution contract is intentionally narrow: an ordinary merge commit
on a ready PR, a classic branch protection rule with strict required status checks
and admin enforcement, no force-push/deletion/bypass allowances, no viewer bypass,
and no active auto-merge or merge queue. The repository and classic protection rule must allow merge commits.
GitHub must report a clean, mergeable PR with satisfied review requirements.
Additional host rules still apply at the normal merge mutation. Runner does not
change repository protections or ready state. A ruleset-only repository remains
eligible for human integration, but not this Runner-owned write path.

The [GitHub merge input](https://docs.github.com/en/graphql/reference/pulls#mergepullrequestinput)
can require the exact head but has no expected-base argument. Runner fetches and
compares the base, re-reads host policy and pinned CI, and sends the expected head.
The documented [strict up-to-date protection](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches#require-status-checks-before-merging)
provides the host guard against competing base advancement. This depends on the
protections remaining enabled; Runner cannot prevent an administrator changing
them or retargeting the PR during a request. The merge API cannot atomically pin
the base branch either; destination and policy must remain stable during dispatch.
Admission reads the rule attached to the actual base ref
through the [branch protection schema](https://docs.github.com/en/graphql/reference/branches#branchprotectionrule),
not a locally inferred branch-name pattern. The actual merge still needs the
existing exact-parent, exact-tree and fetched-ancestry receipt. Neither an old
policy snapshot nor a successful mutation response completes a task by itself.

| Result | Next action |
| --- | --- |
| Queued or active merge | Let the coordinator finish admission; unrelated eligible work can continue within capacity. |
| Blocked before dispatch | Resolve the displayed host review, protection, draft or CI issue, then explicitly request `task merge ID` again. There is no automatic retry. |
| Base moved before dispatch | Return the PR to draft; use `task verify-base ID` and complete refresh/checks/review/synchronization/CI before another merge request. |
| Dispatch consumed, outcome uncertain | Use `task integrate ID` to inspect again. A timeout, rejection, lost response or crash never grants another write for the same publication. If it remains open, a human may resolve host requirements and merge through GitHub, then request inspection. |
| Confirmed exact merge | Task completes with a receipt and releases dependencies. Repeating integration or stopping completed work is harmless. |

`task show` and JSON retain the immutable dispatch time, exact evidence request,
PR/protection identifiers and admission detail even after later inspections.
Stop/replacement revokes work that has not been sent; a dispatched mutation may
still take effect remotely. Its result cannot complete a revoked task. Startup
may repeat admission that was never dispatched; once the dispatch record exists,
startup only inspects. No claim is made that transport dispatch happens exactly
once: the deliberately conservative guarantee is **at most one send**, including
when a crash occurs between recording intent and sending it.

Store format 12 deliberately requires fresh private state. Preserve older stores
with their matching binary; no in-flight authority is imported. This bundle does
not ready PRs, poll, enable auto-merge, enqueue, squash/rebase, delete branches,
close issues, update boards or qualify a release.

### Explicit merge qualification

Implemented on 2026-10-15. The existing integration fixture and protocol seams
were extended; no second scheduler, test framework, dependency or role prompt was
introduced.

| Evidence | Result |
| --- | --- |
| Real Git/SQLite merge journey | Blocked review/draft, head/base drift, fetched-base drift, changed CI or protection after fetching, lost response, restart before/after the durable dispatch record, and stop at transport all preserve authority. An uncertain write cannot be reauthorized or reset; a later exact receipt completes without another write or model invocation. |
| Existing integration coverage | The full suite retains repository/base serialization, shared capacity/drain, unrelated work during slow reads, stale completion rejection and dependency/parent completion checks. Merge uses that same reservation and receipt transaction. |
| GitHub protocol | Complete host admission, absent/unknown fields, queues, auto-merge, bypass, loose/absent checks, linear history and wrong identities cannot dispatch. The mutation uses the recorded PR ID, exact expected head and ordinary merge method; revoked authorization, mismatched admission and incomplete responses fail without internal retry. |
| Built binary | Nine CLI commands exercised explicit request/JSON, blocked status, pre-dispatch retry, uncertain outcome, refused second write, read-only recovery and confirmed completion. The coordinator steps used controlled host observations and real Git/store; no live merge was performed. |
| Read-only GitHub | The final production query ran against historical `cortexium-io/runner#105` and decoded merge availability, queue, bypass and selected CI identity. This PR did not expose a classic protection rule, so positive protected admission and writes were qualified by protocol fixtures, not a live protected merge. |
| Required gates | `go test -race -timeout 15m ./...` passed (replacement engine about 642 seconds), as did `go vet ./...` and `go build -o /tmp/runner-next-merge ./cmd/runner-next`, on Go 1.27.1/macOS arm64 and Git 2.54.0. |

The fifteen-minute package budget carries forward the previous bundle's measured
suite requirement; no assertion or operation deadline was weakened. After final
review tightened mutation/admission binding and rejected linear-history rules,
the affected adapter tests passed again under race detection. Final CLI/config/
store/core race checks, vet, build and the final read-only query also passed.
The complete diff was reviewed and `git diff --check` passed. Qualification logs
remain locally at `/tmp/runner-merge-*.log`; temporary qualification drivers were
removed. No paid model evaluation was needed because model behavior is unchanged.

The eighth bundle stopped at explicit protected ordinary merges. The ninth
bundle below adds automatic local policy; live protected merge execution,
ready-state writes, queues, ruleset-only guards, squash/rebase, polling, cleanup
and platform/release qualification remain outside that qualification.

### Phase 3, ninth bundle: explicit automatic-merge policy

**Implementation and verification brief (2026-10-15, before code).** Add one
live repository policy, `policy.merge: "human" | "automatic"`; omission means
human. Expose it as `config edit --set merge-policy=automatic` through the existing
preview/save/explain/apply flow. Applying automatic explicitly authorizes guarded
merge admission for existing approved work and future work in this configured
repository/base. Preview must explain that effect; saving a file alone grants
nothing. This policy does not approve tasks or change their proof requirements.

- Derive one automatic merge request from a current successful integration
  observation of the exact open **ready** PR, with current accepted candidate,
  base proof and passed pinned CI, and no consumed merge dispatch. Existing CI
  success still schedules one observation. After a human marks ready or resolves
  a host requirement, `task integrate ID` requests another observation; enabled
  policy may then derive a merge. Status/show never trigger work.
- Record the applied policy revision on automatic requests and immutable dispatch
  evidence. Reuse the existing coordinator, repository/base reservation, global
  capacity, drain, protected-host admission, conditional ordinary merge and exact
  receipt. Explicit `task merge` remains available under either policy. Never
  override an explicit request or replay a consumed write.
- Recheck the authoritative policy when queuing, claiming, recording dispatch and
  immediately before sending. Switching to human cancels queued automatic requests
  and prevents an admitted but unsent automatic write. A sent or ambiguously sent
  write still needs inspection and may complete through its exact receipt, even
  after disabling the policy. Graceful drain preserves its existing admission
  semantics; task/parent stop and replacement retain their existing revocation.
- No recurring retries or polling: an automatic admission result cannot trigger
  itself. A blocked/unavailable/stale attempt requires a fresh explicit observation
  or newly passed CI evidence. Unrelated policy edits, restart and off/on toggles
  cannot erase a recorded attempt or reset consumed dispatch. An unsent request
  interrupted while policy remains enabled may resume its admission.
- Keep human mode as the default and local delivery unchanged; reject automatic
  policy in local delivery mode. Explain resolved policy and queued/manual/automatic/
  disabled/consumed states in config, status, show and JSON. Store format 13
  deliberately requires fresh state; no migration of in-flight merge authority.

Extend the existing merge journey for default behavior, explicit policy enable,
ready/draft observations, bounded automatic admission, restart, disable between
queue/claim/dispatch, and receipt recovery after disable. Reuse receipt, host
protection, serialization, dependencies and explicit merge coverage. Extend owning
config/editor tests for validation, preview/save/apply and read-only inspection.
Exercise the built CLI with real Git/SQLite and controlled host observations.
No new host API or role prompt: existing adapter qualification remains applicable;
no live remote write or paid model evaluation is needed. Run the full race suite
with the established 15-minute package budget, vet, build and complete diff review.
Stop after this explicit local policy. Ready writes, remote polling/events,
GitHub auto-merge/queues, ruleset-only guards, squash/rebase and release work remain
separate boundaries. Do not enable automatic merging in this development repo.

### Using automatic-merge policy

Human merge remains the default when `policy.merge` is omitted or `"human"`.
`task merge ID` still explicitly authorizes one protected ordinary merge in either
mode. To allow Runner to derive that request, use the existing configuration flow:

```sh
runner-next --config /absolute/path/runner.json config edit --set merge-policy=automatic
runner-next --config /absolute/path/runner.json config edit --set merge-policy=automatic --save --preview SAVE_DIGEST
runner-next --config /absolute/path/runner.json config explain
runner-next --config /absolute/path/runner.json config apply --preview APPLY_DIGEST
runner-next --config /absolute/path/runner.json status
```

Use the digests displayed by the preceding commands. Saving edits only the file;
applying grants automatic merge authority for **existing and future approved
work** in the configured repository/base. The initialization preview also shows
the resolved policy if it is supplied in a new configuration. Automatic policy
requires `github-draft` delivery and does not grant task approval, ready PRs or
relax proof or host protection requirements. `status` and `task show` display the
applied mode, even when a saved file is different, and never start work.

The coordinator derives one request from a current observation of a ready PR.
Passing CI still triggers one integration observation; a draft waits. After
marking the PR ready or resolving a host requirement, request a new observation:

```sh
runner-next --config /absolute/path/runner.json task integrate TASK_ID
runner-next --config /absolute/path/runner.json run # if absent or drained
runner-next --config /absolute/path/runner.json task show TASK_ID
```

**With automatic policy enabled, that ready observation can lead to a merge.**
Runner reuses the [protected ordinary-merge contract](#using-explicit-runner-owned-merge),
including fresh base/CI/host checks, serialized repository/base admission and the
exact merge receipt. The host's normal merge protection remains authoritative.
Automatic policy does not create a GitHub auto-merge request or merge queue entry.
There is no background polling or extra model, repair or retry allowance.

| State | Behavior and next action |
| --- | --- |
| Ready observation awaiting admission | Run the coordinator if absent or drained. Current policy and all merge requirements will be checked again. |
| Closed without merge | Inspect the closed PR and revise or stop the task. It cannot complete or start automatic merge. |
| Draft, blocked, stale or unavailable | Resolve the reported cause. Return a stale PR to draft and refresh its base/CI first. Use `task integrate ID` for a new observation; restart or a policy toggle does not retry a recorded merge assessment. |
| Queued automatic request | The request retains its originating policy revision. Current enabled policy is still required at claim, durable dispatch and immediately before sending. Unrelated configuration changes preserve that origin. |
| Automatic policy disabled | Use the same edit/save/explain/apply flow with `merge-policy=human`. The coordinator cancels queued unsent automatic requests and refuses unsent active automatic writes. Explicit `task merge` authority remains valid. |
| Dispatch consumed | The same publication can never send another merge write. Use `task integrate ID` to inspect; disabling policy cannot undo an already-sent effect or prevent an exact receipt from completing authorized work. |

An explicit `task merge` can replace queued automatic authority before it is
claimed. It cannot authorize another write after dispatch. A stopped or replaced
task retains the existing revocation rules. Drain stops new admission and allows
active work to settle; use human policy to disable unsent automatic writes.

The current build uses store format 14 (R2 recovery). Preserve an older store with
its matching binary and initialize fresh private state; no in-flight merge authority is imported.
The development repository's own configuration has not been switched to automatic.
Ready-state writes, polling/events, host auto-merge/queues, ruleset-only admission,
squash/rebase, cleanup and release qualification remain separate work.

### Automatic-merge policy qualification

Implemented on 2026-10-15. The brief above preceded code. Automatic merging uses
the existing publication record, coordinator, transaction and merge adapter; no
new scheduler, adapter API, dependency or role prompt was introduced.

| Evidence | Result |
| --- | --- |
| Configuration/editor | Omitted policy resolves to human. Unknown modes and automatic/local combinations fail. The existing preview/cancel/conflict/save/apply journey now checks merge authority scope and preserves the distinction between saved configuration and applied policy. |
| Real Git/SQLite merge journey | Existing approved work becomes eligible only after applying automatic policy and observing a ready PR. Draft/closed observations cannot authorize merging. A blocked assessment survives restart and off/on toggles without retry; a fresh explicit observation permits another bounded admission. Exact completion retains model/repair consumption. |
| Live permission and recovery | Queue claims and dispatch persistence reject disabled policy. Active admission and the final transport callback refuse an unsent automatic write after disablement. Interrupted unsent admission resumes only under enabled policy. Already-recorded dispatch can confirm a receipt under human mode without replaying the write. An explicit request can replace queued automatic authority. |
| Shared guarantees | Existing merge/integration coverage owns protected host admission, fresh base/CI, capacity/drain, repository/base serialization, task/parent revocation and exact receipt/dependency completion. No duplicate adapter or reservation test framework was added. |
| Built binary | 21 commands exercised initialization display, preview/save/apply, human/JSON status, queued automatic authority, disable/cancel, off/on without retry, explicit observation and confirmed completion. Execution used real Git/SQLite and controlled host observations, with one simulated merge. Native Codex capability validation used the user-selected `gpt-6.1-sol / high`; no model was invoked. |

The full `go test -race -timeout 15m ./...` passed (replacement engine about
643 seconds), as did `go vet ./...` and
`go build -o /tmp/runner-next-auto ./cmd/runner-next`, on Go 1.27.1/macOS arm64
and Git 2.54.0. The fifteen-minute package budget is unchanged from the previous
bundles. Final review corrected waiting-work grouping and closed-PR advice, verified
by the affected merge journeys and CLI race suite; final vet and build also passed. The complete
diff was reviewed and `git diff --check` passed. Qualification logs remain locally
at `/tmp/runner-auto-*.log`; the temporary CLI driver is not shipped.

No live remote merge was performed, and automatic policy was not enabled for the
development repository. The previous bundle's host contract and live-execution
limitation still apply. Stop at explicit automatic local policy: ready-state
writes, remote polling/events, GitHub auto-merge/queues, ruleset-only guards,
squash/rebase, cleanup and platform/release qualification remain separate work.

### Simple upgrade or deliberate break

For this frozen first release, use fresh setup; an upgrade utility is deferred.
A later utility may translate straightforward configuration and import
requirements as unapproved drafts. It must preview its result, validate the
new contract, be safe to repeat and leave original files intact. Unsupported
settings are reported, not guessed. Do not make historical workflow conversion,
retained approval signatures, partial plan release, or arbitrary in-flight
recovery state mandatory migration features.

Use this release procedure whether or not an upgrade utility exists:

1. Preserve a usable copy of the old release, configuration and private evidence.
   Inventory active work, retained branches/worktrees and open PRs.
2. Drain and stop the old service, including automatic restart. Quiesce its
   Runner-owned remote auto-merge requests and confirm their state before the
   new service may target the same repository/base. If this cannot be established,
   defer activation. Never run both authorities against the same work.
3. Finish existing work under the old release before switching, or park it with
   its artifacts retained. The operator explicitly chooses what remaining scope
   to resubmit to the new version. Do not automatically inherit old approval,
   QA acceptance, counters or integration ownership.
4. Run fresh setup. Approve resubmitted
   work under the new contract. An existing PR or candidate is evidence to
   inspect; adopting it requires fresh identity checks and required review, and
   is not a prerequisite for the first release. Avoid creating a competing PR
   for retained work without an explicit disposition of the old one.
5. Validate one real local and connected journey. Clearly mark the old board
   as historical until an optional view connects it. Keep old artifacts for
   inspection rather than deleting them as part of upgrade.

Do not restore the old service from a stale snapshot after the new version has
changed branches or remote state. Stop, reconcile actual effects and decide the
recovery path explicitly. This is an ownership requirement, not backward
compatibility. A breaking release can have a simpler installation transition
without pretending historical runtime state is portable.

### Documentation and retirement

AGENTS.md and ADR 0005 establish the replacement authority, package and testing
contracts. Earlier decisions identify their superseded assumptions. ADR 0003's
pinned-reference guarantees remain relevant when references are supported.
The installed legacy role skills remain with the running legacy installation.
The replacement uses its qualified embedded implementation, review and planning
instructions; R4 retired the old bundled skill catalog. The operator interface
is implemented, but a dedicated operator skill remains future work. Use the
[skill reassessment](#skills-and-instructions-assessed) before adapting instructions;
do not change installed skills as an incidental documentation update.

The new release needs its own accurate architecture, setup guide, examples,
operator reference and help output. Resolve older contribution/intake wording.
Retire obsolete application paths and their tests from the active build; keep
history accessible in version control. Do not preserve Project field schemas,
old command aliases, old config versions or test helpers solely for compatibility.
Each retained behavior and test must earn its place through a current requirement.

## Testing policy and ownership

Testing is part of the process design, including how Runner creates tests in
other repositories. Repository owners define the quality policy; the planner
applies it to the task; the implementer writes the change within that policy;
the independent reviewer assesses both the change and its proof. Moving every
test decision from one agent to another would not itself solve overengineering.
The decisions need an explicit contract and independent review.

The originally assessed [planner skill](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/skills/runner-planner/SKILL.md) deliberately leaves
verification technique to the implementer, and the
[reviewer skill](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/skills/runner-reviewer/SKILL.md) repeats that ownership.
The [implementer skill](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/skills/runner-implementer/SKILL.md) already asks for
minimal tests and reuse. The new policy therefore needs more than another
instruction to “write simple tests”: it must change what the assignment defines
and what the reviewer checks.

There is also an architecture cost. Some current
[engine tests](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/internal/engine/engine_test.go) intercept GitHub/Git/harness
commands, while their [support code](https://github.com/cortexium-io/runner/blob/5897f4ce2177c23edaaf6fd565d5768a634fc8a7/internal/engine/github_contract_test.go)
constructs Project fields and approval payloads. These are useful references for
failure scenarios, but should not be the new engine's default testing boundary.
Large files alone do not establish redundant coverage. Keep concrete guarantees,
then test each at the boundary that actually owns it.

### Decide the test scope before coding

Every task's existing verification brief should identify:

- The observable behavior or invariant, its requirement source, and a credible
  incorrect behavior that the evidence must distinguish.
- Representative inputs and expected outcomes derived from that contract.
- The appropriate boundary and evidence method: existing test, new test,
  source inspection, rendered interaction or another justified observation.
- The existing suite or test to extend, the distinct uncovered case, and the
  needed real/fake dependencies. Explain a new suite or shared helper if required.

Keep this in the ordinary task description and proof obligations. Do not add a
separate test-planning service, coverage registry or mandatory tester role.
The planner must inspect relevant tests before naming their reuse; it must not
invent filenames or leave every method as “the implementer will decide.”
Direct Ready work can supply this brief with the task. If it is missing, the
existing planner contract prepares only the verification brief before coding;
it does not decompose or reapprove the task. Existing repository policy and
supplied evidence may already settle the brief without another model call.
Any required planning call uses ordinary capacity and budget and is visible as
preparation. It adds no product requirements or new human approval gate.

The implementer can choose syntax and small local mechanics, and can adapt a
stale placement choice after inspecting the code. It cannot independently replace
the agreed boundary with a large custom harness, weaken expected behavior, or
expand the test scope without a concrete reason. Record material changes to the
brief in the existing result. A missing product decision returns to the human;
a routine change of test location does not.

### Ownership and timing

| Responsibility | Owner and time |
| --- | --- |
| Define supported behavior, quality rules and required gates | Repository owner, before work is delegated; reuse the target repository's established policy. |
| Choose scenarios, expected results, test boundary and existing coverage to extend | Planner during planning, or the task author for Ready work; the same planner fills a missing Ready brief. Approval binds the required behavior. |
| Write or extend tests and implementation | Implementer within the approved task. For a bug, establish the smallest reliable failing reproduction before the fix when practical; for new behavior, settle its cases before implementation. |
| Verify adequacy, independence and economy of tests | Existing independent reviewer before acceptance. Check missing behavior, weakened assertions, redundant scenarios and unjustified fixture complexity. |
| Consolidate or remove superseded tests | Implementer in the same scoped change, with the reviewer checking that each distinct contract remains protected. |
| Run final integration gates | Runner's deterministic verification/delivery path and repository CI according to policy; agents do not waive those gates. |

Tests and production changes should land together. Existing sufficient coverage
can mean no new test. Documentation or a mechanical change does not automatically
need an executable regression test. A reproduction that adds lasting protection
belongs in the existing suite; a temporary diagnostic does not automatically
become permanent infrastructure. Timing a test before implementation helps with
independence, but does not prove its expectation is correct.

### Replacement role instructions

These instructions supplement the preserved isolation, honesty and evidence
contracts. Put them in the replacement assignments before their first model
invocation; do not send a new verification brief with an old prompt saying that
the implementer alone chooses the proof method.

| Instruction owner | Required assignment behavior |
| --- | --- |
| Planner or Ready task author | Inspect the repository's quality agreement and relevant tests. State supported scenarios, expected results, appropriate boundary and inspected coverage to extend. Do not prescribe internal implementation or invent requirements. A missing Ready brief is preparation, not a new task decomposition or approval gate. |
| Implementer | Follow the supplied verification brief. Choose local syntax and mechanics; extend/consolidate the owning suite. Explain a materially different evidence method and its concrete reason. Do not weaken expected results or invent a broad test harness. Return actual commands/observations, failures, reuse rationale and remaining limits. |
| Reviewer | Independently check the requested behavior and whether its proof can detect a credible defect. Inspect coverage reuse, redundant cases and disproportionate helpers. Require a concrete requirement or reachable failure for a rejection; zero findings is valid. Remain read-only and request only missing or invalidated evidence. |
| Operator skill | Explain setup, resolved policy, waits and recovery through the same commands and status. Preview consequential changes; never bypass approval, reset counters or edit the store directly. This is an interface, not a fourth execution role. |

Phase 1 supplies the Ready brief directly and qualifies implementation/review.
Phase 2 qualifies planning, missing-brief preparation and the operator interface.
Pair seeded bad behavior with legitimate controls: a weakened assertion should
fail review; a simpler equivalent test should not. Use the existing behavioral
evaluation approach, not a new evaluator service or mandatory reviewer group.

### What makes a good test

A good test protects a supported behavior or important invariant, fails for a
credible wrong implementation, and makes the cause of failure clear. Its expected
result comes from the contract, not from duplicating the implementation's formula
or recording whatever output the new code currently produces. It exercises enough
real behavior to support its claim and remains valid through internal refactoring
that preserves that behavior.

Use the smallest readable arrangement and representative fixture that establish
the claim. Control time, randomness and synchronization when they are not the
subject of the test. Clean up owned resources. Assertions should identify the
broken guarantee; checking only that a command returned success is insufficient
when the guarantee concerns persisted state or an external effect. Exact command
flags, ordering or private structure are appropriate assertions only when they
are themselves a required adapter, security or protocol contract.

Before accepting a test, the reviewer should be able to answer: what requirement
justifies it, what plausible defect makes it fail, why existing coverage is not
enough, and why its setup is the simplest reliable choice. For a bug fix, the
original defect is usually that demonstration. Targeted mutation can resolve
uncertain test sensitivity; a universal mutation gate is unnecessary. Test count,
line coverage, line count and an elaborate fixture do not establish quality by themselves.

### Extend and consolidate existing tests

Search for the behavior's owning suite before adding a file or helper. Prefer
extending the existing scenario or adding a named case when setup and assertions
are genuinely shared. Use table-driven tests for a coherent invariant with
meaningful input variations; do not combine unrelated behaviors into a table
with many flags and branches merely to reduce the number of functions.

When two tests establish the same outcome through the same boundary, keep the
clearer one or merge their distinct cases. When a refactor replaces a contract,
replace its obsolete tests in the same change. Preserve tests at different
boundaries when they catch different failures: a pure eligibility test and a
store-transaction test are not duplicates merely because both mention approval.
Deleting coverage requires identifying where the still-required guarantee lives.

Helpers may remove repeated mechanical setup. Keep important scenario inputs,
expected results and assertions visible. Do not build a configurable fake of
all Runner behavior or an assertion DSL to hide difficult production boundaries.
A little repeated setup is preferable to a helper that requires understanding
another state machine. Consolidation concerns the touched behavior; it does not
authorize unrelated repository-wide test cleanup on every card.

### Test the correct boundary

| Claim | Preferred evidence for Runner |
| --- | --- |
| Eligibility, retry/escalation rules, dependency outcomes and resource conflicts | Small core tests with explicit inputs and expected decisions. No GitHub JSON or subprocess simulation. |
| Atomic release, counters, restart recovery and competing claims | Integration tests against the real temporary local store. An in-memory fake cannot establish transaction or durability semantics. |
| Provider request mapping, capability handling and ambiguous responses | Adapter tests at the actual protocol/CLI boundary with narrowly controlled responses. |
| Interchangeable board or harness behavior | A small shared contract suite for genuinely common guarantees, plus provider-specific tests. Do not force Trello to mimic GitHub details. |
| Git candidates, workspace isolation and publication integrity | Focused tests with temporary real repositories and the relevant filesystem/process boundaries. |
| Coordinator interleaving and interruption | Explicit completion signals and fault injection at owned operation boundaries, with the real store where state recovery matters. |
| A complete user journey | A small number of scenarios through real application commands, actual store/workspaces and controlled external adapters. |
| Agent judgment, adherence and evidence quality | Separate behavioral evaluations with seeded defects and correct controls; live model runs only when that behavior needs qualification. |

For software Runner builds, apply the same ownership and quality rules using
that repository's stack and conventions. Do not prescribe Go tests, SQLite,
browsers or a particular framework to unrelated projects. Existing test commands
remain the source of execution truth. Run focused tests during development and
repair; run broader gates at their required boundary. The reviewer reuses valid
evidence and does not create a second competing suite.

## Proof that the redesign is ready

Use deterministic scenarios to protect state transitions and failure boundaries,
then selected live harness and operator journeys. Reuse existing fixtures where
they prove the same contract. Test faults at operation boundaries rather than
adding arbitrary waits or broad mock-only coverage.

### Requirement coverage map

Phase 0 is documented. Phase 1 is implemented at commit `d6d0ddb`; its S1–S7
proof and live qualification are recorded in [phase 1 evidence](#phase-1-evidence).
The phase 2 evidence is recorded in the [Ready-policy qualification](#first-phase-2-qualification),
[Plan/preparation qualification](#second-phase-2-qualification) and
[quality-agreement qualification](#third-phase-2-qualification), plus the
[operator qualification](#fourth-phase-2-qualification). Nine phase 3 bundle
qualifications above cover connected delivery through automatic policy.
[R1](#r1-qualification--complete) adds real protected human/automatic merges and
dependency/parent completion. The [release checklist](#frozen-first-release-checklist)
owns the completed R1–R5 evidence and release-ready finish line;
phase 5 board views stay deferred.

| Requirement | Owning phase | Acceptance evidence |
| --- | --- | --- |
| Local Ready authorization and preserved scope | 1 | S1/S2/S5: exact approval, stale preview rejection, idempotent approval, revocation and retained artifacts. |
| Durable admission, candidate binding and no blind replay | 1; extend in 3 | S2/S4/S5: real-store interruption and saved-result tests; the first phase 3 bundle adds durable draft intent, shared reservations and ambiguous publication recovery without repeated PR creation. |
| Readiness and repository quality | 1/2 | Third phase 2 qualification proves explicit agreement changes, immutable task pins and adoption, scoped observed exceptions, unavailable checks, source-bound candidate checks and unchanged task-proof requirements. The live reviewer pair accepts a legitimate exception and rejects a task defect. |
| Plan and dependency success | 2/3 | Second phase 2 bundle proves atomic release, missing/cyclic dependency rejection, stale plan rejection, parent revocation and exact-version waits. Awaiting delivery cannot satisfy dependencies. Phase 3 bundle 7 proves exact child integration, dependency release, original-release parent completion and closed-unmerged handling. |
| Continuous reconciliation and resource safety | 1; extend in 2/3 | S2/S3: exclusive ownership and responsive reconciliation; the first phase 2 bundle proves shared capacity and task/workspace claims across CLI/service; future browser/shared-data capabilities must add their resource claims; phase 3 bundle 7 proves serialized integration inspection without blocking unrelated work; bundle 8 reuses that reservation for one explicit guarded merge and inspection-only recovery; bundle 9 adds applied automatic-policy admission and revocation without another scheduler. |
| Bounded recovery and understandable configuration | 1; extend in 2/3 | S7 validates the limited policy. The first phase 2 bundle proves Ready repair allowance, persistent backoff, rejection-only escalation, invocation budgets and live policy changes without counter resets. Phase 3 proves bounded refresh/review cycles and explicit automatic-merge policy without repeated writes; R2 qualifies restore. |
| Independent, sufficient, economical verification | 1; extend in 2 | S6 plus live candidate review. The first phase 2 paired behavioral cases cover fabricated proof, weakened checks, legitimate consolidation and speculative review; no infrastructure error becomes a code rejection. |
| Provider-neutral architecture and optional views | 1/3/5 | S1 runs without `gh`/Projects. Phase 3 delivery requires no board. Boards and Trello remain deferred; any future adapter must preserve local scheduling/approval authority and protect private evidence. |
| Current-base delivery and confirmed completion | 3/4 | The nine phase 3 qualifications cover base movement, conflicts, CI failure, interrupted publication, exact human/automatic merge receipts and closed-unmerged handling. R1 closes the live protected-merge gap with exact human/automatic receipts and dependency/parent completion; R5 confirms those receipts and unchanged adapter/role contracts against the release candidate. |
| Comprehensible progress and recovery | 1/2/4 | Fourth phase 2 qualification covers attention-first status, actual profiles, waits, retained work, exact recovery guidance, watch and named-setting editing. R3 qualifies the consolidated operator guide with fresh native setup/recovery, controlled failure cases and retained live delivery receipts; R5 completes the packaged journey on native macOS/Linux arm64. |
| Packaging, restore and breaking cutover | 1/4 | Phase 1 qualifies the driver and new entry point's target builds. R2 proves consistent backup/restore; R4 proves the sole promoted entry point, strict format boundary, four verified archives and native macOS smoke. R5 qualifies native macOS/Linux arm64 containment/runtime, isolated installation, ownership/cutover and explicit resubmission. No historical-state converter is required. |
| Honest operational learning | 1/2/4 | Existing attempts retain call/result evidence; R3 records calls, elapsed time, interventions, repeated verification and evidence boundaries above. R5 records the packaged/cutover timings, interventions and evidence reuse without an analytics service. Guidance drafts require human review. No inferred cost or unsupported performance claim. |

Required repository gates for implementation remain `go test -race -timeout 15m ./...` and
`go vet ./...`, plus the relevant existing platform/release checks before release.
Paid behavioral and harness tests are selected deliberately; they are separate
from deterministic evidence and must record provider failures separately from
review or implementation failures.

The operator acceptance journey uses a fresh setup: configure one profile and
an escalation option, submit Plan and Ready work, understand a dependency wait,
answer a question, resume a failed review, change parallelism, drain/restart,
and inspect a delivered result without a board. Record where the user needs
undocumented help. Settings are not considered usable merely because JSON validates.

Measure time to the first reviewed candidate and first accepted PR, Ready-to-merged
elapsed time, time waiting for integration, repeated verification and its cause,
manual interventions, recovery success, and all harness attempts including failed
or stopped work. Keep missing cost/usage unknown. Compare with the existing
[evaluation method](model-profile-evaluation.md), holding model/reasoning and
acceptance requirements fixed when evaluating process changes. Do not claim a
throughput or cost improvement from shorter prompts or a different task mix.

Retain concrete recurring failures in the task evidence. A person reviews the
actual incidents before changing role or repository instructions. The legacy
guidance-draft command is retired; no new learning service is part of this release.
Do not automatically turn repeated model text into execution policy.

## Build readiness

The local process, nine delivery bundles, R1–R5 and the subsequent owner-approved
local-completion and real-project qualification obligations are complete within
their recorded boundaries. Both pilots and renewed combined GitHub Plan delivery
passed; `main` is reconciled through `14a86d2`. Final code candidate `f9a3c6c`
passed the required race/vet gates and full platform/package workflow. The
[continued qualification record](#continued-real-project-qualification) names
actual binary/source boundaries, interventions and retained failures. Subsequent
documentation-only changes receive the PR gate; they do not invalidate unchanged
runtime evidence.

[Runner PR #106](https://github.com/cortexium-io/runner/pull/106) merged into main
on 2026-10-16; [api-mcp #6](https://github.com/cortexium-io/api-mcp/pull/6)
is its unmerged useful pilot result. Genny's existing PR #576 keeps its ownership
and unchanged source. No implementation bundle remains from the original release
checklist. The subsequently requested GitHub Issues extension is defined in its
[bounded brief](#github-issues-and-owner-acceptance-brief). Other optional integrations
and additional harness qualification remain deferred, not
release blockers. The separate local-model comparison is still research; it
cannot silently admit another adapter. Publication and installation/cutover
require separate explicit direction and the reviewed-source release safeguards.

[legacy-plan-delivery]: https://github.com/cortexium-io/runner/blob/14a86d2c26acae4eefe8c499eae4e0cdc4ebede7/docs/decisions/0005-approved-outcome-delivery.md
[legacy-planner]: https://github.com/cortexium-io/runner/blob/14a86d2c26acae4eefe8c499eae4e0cdc4ebede7/skills/runner-planner/SKILL.md
[legacy-reviewer]: https://github.com/cortexium-io/runner/blob/14a86d2c26acae4eefe8c499eae4e0cdc4ebede7/skills/runner-reviewer/SKILL.md
[legacy-interaction]: https://github.com/cortexium-io/runner/blob/14a86d2c26acae4eefe8c499eae4e0cdc4ebede7/skills/runner-interaction-design/SKILL.md

[factory]: https://github.com/imran-sayed-pro/software-factory/blob/d7034f9d17bbad296dc3b3fcea09792551c70173/README.md
[factory-setup]: https://github.com/imran-sayed-pro/software-factory/blob/d7034f9d17bbad296dc3b3fcea09792551c70173/skills/factory-init/SKILL.md
[factory-dispatch-skill]: https://github.com/imran-sayed-pro/software-factory/blob/d7034f9d17bbad296dc3b3fcea09792551c70173/skills/dispatch/SKILL.md
[factory-plan]: https://github.com/imran-sayed-pro/software-factory/blob/d7034f9d17bbad296dc3b3fcea09792551c70173/skills/plan-review/SKILL.md
[factory-guard]: https://github.com/imran-sayed-pro/software-factory/blob/d7034f9d17bbad296dc3b3fcea09792551c70173/scripts/floor-guard.mjs
[factory-cases]: https://github.com/imran-sayed-pro/software-factory/blob/d7034f9d17bbad296dc3b3fcea09792551c70173/evals/cases/build.json
[factory-qa]: https://github.com/imran-sayed-pro/software-factory/blob/d7034f9d17bbad296dc3b3fcea09792551c70173/skills/qa-verify/SKILL.md
[factory-dispatch]: https://github.com/imran-sayed-pro/software-factory/blob/d7034f9d17bbad296dc3b3fcea09792551c70173/scripts/dispatch.mjs
[factory-check]: https://github.com/imran-sayed-pro/software-factory/blob/d7034f9d17bbad296dc3b3fcea09792551c70173/scripts/check.mjs
[anthropic]: https://www.anthropic.com/engineering/redesigning-source-control
