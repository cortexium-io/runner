# Supported heavyweight verification

Runner's `verify` launcher coordinates **one heavyweight command per operating-system
account on a host**, across Projects and entrypoint IDs. This resource is separate
from agent parallelism. It neither increases the live assignment limit nor controls
arbitrary shell commands that do not use the launcher.

An operator configures literal command/argument entrypoints, timeouts, executable
input roots, installed dependency roots and the toolchain/runtime closures they use.
There is no caller-supplied command override. Relative executable names containing
`/` are rejected; use a PATH name or absolute executable, with repository-relative
script paths as arguments. Toolchain declarations must cover interpreters and
interpreters behind wrappers. Use `runtime_paths` for complete external browser
installations/frameworks; an executable stub alone does not identify its engine.
Select the actually configured browser channel, not an unused downloaded browser.

```json
{
  "verification": {
    "focused-domain-check": {
      "command": "node",
      "args": ["--test", "tests/domain.test.mjs"],
      "timeout_seconds": 120,
      "input_paths": ["src/domain", "tests/domain.test.mjs", "package.json"],
      "dependency_paths": [],
      "toolchain_commands": ["node"]
    }
  }
}
```

This illustrative dependency-free check needs no installed dependency directory.
A real application entrypoint must select its actual executable dependencies.
A lockfile alone does not prove that installed dependency bytes are unchanged.
`dependency_exclude_paths` declares explicitly reviewed non-executable cache
directories, not permission to omit executable packages. Whole-root exclusion is
allowed only with preparation and at least one other nonexcluded executable
dependency root. The excluded root remains a preparation write root with the
same no-tracked-content/control/symlink checks. All selections and exclusions affect the settings
digest and therefore plan approval and evidence applicability.

## Dependencies and external runtimes

For a project whose reviewed policy permits ordinary npm lifecycle scripts:

```json
{
  "command": "npm",
  "args": ["run", "validate:heavy"],
  "current_candidate_check": {
    "command": "npm",
    "args": ["run", "validate:current"]
  },
  "timeout_seconds": 3600,
  "input_paths": ["src", "tests", "scripts", "package.json", "package-lock.json", "playwright.config.ts"],
  "dependency_paths": ["node_modules", ".runner-npm-cache"],
  "dependency_exclude_paths": [".runner-npm-cache"],
  "preparation": {
    "command": "npm",
    "args": ["ci", "--cache", ".runner-npm-cache", "--no-audit", "--no-fund"]
  },
  "toolchain_commands": ["node", "npm"],
  "runtime_paths": ["/Applications/Google Chrome.app", "/Users/example/Library/Caches/ms-playwright/webkit-2336", "/Users/example/.nvm/versions/node/v24.20.0/lib/node_modules/npm"],
  "require_current_candidate": false
}
```

This is an illustrative catalog entry, not a complete input policy for every
application. Review all executable source/configuration roots, the installed
browser selections and the maintained gate's actual command. No installation or
configuration is inferred from this example. The cache exclusion explicitly
declares `.runner-npm-cache` non-executable; installed `node_modules` bytes remain
bound. Lifecycle scripts are not silently disabled.
The npm CLI also imports installed modules: its package closure, not only its
small executable script, is declared alongside the actual browser roots.

Preparation is one literal command under the same claim/deadline as the check.
At grant, applicable independently protected heavy proof skips preparation and
the heavy check, avoiding unnecessary installation/network work. A configured
`current_candidate_check` still runs freshly before that return. Directory existence
alone is not preparation proof. Otherwise Runner pins source, authority,
configuration, runtimes and every worktree path outside the declared dependency
roots (including ignored files), runs preparation with owned descendant cleanup,
and rechecks them before accepting the resulting actual dependency bytes as the
check baseline. Tracked content, overlapping roots, Git/control roots, symlink
escapes and undeclared worktree writes fail closed. Exclusions grant no additional
write authority. Preparation failure never launches the check.

This is verification of the supported command, not host-wide filesystem write
enforcement. Host-access lifecycle scripts retain host access; operators must
review them and direct caches into declared roots. Sandboxed callers retain their
existing containment. The later check may create ordinary ignored reports/build
outputs outside executable selections; those outputs do not invalidate executable
applicability merely by existing. Full Git candidate protection remains separate.

