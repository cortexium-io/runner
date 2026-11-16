# Roles and tasks qualification

These examples exercise the general task graph through Runner itself. A short
writing request can complete in one lead turn. A researched article uses an
author and concurrent independent critics, followed by a reasoned editorial
decision. The SaaS lead delegates a planner; its initializer precedes concurrent
backend/frontend workers, then integration, an independent fixed check and the
lead's assessment against the original goal.
Outputs are actual model work, not fixture text. No writing publication, product
merge or deployment is included.

The full SaaS lead admits implementation and its dependent fixed check together.
The nested planner admits one scaffold → parallel backend/frontend → build DAG.
Parents inspect settled task IDs on resumption, rather than submitting those
stages again. A passing fixed report ends the work. At most two repair/recheck
pairs may fix a concrete failure in the same workspace; the repair writer uses
the completed check's typed artifact dependency, or the actual build failure when
no artifact exists. Failed children are never required dependencies. There is no
extra model verifier or planner for a repair.

Use a freshly built Runner containing native role-task support. Do not point
these examples at an installed pilot or existing state. Configuration has explicit
per-request definitions and records the selected runtime. Generated instances use
`enforce_work_limits: false`, disabling aggregate attempt and total child-task
ceilings for every project, including retained runs. Historical definitions and
spent-attempt counts remain intact. Set the instance flag to `true` to opt into
the configured finite ceilings. Local retry/repair limits, concurrency, timeouts,
storage admission and authorization remain enforced.

Run configure.mjs with absolute paths to installed, already qualified runtimes:

~~~sh
node examples/role-tasks/configure.mjs \
  --root /absolute/new/qualification \
  --app-workspace /absolute/new/campaign-studio \
  --project-name "Campaign Studio" \
  --native-context lean \
  --codex /absolute/path/to/codex \
  --genny /absolute/path/to/genny \
  --go /absolute/path/to/required/generated-app/go \
  --npm /absolute/path/to/npm \
  --browser /absolute/path/to/configured/chrome-for-testing \
  --playwright /absolute/path/to/installed/playwright/index.mjs \
  --critic-harness claude \
  --critic-model opus \
  --claude /absolute/path/to/claude
~~~

The Claude options enable a real GPT/Opus editorial comparison using enforced
read-only Claude tools. Supplying --claude also makes the separate write-quick
process available; omitting it keeps Codex-only operation. The script checks
executable paths, refuses non-empty qualification/app directories, creates only
configuration and empty workspaces, and writes synthetic private credentials with
mode 0600. It does not generate an app, install packages, submit a model turn or
start a browser. It observes the selected Go toolchain's two shared cache paths,
derives Genny's exact project-locks directory from Go os.UserCacheDir semantics,
queries the selected npm executable for its normal cache, and adds the exact
Codex cache-maintenance lock directory. It creates missing
runtime directories with mode 0700 and explicitly grants those narrow roots to
SaaS roles. It refuses whole-home or whole-user-cache grants and preserves
existing directory permissions. Keep its output directory out of Git.

New SaaS roles explicitly use lean Codex context with built-in tools and no
inherited MCP/app/plugin/skill catalogs. Project instructions remain applicable;
coding roles read the configured Genny skill file directly. Use --genny-skill
with an absolute path when its standard location differs, or --native-context
native to deliberately inherit the full harness capabilities. The writing
profiles are unchanged. Lead/planner reasoning is medium, coding/integration/
repair reasoning high, all on gpt-6.1-sol. runtime.json records the exact selected
profiles; native provenance records actual exposure, usage and timing.

The product description contains the fixed acceptance once. Each role receives
only its relevant runtime paths and compact dependency reports. Full delivery is
configured with ten tasks, 48 aggregate attempts and two repair pairs. The first
two ceilings apply only when instance `enforce_work_limits` is true; the local
two-pair repair bound remains enforced. Roles read
`task_context.attempt_budget.unlimited` and `task_context.unlimited_tasks` before
using remaining counters; `-1` means unlimited. An explicit ready→fail
route prevents a default model outcome from bypassing the lead's accepted or
rejected decision.

For the current qualification, the selected installed generator is Genny v0.4.1.
Its generated app requires Go 1.27.4; Runner itself uses Go 1.26.9. Use the separate
correct toolchains without a local module replacement. Configure from observed
host paths; the checked-in examples do not assume a particular user's home.

Start the new local service and submit the generated request files:

~~~sh
./cortexium-runner serve \
  --config /absolute/new/qualification/config/instance.json \
  --data /absolute/new/qualification/state \
  --listen 127.0.0.1:8975
~~~

~~~sh
node examples/role-tasks/submit.mjs \
  --runner /absolute/path/to/new/cortexium-runner \
  --data /absolute/new/qualification/state \
  --endpoint http://127.0.0.1:8975/mcp \
  --project writing \
  --request /absolute/new/qualification/requests/writing-small.json \
  --request-id writing-small-1
~~~

Submit writing-deliberation.json with a distinct saved request ID, then saas.json
with project saas and another ID. The helper uses Runner's supported --inputs
argument without shell interpolation. Choose an unused local listener and keep
that endpoint explicit for every command. Rejoin the saved run rather than resubmitting
when a caller stops waiting. Human questions and interventions go through
Runner's normal task controls. A local retry, resource or permission failure is
visible evidence, not permission to bypass that boundary or finish externally.

