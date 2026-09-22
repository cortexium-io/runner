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
  "args": ["run", "validate:complete"],
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
At grant, applicable independently protected proof skips both preparation and
the check, avoiding unnecessary installation/network work. Directory existence
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
preparation and check execution. Authority, configuration and candidate inputs
are checked before waiting, again at grant, after preparation, and after command
completion and cleanup, before releasing the claim. A canceled waiter must not start a command. Waiting,
execution and cleanup have separate observed intervals; unavailable intervals are
not recorded as zero. Receipt `started_at`/`finished_at` cover the whole invocation,
including input observation. Nullable `run_started_at`/`run_finished_at` identify
the supervised run boundary excluding slot waiting and subsequent cleanup; they
are not inferred process-spawn times and are absent when launch was refused.
Preparation has its own command/outcome/report digest and observed UTC run/cleanup
interval. A failed preparation leaves the check's run endpoints absent; no
preparation-only result qualifies as a passing check. Cleanup retains the existing
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