`runtime_paths` contains literal absolute regular files or full installation
directories, not discovery patterns. The collector pins names, modes, content and
internal link closure with no-follow reads. External/missing link targets, special
files and runtime/worktree overlap are refused. Content streaming uses a 64 KiB
buffer, two stable reads and per-observation limits of 100,000 entries, 2 GiB per
file and 8 GiB across declared runtime roots. These are refusal ceilings, not a
claim that arbitrary undeclared runtime dependencies were discovered. Doctor uses
the same runtime collector with a 30-second readiness bound, without executing
preparation or checks. Measure collection cost for the real complete gate; small
focused entrypoints need not declare unrelated browser installations.

External runtime and toolchain observation uses a separate read-only directory
view, not the protected authority-file reader. On Darwin, root/effective-user
owned installations may be group-writable by the trusted `admin` group (GID 80),
including nested package directories and executables. Other group-writable
objects, other owners and world-writable runtime objects are refused. Existing
sticky temporary ancestors remain traversable; they are not accepted as selected
world-writable runtime roots. Linux has no admin-group exception. The view has
no write operations or conversion to a writable directory; ordinary source,
dependency, Git, configuration, credential and private-state readers keep their
strict policy. Ancestor identity/owner/group/mode and selected names, descriptors,
listings and stable content remain checked. Internal framework links still must
resolve entirely through the selected closure.

The operator must trust runtime maintainers and inspect relevant ACL grants:
these POSIX permission checks are not comprehensive ACL enforcement, supplier
authentication or protection against malicious administrators replacing and
restoring installed code. Runtime hashes identify observed bytes and stability,
not an immutable execution mount. The runtime-read policy version participates
in environment identity. Doctor and execution use the same collector; refused
runtime observation is a setup/provenance failure, never a failed executable
check authorizing repair. Measure the full configured closure and Doctor path
against existing time bounds rather than automatically increasing timeouts.

## Execution and containment

```sh
cortexium-runner verify --config /absolute/operator/config.json \
  --entrypoint focused-domain-check --directory /absolute/candidate --json
```

The candidate must be clean and committed. The configured destination base must
resolve locally; this command does not fetch, modify cards, start a worker or
authorize a plan. A standalone CLI receipt remains standalone evidence. Whole-plan
delivery uses the same launcher through the coordinator, with verified plan
authority and a protected acceptance record.

The command inherits its caller's permissions. An isolated harness that cannot
access the account's shared claim or configured tools receives a capability
failure: Runner does not create a second sandbox-local slot, add host permissions
or route the command through a privileged broker. Direct coordinator execution
requires the already-approved host-access profile. Unsupported isolated complete
verification fails closed rather than silently running on the host.

The entrypoint timeout includes input observation, waiting for the heavy slot,
preparation, current-candidate guard and heavy check execution. The guard has no
separate timeout, grant or claim. Authority, configuration and candidate inputs
are checked before waiting, again at grant, after preparation, and after command
completion and cleanup, before releasing the claim. A canceled waiter must not start a command. Waiting,
execution and cleanup have separate observed intervals; unavailable intervals are
not recorded as zero. Receipt `started_at`/`finished_at` cover the whole invocation,
including input observation. Nullable `run_started_at`/`run_finished_at` identify
the supervised run boundary excluding slot waiting and subsequent cleanup; they
are not inferred process-spawn times and are absent when launch was refused.
Preparation has its own command/outcome/report digest and observed UTC run/cleanup
interval. A failed preparation produces no new heavy receipt; its actual phase
result and current invocation accounting remain separate. A check that never ran
is absent, not a fabricated failed or successful check. Cleanup retains the existing
bounded supervisor and unresolved-ownership quarantine even after timeout.

The resource uses a stable private lock and one durable active claim, outside the
repository. Its location is based on the OS account, not harness-replaced HOME/XDG
settings. A free lock is not proof of safe admission after a launcher crash.
Healthy active heavy work does not block unrelated agent admission; abandoned or
unresolved work quarantines its owning task's capacity.

The heavy invocation has a separate inherited ownership marker. It retains an
enclosing harness's marker, so inner cleanup cannot target siblings while outer
cleanup can still find detached descendants. The claim remains active through
descendant cleanup. This guarantee is for supported same-user processes retaining
the markers, not remote services, hostile same-user commands, processes deliberately
erasing ownership, or unrelated protected processes whose environment is unreadable.

## Interrupted-command recovery

An outer cancellation can stop a nested launcher before it clears its claim even
when outer cleanup subsequently removes its descendants. Runner conservatively
requires explicit recovery instead of inferring that the interrupted test passed.

