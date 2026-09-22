# Supported heavyweight verification

Runner's `verify` launcher coordinates **one heavyweight command per operating-system
account on a host**, across Projects and entrypoint IDs. This resource is separate
from agent parallelism. It neither increases the live assignment limit nor controls
arbitrary shell commands that do not use the launcher.

An operator configures literal command/argument entrypoints, timeouts, executable
input roots, installed dependency roots and the toolchain executables they use.
There is no caller-supplied command override. Relative executable names containing
`/` are rejected; use a PATH name or absolute executable, with repository-relative
script paths as arguments. Toolchain declarations must cover interpreters and
browser executables behind wrappers, not just the wrapper itself.

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
`dependency_exclude_paths` is reserved for explicitly reviewed non-executable
runtime cache directories strictly below a selected dependency root; it is not
permission to omit packages. All selections and exclusions affect the settings
digest and therefore plan approval and evidence applicability.

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

The entrypoint timeout includes waiting for the heavy slot. Authority, configuration
and candidate inputs are checked before waiting, again at grant, and after command
completion and cleanup. A canceled waiter must not start a command. Waiting,
execution and cleanup have separate observed intervals; unavailable intervals are
not recorded as zero. Receipt `started_at`/`finished_at` cover the whole invocation,
including input observation. Nullable `run_started_at`/`run_finished_at` identify
the supervised run boundary excluding slot waiting and subsequent cleanup; they
are not inferred process-spawn times and are absent when launch was refused.

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
record. Current-candidate gates can require a new run even where narrower inputs
are unchanged.
