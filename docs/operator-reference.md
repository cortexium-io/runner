# Operator reference

`cortexium-runner` is a local CLI that develops maintainer-approved
GitHub Project work through planning, implementation, agent QA, and a pull
request that awaits a human decision by default. It has no hosted control-plane,
web UI, inbound listener, webhook server, or GitHub Actions runner dependency.

The Runner uses:

- Git and GitHub CLI (`gh`) for repositories, Projects, branches, and pull
  requests.
- One or more natively authenticated AI harnesses: Codex CLI, Claude Code, or
  Pi CLI.
- Configurable role profiles and typed event/action rules over Kanban lanes.
- Three bundled Agent Skills for planning, implementation, and review.

Codex CLI, Claude Code, and Pi CLI can each fill planner, implementer, and
reviewer roles.

Every model process is launched through one immutable Runner-owned execution
profile. Planner and reviewer processes start in disposable private neutral
directories. Probe processes receive no task tools. Implementers start in a
Runner-prepared issue worktree. Codex and Claude roles use their native sandbox
by default; host access is an explicit per-role choice where supported. Runner
verifies the active checkout and task worktree after execution and before QA or
publication. An installed CLI that does not advertise the required
non-interactive and structured-result controls is rejected before a model
process starts.

Sandboxed Codex roles use native `read-only` or Runner-owned split filesystem
profiles. Sandboxed Claude roles use Claude's native sandbox; implementations
launch in the task worktree, while reviewers launch from a private neutral
directory with the repository added as a read-only root. Safe-tool profiles
deny ambient operator-home reads while permitting the assigned workspace,
minimum system runtime files, and the implementer's npm cache. Native and
external sandbox implementations still have platform limits; use a dedicated
OS account or external sandbox for stronger host isolation.

Focused QA checks receive a disposable copy of the exact candidate source.
The reviewer can restore its existing locked dependencies and create build,
cache, and test output there without changing the read-only reviewed checkout.
Source, tests, manifests, and lockfiles must remain unchanged; Runner rejects
the review result if copied source changes. With `safe_tools` enabled, this
focused stage permits loopback and the npm registry plus the fixed public Go
module path through `proxy.golang.org`, the `storage.googleapis.com` archive
redirect, and `sum.golang.org`. It does not permit arbitrary package registries
or external services; audit-only review gets no package-download access. Other
prerequisites must already be available within the configured access boundary.
Local applications must use this copy and their own loopback port, not a shared
server. The copy is removed after the stage. No new configuration is required.
The verification copy has a fresh standalone `.git` index for source inventory
(`git ls-files`), not a link to the project's Git data. It contains only copied
source blobs and index entries, with no commits, history, remotes, inherited
templates, or shared objects. Git revision and diff audits still belong in the
canonical read-only checkout. Runner verifies copied source, the complete private
Git directory inventory and metadata, and logical/staged index entries after the
stage. Normal index cache refreshes are allowed; added Git controls, split-index
sidecars and hidden-entry changes are not. The post-review index is read without
following links and parsed separately from the writable copy. Source staging is
batched, with no per-file Git subprocesses and no source-defined filters.
Run the repository's required validation launcher and retain its complete-suite
or standalone receipt requirements. If repository policy accepts equivalent
underlying commands with Runner-bound evidence, record their actual commands,
settings, exit status, and outcomes. Preserve failed attempts. Do not replace
the index, expose shared Git metadata, run tests in the canonical checkout, or
manufacture a receipt. History-dependent or host-only checks still require
their documented proof path; the temporary index does not invent that evidence.

After upgrading, restart the service
with the new binary and run `doctor --fix --offline --config PATH` to refresh the
bundled reviewer skill before retrying a card blocked by missing QA dependencies.

Host-access Claude roles use `--dangerously-skip-permissions`; host-access Codex
roles use `danger-full-access`. Pi implementation and review require host access
because Pi has no native OS sandbox for its shell/edit tools. Every adapter
suppresses unrelated native customization and exposes a fixed role tool set.
Read the README warning before enabling host access.

## Install and command behavior

Install the latest macOS or Linux release into `~/.local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/cortexium-io/runner/main/scripts/install.sh | sh
```

