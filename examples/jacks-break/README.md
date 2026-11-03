# Connected Jack's Break delivery

`deliver` owns scoped Go development, correction, measurements, independent source
review, candidate publication, product qualification and the approved release.
The external chat submits the request and presents saved owner decisions. No model
or chat selects the next stage.

`admit → owner → check → measure → review → integrate → publish → qualify → prepare → [merge approval] → merge → backup → [production approval] → stop → migrate → pin → deploy → verify-live`

Checks, review and qualification each permit one in-scope correction. A selected
Go benchmark permits one further owner/measurement round. `benchmark: "none"`
records that measurement is outside this request. This is domain iteration;
execution retries neither judge the work nor replace correction policy.
The process permits at most 35 attempts, with bounded routes and no model fallback.
Implementation and review summaries are deliberately short; retained artifacts and
the private operator journal hold the full evidence.

The readiness step refreshes configured `refs/remotes/origin/main`, hydrates source
and pins the run's immutable base before model work. An explicit full commit is
also supported. It checks the selected build runtimes, protected closed-WAL backup
permissions on Linux, npm TLS, target storage and an isolated headless browser.
Coding and review have no release credentials. Publication consumes only the
retained, approved candidate. A correction advances the same owned draft PR using
an exact compare-and-swap; older candidate commits remain pinned.

One fixed project qualification operation packages the clean commit, checks its
manifest against Git, independently reproduces the Linux executable, builds the
artifact-only image, runs the frozen Nonogram development/held-out cohorts,
rehearses migration and previous-image recovery on copied data, probes every
advertised runtime choice and runs 18 headless desktop/mobile player journeys.
It does not claim to cover arbitrary frontend, authentication or other new product
obligations. Unsupported obligations block rather than move to the chat.

## Installation

Copy `process.json` and `roles/` into the private project. Set executable and cache
paths in the command roles. Add this process to the project and select `deliver`
as its default. Keep local-only development processes available when explicitly
selected. Configure instance `release.github` with the trusted `gh` executable and
`release.command` with the fixed operator invocation, locally or through strict
SSH. Keep the operator policy, authentication and journals outside Git.

The operator's `delivery` settings select absolute runtime paths, read-only caches,
a public CA bundle, headless browser and the existing generated-ownership command.
Use the exact Go 1.26.7, Node 24.21.0 and Python 3.12 build contract. For Homebrew
Node, export its normal public trust anchors with `tls.getCACertificates('default')`
under `--use-system-ca`; the sandbox receives this public PEM bundle through
`NODE_EXTRA_CA_CERTS`. It receives no normal account home or authentication.
Select the actual Git binary rather than Apple's `xcrun` wrapper, and give the
builder its owned temporary directory. Do not disable certificate verification.

Build `runner-release`, `runner-measure`, `runner-snapshot` and
`runner-http-probe` from the reviewed Runner commit. The snapshot helper and HTTP
probe run on the Linux Docker host. Reuse the accepted project backup helper;
do not introduce Python source. The `delivery.probe` path names the installed Linux
HTTP probe. `delivery.ownership` is fixed argv for the owning Operations
`check-pr jacks-break` command, followed by Runner's exact PR number and
`--publish-status`.

`delivery.storage_limit` is at most 8 GiB and `delivery.min_free` at least 5 GiB.
Admission and ongoing monitoring cover owned local and target storage. Admission
reserves 4 GiB of local build/correction space and 1 GiB on the target before any
model work. Settled
temporary source/caches and disposable data are cleaned; packages, journals and
recovery snapshots are retained. Storage exhaustion blocks new work instead of
raising allowances or deleting recovery evidence. These operator resources have
separate retention from the core run database.

Back up the pilot's database, owner token and configuration consistently before
cutover. Database format 2 stores settings and operational history as separately
bounded JSON documents in the same transaction. Existing format-1 history remains
readable and is not rewritten by opening it. Both documents obey `max_record`;
the existing database reservation and run count remain unchanged. An old binary
requires its matching pre-upgrade database backup after new format-2 runs exist.

## Recovery policy

- With `recover_before_migration: true`, a known final-backup failure can restart
  only the exact previous container after closed data, environment and no-mutation
  proof. Preparation failure before migration intent has the same boundary.
  Cancellation after an accepted stop, before migration, uses that compensation
  through the same durable operation, including a stop-result/cancel race.
  The release fails with the previous service's verified health recorded.
- A conclusively failed deployment before effects gets one configured retry.
  Failure after effects is inspected, then requires a saved owner decision for
  one reapplication of the same approved image and commit.
- A settled migration failure is inspected with Games stopped. A separate saved
  owner decision may restore the exact final snapshot and restart the previous
  image. The failed release remains failed, with its recovery recorded.
- Lost replies, interrupted effects and unsettled processes retain their original
  identity. They never authorize another release, migration or database restore.
  An SSH timeout before authentication gets one bounded reconnect; a disconnect
  after remote dispatch never gets that retry.

Merge approval does not approve production. Production approval does not approve
database restoration. Shared Docker cleanup and unrelated changes are outside
these recovery routes. The qualified database contract supports migration 0017
and later releases with no schema changes; other migrations need an explicitly
qualified project contract.

## Verification

Ordinary tests are model-free. Configuration tests prevent skipped approvals and
forged receipts. The core integration fixture covers correction, benchmarks,
reconnect, both release decisions, bounded recovery and the combined 35-attempt
path within the pilot's record allowance. SQLite tests preserve legacy history
through upgrade and reject oversized settings or history.

The opt-in `TestDeliveryPlatformQualification` uses a private
`RUNNER_DELIVERY_PLATFORM_POLICY` and `RUNNER_DELIVERY_PLATFORM_BINARY` to test
actual installed tools without models or production mutations. Its synthetic
publication journal is restricted to separate `-qualification/` storage.
`TestDeliveryPlatformRecovery` additionally requires the disposable Linux helper
from `testdata/release-lab`, selected by `RUNNER_DELIVERY_FAULT_HELPER`. It creates
a separate Compose project and synthetic database. Run these platform exercises
serially; no failure is injected into production. Test approval grants no
production authority. Real delivery stops at the actual saved owner decisions.