```sh
cortexium-runner verify --recover --dry-run
cortexium-runner verify --recover --expect-token EXACT_PREVIEW_TOKEN
```

Inspect the interruption first. Recovery acquires the same resource lock, requires
the original supervisor and tagged descendants to be absent, rechecks the exact
claim and clears only that claim. It never kills work or creates passing proof.
Changed, malformed, active or otherwise uncertain claims are not overwritten.
An existing Runner process with quarantined admission still needs an orderly stop
and restart after safe recovery; recovery does not erase its in-memory safety state.

## Proof and reuse

Full candidate integrity and executable-check applicability are different:

- Full Git/worktree protection still binds the exact candidate and detects changed
  code, controls and tracked evidence.
- Applicability hashes the reviewed input selection, names, content, executable
  modes, declared dependency contents, relevant base inputs, approved requirements,
  entrypoint settings, environment and declared toolchain binaries.
- A copied QA checkout can have identical applicability despite different inodes.
  Inodes and no-follow handles protect reads; they are not execution semantics.
- An evidence-only change outside executable roots can preserve applicable proof;
  it cannot preserve an outdated full-candidate acceptance automatically.

Input collection rejects escapes and symlink routes through unobserved inputs.
Environment values are hashed in memory, not written into receipts. Only known
ownership metadata and PWD/OLDPWD are normalized; other environment changes remain
conservative invalidators. Toolchain/input policy is explicit and requires review;
Runner does not claim to discover every dependency of arbitrary shell programs.

A receipt preserves original execution and attempt identities, UTC interval,
candidate/base, argv/settings, outcome, input bindings, output digest and cleanup
status. Reuse remains **historical** and never receives a fresh execution timestamp.
Failed, interrupted, cleanup-unresolved, tampered or unbound receipts cannot satisfy
acceptance. A model-provided receipt plus its own matching hash is not authentication:
the expected digest must come from Runner's independently protected acceptance
record. `require_current_candidate` defaults to false for this explicitly reviewed
catalog: unchanged independently observed applicability may reuse a historical
passing check. Set it true when policy additionally requires the exact current
candidate; the setting participates in the approved catalog digest and CLI output.
No request-level override can weaken it. Both pre-preparation and post-preparation
reuse are assessed under the same claim and followed by final observation.
Historical results return the original receipt unchanged with `historical: true`;
any preparation performed now is separately reported, not relabeled as the old
check's execution. CLI receipts are still not independently protected plan proof.

### Current-candidate guard and protected pair

`current_candidate_check` is one maintained literal command/argv, not a workflow
graph. Use it for checks such as broad lint/architecture discovery whose scope
extends beyond the heavyweight executable-input selection. The maintained native
complete gate should compose that guard and the heavy entrypoint exactly once;
manual complete validation must not silently omit the guard. Input selections
still need to cover all actual heavyweight inputs, including compiler source
discovery, configuration and relevant optional environment-file absence. A guard
is not permission to trust an underspecified heavy catalog.

The launcher runs the guard after any needed preparation, before heavy execution
or either historical-heavy reuse return. It uses the same supervised ownership,
claim and original deadline. Doctor checks guard availability without execution;
its executable bytes participate in environment identity. After a normal exit,
including a nonzero exit, Runner reobserves authority, settings, full candidate
integrity and applicability inputs before accepting the phase result. Commands
retain their existing containment, and no guard configuration grants access.

`VerificationEnvelope` pairs the original heavy receipt with a separate guard
receipt bound to exact current plan revision, repository, candidate/tree/base,
integrity, entrypoint, settings, argv and execution identity. Protect the digest
of the entire envelope, not just the heavy member. Guard failure retains the
original heavy receipt unchanged; absent prior/executed heavy proof remains nil.
Raw phase output is bounded to 64 KiB head/tail per stream and kept separate from
its receipt's digest. It is private diagnostic material, not guaranteed secret-free
and not suitable for automatic wholesale publication.

Envelope assessment authenticates and checks the pair; it does not execute or
renew a guard. Pending publication recovery must authenticate protected evidence,
extract only the original heavy member/digest and call the launcher again: it
can reuse applicable heavy proof but must execute a new guard. A previously
confirmed terminal merge must not run verification again. A configured guard's
missing, failed or stale proof cannot satisfy publication acceptance.

