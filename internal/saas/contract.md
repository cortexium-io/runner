# Fixed local first-flow proof contract

This contract belongs to the configured Runner producer, not to repository test
scripts. The protected first-flow brief remains the product requirement. This is
representative desktop/narrow Chrome for Testing proof with deterministic Worker
fixtures. It does not establish real Worker/model execution. Native-device and
Safari evidence are not required by this Cortexium local route.

Generate the admitted fresh `platform` directory using the positively pinned
Genny v0.4.0 init/auth-with-tenancy/model commands without force. The authority
supplies the actual module, project name, reference go.mod/go.sum and frontend
package/lock bytes. Preserve ordinary generated ownership and use app-owned
extensions. Do not patch Genny or invent adoption metadata. Root legacy source,
data and history remain preserved. Source roles generate/edit only; later fixed
checks, source review, browser observation, image review and integration establish
proof. Corrections repeat all affected checks and both reviews.

## Owned startup adapter

`go build ./...` must include the normal production startup. The additional
`cmd/runner-local` is a thin fixture/bootstrap adapter over the SAME normal App
construction, Genny auth/model handling, routes and business transitions. It may
seed isolated users/workspaces and inject a deterministic Worker through ordinary
extension points. A parallel test application, auth bypass, manufactured success
or separate business transitions are unacceptable in a product candidate.

The fixed helper cross-builds normal startup and `./cmd/runner-local` with
Go1.27.3, CGO disabled, Linux/amd64/v1 and ordinary debug information. The
trusted operator invokes only the exact independently reviewed fixture binary:

`--listen 127.0.0.1:9044 --data-dir /data --ready-file /data/ready.json --fixture`

It must bind its own loopback listener, use only its owned data directory and
write `{ "pid": OWN_PID, "origin": "http://127.0.0.1:9044" }` to the ready
file after readiness. It must preserve fixture sessions, work and receipts across
a real restart using that data directory. It must not start another service,
contact providers or use host accounts. Production mode cannot enable these
fixtures implicitly. All fixture credentials below are synthetic, never real
users. Runner owns process/browser/temporary-profile settlement and cleanup.

## Fixed normal-UI bindings

Use meaningful accessible labels. `data-runner` identifies these normal controls
or visible state; it is not a success-injection API. The driver uses normal mouse,
keyboard, navigation, reload and actual HTTP responses. No repository browser
scripts, state/localStorage injection or arbitrary source-selected cases run.

- Sign-in: `email`, `password`, `sign-in`. Seed alice@fixture.invalid and
  bob@fixture.invalid, password `Runner-fixture-only-12`.
- `workspace` is the normal workspace selector with `personal-alice`,
  `personal-bob`, `shared-alpha`. Alice owns Alpha; Bob initially belongs to it.
  Session-derived workspace authorization applies to all reads/custom actions,
  retained artifacts and Worker dispatch; selecting or sending a tenant ID
  does not grant authority. Foreign run reads and membership-removed reads must
  actually return HTTP403, with visible `access-denied` and no leaked report.
- Flow controls: `new-flow`, `flow-name`, `agent-version` (`review-v1`),
  `skill-version` (`supplied-text-v1`), `function-version` (`report-v1`),
  `worker` (`fixture-review`), `typed-steps`, `save-flow`, `frozen-versions`.
  Typed step-list UI must expose the review → report → human → saved result
  sequence and retain frozen component versions in each run.
- Run controls/state: `supplied-text`, `run-flow`, `run-again`, `run-id`,
  `run-state`, `current-step`, `responsible-party`, `run-versions`,
  `retained-input`, `current-result`, `result-version`, `model-invocations`.
  A saved flow runs on two different supplied texts with distinct durable run
  identities, inputs and reports. `model-invocations` represents actual fixture
  adapter invocation evidence, not a hardcoded claimed count.
- Exact human review: `revision-instruction`, `request-revision`,
  `accept-result`, `version-conflict`, `accepted-version`, `saved-result`.
  A revision creates another exact version. A stale other-page acceptance must
  return HTTP409, remain unaccepted, then permit acceptance of the current exact
  version. Accepted version/report must survive reload.
- Membership: `workspace-members`, `remove-bob`, `membership-state` (Removed).
  Removal must invalidate Bob's subsequent protected reads/actions, even from
  an already open session. Grant controls: `worker-settings`,
  `revoke-worker-grant`, `grant-worker`, `worker-grant-state` (Revoked/Granted).
  A subsequent run after grant removal is Blocked with zero model invocations.