For the minimal writing comparison, submit the same writing-small.json with
--process write-quick and a distinct request ID:

~~~sh
node examples/role-tasks/submit.mjs \
  --runner /absolute/path/to/new/cortexium-runner \
  --data /absolute/new/qualification/state \
  --endpoint http://127.0.0.1:8975/mcp \
  --project writing \
  --process write-quick \
  --request /absolute/new/qualification/requests/writing-small.json \
  --request-id writing-quick-1
~~~

This ordinary one-step process uses Opus with input-only access, tools:none,
skills:none and no task graph. The default writing process and its deliberation
remain unchanged. Record actual native startup, execution, token usage and tool
facts for each profile. Comparing GPT with Opus describes observed profile costs;
the different models prevent attributing every timing difference to tools alone.

After a runtime-permission discovery, preserve the blocked run's frozen settings
and evidence. For a fresh corrected qualification, choose a new empty configuration
root, such as qualification-runtimes-v2, and run configure.mjs again with the
complete observed runtime paths. Reuse the app directory only while it is still
empty and owned by this qualification; the builder checks that condition. Use the
new root's state and an unused loopback listener. Do not rewrite an existing
qualification directory, copy its credentials/state into the new root, or claim
the old run succeeded. Once app source exists, use an explicitly supported
continuation rather than treating fresh configuration as an overwrite route.

To verify an already built app after a Runner runtime repair, create a new
verification-only configuration. The builder references the original runtime
and private credentials, checks the accepted executable identity, and creates no
app source, credential copy, initializer or coding role:

~~~sh
node examples/role-tasks/configure.mjs \
  --root /absolute/new/verification-continuation \
  --verify-existing /absolute/original/qualification \
  --expected-binary-sha256 ACCEPTED_64_HEX_SHA256
~~~

Start that configuration with its new state directory and an unused loopback
listener, then submit requests/verify-existing.json with project saas and process
verify-existing. It executes saas-fixed-check directly, once, with one allowed
attempt and no model turn or planner. A passing check completes; exit 1 maps to
rejected and fails the process with its proof retained. The outputs are check:text
and artifact:artifact.
Preserve the old run, frozen capabilities and earlier evidence. A successful
continuation does not rewrite an old run's result.

An earlier continuation added a model lead to this mechanical verification.
It delegated again instead of assessing its existing result and exhausted its
six-attempt/two-task allowance. That history remains preserved; the direct fixed
process removes the unnecessary model loop. Full SaaS delivery still uses its
lead to assess the original goal and the independent physical evidence.

In full builds and continuations, saas-fixed-check invokes the protected helper
through Runner's shared command harness. The configured argv, environment and
runtime write roots come from the operator's builder, with no model-selected
command, executable, input substitution or source writes. The profile permits
the macOS browser IPC needed by the configured headless Chrome for Testing while
retaining a read-only app source boundary. The helper starts
the final .qualification/server with an owned temporary SQLite
database and a loopback listener. Genny creates two real tenants and three real
password users in that database. Fixed HTTP checks cover CRUD, spoofing, direct
publication bypasses and negative tenant boundaries; the headless configured
Chrome for Testing then exercises real login, create/edit/publish and public UI
at desktop/mobile widths. Private credentials are never command arguments or
report content. Browser, service and database cleanup run in finally, including
failed checks.

The command declares check:text and artifact:artifact, collecting evidence.tar.gz
from its owned scratch directory. The archive contains report.json and at most four
screenshots; Runner retains one canonical artifact and removes loose intermediates.
Downstream tasks use the artifact's supplied canonical read path. Do not retain a
second copied evidence tree. During full delivery, exit 1 maps to ready so the
lead can assess failed proof and choose an in-scope correction. During
verification-only continuation, it maps to rejected and fails directly. All
fixed assertions, executable identity checks and cleanup requirements remain.

The fixed product contract is
[saas-acceptance.md](../../testdata/role-tasks/saas-acceptance.md).
Retain the task graph, actual native provenance and limits/usage, accepted source
and one final binary, writing outputs, the verifier's small report and screenshots,
and meaningful failure evidence. Its report includes checker and executable
hashes and cleanup facts. The verifier uses a new output directory for a repair;
it refuses to overwrite report.json. The archive is available on failed product
acceptance as well as success.

Before declaring qualification, inspect Runner's retained task graph to prove
the nested planner and overlapping backend/frontend execution actually happened.
Check that provenance distinguishes available, permitted and observed tools/skills
and states telemetry gaps. Count native attempts and explicit revisions; do not
infer low overhead from a passing result alone. A fixed assertion failure must
route back through Runner planning/repair; product workers cannot edit the checker.

After retaining accepted evidence and settling all owned processes, remove only
owned regenerable caches/build copies. Keep the canonical accepted app/source and
required run state. No separate test worktrees, copied node_modules or isolated
Go caches are needed. Run the shared Go-cache maintenance tool if Go checks were
performed. SQLite qualification does not prove PostgreSQL or production readiness.