The supported `CheckFailure` marker is restricted to observed normal nonzero
guard/heavy exits with unchanged post-command authority/inputs and resolved claim
cleanup. Preparation, process-start, timeout, cancellation, missing/tampered proof
and uncertain cleanup failures are ineligible. No stderr parsing or model decision
can manufacture this classification. Even an eligible failure requires the
coordinator's existing bounded review/repair authorization; it is not automatic
scope expansion. CLI output distinguishes the current invocation outcome from a
retained historical heavy pass.

## Review and delivery scheduling

Authenticated whole-plan review identifies the exact post-review complete gate.
The reviewer evaluates product behavior, engineering quality and applicable
repository procedure; acceptance is not delivery. Merely awaiting that scheduled
gate does not require a duplicate full suite during focused QA. The coordinator
still requires protected passing gate proof before final publication. An unrun
check must never be marked passed because it is scheduled. Explicit approved
pre-QA obligations, concrete failures and unresolved product questions still need
actual evidence/permitted focused verification or an amendment/blocker. Standalone
cards do not acquire post-review scheduling authority from a boundary label.

Whole-plan acceptance is retained in the existing private parent-feedback record
before the complete gate starts. This progress is not publication authorization:
the immutable final publication record is created only with accepted QA and a
passing protected verification envelope. Recovery revalidates the exact approved
plan/member context, reviewer profile/settings, candidate/base and comment context.
Applicable original heavy proof remains historical; pending publication runs a
fresh configured current-candidate guard. Superseded progress is archived without
rewriting original receipt bytes, assessment, settings or usage. Missing protected
progress or changed provenance blocks rather than inferring acceptance from prose.

Only the launcher's eligible observed command failure can spend one existing
reviewer evidence-audit invocation. The spent intent is durable before launch;
the actual result (including partial/unavailable usage) is retained before any
Project transition. Recovery with an uncertain spent invocation refuses replay;
a retained completed result can apply its exact rejection without another model
or gate run. Classification has no focused-verification follow-up. Unknown causes,
unanswered proof questions, missing/out-of-scope ownership and invalid results
require input or an amendment. An `accept` response cannot turn failed complete
proof into a pass. Concrete findings use the existing approved owning cards and
parent QA rejection allowance, without resetting or charging it twice.
Unresolved classifier cleanup retains its private checkout/runtime artifacts and
actual provider failure/usage, quarantines local admission, and never replays the
spent invocation. Retained owned paths are local diagnostics, not Project payloads.

Pending final-plan PRs retain their accepted workspace and prepared dependencies
until the ordinary terminal cleanup. At the serialized automatic-merge boundary,
Runner authenticates the immutable final acceptance and freshly observes input,
candidate and original reviewer-setting bindings. An unchanged candidate's
passing complete proof and current-candidate guard remain applicable with their
original identities and times; observation does not rerun or relabel them as
fresh. For a still-pending PR with a valid bound action, missing, tampered or
changed proof observed before merge disarms automatic merge and returns the plan
to admitted QA. An unexpected authorization or settings change can invalidate
that action before the proof hook: Runner fails closed, but an operator must
coordinate any already-armed GitHub auto-merge. This is not an atomic continuous
proof guarantee over GitHub state; a PR may merge between observations. Guarded
migrations still finish or stop existing batches before changing their settings.
No model, preparation or verification command runs inside pull-request
reconciliation, and no arbitrary freshness TTL is used.

Confirmed final-PR merge recovery takes precedence over live validation. It rereads
the exact immutable private publication record and retained workspace identity,
then verifies current signed parent authority and the exact remote PR tuple.
The plan branch, checkout, installed dependencies or gate runtime may already be
gone; they are not needed to record that confirmed delivery. Pending/open PRs
retain the full current-candidate checks, and closed-without-merge is not delivery.

## Consumer-boundary fixture

The deterministic ownership suite includes an opt-in check against an actual
selected Node `runBrowser` module. Set `RUNNER_BROWSER_CONSUMER_MODULE` to its
absolute path and `RUNNER_BROWSER_CONSUMER_SHA256` to independently checked bytes,
then run `go test ./internal/subprocess -run '^TestHeavyActualBrowserConsumerParentDeath$'`.
It imports that module with a synthetic candidate/server, crashes the launcher,
checks the two retained ownership markers, verifies quarantine and outer cleanup,
and uses only a temporary claim. No private application source is bundled, no
browser is started and no model is called. Absence of the selected module skips
this integration check, not the ordinary ownership fixtures.