- Allowlisted function fixture `fixture-failure-v1` fails normally with state
  Failed and inspectable `failure-reason` mentioning formatter. It cannot accept
  or advance. Worker fixture `fixture-unsupported` is refused for capability
  before dispatch, state Blocked and zero invocations.
- Deterministic Worker input `Receipt loss fixture` completes one review but
  initially loses its receipt: Receipt pending. `recover-receipt` rejoins that
  receipt. After actual App restart, `work-overview` and the matching `open-run`
  retain the same ID, report and one completed invocation. `open-run` elements
  include each exact run ID as visible text.
- Input `Pending cancellation fixture` stays Running until `cancel-run`, then
  Cancelled survives reload. Input `Uncertain effect fixture` retains Uncertain
  with one invocation across reload; `retry-run` is disabled until supported
  reconciliation. Never replace uncertain work or invent settlement.

Named deterministic fault inputs configure the fixture Worker dependency only;
normal App transitions remain responsible for actual status/result handling.

## Proof, review and actual limits

Checks use the admitted Node26 runtime and exact generated build sequence:
vue-tsc, Vite app, Vite SSR document build, generated document prerenderer, then
Go tests/vet/normal build and Vue tests. Generated assets live in
`internal/app/http/frontend_dist`, including public error/static documents.
No package installation, source-selected shell script, migration or provider
operation occurs during checks. Locked dependencies are prepared before admission.
The original strict Go report is retained intact. For this fixed SQLite scope
only, its exact generated PostgreSQL-DSN skip and three absent server/static-page
route skips may be recorded separately as not applicable. Each requires its
exact package/test/reason, complete settled execution, positively verified
generated ownership, SQLite/auth/tenancy metadata and empty generated route AST
lists where relevant. Missing built shells, another required skip, overflow,
unfinished or zero-test execution blocks. This does not change the strict Go
helper's failed selected-scope verdict/exit2 or claim PostgreSQL/page-feature proof.

Network-disabled checks precede independent source review. A new versioned
checked-build artifact contains the exact Linux executable, canonical embedded
frontend hashes/offsets and full reports: at most16MiB stored/wire and32MiB decoded
TOTAL. It exports no source, credentials or host runtime state. Existing
image/report codecs remain bounded to16MiB decoded. The operator never rebuilds
source. It copies only the authenticated executable and manifest into a positively
owned NEVER-STARTED staging container, commits the copy-only layer, and verifies
the actual final files, modes, ownership, platform and runtime authority before
any App/browser execution. A mutable tag is not runtime authority.

App and trusted full-CFT producer have separate read-only filesystems and PID
namespaces. An inert owned anchor provides their network-none namespace and
retains bounded tmpfs data/proof mounts across process failure. App mounts only
its own data; it cannot mount/read/write browser proofs, controls or profiles,
signal browser processes or reach a DevTools TCP endpoint. Playwright uses its
private CDP pipe. A fixed trusted handshake starts/stops only this same App
container; its state persists across real restart. No host ports, host mounts,
Docker access, private account state, arbitrary commands/URLs or remote egress
are available to the App. The driver also restricts requests to the fixed
loopback origin and disables service workers. Partial snapshots are exported
after writer settlement and before resource deletion; incomplete proof blocks.

Aggregate bounds remain1GiB memory,2CPU and256PIDs, reserving both persistent
tmpfs maxima separately from cgroup memory. All managed/staging storage remains
within1GiB with reserve; no ceiling or correction/attempt allowance is increased.

All nine journeys and actual desktop1440×900/narrow390×844 viewport captures are
declared by the fixed producer. Source regressions remain source proof, distinct
from UI observations and independent visual review. Full bounded Go/Vue reports,
compiler/browser diagnostics and available partial captures accompany correction;
truncation/incomplete evidence cannot pass. Receipt128KiB, artifact16MiB,
image8MiB,160images and existing process/resource/correction limits stay fixed.
Read-only image review receives `saas-saas.json`, `saas-reports-*.json` and
attached `saas-chrome-*-viewport.png` files. It must inspect actual images and
missing observations before APPROVE; source checks or screenshot capture alone
are insufficient. Integration creates only an owned local candidate/ref. Real
Worker/model proof remains explicitly unverified and required separately inside
its admitted Runner process after existing host prerequisites; it is not replaced
by these fixture results. No merge, production or data restoration follows.