The installer selects Intel or ARM, verifies the archive against the release's
`SHA256SUMS`, validates the binary version, and does not use `sudo`. Pass a
strict `vMAJOR.MINOR.PATCH` argument to install a specific release. See the
[README](../README.md#install) for the inspect-before-running form and `PATH`
setup.

### Git selection

Runner uses the first `git` executable in its process `PATH`. Doctor reports
that path and version and requires a successful `git --version`; it does not
prove that every network push will succeed. Runner's sanitized agent tool PATH
preserves the same operator selection, including on macOS, rather than giving
Xcode Git a separate preference. Required Git runtime paths remain read-only
in sandboxed profiles. Runner neither upgrades Git nor selects the highest
installed version automatically.

A launchd service uses the `EnvironmentVariables.PATH` in its plist, which can
differ from an interactive terminal. If publication fails with one Git installation
but succeeds with another, correct the service's Git selection and run Doctor
with that same PATH. To avoid changing other tools, prepend a dedicated directory
containing only a `git` symlink to the chosen executable. Gracefully stop the
service before changing its environment and reload its plist afterward; an
already-running worker does not pick up PATH changes. Keep the old selection
available for rollback. An accepted candidate blocked only on publication can
then be retried normally without changing the candidate or its QA evidence.

### Updating

For an installed release build, update in place with:

```bash
cortexium-runner update --check
cortexium-runner update
```

`update --version vMAJOR.MINOR.PATCH` selects an exact release. The native
updater verifies the checksum, archive contents, and downloaded binary version,
then atomically replaces the resolved executable. Run `doctor` afterward and
rerun `init` when it reports newly required Project fields. For supported local
launchd workers, the updater drains and reloads the services using this exact
executable. See [graceful stop and managed upgrades](#graceful-stop-and-managed-upgrades).

To build the current checkout instead, use the Go version declared in
[`go.mod`](../go.mod):

```bash
go build -o cortexium-runner ./cmd/cortexium-runner
```

That writes `./cortexium-runner` into the checkout.

Installing the Runner starts nothing. It installs no launchd agent, systemd
unit, or background service, and it registers no autostart, timer, or login
item. The Runner operates only while you invoke it
yourself:

```bash
cortexium-runner run --config /absolute/operator/path/runner.json
```

That command polls in the foreground for as long as you leave it running and
continues observing unrelated Project and pull-request events while harness
actions are in flight. It does nothing once you stop it. Use
`cortexium-runner run --once` for one synchronous polling cycle in a diagnostic
or scripted workflow. `init`, `doctor`, `plan`, `approve`, `retry`, `status`,
`metrics`, `guidance`, `role`, `workflow`, `delivery`, `verify`, and `harness` are one-shot commands that exit
when they finish. Running
`cortexium-runner` without arguments shows help; every command supports
`--help`, and `--version` prints the installed version.

## Graceful stop and managed upgrades

```bash
cortexium-runner stop
cortexium-runner stop --wait --timeout 10m
cortexium-runner stop --config /absolute/operator/path/runner.json --wait
```

`stop` discovers the current user's continuous workers on this machine from
their existing local process records. It requires no GitHub access or config
unless the optional project filter is supplied. It does not target other users,
remote machines, arbitrary harness processes, standalone `plan`/retained-QA
commands, or new workers started after discovery. `run --once` remains a bounded
one-shot command, not a drainable continuous worker.

The command requests each selected worker to stop admitting assignments, then
waits for acknowledgment. The worker finishes already-admitted actions, including
their normal evidence, publication and Project transitions, without canceling
their contexts. Subsequent roles, automatic retry attempts, polling reconciliation
and intake do not start after acknowledgment. A stop arriving during a poll is
acknowledged when that poll returns; work admitted before acknowledgment belongs
to the set being drained. Existing per-action timeouts and safe failure behavior
still apply. Shutdown does not wait for all cards to reach Done, CI or human gates.

`status` shows `Stopping` and the remaining active-assignment count; JSON exposes
`process.stopping` and `process.active_assignments`. `--wait` waits for the selected
worker locks to be released and, on launchd, for their jobs to be unloaded.
`--timeout` bounds acknowledgment/shutdown waiting; interruption or timeout returns
nonzero without killing work or canceling an accepted stop request. Repeating a
request is safe. Requests are owner-only, bounded, atomic, and bound to the exact
project/PID/start-time incarnation, so stale requests cannot stop a replacement.

On macOS, a directly launched GUI-domain LaunchAgent is identified using its
launchd service identity, PID and executable. After draining and releasing its
resources, Runner unloads that exact job with `bootout`. This works even with
`KeepAlive=true` and leaves the plist and persistent enabled/disabled settings
unchanged. Normal configured startup on the next login remains intact. System
LaunchDaemons and wrapper-launched jobs are not automatically managed. Other
supervisors must be configured not to respawn an intentionally stopped worker.
`stop` does not install or invent a service definition.

For a release build, `update` downloads and validates the new binary before
requesting any stop. It automatically drains only supported, currently running
launchd services using the executable being replaced, atomically replaces the
binary, then reloads those exact services and checks for a fresh successful poll.
It does not restart services that were already stopped or take over a stop already
in progress. Foreground workers require `stop --wait`, followed by `update` and an
explicit `run`; their terminal sessions are not recreated. A standalone CLI command
is not a continuous worker and is not canceled or restarted by this procedure.

The updater retains the original service paths and content digests in memory.
A changed definition or interveningly reloaded job is left untouched. After a
fully completed drain, replacement failure/cancellation still attempts to reload
the same services with the binary left on disk. If draining fails or is interrupted,
the binary is not replaced and the command reports that accepted stop requests
remain active; inspect each service before recovery. A failed reload or readiness
check is reported explicitly, including whether the binary was replaced.
The update command does not migrate project configuration, install skills, or
roll back a successfully installed binary because of a readiness failure.

Workers started with an older binary do not understand stop requests. They are
reported as unsupported and never signaled or killed; the first upgrade still
requires an idle maintenance boundary. The bootstrap installer and manual
`go install` do not use the native updater's drain/reload sequence.

`Ctrl-C` and SIGTERM retain their existing cancellation/recovery behavior. A
graceful request does not protect a process from OS shutdown or an explicit kill.

## Quick start

For a concise Claude Code-only setup flow, use the
[Claude Code quickstart](claude-code-quickstart.md).

Authenticate GitHub separately when necessary:

```bash
gh auth login
gh auth refresh -s project
```

In a terminal, `init` prompts for missing required choices. From the target
repository, the smallest guided preview is:

```bash
cortexium-runner init --dry-run
```

Remove `--dry-run` after reviewing the preview. The guided flow suggests
`.cortexium/runner.json` in the repository, asks whether to create or adopt a
Project, then collects the remaining runtime choices. Scripts and redirected
input still require an explicit `--config` path.

When stdin and stdout are terminals, every finite choice—including maximum
concurrent cards—uses an arrow-key menu;
press Enter to accept the highlighted value. The current question is cyan and
the selected answer is green. Doctor uses green, yellow, and red for ready,
warning, and failed checks. Set the standard `NO_COLOR` environment variable or
use redirected output to disable colors. Potentially slow one-shot operations
announce what they are checking or changing before they begin. A numbered/text
fallback remains for forced interactive sessions without a terminal. The model
menu is harness-aware: Claude Code shows its supported aliases, Codex uses its
local model catalog, and Pi uses the models reported by `pi --list-models`. Claude
Code does not expose a machine-readable catalog command or stable catalog file,
so Runner does not scrape its private state. Every model menu also allows the
harness's current selection or a custom ID for pinned/provider-specific models.

Use `--non-interactive` for scripts and supply every required choice explicitly.

Create a new GitHub Project, configure it as a status-column Kanban board, link
the repository, create the public-intake label, and write an operator-owned
config. This non-interactive example uses an external path:

```bash
./cortexium-runner init \
  --non-interactive \
  --config /absolute/operator/path/runner.json \
  --owner YOUR_GITHUB_OWNER \
  --create-project "Runner development" \
  --project-visibility private \
  --repository YOUR_GITHUB_OWNER/YOUR_REPOSITORY \
  --project-dir . \
  --harness codex \
  --reasoning high \
  --task-granularity standard \
  --max-parallelism 1 \
  --autonomous-issues \
  --base-update-review required \
  --auto-merge=false \
  --merge-method merge \
  --bootstrap-base-branch
```

`--autonomous-issues` lets labeled issues from a private configured intake
repository enter `Plan` without a separate approval command. GitHub Project
visibility does not grant this trust. For a public intake repository, add an
exact author allowlist with repeatable `--trusted-issue-author LOGIN` flags;
authors not on that list remain in `Needs assessment`. Supplying a trusted
author also enables the policy. Omit both flags for the default human-review
boundary.

Adopt and synchronize an existing Project:

```bash
./cortexium-runner init \
  --config /absolute/operator/path/runner.json \
  --owner YOUR_GITHUB_OWNER \
  --project-number YOUR_PROJECT_NUMBER \
  --repository YOUR_GITHUB_OWNER/YOUR_REPOSITORY \
  --project-dir . \
  --harness codex \
  --reasoning high \
  --max-parallelism 1 \
  --base-update-review required \
  --auto-merge=false
```

Before `init` creates or changes anything on GitHub, it verifies that the config
destination is writable, `--project-dir` is a Git repository, the configured
GitHub remote matches `--repository`, the base branch exists on that remote or
is explicitly authorized for empty-repository bootstrapping,
GitHub CLI is authenticated, the repository has Issues enabled, and the owner
is accessible through GitHub Projects. It then synchronizes the Project fields
and statuses, writes the config, and installs the bundled skills required by the
configured roles. AI harnesses remain user-installed and user-configured.

Project synchronization keeps `Runner Activity` and `QA Failures` visible on
board cards without hiding unrelated fields the user already selected.
Activity is `Planning`, `Implementing`, or `Reviewing` while an agent owns the
card. A transient Codex service failure temporarily shows `Waiting for harness
provider` in the same role lane while Runner performs its bounded delayed
retries. At `PR Ready`, it distinguishes `Awaiting human review`, `Waiting for CI`,
`Waiting for integration slot`, and `Waiting for merge`. A failed integration
check briefly records `CI failed — rework queued` as the card returns to
implementation. Cards with incomplete dependencies show `Waiting for
dependencies`. The internal `Runner Phase` field records the
recovery lane, while the hidden `Runner Transition` field prevents execution
during non-atomic Project updates. `QA Failures` records the current
review-rejection count. `doctor` reports incorrect visibility or field types,
and rerunning `init` repairs the overview.

If only one supported harness executable is available, omitted harness flags
use it for every role. If more than one is available, the guided flow asks for
the default harness for all roles; scripts can still use role-specific harness
flags. `--model` and
`--reasoning` set trusted selections inside the fixed role profile. A
`--planner-*`, `--implementer-*`, or `--reviewer-*` flag overrides the shared
value for that role. Init expands the result into complete role definitions in
the config; these flags do not create a hidden runtime fallback.
`--task-granularity` stores regular (`standard`) or smaller (`small`) downstream
task sizing for both implementer and reviewer. The guided prompt uses the clearer
regular/smaller wording. The role-specific
`--implementer-task-granularity` and `--reviewer-task-granularity` flags
override it. Runner never infers this setting from a harness or model name.
`--base-update-review required` requires fresh QA after automatic base refresh.
New workflows send clean updates directly to the reviewer and conflicts to the
implementer. Existing explicit `pull_request.out_of_date` `updated` routes are
preserved: choose a reviewer lane to avoid unnecessary implementation, or retain
an implementer lane when deliberately required by your workflow. `conflict`
must target an implementer. No route may skip fresh QA before publication.
Automatic merge is separately opt-in through `--auto-merge` or
`github_project.auto_merge: true`. Runner binds the request to the exact
QA-approved head commit, keeps GitHub checks and branch protections in force,
and disarms automatic merge before rework or a branch update. QA publication
queues the PR at `PR Ready`; reconciliation then permits one automatic
integration owner per repository/base. GitHub's enabled auto-merge state makes
that ownership survive a Runner restart. An integration action does not consume
an agent-parallelism slot or admission budget, and existing `PR Ready` cards are
reconciled before new agent work is admitted. In continuous mode, QA completion
wakes that reconciliation immediately. Because `run --once` ends after its
single synchronous cycle, a PR published by QA in that cycle is integrated by a
later invocation. The optional
`--merge-method` flag and `github_project.merge_method` setting accept `merge`,
`rebase`, or `squash`; omitted values preserve the original `merge` behavior.
Runner never silently substitutes another method because that would change the
repository's history policy. When a `rebase` pull request needs a base refresh,
Runner records the complete refreshed tree as a linear candidate on the new
base, sends conflicts back through implementation and QA, and publishes the
approved rewrite only if the remote branch still equals its previously accepted
commit. A local rework history may therefore diverge from that remote commit
before QA; Runner recognizes it only for an existing tracked pull request whose
recorded QA commit still exactly matches the remote head. Any mismatch remains
blocked as a possible external branch change. `merge` and `squash` retain
merge-based base refreshes.

### GitHub repository and merge readiness

Connected Doctor checks the configured repository and base branch before agent
work starts. It verifies that the GitHub CLI account has write access, that
repository auto-merge is enabled when requested, that the configured merge
method is enabled, and that active rulesets permit that method. When the account
can read classic branch-protection details, Doctor also rejects `merge` when
the base branch requires linear history.

The recommended Runner account has write rather than administration access.
GitHub hides classic branch-protection details from that account even though it
reveals that the branch is protected. Doctor therefore emits a warning with the
exact setting to inspect; it does not claim the hidden policy is compatible.
`doctor --fix` repairs only Runner-managed local skills. It never changes
repository permissions, merge settings, branch protection, rulesets, required
checks, or organization policy.

Common readiness failures are:

| Failure | Doctor behavior | Recovery |
| --- | --- | --- |
| GitHub CLI missing, logged out, or missing Project access | Blocks before work | Install `gh`, run `gh auth login`, and grant the `project` scope. Use persistent GitHub CLI login rather than relying only on an environment token for publication. |
| Runner account lacks repository write access | Blocks before work | Grant the account or its team write access to the configured repository. Administration access is not required for normal operation. |
| Configured repository, remote, or base branch disagrees | Blocks before work | Correct `intake_repository`, `remote_name`, or `base_branch`, then fetch the configured base. |
| Repository auto-merge is disabled while Runner requests it | Blocks before work | Enable **Allow auto-merge**, or set `github_project.auto_merge` to `false`. |
| Configured merge method is disabled | Blocks before work | Enable that repository merge method, or explicitly select an allowed `merge_method`. |
| Linear history or a ruleset conflicts with `merge` | Blocks when visible; otherwise warns for protected branches | Remove **Require linear history** if merge commits are intended, or explicitly select `rebase` or `squash`. |
| Required status check never reports | GitHub leaves the PR open; Doctor cannot prove that every future workflow will emit the configured check | Verify Actions is enabled and the required check name exactly matches the workflow's reported check. |
| Harness login, model access, or structured-output support is unavailable | Ordinary Doctor checks the installed CLI surface; `--probe-harnesses` makes a minimal live call; `harness check` exercises each configured role adapter | Authenticate with the harness's native flow, choose an accessible model, and rerun the appropriate live check. |
| Global Git commit signing opens pinentry in repository tooling | Runner-owned commits disable signing, but external setup/test commands may still prompt | Configure test fixtures with `commit.gpgSign=false`, or ensure the operator GPG agent is available; do not give the agent the signing passphrase. |
| Chrome/Chromium is older than 149 | Doctor reports the optional browser capability as blocked; browser-dependent work will fail | Upgrade Chrome/Chromium. Runner keeps the 149+ requirement because its loopback-only MCP URL allowlist depends on that browser feature. |
| Browser, Docker, database, or external-service prerequisites are repository-specific | Checked only when represented by an explicit capability or acceptance obligation | Document the repository's safe local entrypoint and add explicit Doctor requirements where a stable local capability exists. |

For an organization-wide merge policy, create an organization branch ruleset
targeting the default branch of the intended repositories, require pull requests,
and set the allowed merge method explicitly. Organization rulesets are additive:
they cannot relax an existing repository branch-protection rule. Migrate legacy
classic rules once, then use the organization ruleset as the durable baseline.
The repository must still enable the selected merge method and, when used,
auto-merge. GitHub Team or Enterprise is required for organization rulesets
covering private repositories.

When the remote has no branches, `init` reports that state and the exact remedy.
`--bootstrap-base-branch` authorizes it to push an existing local base branch or
initialize and push a new one. An empty-repository bootstrap produces an empty
initial commit; other staged and untracked files are not included. Runner
refuses this bootstrap when the remote already contains another branch or when
local history would make the intended base ambiguous. In a guided terminal
session, Runner asks for this authorization instead of requiring the flag.

The generated config is machine-local and privileged. `run`, `plan`, live doctor
probes, and role-edit commands auto-detect the project-local default; use an
explicit `--config` path for another location.
On macOS and Linux it must be a regular, non-symlinked, single-link file owned by
the effective user and not writable by group or other users. It must be outside
every `workspace_write_root`. By default, `init` places the config at
`.cortexium/runner.json` and ensures it is ignored, adding its exact path to the
root `.gitignore` when needed without staging or committing that change. This is
a safe default, not a hard policy: users may choose an external path or
deliberately track the config.
Unsupported platforms fail closed.

When `--config` is omitted, Runner commands resolve
`.cortexium/runner.json` from the current Git repository root. An explicit
`--config` path always takes precedence.

Configuration v5 is the current pre-stable contract. Runner rejects any other
version and every incomplete configuration instead of inferring operational
values. There is no compatibility or migration layer. Create a complete v5
configuration with `init` before using `doctor` or `run`; an existing v4 file
must be rewritten from the generated example because changing its version alone
does not convert lane behavior into typed rules.

Preview initialization without changing GitHub, writing the config, or
installing skills by adding `--dry-run`. After editing an existing config,
preview and synchronize its GitHub Project with:

```bash
./cortexium-runner init --config /absolute/operator/path/runner.json --dry-run
./cortexium-runner init --config /absolute/operator/path/runner.json
```

For a Project created by `init`, Runner replaces GitHub's initial views with one
fresh `BOARD_LAYOUT` view. This prevents inherited view-only settings such as
column limits. When adopting an existing Project, Runner preserves its views
and creates or converts one view only when it needs a board. Configuration also
adds the workflow statuses and lifecycle fields, creates the intake label, and
links the repository when GitHub permits the Project and repository owner
combination. Cross-owner Projects still work through explicit issue and
repository URLs. It does not delete cards or replace a config file.

Normal configuration remains additive. To remove Status options that are not in
the configured workflow, preview the exact plan and then prune:

```bash
./cortexium-runner init --config /absolute/operator/path/runner.json --prune --dry-run
./cortexium-runner init --config /absolute/operator/path/runner.json --prune
```

Pruning preserves the IDs of configured Status options and removes an extra
option only when no active or archived Project item uses it. Any occupied extra
option blocks synchronization and is reported with active and archived counts.
Pruning never deletes Project items, issues, pull requests, Runner fields, or the
Project itself.

Validate the configuration and embedded skills without network access:

```bash
./cortexium-runner doctor --config /absolute/operator/path/runner.json --offline
```

If `init` reports that an installed bundled Runner skill differs, it leaves the
file unchanged and prints the exact recovery command. Review the reported file,
then replace only the bundled Runner-managed skills for the harnesses in this
config and re-check local readiness with:

```bash
./cortexium-runner doctor --config /absolute/operator/path/runner.json --fix --offline
```

`doctor --fix` does not change harness configuration or the repository. It
replaces differing copies of the configured Runner-managed skills and their
pinned references; unchanged files are retained and missing files are installed.
It never stages, commits, or untracks files.

If Git or GitHub CLI is missing, initialization fails closed and prints manual
recovery guidance:

```bash
./cortexium-runner init [new-project options]
```

Missing prerequisites must be installed through their official distribution or a
trusted local package manager. Missing AI harnesses are reported with manual
installation guidance. The Runner does not install package managers, use `sudo`,
or require Node/npm.

Then verify and run:

```bash
./cortexium-runner doctor --config /absolute/operator/path/runner.json
./cortexium-runner harness check --config /absolute/operator/path/runner.json
./cortexium-runner status --config /absolute/operator/path/runner.json
./cortexium-runner status --verbose --config /absolute/operator/path/runner.json
./cortexium-runner metrics --config /absolute/operator/path/runner.json
./cortexium-runner run --config /absolute/operator/path/runner.json
```

`run` auto-detects the project-local default and keeps polling with built-in
interval defaults until interrupted, including while admitted harness actions
are still running. Use `--poll-interval` or
`--max-idle-interval` only to tune
polling, and `--once` only when exactly one synchronous cycle is wanted.

`doctor` first rejects unknown config fields, invalid repository-reference
declarations, unsafe role definitions, incomplete transitions, and bundled
skills whose pinned hashes differ. Unless `--offline` is used, it then checks
Git, `gh`, GitHub API access,
every configured Project lane and lifecycle field, the configured repository,
harness executables, the exact CLI flags Runner needs for each configured
harness's non-interactive invocation, skills, and explicit tool/MCP
requirements. It does not call a model or inspect AI-harness authentication unless
`--probe-harnesses` is explicit. That flag makes one minimal live model call per
distinct configured harness, command, model, and reasoning profile and validates
the real structured-output path. It does not edit the repository or prove that
implementation tools can execute in the participant's environment.
Normal connected doctor also resolves and validates every configured repository
reference. `doctor --offline` checks only its static shape and role policy.

`harness check` is the paid local adapter smoke test:

```bash
./cortexium-runner harness check \
  --config /absolute/operator/path/runner.json \
  --timeout 5m

# Include browser proof for browser-enabled implementer and reviewer profiles.
./cortexium-runner harness check \
  --config /absolute/operator/path/runner.json \
  --browser
```

The command checks the trusted config and configured executable, then starts one
live conformance attempt through every configured execution-role profile.
Planner checks prove structured output and read-only repository access.
Implementer checks create and verify one exact artifact in a Runner-owned
worktree and pass through the normal post-harness integrity verification.
Reviewer checks exercise the shared evidence-audit contract against a known-good
fixture; as in normal QA, a reviewer may make one additional focused call if its
audit leaves a proof unresolved. `--browser` adds a browser proof for each
implementer or reviewer profile with safe tools enabled. The per-call timeout
defaults to five minutes.

This full role check subsumes the authentication and structured-output evidence
from `doctor --probe-harnesses`; running both is unnecessary. Use the smaller
Doctor probe when file-write and reviewer conformance are not needed.

Runner performs no GitHub operations and assigns only a private temporary Git
repository, which it removes afterward. Configured repository references remain
read-only context for planner, implementer, and reviewer profiles. The command uses the
profile's configured model, reasoning, access, harness configuration, pinned
skills, safe tools, and explicit MCP grants. Consequently, a `host` profile is
still host-access and `host/inherit` remains unrestricted; the result labels
that policy rather than pretending the temporary fixture is a security
sandbox. Browser checks are omitted unless `--browser` is explicit. A failed
profile does not prevent independent profiles from being checked, and any
failed requested check makes the command exit unsuccessfully.

`status` is operational rather than diagnostic: it reports current card counts,
active, queued, waiting, blocked, and PR-ready work, whether a local Runner
process holds the Project lock, and that process's PID, uptime, last and next
poll. `Queued work` contains only cards that the same read-only eligibility
classifier used by execution considers claimable. Agent-lane cards held by a
transition, invalid current authority, dependencies, an incomplete planning
batch, or invalid batch or sibling authority appear under `Waiting work` with a
fixed bounded explanation. `status --json` includes each waiting card, its
stable reason code, and that summary in `work.waiting`.
Authenticated plan children that are accepted and integrated, but whose plan has
not delivered, appear under `Integrated into plan — not delivered`
(`work.integrated_undelivered` in JSON). They are neither queued nor Done and do
not satisfy external dependants. Historical parents whose Done state recorded
only planning completion are labeled separately (`work.planning_completed`);
that label makes no delivery claim and does not rewrite historical state.
Cancelled plans remain inspectable under `Cancelled plans — retained work, no
admission` (`work.cancelled_plans`). Their accepted/integrated children remain
undelivered; cancellation does not remove recoverable work or grant admission.
The emitted codes are `transition_locked`, `planning_metadata_invalid`,
`action_authority_invalid`, `dependencies_incomplete`,
`planning_batch_incomplete`, `planning_batch_sibling_authority_invalid`,
`planning_batch_authority_invalid`, and the fail-closed
`eligibility_unavailable` fallback.

On macOS and Linux, status also inspects the Runner's process tree and reports
active harness or other direct subprocesses by executable name, PID, process
health, elapsed time, configured role timeout, and associated card when the
mapping is unambiguous. It never prints subprocess arguments because those can
contain the approved prompt. Eligibility explanations likewise omit raw
validation errors, assertions, sibling content, and signing material. An
`alive` process proves that the harness process still exists, not that a remote
model is currently producing tokens; the harness timeout remains the final
bound. Nested tool and MCP processes belong to their direct Runner-launched
harness and are not listed as additional harness attempts. Blocked cards include
up to three concise result lines, the configured `Ready` status for an
implementation retry, and the exact `retry` command when Runner recorded a safe
originating lane, so both recovery paths are visible without opening the
Project item.
`status --verbose` adds the current fixed Runner stage and elapsed time for
each unfinished attempt. It is intentionally sanitized: it does not retain or
print prompts, model responses, tool commands, subprocess arguments, or
worktree paths.
Use `doctor` for installation and configuration readiness.

`metrics` reports the recorded duration, outcome, role, harness, model,
reasoning level, QA iteration, recovery classification, harness-reported token
counters, and harness-reported monetary cost for each attempt. Its summary also
shows completed harness invocations and attempts that resumed an exact saved
plan, implementation result, or QA acceptance without another model call. It also shows a
stage timeline for workspace preparation, repository preparation, harness
execution, result validation, workspace verification, candidate construction, Project
transitions, and pull-request publication when those stages apply. A recovered
publication shows its total attempt count; an exhausted publication also shows
the fixed failing operation. Raw GitHub and Git diagnostics are not retained in
metrics. Use
`metrics --item ID_OR_TITLE` for one card or `metrics --json` for
machine-readable output. `status` includes a compact accumulated total and the
current admission-budget state.

Token units are recorded separately as `token_accounting`. With
`inclusive_input_v1`, input includes cache reads and writes, and output includes
reported reasoning tokens. The checked total is **input + output**; cache and
reasoning columns are subsets, not extra tokens to add. `metrics --json`
summaries expose `reported_tokens` as a number when the units are known, or
`null` when unavailable, unresolved or invalid—not zero. A partial summary's
number is only the reported lower bound. Historical records are normalized in
memory using their saved harness identity, including model breakdowns; raw
history is not rewritten and current settings never supply missing provenance.
Unknown or mixed legacy units remain `unresolved`, even with zero cache counts.
See [reported-token accounting](../internal/metrics/USAGE.md) for provider sources
and validation rules. These counters are neither a bill nor a provider quota.

New native harness records distinguish usage `coverage`: `complete`, `partial`,
or `unavailable`. Failed/canceled invocations keep any reported counters as
partial; unavailable is not zero. Old records with counters but no completeness
evidence are shown as `unknown`. Collection precedes diagnostic truncation and
does not retain raw transcripts. Cumulative snapshots are not added repeatedly.
Combining available and missing invocation usage remains partial; configured
token/cost budgets pause admission when the rolling window contains partial
totals. No cost is estimated. A provider that emits nothing before failure
still has unavailable usage. Extremely large individual event/envelope records
remain bounded; later ordinary JSONL records can still supply counters.

`harness_cleanup` measures Runner's cleanup after a native harness exits or is
canceled. It is nested in harness duration, not additional wall time or measured
test time. Test-command/validation durations still require retained project
evidence; Runner does not infer them from the harness interval.

Completed Codex/Pi harness stages retain `harness_activity` in metrics. The
item-history JSON exposes it as `runner_observed_harness_activity`; readable
metrics show a compact summary. It includes the last event receipt time/category,
tool start/completion counts, summed completed tool intervals, and the oldest
tool without a completion event. Use this to distinguish observed tool activity
from an unexplained quiet interval, not to infer which test ran or whether a
process survived cleanup. Intervals can overlap; they are not validation/CPU
time. Commands, tool names, arguments and output are not retained. Coverage is
`observed`, `partial` (malformed/oversized events or bounded tracking gaps), or
`unavailable` (including non-streaming Claude JSON and buffered fallback output).
Older attempts have no backfilled activity. Summaries persist at stage completion,
including timeout/cancellation, not continuously; abrupt Runner termination can
lose the in-memory summary.

New CLI-recorded attempts include `run_context` with the recording build's
`runner_version`, `bundled_skills_version`, and `config_digest`. The last is a
SHA-256 fingerprint of the loaded config after CLI overrides, not its contents.
It can distinguish changed configurations but cannot reconstruct them or detect
changes to native harness settings, environment, or files referenced by path.
The bundled skill version is not proof of the installed role guidance; use the
stage's `prompt_contexts` for that. Older attempts retain unknown identity, and
exporting them with a newer Runner does not relabel them. These fields confer no
approval, receipt validity, or cache guarantee.

Completed review attempts also record `review_verdict`: `accept`,
`needs_changes`, or `blocked`, after result validation and workspace integrity
checks. This includes one-shot operator QA. The verdict is distinct from the
attempt outcome: QA can accept a candidate whose publication subsequently
fails, and a rejection that exhausts the configured limit still records
`needs_changes`. The summary counts these recorded verdicts separately.
This is diagnostic evidence, never approval or publication authority. Missing
verdicts are unavailable, not inferred from historical summaries, roles, or
attempt outcomes. Resuming saved acceptance without another review does not
record a new verdict; stage records do not carry attempt verdicts either.

The summary groups recorded stages by name, with completed/unfinished counts,
failed and blocked counts, recorded duration, and reported usage/cost coverage.
It includes completed stages inside unfinished attempts and failures inside
attempts that later succeeded. Recovered publication retries are counted
separately. Stage durations may nest or overlap: do not add them to attempt
duration or interpret their sum as time through integration. Stage coverage is
explicit, so older attempts without it are not treated as zero-work runs.
Individual test-command duration/repetition and the causes of waits between
attempts remain unavailable; an implementation-stage interval is not measured
test time. Inspect retained project verification evidence for those details.

After a Project planning card produces a valid executable plan, Runner stores
that exact normalized plan in a private mode-`0600` checkpoint before creating
the first child. The record is bound to the approved source content, bounded
issue discussion, role, lane, destination, repository, and deterministic batch
fingerprint. If GitHub staging fails partway through, retrying the unchanged
planning card skips the planner and creates only the missing children; already
staged matching children are reused while the recorded planning source still
matches the destination. If that branch advances, Runner preserves the completed
checkpoint and blocks, without another paid planner call or rewriting its recorded
inspection. Ordinary retry refuses that checkpoint again. Revalidating a retained
plan against a different commit is not supported; explicitly coordinate a new plan
and any partial batch first. A changed planning context discards the
stale checkpoint, while malformed retained state blocks for inspection. Runner
does not archive or delete partial children automatically and clears the
checkpoint after the exact batch is staged successfully.

After a successful implementation, Runner stores one evidence entry per
approved verification check in private mode-`0600` state. The record is bound
to the item, approved-content digest, repository, branch, candidate commit,
candidate tree, and ordered criteria. Agent QA receives it as historical
evidence rather than instructions and reruns only checks whose evidence is
missing, stale, insufficient, or contradicted by direct diff inspection. The
Runner never executes commands merely because they appear in model-authored
evidence.

Runner separately checkpoints a successful implementer result before
post-processing. The private mode-`0600` record binds the approved-content
digest, human and QA context, repository, base revision, branch, exact workspace
snapshot, proof obligations, and candidate commit/tree when one exists. If
candidate construction, evidence persistence, or the GitHub Project transition
then fails, retrying the unchanged card and workspace resumes the saved result
without relaunching the harness. A changed card, comment context, QA feedback,
base, branch, or workspace makes the checkpoint stale and restores the ordinary
implementation path. A recoverable candidate-content rejection clears the
checkpoint so the recorded retry reruns implementation with Runner's sanitized
correction; workspace-integrity failures remain opaque and fail closed.
Supplying explicit `retry --feedback` also clears the checkpoint before the
card moves. Otherwise Runner clears it only after the successful Project
transition.

Runner stores an append-only JSONL history in the user configuration directory,
outside the project repository; set `CORTEXIUM_RUNNER_STATE_DIR` to relocate
that application state. The directory is mode `0700`; the effective-user-owned,
single-link history file is mode `0600` and is opened without following a
substituted leaf. Attempt and stage start records are written before work
and completion records afterward, so an abrupt stop remains visible. Attempt
records contain bounded reports and may contain the exact protected approved
canonical approved-content snapshot and its validated delegated-content digest,
and structured Runner-observed lineage. A bounded report says explicitly when
its retained details are incomplete. Stage records are
restricted to attempt identity, a fixed stage name, timing, outcome, recovery
classification, reported usage, and prompt-context fingerprints. The history
does not retain assembled provider prompts, transcripts, hidden reasoning,
credentials, command or environment payloads, raw harness responses, or raw
failure diagnostics. Runner never estimates missing tokens or cost: Claude Code cost is
shown only when Claude reports it, Codex token counts are shown when its JSON
event stream includes them, and unavailable Pi counters remain explicitly
unavailable. History begins with the first metrics-enabled run and cannot
reconstruct earlier attempts. Cooperative local writers serialize each append
and its limit check; readers hold a shared lock for a consistent snapshot while
Runner is active. The file has a fixed 64 MiB ceiling; exhaustion
refuses another append without changing earlier records rather than rotating or
silently discarding attempts.

Use `metrics --item ITEM_ID` (or an unambiguous exact title) for a chronological
oldest-first history of one card. Both the terminal and `--json` forms separate
`runner_observed`, `model_reported`, and provenance-unavailable legacy facts.
The view includes the protected canonical approved-content snapshot and digest,
bounded reported rationale/actions/verification/review/usage, and every retained
base, candidate, evidence-candidate, reviewed, rebased, published, pull-request,
and merge identity. Missing facts are shown as unavailable; in JSON, unavailable
usage is omitted and named in `unavailable`, never represented by zero counters.
A verification evidence candidate is not relabeled as certifying a later rebased
or published commit. Malformed records are counted and ignored while valid
selected records remain readable. The command only projects the metrics store:
its output is not approval, recovery state, or workflow input.

The `metrics` output shows its exact `History` path; to clear it, stop Runner and
delete that one file. The next attempt recreates it.

### Recurring-failure drafts and shared guidance

```bash
cortexium-runner guidance --config "$RUNNER_CONFIG"
cortexium-runner guidance --config "$RUNNER_CONFIG" --min-occurrences 3 --json
```

`guidance` is a read-only, local view of that history. It needs no GitHub or
model call and can run while the background service is working. The service
also emits a fixed, content-free notice when a new draft reaches the threshold.
By default, a pattern must occur on **two distinct cards**. Set the optional
top-level configuration field `"guidance_min_occurrences": 3` to require three
(any integer of at least two is valid); restart the service to apply it.
`--min-occurrences` overrides the threshold for one inspection only. Existing
configs need no migration, and `init` preserves this setting.

The detector groups exact failed QA finding text, normalizing whitespace only,
and Runner's fixed failure-class/operation combinations. It separates projects,
repositories, roles, harnesses, and finding categories. Multiple findings or
retries of one card count once per pattern. A different model on the same role
can contribute another card; model, reasoning, attempt ID, candidate commit
when available, and prompt-context fingerprints remain in the draft's incident
references. Use `metrics --item CARD_ID --json` to inspect the original evidence
and cost, including all retries.

Completed failed or blocked stages also contribute, even when their enclosing
attempt later succeeds; replaying history preserves those incidents. Stage
observations do not claim the final candidate's identity, which may have changed
after the incident. Repeated successful publication recoveries produce a
separate investigation draft, not a guessed transport diagnosis. Stage and
attempt observations of the same failure on one card still count once.

Drafts suggest an investigation destination: project knowledge for repeated
failed QA findings, skill guidance for repeated evidence/contract/candidate
validation problems, and Runner/tooling investigation for other classified
failures. These are triage suggestions, **not diagnosed root causes**. A blocked
review is an evidence gap, not a confirmed code defect. Unknown failures, human
input requests, cancellation, stage-only records, and records without repository
provenance do not produce drafts. Old history lacks the new structured QA
observations; Runner does not guess them from prose or backfill missing scope.
Different wording is not automatically clustered. Distinct cards are a useful
signal, not proof that two incidents have independent causes.

No draft is activated, injected into prompts, written into a skill, or posted to
GitHub. Drafts have stable IDs and are reconstructed from the existing private
history; there is no extra memory database or persistent approval queue. Restart
rebuilds the same view without announcing existing drafts again. History and
drafts currently have no automatic expiry or resolved/dismissed state. Review
current source and fixes before turning an old pattern into guidance.

To publish a useful lesson, independently check its incident evidence and then
make an explicitly approved, ordinary Git change to the appropriate project
guide or role skill. Keep a project guide small and link it from the project's
`AGENTS.md`: orientation, concrete invariants, established test entrypoints, and
short validated lessons with source references. Do not copy raw logs, card
transcripts, secrets, or model-authored instructions into it. Merge related
lessons, remove obsolete ones, and prefer a Runner code fix when the mistake can
be prevented deterministically. Guidance must not relax acceptance conditions,
security boundaries, or independent QA.

### Context layout and token caching

Runner places pinned skill/capability text and fixed role/stage instructions
before changing card titles, approved bodies, proof lists, prior review data,
and workspace paths. It does not insert live draft counts, timestamps, or
history into that prefix. The planner, implementer, and shared reviewer use
this layout across Codex CLI, Claude Code, and Pi. Repository guidance remains
part of normal repository instruction/source inspection, not an automatically
injected or hot-reloaded memory block.

`metrics` exposes `prompt_contexts`: a `layout` version and a SHA-256
`guidance_digest` of the actual pinned skill/capability text. Stages retain the
version selected at start; attempts list the distinct versions encountered.
An exact saved-result resume does not pretend a new prompt was sent. The digest
does **not** cover repository instruction files, arbitrary tool results, the
entire provider request, or cache contents. No prompt text is logged.

Stable-first rendering removes avoidable prefix changes; it does not guarantee
cache hits. Native harnesses control the rendered messages, tools, output
schemas, routing, and provider caching. In particular, shared text alone does
not ensure an eligible reusable prefix, and switching models or configurations
can prevent reuse. Runner does not set API-only cache keys, retention policies,
or breakpoints through unsupported CLI flags. See OpenAI's
[prompt-caching guide](https://developers.openai.com/api/docs/guides/prompt-caching).

Compare total reported usage, cost when available, harness time, and rejection
counts for completed cards including all attempts, grouped by model/reasoning
and prompt context. Cache counters alone are not a success metric, and missing
provider counters are not evidence of zero caching. A changed prefix may lose
an existing cache entry; correctness, useful context, and isolation take
precedence over retaining stale guidance. Live cache/cost gains need a measured
project trial; local prompt tests do not establish them.

### Repository workspace

`project_dir` is the source checkout for the one configured
`intake_repository`. Its configured GitHub remote must identify that same
repository whenever `doctor` or Runner performs repository work. The checkout
does not have to be clean: Runner fingerprints and leaves its tracked and
untracked files untouched. Each implementation item owns one deterministic
task branch and worktree outside that checkout. Agent QA reads a private detached
checkout of the exact candidate; dynamic checks use a separate disposable source
copy. The implementation worktree is retained until publication. Runner then
removes the local worktree while retaining the branch. Patch handoff files and
manifests are not generated; the branch and GitHub pull request are the recovery
path.

### Retained review evidence

By default QA receives committed source and candidate-bound structured evidence,
not ignored implementation reports. To expose retained receipts, reports, or
screenshots, select explicit worktree-relative files or subdirectories:

```json
"review_evidence_paths": ["test-results/runner-review-evidence"]
```

Select only evidence meant for review: every file in a selected directory is
included, including ignored files. Do not select credentials, dependencies, or
unrelated private material. Paths are literal (no globs), cannot overlap, escape
the worktree, name the whole worktree, or include `.git`. Runner rejects symlinks,
hard links, special files, unsafe file ownership/permissions, concurrent capture
changes, and snapshots exceeding the configured `resource_limits` entry/per-file/
total-byte limits. Missing selected paths are recorded in the manifest.

Implementers receive these destinations in their task prompt and are asked to
retain minimal receipts, candidate manifests, setup outcomes and a concise
applicability index as work progresses. Prefer a dedicated directory already
covered by the repository's ignored-artifact convention; do not select the whole
reports tree. The prompt also states the effective implementation runtime budget
and asks the implementer to reserve time for required final verification. This
does not extend the timeout or relax a gate. Configuration does not reconstruct
old reports: when recovering an existing candidate, explicitly collect its
retained proof without altering results or claiming unobserved checks succeeded.

Before QA, Runner copies the selection into a private read-only evidence bundle
outside both the candidate and writable verification copy. The manifest lists
original relative paths, captured hashes, missing inputs, and the candidate
commit/tree. Reports keep their original bytes: reviewers map references through
the manifest, inspect provenance and applicability, and cannot follow arbitrary
external report paths. This is not a claim that historical checks cover the
current candidate, nor permission to execute bundled tools or bypass validation.
The review copy is removed with the private review checkout. For plan members,
Runner also preserves the exact sealed snapshot in private publication state
before review; an accepted review binds its digest to the immutable acceptance.
Whole-plan QA receives these snapshots in separate member namespaces alongside
parent evidence, under one aggregate size/entry limit. It sees original execution
identities and results, not freshly executed checks. Applicability to the combined
candidate remains a review obligation. Explicit amendment carry-forward preserves
unaffected evidence; changed or retired members cannot supply current authority.
Existing implementation reports remain untouched. Codex and Claude enforce
read-only grants; Pi still requires explicitly trusted host access.

If required proof is unavailable and no permitted check can establish it, QA
reports `blocked` / `review_incomplete` without consuming a rejection or routing
to implementation. Restore the missing input/access and use ordinary `retry`.
An observed defect or established skipped required gate still counts as a failure.

### Repository references

`repository_references` optionally exposes existing secondary Git checkouts to
planner, implementer, and reviewer contracts as evidence:

```json
"repository_references": [
  {
    "name": "legacy-frontend",
    "path": "/absolute/path/to/legacy-frontend",
    "commit": "714128eaeb8e3805431f8fdeaa49a570e2830cea"
  }
]
```

Each name must be unique, `project_dir` and each reference path must be
absolute, and each commit must be a full 40- or 64-character hexadecimal Git
object ID. Normal `doctor` and every eligible harness launch resolve symlinks
and require the path to be the exact root of an existing Git checkout with that
`HEAD` and no tracked or untracked
changes. References may not overlap the primary repository, any configured
worktree root, or another reference. If a checkout changes after doctor,
Runner rejects the launch before invoking the model.

The checkout's `.git` metadata must be a directory contained inside that root.
Linked worktrees and repositories created with an external Git directory are
not accepted, because safely using them would expose files outside the declared
reference. Use a standalone clone for a reference instead.

Runner treats these checkouts as operator-managed inputs. It never clones,
fetches, checks out, resets, cleans, or updates them. Inspect and repair one
manually, then update its configured pin when intended:

```bash
git -C /absolute/path/to/legacy-frontend status --short
git -C /absolute/path/to/legacy-frontend rev-parse HEAD
```

Planner, implementer, and reviewer contracts receive every configured reference;
custom roles inherit the behavior of their base contract. Probe
contracts never receive references, and there is intentionally no per-role
reference list. Sandboxed Codex and Claude receive explicit read access while
write access is denied. Claude is also prevented from loading project
instructions from an added reference directory. Pi roles using references must
select `access: "host"`, because Pi cannot enforce a read-only reference root.
Host mode remains unrestricted and may see more than the configured list.

Implementer launches validate references on every attempt, including retries,
and permit source inspection while retaining write access only to the assigned
worktree and allowed runtime/cache locations under sandboxed execution. No
configuration migration is needed when upgrading to implementer reference
support. Existing cards can retain obsolete instructions denying this access.
`retry --feedback` can correct stale operational feedback, but does not amend
approved requirements. Use the explicit requirement amendment described below
when the card itself must change. Do not hand-edit authenticated planning
metadata. Runner embeds the updated role skill in each launch; `init` refreshes
installed skill copies when needed.

Reference files are labeled as untrusted evidence, not instructions or Runner
authority. The entire root is readable, including ignored files, because Git
cleanliness does not report them. Use a dedicated checkout containing no
credentials, environment files, or unrelated private material. This boundary
is designed for pinned supporting source, not secret-bearing working copies.

## Adding human work

The enqueue-only command has two explicit destinations:

```bash
cortexium-runner add plan \
  --config /absolute/operator/path/runner.json \
  --title "Plan CSV export" \
  --body-file export-goal.md

cortexium-runner add ready \
  --config /absolute/operator/path/runner.json \
  --title "Fix the CSV header" \
  --body-file fix-card.md
```

Use `--body TEXT` for a short inline body. A title and a nonempty body are
required. `--dry-run` loads and validates the trusted config, then prints the
resolved Project and lane without changing GitHub. For Ready it also displays the
resolved starting implementation profile, harness, model, and reasoning. With no
selection, the current configured default and existing escalation policy apply.

Use `add ready --profile ID` to select an existing allowed implementation profile
without changing configuration. It is checked against the same allowlist used
at execution and saved in the visible `## Runner implementation profile` body
section as one exact ID. Ordinary manually created Ready cards may use that same
section; the exact body and selection are covered by normal card authorization.
Malformed, duplicate, conflicting, or unavailable selections are refused, never
silently substituted. `--profile` is not a planner option and cannot rewrite
the authenticated profile of an existing planned card. Requirement-only
amendments also cannot change this execution selection.

`add plan` creates one unsigned Project draft in the configured planner lane.
The running event loop converts it to an issue, authenticates the exact observed
snapshot, and asks the planner to stage a dependency-aware proposal for human
review. `add ready` does the same in the implementer intake lane, where the
status event directly authorizes implementation once declared dependencies have
succeeded and resources are available. Neither command takes the Runner process
lock, so a maintainer can add work while continuous mode is active. If creating
the draft succeeds but setting its status fails, the command reports the item ID
for manual recovery; the unscheduled draft cannot be claimed.

The separate [`plan` command](#immediate-planning-from-the-cli) runs an immediate
operator-controlled planning call and can preview or stage its returned batch.
Use `add plan` when the ordinary event-and-action workflow should own the work.

Humans can also create an issue in the configured intake repository and apply
the configured intake label. The normal synchronization adds it to `Needs
assessment`. With autonomous issue intake enabled, a private-repository issue
or an issue from an allowlisted public author enters `Plan` automatically. The
planner either stages one or more dependency-aware cards or posts its open
questions to the issue and moves the source to `Blocked`. Reply on the issue and
run the recorded `cortexium-runner retry --item ...` command when the missing
decision is resolved so the card returns to its planner lane. Moving it to
`Ready` instead deliberately skips planning and authorizes implementation.

## Generated Kanban workflow

The board shows `Runner Activity` and `QA Failures` on cards by default. Runner
updates both through the same authenticated lifecycle transitions as Status;
manual changes invalidate the approved action and return the card for human
assessment rather than allowing stale state to run. Agent activity is replaced
by the appropriate waiting activity when accepted QA reaches `PR Ready`, then
cleared when the card leaves that lane. The hidden phase retains recovery state
and the hidden transition lock is set only while Runner commits a multi-field
update.

| Lane | Owner and meaning |
| --- | --- |
| `Needs assessment` | Intake awaiting human assessment, or a transient staging boundary. |
| `Backlog` | Human: approved request retained for later scheduling. |
| `Plan` | Planner agent: split one approved request into implementation cards. |
| `Ready` | Implementer agent: implement or revise one card. |
| `In Progress` | Runner: temporary lane while an agent owns the card. |
| `Agent QA` | Reviewer agent: evaluate the exact branch and worktree. |
| `PR Ready` | Pull request awaits human review (`Awaiting human review`) or automatic integration (`Waiting for integration slot`, `Waiting for CI`, or `Waiting for merge`). |
| `Blocked` | Human: input, a non-retryable execution error, exhausted provider/browser-startup retries, a closed-without-merge PR, or the maximum QA rejections were reached. |
| `Done` | Terminal success: planning completed or the pull request was merged. |

`Plan` and `Ready` are human scheduling boundaries for ordinary cards. When a
maintainer creates an unsigned card in either lane, Runner converts a Project
draft to an issue in the configured intake repository when necessary, then
authenticates that exact body, repository, and dependency snapshot for the
lane's rule-selected role before claiming it. A forged or content-modified
nonempty approval is never replaced, and a staged planner child cannot use this
path to bypass complete-batch release.
Moving a previously authenticated card back to `Ready` also authorizes its next
implementation attempt. A status-only move from `Blocked` preserves the prior
result and QA failure count as context, validates every other signed field, and
replaces the blocked authorization with one for `Ready`. Use the CLI `retry`
command instead when the card should return to its recorded planner,
implementer, or reviewer lane. Issue comments authored by the account currently
authenticated in `gh` and present at assignment time are included as bounded
historical context; other authors are ignored. Trusted comments added during an
active attempt apply to a later attempt.

An ordinary Ready card may declare dependencies in its body with exact Project
item IDs or GitHub issue URLs and no descriptive suffixes:

```markdown
## Dependencies

- https://github.com/owner/repository/issues/42
- PVTI_project_item_id
```

References may point across planner batches or to ordinary human-created cards,
but each target must be uniquely present in the same Project. Runner releases
the dependent card only after every target has a valid Runner signature for the
configured successful outcome. Moving a card to `Done` manually does not satisfy
that condition.

The lifecycle generated by `init` is:

1. An issue carrying `needs-assessment` is synchronized into
   `Needs assessment`. Runner never executes that lane directly. By default it
   waits for human approval. When `autonomous_issue_intake` is configured,
   Runner verifies the intake repository immediately before mutation and
   routes the issue to `Plan` only if that repository is private or its public
   author is explicitly allowlisted.
2. `approve` authenticates the exact request and planned role, removes the intake
   label, converts a Project draft to an issue in the configured intake
   repository when necessary, and moves the card to `Backlog`.
3. A human moves scheduled work to `Plan`, or creates it there directly. The
   planner stages the complete
   normalized child batch, unapproved, in `Needs assessment`. Every child
   carries the original request, project outcome,
   cross-cutting success criteria, constraints, and its local acceptance
   criteria, so later roles retain the complete product context. The planning
   card waits non-executable in `Needs assessment` with the `planner_approval`
   phase. For ordinary intake, a maintainer runs
   `approve --item PLANNING_SOURCE --dry-run` to review every exact child and
   destination, then reruns without `--dry-run`, reviews the refreshed exact
   batch, and explicitly chooses Yes to authorize and release the complete
   batch to `Ready` and complete the planning card. For autonomously trusted
   issue intake, Runner rechecks the source trust and performs this exact-batch
   release itself; it never approves a changed or incomplete child set.
   Approval converts every
   released child draft to an issue before it becomes executable, so the card
   has one durable human and agent conversation from its first implementation.
   If the planner
   reports
   unresolved decisions, it creates no cards, posts the questions to the source
   issue, and follows `needs_input` to `Blocked`. Issue discussion is included
   as bounded historical planning context on a human-authorized retry.
   Completing the planning card records that the batch was released; the source
   issue remains open until all exact children have merged successfully.
4. An implementer works in an isolated branch/worktree. Runner constructs the
   candidate commit before moving the same card to `Agent QA`. It retains the
   structured work and verification evidence privately and records a summarized
   outcome on the card.
   If implementation's staged candidate has unresolved conflicts or fails
   `git diff --cached --check`, Runner retains the worktree, clears the unusable
   saved result, and immediately gives the same implementer one corrective pass
   within the current action. The card stays in progress; no polling delay or
   QA rejection is involved. The implementer receives the correction and prior
   evidence, while Runner still owns staging, commits, and final validation.
   Both harness calls count toward usage. The `candidate_construct` stage retains
   each construction result, including a `candidate_validation` failure inside
   an attempt that later succeeds. Its duration includes the recovery guards;
   `automatic` is recorded only after those guards admit the corrective pass.
   A resumed saved candidate does not invent another construction stage.
   If candidate validation fails again,
   Runner records an actionable `candidate_validation` blocker without publishing
   file contents or paths. The recorded plain retry reruns implementation with
   that correction. Git identity and administration failures remain private
   workspace-integrity blockers and do not trigger automatic correction.
   Other errors or requests for human input move the card to `Blocked` and
   retain the intended retry lane, except recognized transient provider failures
   and pre-session browser startup timeouts, which receive bounded operational
   retries in the same role lane. Ctrl-C is different: Runner uses a fresh bounded
   context to verify the retained workspace and returns the card to the
   interrupted role lane for the next run.
5. QA reviews the candidate commit. Acceptance validates its
   immutable publication tuple, re-fetches the approved base, and pushes that
   exact accepted commit OID to its recorded full branch ref under sanitized
   Git configuration. If publication or its Project transition is interrupted,
   an exact retry resumes these deterministic operations from the retained
   tuple without invoking the reviewer again. Recognized transient network and
   GitHub 5xx failures are retried immediately within that publication action,
   up to three total attempts. Every attempt revalidates authority and safely
   reuses an already-pushed exact commit or an existing matching PR; rate-limit,
   authorization, validation, cancellation, and unknown failures still stop for
   operator recovery. Runner then creates or reuses a pull request with
   structured Agent QA results, stores that same commit in the `QA Commit`
   Project field, moves the card to `PR Ready`, and removes the local task
   worktree. The task branch is retained. `Runner Activity` shows `Awaiting
   human review` or the current automatic-integration wait until the card leaves
   `PR Ready`.
   With `github_project.auto_merge: true`, this queues the PR for the separate
   integration action. Reconciliation asks GitHub to merge only after the PR
   owns its repository/base integration resource and still matches the latest
   reviewed base. Runner never uses an admin bypass. Automatic merge is
   disabled by default.
6. A QA rejection moves the card back to `Ready` and increments its rejection
   counter. With the generated `max_qa_rejections` of 3, the third consecutive
   rejection moves the card to `Blocked`. After human attention, moving it to
   `Ready` retries through implementation; `cortexium-runner retry` can instead
   restore its recorded lane. Detailed actionable rejection evidence is stored
   in a private mode-`0600` local record bound to the card ID and
   approved-content digest, then supplied to the next implementation and
   subsequent reviewer until acceptance. For every
   executable card, Runner also posts a bounded readable, idempotent QA comment
   to its issue. The private feedback remains the authenticated
   implementation-to-QA channel if comment publication is temporarily
   unavailable. The authenticated operator may optionally comment to clarify or
   add requested work before the next `Ready` attempt.
7. A human may comment on the PR and move `PR Ready` back to `Ready`. Runner
   imports PR comments/reviews, resets the rejection counter, and restarts the
   implementation-to-QA loop in the same deterministic workspace, recreating it
   from the retained branch when necessary. A comment alone does not authorize
   rework.
8. At `PR Ready`, Runner checks that the PR still belongs to the configured
   repository, uses the persisted branch and base branch, and points at the
   exact commit accepted by QA. Any other head mutation restarts
   implementation and QA. With automatic merge enabled, one PR per
   repository/base owns integration. Runner lazily refreshes that candidate
   against the latest base; a clean update returns through its configured
   `updated` route (QA in new workflows), while conflicts require implementation
   and QA before reclaiming integration. Pre-QA and pre-publication clean
   refreshes return to the current reviewer lane. Historical evidence retains
   its original source commit/tree; QA checks applicability and requests missing
   current-candidate verification, including repository-required gates. No old
   acceptance is transferred to the refreshed candidate. With automatic
   merge disabled, Runner leaves base-update decisions to the human reviewer.
   Automatic branch refreshes preserve QA rejection counts and their associated
   implementer escalation. Only an explicit human retry/reset grants a new
   rejection budget; a changed base does not erase prior findings.
9. A merged pull request moves the card to `Done` and records an authenticated
   successful outcome, regardless of whether GitHub records a person, app,
   merge queue, or other automation as the actor. A pull request closed without
   merge moves the card to `Blocked` with a reviewer retry phase and does not
   release dependent work. Runner refreshes Project state immediately so newly
   unblocked work can start without waiting for the next polling interval.
   A terminal PR state always wins over concurrent base-refresh detection, and
   Runner does not refresh a task branch that is already contained in the base.
   When automatic merge is enabled, GitHub performs the merge; Runner still
   moves the card only after observing the merged PR state. Runner then closes
   that implementation issue with the `completed` reason. For work decomposed
   from a source issue, it closes the source only when every exact child in the
   authenticated released batch has a merged-PR outcome. It does not emit PR
   closing keywords because any child may merge first. A closure failure is a
   warning retried by later reconciliation and does not hold unrelated work.

For the selected automatic integration candidate,
`pull_request.out_of_date` means its head does not contain the latest
target-branch commit. Runner first checks remote refs without recreating the
task worktree. When an update is necessary, it recreates the deterministic
workspace and attempts a normal, non-force branch update. A conflict is
retained in the isolated worktree and returns the card to `Ready`. A clean
update remains local and also returns to `Ready`, so its resulting tree
completes the normal implementation, integrity, and QA path before a
replacement immutable tuple can be published. Other open PRs are checked only
when they later acquire integration; manual-review PRs are never eagerly
refreshed. Direct PR-head changes invalidate prior QA. Refresh errors go to
`Blocked`.

## Public issue authority

Preview the exact approval, then approve it:

```bash
./cortexium-runner approve \
  --config /absolute/operator/path/runner.json \
  --item ISSUE_URL \
  --dry-run

./cortexium-runner approve \
  --config /absolute/operator/path/runner.json \
  --item ISSUE_URL
```

Ordinary item approval writes a `v2` operator-authenticated assertion to
`Runner Approval`.
`approve --json` is always a read-only machine-readable preview; releasing an
approval requires the normal operator-facing command, which displays the exact
authorization before mutation.
The assertion is signed with a Runner-local key stored under the private Runner
state directory; Project writers never receive that key. It binds the Project,
item identity, exact content, repository, dependencies, role, planning metadata,
result history, phase, branch, pull request, and commit snapshots that can steer
execution or cleanup. Moving a card into an incompatible lane or editing a
bound value does not grant authority: Runner returns it to `Needs assessment`.
The assertion and staged-batch authority also cover one stable delegated-content
digest derived from the exact approved body snapshot, repository, dependency
item IDs, and planning provenance. Before implementation or review, Runner
refreshes Project-backed content and requires the same digest. Assignments carry
that approved snapshot and digest; an issue URL remains provenance and is not an
independent harness context reference. Mutable content mismatches return to
assessment before a harness is invoked, while title-only changes do not alter
the delegated-content identity.
Harness prompts distinguish current validated Runner authority from historical
planning provenance. Planner-generated goals and acceptance conditions describe
work once approved, rather than embedding permanent "planning-only" or
"unapproved" instructions. Original requests remain intact for traceability;
substantive restrictions, prerequisites, and later operational pauses still
apply. Runner does not strip restrictions or silently edit signed card bodies.
Implementation workspaces have a separate private identity record outside the
mutable worktree. That record binds the Project item ID, delegated-content
digest, full approved base ref and exact resolved base commit, repository,
branch, and worktree path. An
unchanged retry reuses the same registered workspace. At implementation start,
an owned worktree or retained branch with a different identity is moved and
renamed under the workspace root's `.runner-quarantine` area before Runner
creates a clean branch from the newly resolved base. Uncommitted files remain
inspectable there, collision checks prevent an earlier quarantine from being
overwritten, and a path not registered to the configured repository is never
moved or removed.

On macOS and Linux, the workspace-write root is a private directory owned by
Runner's effective user with mode `0700`. Runner creates missing components
with that mode and refuses an existing root that is a symlink, is not a
directory, has another owner, or grants group or world access. It traverses
ancestors without following symlinks and rejects directories another local user
could replace; root-owned system ancestors and sticky temporary directories are
allowed. Preparation, reuse, private identity access, quarantine, and cleanup
revalidate this boundary. This protects against filesystem-object substitution
by other local users, but it is not isolation from processes running as the
same account. Other operating systems have no equivalent workspace guarantee.

QA, PR refresh, publication, and cleanup do not perform that replacement. They
fail closed when the current approved content or resolved base does not match
the private record, preserve the old work, and route the item back for safe
implementation or human recovery. The operator should inspect or archive the
quarantine, then retry only after confirming the approved snapshot and base are
the intended inputs. Runner never automatically deletes a quarantine or treats
a legacy branch without an identity record as compatible.

Before transition to Agent QA, Runner constructs a clean committed candidate
with pinned linked-worktree administration, index, and object paths. Hooks,
signing, replacement objects, filters, fsmonitor, inherited Git selectors, and
external Git configuration are disabled at that privileged boundary. QA's
private pre-review checkout is created afterward in a new mode-`0700` detached
worktree outside the implementation sandbox and records the candidate HEAD and
tree. If that private checkout, the retained implementation worktree, and the
active checkout remain unchanged after acceptance, Runner writes an
exclusive private publication record keyed by the commit and binding the
approved item/content, commit/tree, base ref/OID, repository, and full branch
ref. An exact retry reuses that tuple. If a CI-only retry recreates the worktree
without changing the candidate or its approved identity, the new workspace
fingerprint requires fresh QA. A successful review receives a separate immutable
snapshot-specific record and can reuse the same commit and PR; the original
acceptance is preserved. Changed approval, destination, base, or candidate
bindings and malformed records still fail closed. The retained-acceptance error
states that no reviewer ran; inspect the local acceptance state rather than
treating it as a QA rejection or repeatedly retrying an unresolved blocker.

On macOS and Linux, repository snapshots use the same no-follow secure-filesystem
boundary as workspace roots and `.gitignore` updates. The fingerprint includes
literal index entries and flags, the current worktree identity, common and
enabled per-worktree config, ignored or concealed `.gitignore`, `.gitattributes`,
and `.gitmodules` files, repository-local ignore and attribute files,
sparse-checkout state, alternates, graft and replacement metadata, and every
default hook name that can affect Git operations. Reads are descriptor-relative;
file type, identity, content, and symlink-target replacement are rejected rather
than certified.

The task checkpoint keeps the complete fingerprint. For the active checkout and
the before/after QA boundary, Runner excludes only `branch.*` entries from the
shared local config. Unrelated branch publication and maintenance legitimately
change those entries, so they are not evidence that card content changed. Every
other local Git setting, including hooks and other security-relevant controls,
remains part of the comparison.

Every indexed gitlink contributes its path and recorded commit. An initialized
submodule additionally contributes its own HEAD, index flags, status, protected
controls, and recursively initialized nested submodules without fetching or
initializing anything. Uninitialized submodules remain explicit only while
their pinned worktree directories are empty; concealed entries fail closed.
Missing, symlinked, or otherwise unsafe indexed submodule paths are also
rejected. Agent QA captures its private candidate checkout, the retained
implementation worktree, and the active checkout before and after review; a
mismatch stops the workflow before Runner commits,
pushes, creates a pull request, or removes the recoverable worktree.

Snapshot traversal, per-payload bytes, and aggregate bytes are bounded before
collection or read growth. Git and GitHub command capture, Project collection
and pagination, pull-request feedback, and public-intake mutation fan-out also
fail closed at fixed operational caps. Public intake runs after recovery,
pull-request reconciliation, and admitted execution. In continuous mode an
intake-local failure neither cancels in-flight work nor discards its eventual
result. Privileged candidate
construction and accepted-tuple publication pin Git administration and object
paths, scrub inherited selectors and effective configuration, disable hooks,
signing, and filters, and use a literal GitHub URL with an explicit
commit-OID-to-full-ref refspec. Ordinary snapshots still observe repository
controls rather than bypassing them.

Runner can replace result/report text as part of a fail-closed transition and
authenticates the replacement before making the destination lane executable.
Planner-agent staging uses a separate authenticated batch marker in the same
field. It binds the exact source, ordered child identities and content, source
lane, destination, batch size, and a fresh staging generation. Successful
complete-batch approval replaces it with an authenticated release commit only
after every child has received valid authority and reached its destination.

The Project no longer needs a role field. A typed `lane.entered` rule may run
one configured role profile in that lane, and that profile selects one harness
plus one or more skills. Different lanes may run different profiles that inherit
the same planner, implementer, or reviewer contract. Project cards do not
rewrite local Runner or harness configuration; their approved content is passed
to the selected harness as the assignment.

GitHub Projects do not provide an atomic cross-machine claim. An operating
system lock prevents two local processes from running the same Project on one
machine. Distributed claiming remains unsupported. The explicit
`max_parallelism` setting controls independent attempts inside one process and
must be between 1 and 16. Interactive `init` offers 1, 2, or 4, with 1 selected
as the safest first-run value; scripts can choose any supported value with
`--max-parallelism`. Continuous mode replenishes available capacity as actions
finish and keeps reconciling unrelated pull requests while capacity is full.
When a slot opens, the coordinator prefers eligible review work to shorten the
time to finish existing cards. After two admitted reviews, an eligible,
resource-safe non-review action gets preference. Project order is preserved
within each class; skipped or failed claims do not consume a turn. This bounded
preference is local to the running coordinator, not a lane quota, preemption,
extra capacity, or a durable cross-process ordering guarantee.
Planner output must use existing task titles for dependencies and cannot
contain cycles. Runner schedules and then rechecks a claim only when every
declared dependency is uniquely present with a
Runner-authenticated successful outcome. Dependency references may cross
planner batches; a manual status move cannot forge success.
Dependencies represent unfinished prerequisites, not a way to serialize cards
that might edit the same files. Before claiming harness work, Runner reserves
the immutable Project item. Implementer and reviewer actions also reserve the
exact repository/branch identity produced by the workspace subsystem. Planner
actions need only their item. The selector skips conflicting candidates and
continues through the current Project snapshot until it fills the available
global capacity or runs out of safe work. This allows QA and implementation on
different task branches to run together while preventing two actions—or PR
reconciliation—from using the same branch concurrently. Repository/base
integration serialization remains the next slice in
[ADR 0001](decisions/0001-event-action-runner.md).

An optional `admission_budget` limits the start of new agent attempts over a
rolling window using that local metrics history. It is an admission ceiling,
not an in-flight cancellation or per-call hard limit: attempts already running
may finish above a ceiling. When the window is exhausted, Runner continues PR
reconciliation but pauses every new agent claim, including Agent QA; it never
skips QA to save budget. `status` reports the reason and next rolling-window
evaluation. For example:

```json
"admission_budget": {
  "window_seconds": 86400,
  "max_attempts": 12,
  "max_harness_seconds": 28800
}
```

The available ceilings are attempts, completed harness seconds,
harness-reported tokens, and harness-reported USD cost. Token and cost ceilings
fail closed if an attempt in the window is unfinished or lacks the corresponding
harness-reported counter; harness-time ceilings likewise fail closed for an
unfinished attempt. Unresolved token units block a token-total ceiling, but do
not themselves block independent attempt, harness-time or complete cost
ceilings. Invalid usage/history still fails closed. Runner also fails closed before the next agent call if
the configured history contains malformed records or an admission reservation
cannot be written. Configure ceilings during first-time setup with
`--admission-window` plus one or more of `--max-admission-attempts`,
`--max-admission-harness-time`, `--max-admission-tokens`, and
`--max-admission-cost-usd`. Removing the history file also removes the evidence
used by a rolling budget.

Repository snapshot scale is the only resource limit intended for operator
tuning. Existing version-2 configurations that omit `resource_limits` use
100,000 directory entries, 64 MiB for any individual regular-file or symlink
payload, and 1 GiB of aggregate snapshot content:

```json
"resource_limits": {
  "snapshot_max_entries": 100000,
  "snapshot_max_file_bytes": 67108864,
  "snapshot_max_total_bytes": 1073741824
}
```

All values must be positive, and the aggregate limit must be at least the
individual-file limit. A snapshot limit failure names the limit and safe path
or count context without including file content. Reduce the repository scale
or raise only the necessary snapshot value, then retry the interrupted item;
Runner does not start or publish work from a partial snapshot.

## Immediate planning from the CLI

This operator utility invokes the planner immediately and is distinct from
`add plan`, which only creates a `Plan` event for the normal running coordinator.

Both paths inspect a fresh private detached checkout of the configured destination
branch, not the saved checkout's possibly stale or unfinished contents. Runner
fetches and pins the commit/tree, then supplies that same source identity to the
repository-aware outline and tool-free details stages. Your checkout, local
branches, index and uncommitted work are not moved or rewritten. Fetch or snapshot
validation failure stops planning; it never falls back to older local code.

An outline with unresolved `open_decisions` stops before the details call.
Details may identify new conflicts through the same field. Either result retains
the outcome, constraints and questions with no executable work items, and cannot
stage or release cards. The planner preserves exact requested constraints and
approved tradeoffs, including verification timing and environment; incompatible
requirements need a human choice rather than a silently weakened requirement.

You do not need to stop the background Runner to preview, stage, create, or
approve a standalone plan. Use the same operator configuration as the service.
Only another standalone `plan` command holds the planning lock; the worker keeps
its separate lifetime lock, PID, and runtime status throughout.

The planner invocation shares `max_parallelism` and any configured rolling
admission budget with background assignments. If all slots are occupied, the CLI
reports capacity exhaustion without creating cards or starting a paid call;
retry when capacity is available, or use `add plan` for queued planning. A short
admission handoff may wait, but a model call or human approval prompt never holds
that shared gate. Staging and release briefly exclude interrupted-state recovery,
so an in-progress batch is not mistaken for a crashed operation. Unapproved
staged cards still cannot execute.

Install this build and restart the service once before using concurrent planning;
both processes must use the updated coordination and the same configuration.
This locking change requires no new configuration or skill settings.
Schema/configuration maintenance retains its existing restart requirements.

Start an interactive multiline idea using the auto-detected project-local config:

```bash
./cortexium-runner plan
```

Runner asks for the project idea, constraints, and acceptance criteria. Enter
as many lines as needed, including blank lines, then press Ctrl-D at the empty
input prompt. Runner previews the complete plan and then asks, with a Yes/No
menu, whether to create and approve the proposed cards in the configured GitHub
Project. Choosing Yes authorizes Runner to stage the whole batch unapproved,
reload and revalidate it, and release it to its configured work lane. Choosing
No leaves GitHub unchanged.

For scripts, the planner accepts an inline idea, a file, or piped standard
input. These non-interactive forms remain preview-only unless `--create` or
`--stage-only` is explicitly supplied. A file-based preview looks like this:

```bash
./cortexium-runner plan \
  --config /absolute/operator/path/runner.json \
  --idea-file project-idea.md \
  --small-tasks
```

`--small-tasks` overrides both downstream roles for this planning call only; it
does not edit the saved configuration. Smaller-task planning asks
for one primary independently verifiable behavior per card, splits independent
acceptance clusters, and scopes each card with substantial margin inside the
configured implementer timeout. Runner never infers this choice from a harness
or model name.

Create, revalidate, and approve the proposed implementation cards:

```bash
./cortexium-runner plan \
  --config /absolute/operator/path/runner.json \
  --idea-file project-idea.md \
  --create
```

The cards are released together to the configured work lane. To leave them
unapproved in `Needs assessment` for a separate review, use `--stage-only`
instead. The result prints a compact receipt and a fingerprint-bound command:

```bash
./cortexium-runner plan --config /absolute/operator/path/runner.json --approve-staged v1:BATCH_FINGERPRINT
```

With `--json`, preview-only planning writes the plan directly, including its
original `source_context`, `planning_source` (repository, destination branch,
inspected commit and tree), and `target` (Project owner/number, repository, base
branch and destination). Retain this JSON privately; it contains the original
request and may contain sensitive project context. Successful
`--stage-only` writes `{ "plan": ..., "staged": ... }`; successful `--create`
writes `{ "plan": ..., "released": ... }`. If planning completes but staging
fails, stdout still contains one valid JSON object with the complete `plan`
(including `open_decisions`) and an `error` string. The command also reports
the error on stderr and exits nonzero; scripts should retain stdout on failure.
An error response is not a staging or approval receipt: a GitHub failure may
have left partial unapproved cards. Runner does not automatically rerun the
planner or add a persistent plan store for this CLI output recovery.

Save a generated proposal, then stage it without another planner call:

```bash
umask 077
./cortexium-runner plan --config /absolute/operator/path/runner.json --idea-file project-idea.md --json > proposal.json
./cortexium-runner plan --config /absolute/operator/path/runner.json --plan-file proposal.json --stage-only --json > staging-result.json
```

`--plan-file` also accepts the whole JSON staging result or error response;
only its `plan` is imported. Existing `staged`, `released`, and `error` fields
grant no authority. Omit `--stage-only` to inspect the saved proposal without
changing GitHub. Imported proposals cannot use `--create`, idea flags,
`--small-tasks`, or `--approve-staged` in the same command. Approve the complete
staged batch separately with its displayed `--approve-staged` command.

The saved file must be regular JSON no larger than 2 MiB. Runner rejects missing
or mismatched target/context metadata, unknown plan fields, invalid dependencies,
foreign repositories, and unavailable execution profiles. JSON from older
versions without replay/source metadata is not importable. Before staging, Runner
freshly checks the recorded source against the destination. If it changed, the
retained proposal cannot stage. Runner does not silently change its provenance or
rerun a model. Preserve the original proposal rather than rewriting its source
identity to make an outdated inspection look current. This version has no retained-
proposal revalidation operation for a new commit; a new planning run needs an
explicit request, and any existing partial batch must be resolved first.
Open decisions still
prevent every card creation. Either answer them in the idea and rerun the same
planning command, or explicitly review and amend the saved proposal: update
`source_context` with the answers, update affected cards and proof obligations,
and remove only resolved `open_decisions`. A conflict-only result has no executable
cards; rerun planning with the answers or supply the complete reviewed card
contracts before staging. Then stage it for fresh approval.
Do not overwrite your sole saved proposal with the output of its own replay.

Generated cards contain the original request, project outcome, project-wide
success criteria and constraints, a local objective, acceptance criteria, proof
obligations, selected assumptions and risks, repository, dependencies, and
planning-batch identity. Proof obligations describe what evidence must
establish; the implementer selects the method. Non-interactive `--create` releases its exact
complete batch after revalidation. `--stage-only` and automated planner children
remain unapproved in assessment until the operator previews and accepts the
exact complete batch. The accepted children then receive Runner-authenticated
implementer authority.
Planner output has an emergency ceiling of 1,000 children solely to bound
pathological model output and GitHub staging loops. It is not a recommended or
expected project size. Each child is sized for one configured implementer
invocation and ends at a natural review boundary rather than an arbitrary file,
layer, or task-count limit. The project
contract guides each task without making an early slice responsible for declared
later dependencies. Runner stages the entire batch before any child approval,
then revalidates the exact preview and planning source during the explicit
operator approval. Interactive direct planning uses the displayed plan and one
Yes as authorization, then revalidates the staged batch before release. Explicit
`--create` performs the same release for scripts; `--stage-only` batches require
the fingerprint-bound interactive `--approve-staged` action. Project
planning-source batches use an interactive,
default-No `approve --item` confirmation after the refreshed complete preview.
Before accepting interactive project text or launching another planner, Runner
checks for an earlier unapproved direct-planning batch. A complete batch must be
reviewed with the displayed `plan --approve-staged` command. An incomplete batch
is reported with its exact Project item IDs. Resume it using the original saved
JSON with `--plan-file` and `--stage-only`; matching children, dependencies and
batch identity are reused. A changed proposal, changed child, unrelated pending
batch or partially released batch is refused before new writes. If the original
JSON is unavailable or the proposal needs changes after partial staging, review
and remove that unapproved batch before replanning or staging the revised one;
Runner never silently combines or deletes it. Replaying a complete unapproved
batch creates no duplicate cards and does not release it. The destination lane
determines the role after separate approval.

## Delivery rollout, cancellation and contract/membership amendments

Plan delivery is default off. It applies only to explicitly approved new plans;
standalone Ready cards retain the individual-card path, and historical Done
planning parents are not relabeled as delivered. Before rollout, finish existing
active batches, independently review the configured maintained complete gate and
its input/dependency/runtime selections, then preview:

```bash
cortexium-runner delivery migrate --config /absolute/operator/path/runner.json --entrypoint complete --dry-run
cortexium-runner delivery migrate --config /absolute/operator/path/runner.json --entrypoint complete --json
```

Both forms are read-only. The preview binds the exact configuration and Project
snapshot and shows the complete existing catalog entry. Migration can add only
the exact `Runner Plan Release` TEXT field and enable `plan_delivery` with that
catalog ID. It does not create a verification command, grant host access, change
models/limits, alter cards or reinterpret historical planning completion. A
same-named conflicting or duplicate field is refused, not repaired implicitly.

The preview reports `preserved_legacy_done_items`: legacy planning records kept
unchanged because their complete current batch is Done and transition-free.
This is administrative quiescence, not renewed execution/dependency authority
or proof of delivery. Stale or missing old approvals and retained historical
phase/QA snapshots are not rewritten or matched retroactively to a PR head.
Declared batch size, unique indices, common provenance and any actual parent
must remain structurally consistent; missing members/parents, malformed metadata
or nonterminal work block migration. Archived records are not relabeled or
restored; a partially missing current batch cannot silently qualify. New plan
manifests/releases still require current authenticated contract and completion.
Apply revalidates the exact preview, including all retained record contents.

Gracefully stop the existing service and let assignments and their descendants
finish. Finish standalone planning, review and configured verification operations
too. Run the same command without `--dry-run` or `--json` in an interactive
terminal, review its fresh preview, and explicitly choose Yes (the default is
No). Piped confirmation is not accepted. Apply acquires the existing Project
worker/planning/mutation and per-operation guards, checks unresolved owned work,
then revalidates exactly what was previewed. It does not kill or drain anything.
No long-running verification holds the Project mutation lock.

The field is confirmed before configuration activation. Only `plan_delivery`
changes semantically, via conditional secure replacement; an exact private
`runner.json.before-plan-delivery-<digest>` backup is retained next to the actual
config filename. Intervening operator edits are preserved. If field creation
succeeds but later state checks fail, the inert field is retained and config
activation is refused. Inspect the reported state and review a fresh preview;
never delete a field to retry. An already matching field/config does not need
another mutation. No service starts: run normal Doctor and restore the original
service only after the complete rollout is ready. Do not blindly restore config
over active work or newly created plan authority.

To cancel an approved, unpublished delivery plan:

```bash
cortexium-runner delivery cancel --config /absolute/operator/path/runner.json --item PVTI_PARENT --dry-run
cortexium-runner delivery cancel --config /absolute/operator/path/runner.json --item PVTI_PARENT
```

The same quiescence and interactive exact-preview rules apply. Cancellation is
one signed parent transition to `plan_cancelled`: it fences new admission and
subsequent authority refreshes, preserving the immutable release, member cards,
branches, accepted work, historical feedback and rejection counts. It does not
delete, revert, retry or deliver anything. Status and cancellation preview still
inspect the retained plan; execution does not accept cancelled authority.

Any existing final PR—including one created before a lost Project response—must
be coordinated separately. Cancellation never closes a PR or undoes a merge.
Changed membership/authority, running or transition-locked items, and publication
races refuse apply. If a Project transition write is interrupted, leave work
stopped, inspect the exact parent and existing transition recovery state, and
resolve it before resuming; the command does not claim cancellation succeeded or
grant another retry. Reapplying a freshly inspected already-cancelled plan makes
no further Project writes. There is no implicit reopen.

An amendment changes the approved contract, not merely a card's
displayed text. Prepare JSON with `expected_revision`, a bounded `reason`, the
complete replacement `manifest`, and `member_bodies` keyed by the exact IDs whose
local contracts change. Copy the canonical manifest from the parent and increment
its `amendment` ordinal by one (absent means zero). Keep the original `request`,
repository and destination. Keep existing member rows in their original order;
append additions and explicitly retire removed scope as described below. Member bodies must retain
their canonical planning metadata and provenance; changed dependency/profile
metadata must match the replacement manifest and current allowed profiles.
`additional_affected_ids` can explicitly invalidate more members, never reduce
the required affected closure.

```bash
cortexium-runner delivery amend --config /absolute/operator/path/runner.json --item PVTI_PARENT --amendment-file amendment.json --dry-run
cortexium-runner delivery amend --config /absolute/operator/path/runner.json --item PVTI_PARENT --amendment-file amendment.json
```

The preview shows the exact before/after shared and changed local contracts,
affected IDs, and original acceptance identities retained for unaffected members.
It also shows the original body and resolved profile of each adopted issue, and
the retained branch/accepted commit/base and original accepted diff summary for
each retirement. That historical delta is not a promise that later changes left
every line intact, and it is not removed by retirement.
Apply uses the same graceful-quiescence, default-No interactive confirmation and
fresh preview checks as migration/cancellation. Local changes invalidate their
dependants over **both** old and new dependency graphs. Shared outcome, criteria,
scope, decisions or verification-policy changes conservatively affect every
member: the current manifest does not attest per-criterion ownership.

Affected members return to Ready for renewed acceptance; the parent returns to
active delivery and requires renewed combined review. Already integrated code is
never removed by a scope amendment. Repairs start from the current plan state.
Unaffected acceptance is carried forward under the new revision with its exact
original report, candidate, original-revision/digest and amendment provenance;
it is historical proof, not a fresh model execution. Old acceptance and superseded
checkpoints/proof remain private historical records. Results, feedback and rejection
counts are preserved. An unfinished implementation's spent correction/specialist
allowance or a spent verification classification with no durable result must be
explicitly resolved first; amendment does not reset either. Prior completed
parent verification progress is archived unchanged, not re-labelled as evidence
for the new revision. Repeating the exact completed amendment against unchanged
after-state is acknowledged without further writes; subsequent work or operator
changes are never overwritten to replay it.

Before the first Project write, Runner saves the approved before/after intent in
the existing protected parent evidence. A `plan_amending` fence prevents admission
while contract, workspace-binding and proof updates are partial. On restart the
coordinator completes only that exact recorded transaction before admitting work,
without repeating model calls or Git integration. Unexpected operator changes,
missing evidence or a newly published PR stop recovery; inspect them rather than
editing away the fence or deleting the protected record. Generic transition
recovery never reauthorizes a partial amendment.

Keep remote operator edits quiescent throughout apply/recovery. Runner checks
each freshly read item against the recorded before/after states before writing,
checks transition acquisition, and verifies write readback. GitHub does not offer
transactional compare-and-swap across these body and Project field writes:
an edit after the last check can still race a write, and a conflict may only be
detected after partial mutation. The fence and retained intent are recovery aids,
not an atomic remote transaction; inspect any conflict before resuming.

To add a member, append its exact existing issue-backed Project ID, canonical
dependencies, allowed implementation profile/digest and reason to `members`.
The issue must be open, unapproved and in Assessment, with no earlier planning,
execution or workspace authority. Its current body and dependencies are bound to
the preview; edit Assessment content first rather than using `member_bodies` to
silently replace it during adoption. Runner appends owned planning provenance,
but preserves existing members' original batch sizes and bodies. It does not
create or invent cards. Partly attached additions are recoverable only from the
exact protected intent; unknown extra members still fail closed.

To retire a member, keep its row and contract unchanged, set `retired: true` and
provide `retirement_reason`. Its code, worktree, accepted evidence, feedback and
counters remain. It is displayed as retired scope, never Done, and cannot satisfy
internal or external dependencies—even after the rest of the plan is delivered.
Amend any surviving dependent's contract explicitly in the same preview. Retired
rows cannot be deleted, reordered or reactivated; an entirely retired plan must
be cancelled instead. Cancelled/delivered plans and published final PRs cannot be
amended. Retargeting or unsafe combinations require a separately approved
corrective plan, not implicit work.

These controls do not complete the rollout by themselves. Production
historical-proof reuse and failed-complete-gate owner
classification remain separate required work before live activation. At this
stage a failed maintained complete gate blocks safely; it does not yet route a
repair automatically or authorize a speculative owner/card.

## Roles and harnesses

A harness defines only `command`, `enabled`, and an external
`workspace_write_root`. `command` accepts one executable name or path. Model,
reasoning, skills, and timeout belong to role definitions. Workspace class,
repository identity, mutation intent, and post-run verification come from the
Runner role profile. `roles.<role>.access` selects `sandboxed` (the default) or
explicit `host` access for planner, implementer, and reviewer contracts. Host
access removes OS containment and should be used only for trusted repositories
and machines, including when the role contract is otherwise read-oriented.

Runner validates every planner and agent result before changing workflow state.
Codex and Claude use their native schema-backed output controls. Each Codex
invocation receives a unique private mode-`0700` directory containing pinned
mode-`0600` result and schema files. Runner reads the result through the file
descriptor opened before launch and rejects path, identity, type, owner, or mode
changes. For Pi, the same temporary-artifact boundary holds an extension
containing only the required result schema. Runner requests provider-side strict
sampling when supported and accepts exactly one session-attributable,
identity-matched, terminating result-tool start/end pair whose returned details
match its arguments and carry the invocation-bound extension provenance.
Runner disables extension discovery and loads only this pinned result extension,
so installed or workspace extensions cannot observe or forge its provenance.
Progress events are discarded while streaming so long runs stay bounded and do
not retain prompts. Raw JSON and model-authored lookalike events are not Pi
result channels. The artifacts are removed after the invocation without
following a substituted path. After a successful harness run, Runner applies
one deterministic local representation policy before strict decoding: it may
unwrap one whole-response JSON object fence and remove the exact stray
top-level JSON Schema residue `"type":"object"`. Missing substantive fields,
all other unknown fields, malformed JSON, and semantic contract failures fail
closed without another model invocation. Runner never switches models or
retries invalid content inside the same attempt. The optional implementer
ladder described below acts only on a later implementation attempt after a
valid Agent QA `needs_changes` verdict.

```json
"harnesses": [
  {
    "kind": "codex",
    "command": "codex",
    "enabled": true,
    "workspace_write_root": "/absolute/path/outside/project/.runner-worktrees"
  }
]
```

A role defines agent-specific execution settings:

```json
"roles": {
  "reviewer": {
    "harness": "codex",
    "access": "sandboxed",
    "harness_config": "isolated",
    "skills": ["runner-reviewer"],
    "reasoning": "high",
    "task_granularity": "standard",
    "timeout_seconds": 3600
  }
}
```

New configurations give planner roles a 20-minute timeout, reviewer roles a
one-hour timeout, and implementer roles a two-hour timeout. These are safety
bounds rather than targets; edit a role's explicit `timeout_seconds` when a
known workload needs longer.
Changing a config does not alter a harness process that is already running.
For an explicitly long-running project:

```bash
./cortexium-runner role edit implementer --config /absolute/operator/path/runner.json --timeout 6h
```

`access` and `harness_config` are independent per-role policies:

| `access` | `harness_config` | Effect |
| --- | --- | --- |
| `sandboxed` | `isolated` | Safe default: Runner containment and suppressed ambient harness configuration |
| `sandboxed` | `inherit` | Native shell/filesystem sandbox remains, while ambient rules, tools, plugins, and MCP configuration load; supported by Codex and Claude |
| `host` | `isolated` | No OS containment, but Runner still suppresses ambient harness configuration and fixes its tool envelope |
| `host` | `inherit` | Unrestricted agent execution with the OS account's accessible files, processes, network, tools, and credentials |

Pi rejects `sandboxed` plus `inherit` because Pi cannot provide an OS boundary
around ambient tools. Runner's live readiness probe always remains
`sandboxed` plus `isolated`, regardless of the role being probed.
For Codex and Claude, inherited out-of-process MCP servers, plugins, hooks, and
extensions can have their own OS permissions outside the harness's shell
sandbox. Inspect those native definitions before enabling inheritance; use
`isolated` when the sandbox must also exclude ambient helper processes.

`execution_policy` is not a configuration field. Legacy init policy flags have
no effect. `doctor` reports every effective role as
`ROLE=ACCESS/HARNESS_CONFIG` and labels `host/inherit` as unrestricted. It also
rejects an installed CLI that does not advertise every flag required by the
selected mode.

`model` is optional; absence uses the harness's native default. `init` accepts
`--harness`, `--model`, and `--reasoning` as shared setup values. The
corresponding `--planner-*`, `--implementer-*`, and `--reviewer-*` flags override
the shared value for one role. Init also accepts shared
`--harness-config isolated|inherit` and the role-specific
`--planner-harness-config`, `--implementer-harness-config`, and
`--reviewer-harness-config` overrides. Per-role access is selected with
`--planner-access`, `--implementer-access`, and `--reviewer-access`. For an
existing config whose built-in roles should all use the harness as natively
configured, use:

```bash
cortexium-runner role edit --all --config /absolute/operator/path/runner.json --harness-config inherit
cortexium-runner role list --config /absolute/operator/path/runner.json
cortexium-runner doctor --config /absolute/operator/path/runner.json
```

The bulk edit changes the planner, implementer, and reviewer definitions in one
validated atomic config replacement. Custom roles inherit the changed built-in
policy unless they have an explicit `harness_config` override. Access modes are
not changed. Consequently, the edit fails without replacing the config if, for
example, a sandboxed Pi role would become `inherit`. Use an explicit per-role
edit such as `role edit implementer --access host --harness-config inherit`
only when unrestricted host access is intended. Changing a config never changes
an already-running harness process; restart Runner for later work to use the new
policy.
Implementer and reviewer roles also accept `task_granularity`: `standard` uses
the ordinary concise planning contract, while `small` asks the planner for
smaller coherent slices, explicit boundaries and assumptions, literal acceptance
criteria, and observable proof obligations. It never reduces correctness or
verification requirements and does not add fields to the plan schema. Change
it with `role edit ROLE --task-granularity standard|small`.

Codex roles accept `low`, `medium`, `high`, `xhigh`, or `max` reasoning; the
selected model and installed Codex must support the requested effort. Runner
passes it through unchanged and does not map `max` to `xhigh`. Claude and Pi
retain their existing effort contracts; `max` is not an alias for either.
No role default changes when upgrading.

When `harness` is `pi`, set
`roles.<role>.model` to the full `provider/model-id` string that Pi CLI
recognizes, for example:

```json
"roles": {
  "reviewer": {
    "harness": "pi",
    "access": "host",
    "harness_config": "isolated",
    "model": "provider/model-id",
    "preserve_reasoning": false,
    "skills": ["runner-reviewer"]
  }
}
```

Provider endpoints and credentials remain managed by Pi in its own
configuration. Runner only passes the configured model string through to Pi
CLI; it does not configure providers, credentials, or endpoints. Runner's
temporary Pi result extension selects its transport from the explicit model
provider. For `lmstudio/...` models, Pi performs ordinary tool work first, calls
an empty Runner finalizer, and receives the result schema through LM Studio's
native JSON response format on the following tool-free turn; Runner disables
reasoning for that final formatting turn. Runner sends the role's configured
reasoning effort on working turns. For LM Studio Qwen models,
`preserve_reasoning` controls Qwen's `preserve_thinking` request value and
defaults to `false`; it is an inherited per-role Pi setting rather than a shared
harness contract. Configure it with:

```bash
./cortexium-runner role edit implementer --preserve-reasoning
./cortexium-runner role edit implementer --no-preserve-reasoning
./cortexium-runner role edit implementer --clear-preserve-reasoning
```

The clear form inherits the parent setting, or the default `false` when none is
configured. The native final formatting request always uses
`preserve_thinking: false` with thinking disabled, regardless of the working-turn
setting. Other Pi providers retain their existing structured-result transport
and do not receive this LM Studio chat-template option. Runner therefore does
not depend on LM Studio's visible preset. Staged planner synthesis already
has complete Runner-validated context and no tools, so it receives the native
schema on its initial request and has no finalizer tool to call. Runner disables
extension discovery for the invocation without
rewriting installed extensions, skills, or provider configuration. Skill and
project-context discovery are disabled for the launched process; Pi's
configured provider and authentication remain available.

Pi defaults its HTTP idle timeout to five minutes. A local model can exceed
that while ingesting a long prompt even though the Runner role timeout is much
larger. Configure Pi's `httpIdleTimeoutMs` to a suitable bounded value (for
example `3600000` for one hour) when using slow local models; Runner's role
timeout remains the outer process bound.

Runner verifies installed copies of its bundled role skills, disables native
skill discovery for privileged launches, and injects only the pinned embedded
bundled instructions selected by the role. Custom local skill files are never
discovered as execution policy. `roles.<role>.skills` remains an explicit
operator selection inside the immutable ceiling. Custom roles inherit the planner,
implementer, or reviewer execution contract:

```json
"security_reviewer": {
  "extends": "reviewer",
  "skills": ["runner-reviewer"],
  "reasoning": "xhigh"
}
```

Built-in `planner`, `implementer`, and `reviewer` roles are protected execution
contracts. Use `role list` and `role show NAME` to inspect resolved profiles;
use `role add`, `role edit`, and `role remove` to manage config-backed custom
profiles or edit a built-in role after initialization. For example:

```bash
./cortexium-runner role add security_reviewer \
  --config /absolute/operator/path/runner.json \
  --extends reviewer \
  --skill runner-reviewer \
  --reasoning xhigh

./cortexium-runner role edit reviewer \
  --config /absolute/operator/path/runner.json \
  --reasoning xhigh \
  --task-granularity small
```

Base roles describe authority and result contracts, not specialties. Product
planning or issue triage remains a planner profile; UI, migration, and
documentation work remains an implementer profile; security, accessibility,
and performance review remains a reviewer profile. Assign a custom profile by
referencing its ID from a `run_role` workflow action. Multiple lanes may use
different profiles derived from the same contract, including sequential review
lanes. Publishing, merging, branch refresh, approval, and coordination remain
deterministic Runner actions or human policy rather than agent roles.

Overrides inherit from the parent when omitted. A built-in contract cannot be
removed, and a custom role cannot be removed while a workflow rule or another
role still references it. A role referenced by `implementer_ladder` likewise
cannot be removed until the ladder is changed or cleared.

### Planner-selected execution profiles

Profile selection is optional. A profile is an existing named implementer role
with its model, reasoning, permissions, timeout, and task granularity. Give it a
`description` explaining suitable tasks, then list allowed IDs in
`planner_implementers`, in preferred order. No new model or reasoning scale is
introduced. The planner suggests a profile and a short reason on each card;
approving the plan approves that choice with the rest of the card.

```bash
cortexium-runner role add mechanical --extends implementer \
  --model YOUR_SMALLER_MODEL --reasoning medium --task-granularity small \
  --description 'Prefer for bounded mechanical edits with existing examples and straightforward proof.'
cortexium-runner role edit implementer \
  --description 'General implementation and tasks with substantial design uncertainty.'
cortexium-runner role edit planner --implementer-profile mechanical --implementer-profile implementer
```

The corresponding top-level setting is
`"planner_implementers": ["mechanical", "implementer"]`. Inspect the resolved
roles with `role show NAME --json`. Use
`role edit planner --clear-implementer-profiles` to disable selection for future
plans. Empty selections retain the configured default. Without a ladder, a
selected profile stays fixed on retries. With `implementer_ladder`, every allowed
profile must be in that ladder; retries start at the selected entry and advance
toward its end. Removed or disallowed selections on already-approved cards block
execution until the operator restores the profile or replans and approves the
card. Config edits use the current resolved role for subsequent invocations.

Profile reasons consider contract clarity, applicable repository examples,
verification strength, and the consequence of mistakes—not file count or a
universal model ranking. See [Evaluating model profiles](model-profile-evaluation.md)
for an opt-in Codex comparison setup and end-to-end measurement guidance.

`task_granularity` replaces `planning_support`: migrate the old key and CLI flag
to `task_granularity` / `--task-granularity`, and replace its `high` value with
`small`. `standard` is unchanged. Old keys and values are rejected.

### QA follow-up scope

Rework receives every failed or blocked proof obligation, blocking repository
finding, and failed or blocked maintainability check, with its complete summary
and supporting evidence. Runner does not cut findings at a character limit or
silently omit findings after a fixed count. The private record and the rendered
feedback each have a 1 MiB safety limit; exceeding either pauses the handoff with
an explicit size-limit error, without replacing prior feedback. This is a
structured-result capacity failure, not evidence of workspace tampering.

When a valid full assessment is retained, Runner derives the handoff from that
assessment. This also recovers older clipped feedback, including the Unicode
cutoff failure, without rewriting the stored record or resetting QA rejections.
Malformed or inapplicable review baselines still cannot certify a candidate.
Public comment and terminal previews may remain shortened; they are not the
source of the implementer's complete QA handoff. No model or skill change is
needed for this behavior.

The first review gathers all concrete blockers reasonably visible within the
approved card. Runner saves rejected assessments privately with the reviewed
commit, base revision, repository, approved content, proof obligations, and the
bounded comment context seen by that review. With compatible, verifiable history,
the next review verifies those findings and the repair diff, reusing still-valid
passed conclusions. Runner also supplies the complete current comments and an
exact prior/current comparison. Operational coordination changes can retain
applicable conclusions; material additions, edits, and removals cause the existing
reviewer to reassess affected conclusions and expand only the affected scope.
Unchanged comments retain the established follow-up behavior. A prefix, claimed
author, or QA-like marker does not make a comment trusted or hide it from review.

Findings distinguish unresolved prior issues, repair regressions, concrete late
defects, and genuinely new or out-of-scope requirements without suppressing valid
blockers. A repository, approved-content, proof-obligation, or base mismatch;
missing or malformed baseline data; or an unavailable prior commit prevents reuse
and causes a renewed full review after the normal authority checks. Invalid
execution authority instead blocks execution; historical baseline evidence never
grants authority. Accepted review feedback is cleared. This policy works with one
model, equal reasoning settings, or no escalation ladder. Refresh the bundled
skills before starting Runner; Doctor checks their contents against the embedded
versions.

On rework, the implementer independently reassesses the requirements and current
diff, preserving sound work without assuming the earlier approach is correct.
Its `work_done` report describes whether that approach was preserved and improved,
partly replaced, or largely replaced, and why (or notes insufficient history).
This is qualitative evidence for later evaluation, not an automated reuse metric.

### Implementer ladder

`implementer_ladder` is optional. When omitted, Runner always launches the
`ready_lane` rule's configured implementer role unless an approved card selects
a planner-enabled profile (above). When present, it lists complete
implementer role profiles in escalation order. The first entry must be the
workflow implementer role; later entries must be unique custom roles that
inherit the implementer contract. The list needs at least two entries and
cannot exceed the reviewer action's `max_qa_rejections`, because a longer ladder
would contain unreachable profiles.

For example, create two stronger Codex profiles after a Pi/Qwen implementer,
then configure their order:

```bash
./cortexium-runner role add implementer_luna \
  --config /absolute/operator/path/runner.json \
  --extends implementer \
  --harness codex \
  --access sandboxed \
  --model gpt-5.6-luna

./cortexium-runner role add implementer_sol \
  --config /absolute/operator/path/runner.json \
  --extends implementer \
  --harness codex \
  --access sandboxed \
  --model gpt-5.6-sol

./cortexium-runner role edit implementer \
  --config /absolute/operator/path/runner.json \
  --next-implementer implementer_luna \
  --next-implementer implementer_sol
```

This writes the explicit full order:

```json
"implementer_ladder": [
  "implementer",
  "implementer_luna",
  "implementer_sol"
]
```

The initial implementation uses `implementer`. A valid Agent QA rejection
increments the Project's `QA Failures` value; the next implementation therefore
uses `implementer_luna`, then `implementer_sol`. If `max_qa_rejections` permits
further attempts after the last configured profile, Runner keeps using that last
profile. A two-profile ladder works the same way. Restarting Runner is stable
because selection depends only on the authenticated Project value, not process
memory.

Only a reviewer `needs_changes` verdict advances this ladder. Harness errors,
invalid structured results, timeouts, cancellations, missing input,
authentication, permissions, unavailable capabilities, invalid configuration,
and integrity failures remain blocked for explicit operator recovery. Runner
does not infer model size, quality, price, or ordering from model names. Every
rung is a normal separately measured attempt and remains subject to the rolling
admission budget.

Disable the ladder without removing its now-unused role profiles:

```bash
./cortexium-runner role edit implementer \
  --config /absolute/operator/path/runner.json \
  --clear-implementer-ladder
```

Every harness referenced by the ladder must already have an enabled harness
configuration and workspace-write root. Normal `doctor` and skill setup cover
all ladder profiles. `harness check` exercises every configured ladder role;
the smaller `doctor --probe-harnesses` groups identical harness, command, model,
and reasoning selections when only paid authentication and structured-output
validation is wanted.

The supported matrix is fail-closed:

| Harness | Planner | Implementer | Reviewer |
| --- | --- | --- | --- |
| Codex CLI | Sandboxed/isolated by default; host and/or inherited config opt-in | Sandboxed/isolated by default; host and/or inherited config opt-in | Sandboxed/isolated by default; host and/or inherited config opt-in |
| Claude Code | Sandboxed/isolated by default; host and/or inherited config opt-in | Sandboxed/isolated by default; host and/or inherited config opt-in | Sandboxed/isolated by default; host and/or inherited config opt-in |
| Pi CLI | Isolated fixed read tools by default; inherited config or repository references require host | Explicit host access; isolated fixed tools or inherited ambient config | Explicit host access; isolated fixed tools or inherited ambient config; repository references use that host boundary |

The probe profile exposes only model invocation and Runner's structured output
channel and always suppresses ambient configuration. Work roles use both
configured per-role policy dimensions.
Runner supplies a neutral reviewer workspace or isolated implementation
worktree and applies repository-integrity, candidate, QA, and publication checks
after every harness. Isolated mode suppresses ambient plugins, ungranted MCP
servers, skills, hooks, and project instructions. Inherited mode deliberately
loads them. Runner continues to pass unattended/non-interactive flags, its
bundled role instructions, structured-result contract, and explicit model and
reasoning selection in both modes.

### Browser-dependent verification

Browser rendering, console inspection, and interaction checks are optional
harness capabilities, not part of the basic structured-result adapter
contract. Sandboxed Codex and Claude implementers and reviewers receive
Runner's bounded development profile by default. It provides package commands
inside the native filesystem sandbox. Safe-tool implementers receive loopback
and the npm registry plus the fixed public Go module path through
`proxy.golang.org`, the `storage.googleapis.com` archive redirect, and
`sum.golang.org`; focused reviewer verification receives the same package hosts
only in its disposable source copy. Planners and audit-only reviewers receive
no package-download access. The profile also provides a pinned `runner_browser`
server restricted to loopback pages with external name resolution disabled.
The browser uses a temporary profile and mock keychain; it cannot attach to the
operator's normal browser profile. Its `npx` cwd and npm configuration are
temporary mode-`0700`
host-owned paths, while its reusable package cache remains under the same
private root; none are writable from the harness sandbox. Runner does
not download Chrome. Chrome or Chromium 149+ is required
because the pinned MCP server's URL allowlist uses browser enforcement added in
that release. Ordinary `doctor` reports Chrome as
an optional safe-tool capability; its absence does not make the project
unready unless `doctor_requirements` explicitly marks that browser capability
as required.

`doctor --probe-harnesses` proves authentication, model selection, invocation
flags, and structured output. It deliberately does not launch a browser and
therefore does not prove browser verification readiness. Before scheduling
browser-dependent work, run:

```bash
./cortexium-runner harness check \
  --config /absolute/operator/path/runner.json \
  --browser
```

This proves the configured browser path against Runner's temporary fixture.
The project's acceptance conditions must still cover its actual application
entrypoint and behavior; the conformance fixture is not a substitute for a
project-specific browser check.
Codex may expose the injected MCP operations as direct calls or through its
Code Mode tool catalog; Runner supports both callable surfaces and uses the
exact `navigate`, `evaluate`, and `screenshot` operation names.

Each harness invocation starts its own configured MCP connections. The
Runner-owned stdio browser server uses a fresh isolated profile per invocation;
it does not share browsing state across cards or between implementation and QA.
Its private npm package cache is reused. A card may have multiple invocations,
including separate review stages, so it may initialize MCP more than once.

If Codex exits before starting a session with the exact required
`runner_browser` startup-timeout diagnostic, Runner classifies it as
`browser_startup`. The background engine retains the current role lane, shows
the existing `Waiting for harness provider` activity, and retries after 30
seconds, two minutes, and five minutes, subject to normal admission limits.
These retries do not increment `QA Failures` or escalate the implementation
model. A fourth consecutive operational failure blocks for manual recovery.
The retry budget and delays are shared with transient provider failures and
are in-memory; restarting Runner resets them. Standalone CLI checks/planning
report the failure but do not start a background retry loop.

This recovery requires empty session stdout, a recognized CLI startup envelope,
successful process cleanup, and passing workspace-integrity checks where
applicable. It does not apply to model-reported browser problems, other MCP
servers, unknown startup failures, or failures after session output begins.
The browser remains required, and its 60-second startup timeout is unchanged.
Local output retains the recognized startup diagnostic; the card and metrics
receive a fixed browser-startup classification. If retries exhaust, inspect
the local log and run the browser conformance check above before a manual retry.
No configuration, Project-field, or skill migration is needed for this fix.

For unknown Codex CLI exits, local Runner output retains the terminal
`turn.failed` reason when present, otherwise bounded tails of stderr and stdout.
Diagnostics are limited to 4,000 bytes; full session transcripts are not retained.
This avoids losing the failure behind opening progress output without adding
automatic retries for unknown errors. Raw diagnostics stay local, not in GitHub
reports or metrics. It cannot recover diagnostics already discarded by an older
Runner version.

Codex's exact terminal failure `Selected model is at capacity. Please try a
different model.` is a retryable `capacity_exhausted` failure, as is a recognized
HTTP 429 failure. The background engine keeps the card in its current role lane,
retains its workspace, and shows `Waiting for model capacity`. It retries after
30 seconds, two minutes, and five minutes without changing the configured model
or reasoning level or incrementing `QA Failures`. The card's result shows the
retry count and next retry time; after exhaustion it identifies model capacity
as the blocker and provides the manual retry command. These attempts share the
existing in-memory operational retry budget described above; a restart resets it.

This classification requires Codex's terminal `turn.failed` event, not a model
message, progress error, or arbitrary stdout/stderr containing the same words.
Unknown diagnostics, invalid model selection, and account-limit text do not
gain automatic retry authority from mentioning capacity. Authentication failures
still require operator recovery, and all workspace-integrity checks still apply.
Standalone CLI planning reports the classification but does not retry itself.
No configuration, Project-field, or skill migration is needed.

If Runner reports unavailable browser capability, stop repeated retries. A
capability blocker does not increment the QA rejection count. An inconclusive
QA verdict instead reports `review_incomplete` ("QA evidence incomplete"); that
label alone does not diagnose a browser, Docker, permission, or dependency
failure. Inspect the retained local metrics/evidence before choosing recovery.
On macOS,
Codex's native sandbox can reject Chromium's Mach-port registration even while
local servers and repository operations work. Runner therefore launches the
pinned browser as a separate local process with only three tools, loopback-only
URL patterns, no telemetry/CrUX, redacted headers, and an isolated profile.
Use `doctor` to verify Node/npm/npx before retrying:

```bash
cortexium-runner doctor --config /absolute/operator/path/runner.json
cortexium-runner retry --config /absolute/operator/path/runner.json --item ITEM_ID
```

Set `roles.<role>.safe_tools` to `false`, or run `role edit ROLE
--no-safe-tools`, to disable the defaults. If no trusted browser capability is
appropriate, revise the card's proof obligation through normal human
assessment rather than treating an unrun browser check as passed.

Pi implementer and reviewer roles receive that same pinned browser through a
temporary Runner-generated Pi extension with only navigate, evaluate, and
screenshot tools. Ambient Pi extensions remain disabled in isolated mode; in
inherited mode they are loaded alongside Runner's explicit extension. Browser
navigation through Runner's extension remains loopback-only and uses
an isolated headless profile. Pi itself still requires explicit `host` access
because it does not provide a native OS sandbox for its shell and edit tools.

Runner treats the card's existing result as historical context on the next
attempt. Actionable review feedback remains required, but an earlier claim that
a tool, permission, service, or browser was unavailable must be re-checked
against the current harness invocation. The simplest implementation retry is a
status-only move from `Blocked` to `Ready`; Runner preserves the result and QA
failure count, verifies that the other signed fields are unchanged, signs the
new `Ready` action, and considers it on the next poll.

When the card should return to its recorded originating lane instead, retry by
exact title, Project item id, or source URL:

```bash
cortexium-runner retry --config /absolute/operator/path/runner.json --item "Exact card title" --dry-run
cortexium-runner retry --config /absolute/operator/path/runner.json --item "Exact card title"
```

If the stored failure feedback is stale or incorrect, replace it explicitly
while retrying. This discards matching private Agent QA feedback and any saved
implementation result, then resets the QA failure count so the corrected
attempt reruns implementation with a fresh review budget:

```bash
cortexium-runner retry --config /absolute/operator/path/runner.json --item ITEM_ID \
  --feedback "Keep task-owned edits and leave unrelated operator changes untouched."
```

This feedback is historical retry context, not a durable requirement amendment.
It cannot override the signed card body or its proof obligations, and later
attempt results replace it. Repository notes and issue comments claiming human
approval cannot amend that contract either.

### Amending an approved requirement

For an explicitly approved change of requirements on **unpublished retained
work**, use `amend`. Prepare a complete replacement body that resolves the old
requirement everywhere it appears, including acceptance criteria, constraints,
and proof obligations. Preserve the exact Runner planning metadata and
dependencies; this command cannot change scheduling, repository, or profiles.

```bash
cortexium-runner amend --config /absolute/operator/path/runner.json \
  --item ITEM_ID --body-file approved-requirements.md --dry-run
cortexium-runner stop --config /absolute/operator/path/runner.json --wait
cortexium-runner amend --config /absolute/operator/path/runner.json \
  --item ITEM_ID --body-file approved-requirements.md
cortexium-runner retry --config /absolute/operator/path/runner.json --item ITEM_ID --dry-run
```

The applying command requires an interactive terminal, previews the complete
old and new bodies and exact clean candidate, and defaults to **No**. `--json`
is preview-only. The selected card must have intact existing approval, a
registered clean committed workspace, an implementation/reviewer retry phase,
and no PR, QA acceptance, active assignment, or transition. Fresh staged cards
still require complete-batch approval. Changed previews, workspace identities,
candidates, and incomplete or invalid batches are refused. No model is called.

Runner updates the issue body and its signed authority, explicitly rebinds only
that retained workspace to the new content digest, and preserves the candidate,
branch, rejection count, dependencies, profiles, and siblings. For a released
Project-driven plan, its source's exact-child release binding is renewed without
authorizing or changing another child. Previous QA feedback stays historical;
the old private verification record is archived beside the active record under
a content-digest suffix. Old proof and acceptance are never relabelled as
evidence for the revised requirements. Both subsequent implementation and QA
receive the same revised body and proof obligations through the existing shared
assignment contract.

The card is left `Blocked`, in its existing retry phase. After checking the
result, an ordinary `retry` returns it to that phase without resetting the QA
count; restart the original service when ready. Amendment itself does not
implement, review, publish, or merge. A reviewer-phase candidate therefore can
receive fresh QA directly, without another implementation pass.

Applying an amendment requires the coordinator to have fully drained and
stopped, and excludes concurrent operator QA and Project mutations. Normal
intake/planning/retry commands retain their concurrent behavior. Failed writes
are read back: a confirmed commit is recognized, and known partial writes are
restored only if no intervening operator edits occurred. On an uncertain outcome
or interrupted amendment, leave Runner stopped and inspect the preview, issue,
release binding, and workspace identity before recovery; do not clear authority
or reset history to force a retry. Published work needs separate reassessment
and is deliberately outside this command's current scope.

### Retry and approval recovery

In a terminal, `cortexium-runner retry --config /absolute/operator/path/runner.json` presents the retryable blocked
cards as an arrow-key menu. The command preserves the previous result as attempt
history and moves only the selected card to its recorded lane. Both board and
CLI retries are human authorization events. A running Runner checks newly
available work on its next poll without waiting for unrelated harness actions
to finish.

CLI `retry` and `approve` writes share the short mutation guard used by planning
and background recovery. You do not need to stop the service: it continues
observing other work and cannot mistake these live transitions for abandoned
ones. The guard is not held while you inspect a preview or answer a prompt.

For an older whole plan whose accepted children predate durable evidence capture,
the ordinary parent `retry --dry-run` includes an evidence-recovery preview: exact
members, retained workspaces, acceptance/candidate identities, selected paths,
file digests and missing inputs. Recovery requires terminal confirmation of that
preview, defaulting to **No**; `--json` is preview-only when new historical capture
is required. Apply rechecks the same preview under the existing ownership and
mutation guards. Missing selected member evidence, changed identities or a changed
preview refuse recovery before a model call. No new flags or configuration are
needed.

Recovered snapshots are labeled **historical evidence requiring fresh parent
review**, never proof of what the earlier child reviewer saw. Recovery does not
rewrite child acceptance, requeue implementation, reset allowances or replace
immutable captured files. A saved parent review cannot resume publication using a
different evidence collection without fresh whole-plan QA. An interrupted capture
may require a fresh preview; already preserved identical files are not overwritten.

If an interrupted update has left a previously executed, unpublished
implementation in `Needs assessment` with its Runner approval missing, preview
and explicitly reauthorize just that card:

```bash
cortexium-runner retry --config /absolute/operator/path/runner.json --item ITEM_ID --reauthorize --dry-run
cortexium-runner retry --config /absolute/operator/path/runner.json --item ITEM_ID --reauthorize
```

The second command requires a terminal and defaults to **No**. Inspect the
exact body, runtime fields, destination, and retained worktree before choosing
Yes. In this mode `--json` is preview-only, and `--feedback` is not allowed.
Runner requires unchanged approved content and its existing private worktree
identity, the configured Ready implementation phase, no PR or QA commit, and
valid complete-batch sibling authority. It checks these again before writing.
This cannot individually approve a new staged child or recover changed task
scope. Ordinary `retry` remains restricted to validly signed blocked work.

Reauthorization moves only the selected card to Ready, preserves its branch,
uncommitted work, private QA feedback, and QA failure count, and replaces the
assessment error with a fixed operator-retry result. It does not grant QA
acceptance, approve siblings, or reset the review budget. No config migration,
skill update, or new state store is required for this recovery path.

### One-shot QA of a paused retained candidate

When approval was withdrawn from existing QA work, do not clear its phase,
branch, PR, feedback, or rejection count to make fresh approval accept it.
Instead, preview an explicitly review-only operation:

```bash
cortexium-runner retry --config /absolute/operator/path/runner.json --item ITEM_ID --reauthorize --qa-only --dry-run
cortexium-runner retry --config /absolute/operator/path/runner.json --item ITEM_ID --reauthorize --qa-only
```

The second command requires interactive terminal confirmation, defaulting to
No. `--json` is preview-only; `--feedback` cannot be combined with this mode.
The original card must remain Blocked with missing approval, a configured
reviewer phase, no active transition/activity, and a retained clean candidate
whose branch and commit match its open PR. Auto-merge must be disabled.
This does not approve fresh staged proposals or bypass complete-batch approval.

Inspect the exact current body, historical comments/feedback, retained content
identity, candidate/tree, comparison base, current PR base, and reference pins.
If the body changed during reconciliation, the preview explicitly shows the
different content identities. The original full body is not stored in the
workspace identity: consult source history if a textual comparison is needed.
The confirmation authorizes review against the displayed current requirements,
not rewriting the old workspace binding. Prior feedback remains historical;
proof from a different content identity is retained on disk but not reused for
the new requirements. No previous QA acceptance is reused.

Runner rechecks the preview before execution and after review. Changed card
content/runtime state, PR identity, candidate, workspace, reference pins, or
review context invalidate the operation. One confirmation is single-use, even
if the attempt fails. Reviews of the same item cannot overlap; the operation
shares global execution admission with the background service without taking
its lifetime lock or holding the Project mutation lock during model calls.

This is one normal reviewer attempt (source audit and, when needed, its bounded
focused-verification stage), not one model call. It reviews the exact candidate
against its retained base; it never refreshes or rewrites that candidate. The
complete candidate-bound result is printed locally, and normal private attempt
metrics are recorded. Save the terminal output if a full standalone report is
needed. There are no GitHub writes: the card stays Blocked with approval empty,
even on acceptance. Existing feedback, proof records, rejection counts, and PR
state remain untouched. Publication, implementation, merge, siblings, external
mutation authority, and automatic retries are not granted. Any subsequent work
requires a separate human decision; another QA attempt requires a new preview
and confirmation. No configuration or Project-field migration is required.

### Clarification and interrupted integrity checks

A structured implementation clarification is reported as "Awaiting human
input", preserving the manual retry phase and local question without consuming
a QA rejection or exposing model-authored diagnostics on the board.

After QA stops, Runner checks workspace integrity with a separate bounded
30-second context. Cancellation with unchanged work returns to the interrupted
lane. A detected change still blocks as `integrity_violation`. A check that
cannot complete blocks as `integrity_unverified`, preserving the QA retry lane;
it does not claim the reviewer changed files. Neither condition permits
publication without a successful integrity check.

When candidate staging encounters Git's exact lock-exists error for the pinned
worktree index, Runner retries that index operation twice, 100 milliseconds
apart. It never deletes the lock. If contention persists before QA, the card
blocks as `integrity_unverified` with its QA retry phase and rejection count
preserved. Inspect the local diagnostic and active Git processes; after the lock
owner finishes, use the recorded plain `retry` command to resume QA on the
retained candidate. This does not authorize implementation or bypass candidate
checks. Unknown Git errors and actual identity changes remain fail-closed.
Use `git --no-optional-locks status` for operator inspection of active worktrees
so the inspection does not compete for Git's optional index-refresh lock.

## Workflow configuration

See
[`examples/runner.config.json`](../examples/runner.config.json)
for the complete v5 configuration generated by `init`.

Lanes contain only stable IDs and GitHub Project status names. Rules bind one
typed trigger to one typed action. A transition into another lane emits that
lane's `lane.entered` event, so larger workflows compose without ordered action
arrays or an embedded scripting language.

The supported trigger catalog is:

- `lane.entered`, with an explicit `lane`;
- `pull_request.merged` and `pull_request.closed`;
- `pull_request.checks_failed`;
- `pull_request.out_of_date`.

The supported action catalog is:

- `run_role`, with a configured role profile and outcome transitions;
- `transition`, with one destination lane;
- `publish_pull_request`;
- `update_branch`, with `require_review: true` and outcome transitions.

`run_role` accepts a built-in role or any custom role that inherits the planner,
implementer, or reviewer contract. Planner actions require `creates_in`.
Reviewer actions additionally require `max_qa_rejections`, `rejected`, and
`exhausted`. All role actions route `success`, `needs_input`, and `error`.
All active implementer profiles use one `workspace_write_root`, preserving the
same card workspace and candidate as work moves between specialized profiles.
`plan_lane` and `ready_lane` explicitly identify the default destinations used
by `add plan`, interactive planning, and `add ready`; this avoids guessing when
several profiles share a contract. `active_lane` remains Runner's temporary
claim lane and cannot have an action.

Use the focused local commands before restarting Runner:

```bash
cortexium-runner workflow validate --config /absolute/operator/path/runner.json
cortexium-runner workflow explain --config /absolute/operator/path/runner.json
```

The explanation shows every effective trigger, action, role contract, and
outcome route, followed by the safety constraints configuration cannot disable.
Validation rejects duplicate triggers, unsupported event/action pairings,
automatic success cycles, and error, input, or exhausted routes that would
silently start more agent work instead of stopping in a human recovery lane.
Planner output destinations, QA rejection routes, and branch-refresh conflicts
must lead to implementer lanes. Clean branch refreshes may target a reviewer or
implementer lane; new workflows choose the reviewer. Unexpected PR-head changes
and invalid workspace identities use the implementation recovery route, never
the clean-refresh shortcut.
Merged and closed pull-request events terminate in lanes without an automatic
action.

- `github_project.auto_merge` is an explicit opt-in. When true, Runner asks
  GitHub to merge one reconciled repository/base candidate after checks and
  branch protections pass; it never uses `--admin` or weakens repository
  requirements.
- `github_project.merge_method` selects `merge`, `rebase`, or `squash` for that
  request and is required. A divergent
  `rebase` refresh produces a linear candidate on the new base and uses an exact
  expected-old-commit lease when replacing an existing pull request branch.
- The out-of-date event explicitly sets `require_review` to `true`; a clean
  base refresh remains local until the refreshed tree completes QA and records
  a replacement accepted tuple.
- The checks-failed event targets an implementer lane. Runner cancels an armed
  automatic merge, preserves the pull request and branch context, leaves the QA
  rejection count unchanged, and releases the integration slot before applying
  that event.
- Events handle PR merge, closure, terminal check failure, and an out-of-date
  branch. Moving an open Runner PR from the human gate to an implementer lane
  is itself the rework request; no separate event declaration is required.

Configuration v5 intentionally replaces v4 rather than interpreting both
models. Existing v4 files must be rewritten using the generated v5 example;
changing only `config_version` is insufficient because lane behavior and the
separate `events` collection have moved into `workflow.rules`.

Repository-wide code style, architecture, testing, and contribution rules stay
in `AGENTS.md`, installed skills, and ordinary repository documentation. The
workflow decides which supported action responds to an event and where its
outcomes move work.

## Skills, tools, and MCP servers

The embedded skills are:

- `runner-planner`
- `runner-implementer`
- `runner-reviewer`
- `runner-interaction-design` (optional, additive design guidance)

Setup refuses to overwrite a differing installed skill unless `--force` is
explicit during initialization or `doctor --fix` is explicit afterward. In
either case, only the embedded Runner-managed skills are replaced. Privileged
launches disable native skill discovery and inject the selected embedded copy,
so a modified installed copy cannot change execution. Role configuration
rejects skills outside this pinned catalog.

Skills may include reviewed Markdown files under `references/`. Installation
and Doctor check every bundled file, not just `SKILL.md`; modified or symlinked
references do not count as ready. A normal install preserves differing files;
explicit repair replaces only managed files and leaves unrelated files alone.
Privileged launches use disposable copies of the selected embedded references,
never installed/reference-repository content. Codex and Claude grant these copies
read-only access; Pi retains its documented host-access limitations. References
are loaded on demand, while a stable path/hash manifest participates in the
prompt guidance fingerprint. Their temporary location is outside the stable
prefix. Tool-free planner synthesis and capability probes receive no reference
grant. No internet retrieval, extra MCP server, or automatic model call is added.

### Opt-in interaction design

`runner-interaction-design` complements a role, rather than defining a fourth
contract or an additional QA pass. For example, add a reviewer profile:

```bash
cortexium-runner role add ux_reviewer \
  --config /absolute/operator/path/runner.json \
  --extends reviewer \
  --skill runner-reviewer \
  --skill runner-interaction-design
```

Select `ux_reviewer` in the relevant existing workflow `run_role` action when
approving the workflow change. Creating a profile alone does not route work to
it. For an initial scoped UI trial, replace the ordinary reviewer at that action;
do not add a second complete QA lane. This changes the reviewer for work using
that rule, not just a card whose title sounds UI-related. Review the affected
work before enabling it, and restore the original routing after a bounded trial.

Existing card authority is bound to its role identity. Do not rename the role
under already-authorized or retained work: a queued `reviewer` action cannot
silently become `ux_reviewer`. For a trial on those cards, retain the existing
reviewer ID and explicitly add the two skills to that profile with `role edit
reviewer --skill runner-reviewer --skill runner-interaction-design --config PATH`.
Keep its runtime settings unchanged and restore the prior skill selection after
the trial. Named inherited profiles are appropriate when routing new work before
it receives role-bound authority.

The same approach can create a design-aware planner or implementer. Keep its
ordinary `runner-planner` or `runner-implementer` skill alongside the design skill.
Skill lists replace, rather than append to, inherited lists; validation requires
the matching ordinary skill whenever interaction design is selected. Models,
reasoning, timeouts, role authority and tool ceilings otherwise inherit unchanged.
The built-in role defaults do not opt in automatically.

After the explicit configuration change, inspect `workflow validate` and
`workflow explain`, install/check the selected bundled skill with
`doctor --config PATH --fix --offline`, and run normal Doctor. Review differing
managed files before repair. Coordinate a running service's graceful stop and
restart before upgrading its executable or changing its effective configuration.

The planner records a concise interaction contract in existing card content;
the implementer inspects relevant rendered states before costly final checks;
the reviewer critiques the journey using the existing evidence-audit and focused
verification stages. Static audit does not gain permission to run a browser or
tests. Requirement violations, improvements, preferences and unobserved behavior
remain distinct in existing feedback: optional suggestions are not new acceptance
criteria. Material concerns remain fixed, explicitly human-accepted, concretely
deferred or unresolved; cross-card concerns are not silently waived or new work
authority. No additional findings store or automatic card creation is introduced.

Theory references provide contextual principles and their sources, not one
mandated visual style. A heuristic evaluation is not user research or complete
accessibility certification. Before broad adoption, use historical raw evidence
without revealing expected answers and a sound comparison case, then an already
approved UI journey. Compare useful corrections, false positives, escaped issues,
rework, and total time/available usage including review overhead. Human assessment
of the result remains part of acceptance; a small trial is not a causal benchmark.

Runner roles and native harness agent roles are separate concepts. A Runner
role is the configured planner, implementer, or reviewer profile that selects a
harness, skills, model, reasoning level, and timeout for a workflow lane. Runner
invokes that harness's primary non-interactive CLI. Isolated launches suppress
native custom agents, plugins, and delegation; inherited launches load them.
All work roles use the configured sandboxed or host boundary with Runner's
workspace-integrity checks.

Each harness may run only the verification available through its active native
configuration and Runner workspace. Agent results must report only checks
actually performed; a missing required capability is a blocker rather than a
successful result.

The planner derives proof obligations from the product contract rather than
harness capabilities. It states what evidence must establish without prescribing
a command, test framework, file, or implementation technique. The implementer
inspects the repository and selects the smallest reliable proof method, adding
or updating durable focused tests when that is the clearest protection for
changed behavior or an important invariant. The reviewer first completes one
source-and-evidence audit without running dynamic checks. Read-only shell
commands to inspect files, diffs, and existing logs are allowed. Runner provides
the exact candidate/base comparison; the reviewer must not substitute a stale
local branch as the base. Finding one defect
establishes failure for that exact behavior but does not end the bounded audit
of the proof obligation. The reviewer continues through its remaining
card-owned behavior and groups concrete variants of directly exposed invariants
in the same result. If any concrete proof questions remain, Runner starts a
fresh focused-verification call containing only unresolved proof keys, even
when another key already failed. It carries the pinned comparison, any repair
baseline, and the original evidence for those keys. Unresolved repository-rule
or maintainability checks also receive the approved scope. The stage may inspect
the necessary source; it does not assume an incomplete audit already happened.
Evidence paths outside its isolated workspace are not automatically copied, and
missing logs are not themselves candidate defects. Final summaries describe the
merged check results rather than repeating obsolete stage blockers.
The final private evidence also retains each focused check's original audit
question and context, labelled as historical rather than a current blocker.
Use these alongside the actual result and candidate identity to investigate
repeated verification; older results may not retain the original question.
Runner's binding of implementation evidence to the approved candidate is not
independent proof that the reported commands passed. Reviewers still assess
coverage and invalidating changes, but a pre-commit HEAD mentioned in a report
does not by itself require repeating a check over the same final tested delta.
The focused stage reuses the smallest relevant existing checks. It does not
create tests, benchmarks, a custom harness, or broad diagnostics unrelated to a
concrete diff concern. Roles do not assume a browser
or any other interface. Broad or long-running checks belong only at the narrowest
integration boundary that needs them. Time-based behavior uses controlled clocks
or ordinary fixed-size simulation steps executed without wall-clock pacing;
real-time smoke checks remain short and are required only when real scheduling,
pacing, or presentation integration is part of the claim.

Implementers and reviewers run heavyweight verification commands sequentially
within an assignment: no overlapping test suites, browser runs, builds, or
dependency installation through parallel tool calls or background jobs. A server
needed by the active check may remain running. Commands retain their configured
worker counts and timeouts. This is agent guidance, not a host-wide resource lock;
independent cards still follow Runner's configured admission limits.

The implementer must return self-contained evidence in each proof entry. After
a failure and rerun, include affected file/test names, commands/selectors,
worker counts, timeout limits, relevant non-secret environment differences,
both outcomes, and diagnostic observations. Runner retains these entries bound
to the candidate; ignored reports and temporary logs are not copied into QA.
Artifact paths and aggregate pass counts alone do not establish the result.

Bundled skills 1.10.3 narrow regression investigation to supported fixtures and
the affected event or transaction sequence. Start with the smallest faithful
reproduction, adding browser coverage when interaction or rendering is part of
the unresolved claim. Classify a product defect, an incorrect test assumption,
and an expectation superseded by the approved change separately. Preserve
applicable historical proof with its original candidate, settings and reuse
rationale, and establish evidence for the changed behavior. Reports distinguish
"not reproduced", "fixed", and "unverified": a focused pass alone does not prove
the cause or correction of an earlier failure, and unrun or inconclusive checks
remain unverified. These distinctions use existing evidence fields and statuses.

A known unexplained timing failure gets one focused, unchanged confirmation with
a trace or equivalent diagnostics inside the existing verification call. An
existing unchanged diagnostic retry counts toward that bound. When historical
test names, settings, or reports are missing, QA gathers fresh evidence with the
smallest existing check covering the unresolved requirement under documented
repository settings. It must distinguish fresh verification from reproducing
the old run and retain uncertainty about unknown historical failures. Fresh
focused success is not evidence that the full historical suite passed. QA must
not raise timeouts, reduce a command's configured worker count, change assertions,
or rerun until green. If heavyweight commands accidentally overlapped, correct
their scheduling before the bounded confirmation and record that difference;
do not recreate the overlap or describe the corrected run as an unchanged
reproduction. A sequential pass does not establish concurrent-load reliability.
Concrete defects remain failures; genuinely inconclusive proof reports
`review_incomplete`, retaining the manual QA retry lane without consuming a
QA rejection. This is not an instruction to repair tooling or implementation
unless the evidence identifies such a problem.

Bundled skills 1.8.11 keep shared role rules in the pinned skills and stage
procedures in Runner's prompts. Reviewer timeout confirmation, heavyweight-check
scheduling, and interface execution guidance appear only in focused verification,
not in the source-and-evidence audit. This is deterministic stage selection,
not keyword filtering of findings or a model-specific relaxation. The shared
reviewer skill still requires all visible blockers, reliable candidate-bound
evidence, and unchanged canonical workspaces. Shared harness capability guidance
does not authorize dynamic checks during the static audit. Implementer verification policy
also has one home in its skill, rather than being repeated in the launch prompt.

Sequential heavyweight verification, complete evidence handoff, and the
fresh-verification fallback remain unchanged. Planner guidance carries exact accepted
references and distinguishes historical ideas. It separates immutable reference
pins, each card's current accepted starting base, and final candidate identity,
while honoring explicitly fixed execution bases. It sizes cards by behavior and
risk rather than a target count and establishes real shared-contract dependencies.
Integration cards must name distinct missing proof. Documented host-only checks
remain separate from sandbox work, with an approved exact-candidate handoff.
A changed candidate needs a new bound record, but applicable underlying checks
may be reused with explicit reasoning and delta coverage. Do not relabel an old
receipt as a fresh whole-candidate run or mandate complete reruns solely to
renew the receipt.
Repair reports identify the underlying invariant, adjacent cases and regression
proof in existing result fields. Review guidance compares changed comment context
without discarding unaffected conclusions or broadening authority.
After upgrading,
use `doctor --fix --offline` with the project configuration to refresh installed
bundled skills, reviewing locally customized copies before replacement. No
configuration or Project-field migration is required.

An agent that needs a decision, permission, credentials, access, or designated
test data should return `needs_input`. Runner reports `Awaiting human input.`
and follows the configured input route. A generic agent `blocked` outcome is
classified as `agent_blocked`, reports `Work blocked.`, and follows the configured
error route; Runner does not guess its cause from free-form text. When stopping
in `Blocked`, both retain the manual retry lane. Partial work and local evidence
are preserved without consuming a QA rejection or publishing an incomplete
candidate. The detailed blocker is kept
in local Runner output, not copied to the Project. Inspect that output and
`metrics --item ITEM_ID` before retrying; supply the missing prerequisites
through the project's approved local setup rather than repeatedly rerunning
passing checks. Neither outcome grants additional data-mutation authority.

For ordinary in-scope defects, the implementer should keep repairing within its
current session. If a fresh pass is genuinely needed, `repair_needed` asks Runner
for one bounded continuation, carrying retained work, identifiable failure
evidence and the remaining correction. Runner rechecks authority and workspace
integrity before admitting it; the card remains in implementation and exposes
`Repairing implementation (1/1)` as activity. This shares the existing candidate
formatting-correction allowance and original runtime deadline, not a fresh
timeout or QA rejection budget. Models, concurrency and required proof do not
change. Passing evidence is reusable only when applicable to the new candidate.

Another unfinished repair or insufficient remaining runtime stops with
`repair_exhausted`. Missing inputs/capabilities and integrity problems still stop
without automatic repair. An interrupted corrective pass also requires explicit
retry: restarting Runner does not grant another allowance. Inspect the retained
work and local attempt evidence before using the existing `retry` command or
moving a blocked card to Ready. Either human action starts a new implementation
budget; it does not erase QA rejection history. No configuration or Project-field
migration is needed; update the bundled implementer skill to 1.9.3 using Doctor.

When distinct integration or release evidence cannot be established on delivery
cards, a project-readiness card depends on the relevant delivery paths and names
that proof gap. It is not mandatory for every multi-card plan and must not simply
repeat completed checks. Verification follows the project's policy: focused
development and repair checks, explicit reuse of unaffected evidence, broader
checks for concrete cross-cutting risk, and complete required proof against the
integrated candidate. A host-only gate uses its approved handoff; it is not
silently replaced by weaker sandbox proof. None of this invents browser,
deployment, CI, or test-framework requirements absent from the project contract.

Codex roles can grant explicitly named local stdio MCP servers:

```bash
cortexium-runner role edit reviewer \
  --config /absolute/operator/path/runner.json \
  --mcp-server chrome_dev_tools
```

The repeatable `--mcp-server` option updates only the selected role;
`--clear-mcp-servers` removes that role's override and restores any parent-role
grant. Runner reads Codex's
native MCP catalog, reconstructs only the selected definitions in the launch,
and suppresses all unlisted servers and other ambient configuration in isolated
mode. In inherited mode, the native catalog remains available and the named
grants document role expectations rather than forming the complete ceiling.
Missing or disabled grants fail before model work. Doctor automatically treats every role
grant as required, so the same capability does not need a duplicate
`doctor_requirements` entry.

Role-launched MCP tools are auto-approved because the harness invocation is
non-interactive. Their stdio server processes are separate trusted principals
outside the Codex shell sandbox and may read files, use the network, or launch
processes according to their own command and OS permissions. Runner rejects
remote transports and inline environment values, but operators must inspect and
trust each command and keep its tool surface minimal. Use environment-variable
names rather than inline secret values when a selected server requires
credentials.

Custom MCP grants are separate from the default `runner_browser`. Runner never
reads an ambient MCP definition to construct the default browser; its pinned,
loopback-only definition is owned by Runner. Use `role edit ROLE
--no-safe-tools` to opt out of all default development tools for that role.

An observational capability that is not granted to a role can still be added
to `doctor_requirements`, for example:

```json
{
  "id": "codex/semantic",
  "type": "mcp_server",
  "required": true,
  "reason": "This project requires semantic navigation."
}
```

For an explicit Claude Code MCP requirement, `doctor` requires the exact named
entry to report a successful connection. This readiness observation does not
grant that server to a role process. Role `mcp_servers` grants currently require
Codex; configuration rejects them for Claude and Pi until Runner has an
equivalent isolated injection boundary.

## Code structure

The repository is one Go command and a small modular monolith. Go convention
puts a command's package in a directory under `cmd`, so the executable remains
in `cmd/cortexium-runner` even though the repository builds only one binary.

```text
cmd/cortexium-runner  CLI parsing and dependency composition
skills/               embedded, installable Agent Skills
internal/config/      file config, validation, workflow resolution
internal/engine/      planning-to-PR orchestration
internal/execution/   Codex, Claude, and Pi adapters and review evidence
internal/github/      GitHub Project, issue, branch, and PR operations
internal/metrics/     privacy-preserving attempt/stage history and aggregates
internal/setup/       doctor checks, bundled skill installation, and allowlisted prerequisites
internal/workspace/   isolated Git worktree lifecycle
internal/subprocess/  bounded process execution and process-group cancellation
```

Persisted `config.Config` and validated `config.RuntimeConfig` are separate.
Execution adapters receive only a role-specific `config.ExecutionConfig`; JSON
configuration structs do not carry hidden runtime fields. See
[`architecture.md`](architecture.md) for package responsibilities and
dependency direction.

## Current boundaries

- Polling only; no webhook or inbound server.
- Public assessment intake is capped at 1,000 open labeled issues per repository
  and a Project at 10,000 items; Runner fails clearly instead of silently
  ignoring items beyond those bounds.
- Each poll fetches one Project item snapshot through a narrow GraphQL query
  that asks only for lifecycle fields and paginates at 100 active items per
  request. The Project schema uses a separate narrow query and is cached for the
  process lifetime. When work is claimable, one additional fresh snapshot
  validates the selected claims instead of re-listing the Project per item.
  Exact-ID authorization and delegated-content refreshes reload only that item,
  and bounded multi-item reloads use one `nodes(ids: ...)` query per 100 cards.
  Transition locks remain separate recovery boundaries, while the lifecycle
  fields between lock and unlock are committed in one GraphQL mutation.
  Interruption recovery runs only when no local action is in flight, so it
  cannot reclaim a live card. Pull-request reconciliation excludes items whose
  item or repository/branch resources conflict with live harness work but
  continues for unrelated items. A state-changing poll rechecks immediately.
  Quiet polls remain at `--poll-interval` while actions or other nonterminal
  work are observable. Both `--poll-interval` and `--max-idle-interval` default
  to 30 seconds, so the default does not back off; an operator can explicitly
  choose a larger idle ceiling for a quiescent board. Public intake
  synchronization is separately limited to at most once every two minutes. Its
  schedule advances only after a successful synchronization, and an explicitly
  larger idle interval is capped so the next intake check is not skipped.
  Autonomous intake routes at most eight trusted issues per synchronization;
  progress causes an immediate coordinator poll, while later intake resumes on
  a subsequent synchronization. Deterministic issue and batch authorization
  does not consume harness parallelism. Completed-issue reconciliation attempts
  at most 16 closures per poll and likewise uses no harness slot.
- Pull requests on successfully merged terminal cards are not polled. A
  closed-without-merge PR remains visible on its blocked card, and a rework
  request is inspected and reset only once per poll. Routine PR observation
  omits comments and reviews; those are fetched only when trusted feedback can
  affect a rework or refreshed-branch handoff. GitHub primary rate-limit errors wait
  until the reported reset, while secondary limits wait at least one minute and
  continue through the existing bounded error backoff.
- One active Runner process per Project on one machine; no distributed claim.
- Harness stdout/stderr and final structured results have explicit size bounds.
  Every terminal path—success, command failure, timeout, or cancellation—uses
  bounded cleanup of the direct process, original process group, and same-user
  descendants retaining the invocation's ownership marker on macOS/Linux.
  This includes detached sessions. Matching uses a random marker and process
  start identity, never executable names or workspace-path guesses. Output and
  usage already observed are preserved. `cleanup_unresolved` pauses new agent
  admission and retains local execution capacity rather than automatically
  retrying. Inspect the local diagnostic and surviving work, stop only processes
  whose ownership you have confirmed, then stop/restart Runner before retrying
  the card. Do not restart repeatedly or broadly kill Node, shells or browsers.
  Replacement Runners refuse admission while identifiable tagged orphans remain.
  Independently running tool services, processes that erase their marker, and
  uninspectable processes are not safely attributable; Runner never guesses and
  kills them. Unclosed inherited output pipes also produce a cleanup failure.
  Agent QA uses a newly created private
  candidate checkout, so its trust boundary does not depend on a child remaining
  in that Unix process group. Failed
  harness diagnostics stay in local Runner output and are not copied to GitHub
  Project fields. Capacity and provider retry timing come only from explicitly
  parsed adapter-owned structured evidence. Browser-startup retry authority
  additionally accepts the exact pre-session Codex envelope described above,
  subject to empty stdout and successful teardown. Arbitrary phrases in model
  output, stdout, or stderr have no recovery authority. Project results use fixed,
  bounded Runner templates and allowlisted enums or structured retry fields.
  Model-authored summaries, blockers, review evidence, prompts, tokens, session
  data, and stack traces remain local even when embedded in schema-valid output.
- Runner retains task worktrees while work is unpublished and resumable. Reuse
  requires the exact private item/content/base/repository/branch/path identity;
  branch names alone are not authority. Once the accepted commit is pushed and
  `PR Ready` is recorded, Runner removes the worktree but retains both branch
  and identity so unchanged rework can reopen it. Reconciliation removes only
  identity-matched worktrees for published and terminal items after interrupted
  cleanup. A mismatch preserves the workspace and is reported for safe
  reimplementation or human recovery; it never deletes or certifies stale
  content. Other cleanup failures remain cycle warnings and do not prevent
  unrelated cards from progressing.
- Initial publication never force-pushes. Tracked PR rework may replace the
  exact previously reviewed remote commit with `--force-with-lease`. If a
  Project update was interrupted, an immutable private publication record for
  the same card content, repository, and destination may recover the exact
  remote lease; Runner still refuses an unrecorded moved branch. Publication
  never merges a PR or deploys.
- External Project configuration changes happen only through `init`; use
  `init --dry-run` to preview them and `init --prune` to remove only unoccupied
  extra Status options. `plan --create` creates and approves a direct batch;
  `plan --stage-only` leaves one staged until the fingerprint-bound interactive
  `plan --approve-staged` confirmation. Issue authority requires the explicit
  `approve` command. A status-only `Blocked` to `Ready` move explicitly retries
  through implementation; the `retry` command restores the recorded lane or
  replaces stale feedback. The two CLI commands have read-only `--dry-run`
  forms.
  Missing Git/GitHub CLI prerequisites are reported with bounded manual
  recovery guidance. AI harness installation and configuration remain
  operator-owned.

## Development

Tests use fakes and local repositories; they do not call an AI model or mutate
live GitHub state. The release-readiness harness is the single pre-release gate:

```bash
sh scripts/test-release-readiness.sh
```

It runs the complete deterministic suite once with the race detector, covering
new and existing Projects, empty and initialized remotes, base-branch bootstrap
and refusal paths, dry-run and apply, interactive and scripted setup, shared
role defaults and overrides, and Project repair/idempotency. It also runs static
analysis, a packaged binary smoke test, a known-vulnerability scan with the pinned
`govulncheck@v1.6.0` command, and builds/checksums for every published platform.

To add read-only verification against an existing real Project and one minimal
model call per configured harness profile:

```bash
sh scripts/test-release-readiness.sh \
  --live-config /absolute/path/to/runner.config.json
```

The deterministic matrix never creates or deletes live GitHub resources. A
throwaway card through implementation, Agent QA, and PR publication remains the
final proof of real write permissions and end-to-end harness behavior.

The opt-in launch evaluation accepts any non-empty subset of the advertised
harnesses and one or two repetitions. Each full run has seven scenarios per
harness: three planner contracts, one exact-file implementer check, and three
reviewer candidates. The reviewer cases cover correct record editing with
sufficient backend tests, a tenant-access defect accompanied by passing shallow
tests, and an access-control repair that discards record ownership. The last
case supplies the prior rejected assessment so review must detect a regression
in previously accepted behavior.

`--smoke` selects one planner, one implementer, and both the correct and
access-defective reviewer candidates: four scenarios per harness. A scenario
can invoke multiple model stages; scenario counts are not model-call counts.
Use the affected harness while iterating. Reserve the full matrix twice from
one clean candidate (42 scenarios across all three harnesses) for qualification
that requires cross-harness evidence. All model calls remain paid and opt-in;
there are no GitHub writes.

Sanitized `EVAL_CASE` records retain expected and observed verdicts and fixed
`review_judgment` labels. `EVAL_SUMMARY.reviewer_judgments` counts `correct`,
`false_acceptance`, `unnecessary_rejection`, `missed_defect` (rejection without
failing the affected proof), and `incomplete_review`. Execution and admission
failures remain separate from judgments. Reviewer judgment failures do not skip
later candidates; execution or budget failures stop the run. A correct verdict
and failed-proof match are useful signals, not independent validation of the
reviewer's natural-language reasoning or a general correctness guarantee.

Each candidate's visible tests run before review, and their actual result is
supplied as evidence bound to the candidate. Expected verdicts and reference
assertions for faulty candidates remain outside the reviewer workspace. Ordinary
tests independently confirm that the visible suites pass and reference
assertions expose the seeded faults. These fixture checks use Go and temporary
local repositories, without browsers or live models.

Private mode-`0600` JSONL includes `duration_ms` for the whole scenario,
`harness_duration_ms` for reported harness execution, and
`fixture_test_duration_ms` for the backend tests run before review. The remainder
includes setup and orchestration; none of these fields measures all tests a
reviewer may execute internally. Inspect applicable private review evidence for
that question. Prompts, results, and diagnostics are not retained in this
sanitized artifact.

```bash
candidate=$(git rev-parse HEAD)
sh scripts/test-agent-behavior.sh --candidate "$candidate" --repeat 1 --smoke \
	--codex-model gpt-5.6-luna --claude-model sonnet --reasoning medium \
	--pi-model lmstudio/qwen/qwen3.8-27b --allow-pi-host --max-tokens 1000000 \
  codex,claude,pi

sh scripts/test-agent-behavior.sh --candidate "$candidate" --repeat 2 \
  --pi-model lmstudio/qwen/qwen3.8-27b --allow-pi-host --max-tokens 3000000 \
  codex,claude,pi
```

Selecting Pi requires `--allow-pi-host` because its implementer and reviewer
calls use Pi's fixed host-access profile inside a test-owned disposable
worktree. Run that evaluation only on a trusted machine or inside an external
sandbox. Codex and Claude remain natively sandboxed.
The optional per-harness model flags and `--reasoning` select an explicit live
test tier without changing the candidate's normal role defaults.

Each case is bounded to 20 minutes by default, matching the shipped planner
timeout, and each run remains bounded to 75 minutes.
The required `--max-tokens` ceiling and optional `--max-cost-usd` ceiling reuse
Runner admission rules and fail closed when a selected harness does not report
the configured counter. Reported tokens count inclusive input plus output once;
cache reads/writes are already included in input, not added again. The
operator selects a ceiling appropriate for the chosen providers and can use a
cost ceiling when spend is the concern. Normal tests validate the coordinator, corpus, budget refusal, and
safe reporting but skip all paid calls. The two live runs are an explicit
operator-controlled launch gate, never ordinary CI. Runner does not certify
model quality or prevent any supported harness from serving any role.

Public bug reports and feature proposals are welcome through GitHub Issues and
start in assessment. External pull requests are not accepted for now. See
[`CONTRIBUTING.md`](../CONTRIBUTING.md) and [`SECURITY.md`](../SECURITY.md).

Licensed under the [MIT License](../LICENSE).
