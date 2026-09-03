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

For an installed release build, update in place with:

```bash
cortexium-runner update --check
cortexium-runner update
```

`update --version vMAJOR.MINOR.PATCH` selects an exact release. The native
updater verifies the checksum, archive contents, and downloaded binary version,
then atomically replaces the resolved executable. Run `doctor` afterward and
rerun `init` when it reports newly required Project fields. An already running
Runner process keeps its loaded release until it is stopped and restarted.

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
`metrics`, `role`, `workflow`, and `harness` are one-shot commands that exit
when they finish. Running
`cortexium-runner` without arguments shows help; every command supports
`--help`, and `--version` prints the installed version.

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
  --planning-support standard \
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
`--planning-support` stores regular (`standard`) or smaller (`high`) downstream
task sizing for both implementer and reviewer. The guided prompt uses the clearer
regular/smaller wording. The role-specific
`--implementer-planning-support` and `--reviewer-planning-support` flags
override it. Runner never infers this setting from a harness or model name.
`--base-update-review required` sends every clean automatic base refresh through
implementation and QA again. The required policy is written into the config
rather than applied as a runtime default.
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
replaces differing copies of Runner's three embedded role skills; unchanged
copies are retained and missing copies are installed. It never stages,
commits, or untracks files.

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
read-only context for planner and reviewer profiles. The command uses the
profile's configured model, reasoning, access, harness configuration, pinned
skills, safe tools, and explicit MCP grants. Consequently, a `host` profile is
still host-access and `host/inherit` remains unrestricted; the result labels
that policy rather than pretending the temporary fixture is a security
sandbox. Browser checks are omitted unless `--browser` is explicit. A failed
profile does not prevent independent profiles from being checked, and any
failed requested check makes the command exit unsuccessfully.

`status` is operational rather than diagnostic: it reports current card counts,
active, queued, blocked, and PR-ready work, whether a local Runner process holds
the Project lock, and that process's PID, uptime, last and next poll. On macOS
and Linux it also inspects the Runner's process tree and reports active harness
or other direct subprocesses by executable name, PID, process health, elapsed
time, configured role timeout, and associated card when the mapping is
unambiguous. It never prints subprocess arguments because those can contain the
approved prompt. An `alive` process proves that the harness process still
exists, not that a remote model is currently producing tokens; the harness
timeout remains the final bound. Nested tool and MCP processes belong to their
direct Runner-launched harness and are not listed as additional harness
attempts. Blocked cards include up to three concise result lines plus the exact
`retry` command when Runner recorded a safe retry destination, so the recovery
path is visible without opening the Project item.
`status --verbose` adds the current fixed Runner stage and elapsed time for
each unfinished attempt. It is intentionally sanitized: it does not retain or
print prompts, model responses, tool commands, subprocess arguments, or
worktree paths.
Use `doctor` for installation and configuration readiness.

`metrics` reports the recorded duration, outcome, role, harness, model,
reasoning level, QA iteration, recovery classification, harness-reported token
counters, and harness-reported monetary cost for each attempt. Its summary also
shows completed harness invocations and planner or implementation attempts that
resumed an exact saved result without another model call. It also shows a
stage timeline for workspace preparation, repository preparation, harness
execution, result validation, workspace verification, Project
transitions, and pull-request publication when those stages apply. A recovered
publication shows its total attempt count; an exhausted publication also shows
the fixed failing operation. Raw GitHub and Git diagnostics are not retained in
metrics. Use
`metrics --item ID_OR_TITLE` for one card or `metrics --json` for
machine-readable output. `status` includes a compact accumulated total and the
current admission-budget state.

After a Project planning card produces a valid executable plan, Runner stores
that exact normalized plan in a private mode-`0600` checkpoint before creating
the first child. The record is bound to the approved source content, bounded
issue discussion, role, lane, destination, repository, and deterministic batch
fingerprint. If GitHub staging fails partway through, retrying the unchanged
planning card skips the planner and creates only the missing children; already
staged matching children are reused. A changed planning context discards the
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
that application state. Attempt and stage start records are written before work
and completion records afterward, so an abrupt stop remains visible. Attempt
records can contain concise task and verification summaries. Stage records are
restricted to attempt identity, a fixed stage name, timing, outcome, recovery
classification, and reported usage; neither record type contains prompts,
transcripts, command arguments, raw harness responses, or raw failure
diagnostics. Runner never estimates missing tokens or cost: Claude Code cost is
shown only when Claude reports it, Codex token counts are shown when its JSON
event stream includes them, and unavailable Pi counters remain explicitly
unavailable. History begins with the first metrics-enabled run and cannot
reconstruct earlier attempts. Runner does not yet rotate or expire this history
automatically. The `metrics` output shows its exact `History` path; to clear it,
stop Runner and delete that one file. The next attempt recreates it.

`project_dir` is the source checkout for the one configured
`intake_repository`. Its configured GitHub remote must identify that same
repository whenever `doctor` or Runner performs repository work. The checkout
does not have to be clean: Runner fingerprints and leaves its tracked and
untracked files untouched. Each implementation item owns one deterministic
task branch and worktree outside that checkout; implementation and agent QA use
that same workspace until the accepted commit has been published. Runner then
removes the local worktree while retaining the branch. Patch handoff files and
manifests are not generated; the branch and GitHub pull request are the recovery
path.

### Repository references

`repository_references` optionally exposes existing secondary Git checkouts to
planner and reviewer contracts as evidence:

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

Planner and reviewer contracts receive every configured reference; custom
roles inherit the behavior of their base contract. Implementer and probe
contracts never receive references, and there is intentionally no per-role
reference list. Sandboxed Codex and Claude receive explicit read access while
write access is denied. Claude is also prevented from loading project
instructions from an added reference directory. Pi roles using references must
select `access: "host"`, because Pi cannot enforce a read-only reference root.
Host mode remains unrestricted and may see more than the configured list.

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
resolved Project and lane without changing GitHub.

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
decision is resolved.

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
| `Blocked` | Human: input, a non-retryable execution error, exhausted transient-provider retries, a closed-without-merge PR, or the maximum QA rejections were reached. |
| `Done` | Terminal success: planning completed or the pull request was merged. |

`Plan` and `Ready` are human scheduling boundaries for ordinary cards. When a
maintainer creates an unsigned card in either lane, Runner converts a Project
draft to an issue in the configured intake repository when necessary, then
authenticates that exact body, repository, and dependency snapshot for the
lane's rule-selected role before claiming it. A forged or content-modified
nonempty approval is never replaced, and a staged planner child cannot use this
path to bypass complete-batch release.
Moving a previously authenticated card back to `Ready` also authorizes its next
implementation attempt. Issue comments present at assignment time are included
as bounded historical context; comments added during an active attempt apply to
a later attempt.

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
4. An implementer works in an isolated branch/worktree. Success moves the same
   card to `Agent QA`. Runner stores the structured work and verification
   evidence on the card. Errors or requests for human input move it to
   `Blocked` and retain the intended retry lane. Ctrl-C is different: Runner
   uses a fresh bounded context to verify the retained workspace and returns
   the card to the interrupted role lane for the next run.
5. Runner constructs the candidate commit before QA. Acceptance validates its
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
   If the candidate has unresolved conflicts or fails
   `git diff --cached --check`, Runner records an actionable
   `candidate_validation` blocker without
   publishing file contents or paths, clears the unusable saved result, and
   retains the worktree. The recorded plain retry reruns implementation with
   that correction. Git identity and administration failures remain private
   workspace-integrity blockers.
   With `github_project.auto_merge: true`, this queues the PR for the separate
   integration action. Reconciliation asks GitHub to merge only after the PR
   owns its repository/base integration resource and still matches the latest
   reviewed base. Runner never uses an admin bypass. Automatic merge is
   disabled by default.
6. A QA rejection moves the card back to `Ready` and increments its rejection
   counter. With the generated `max_qa_rejections` of 3, the third consecutive
   rejection moves the card to `Blocked`, where `cortexium-runner retry` returns
   it to implementation after human attention. Detailed actionable rejection
   evidence is stored in a private mode-`0600` local record bound to the card ID and
   approved-content digest, then supplied to the next implementation and
   subsequent reviewer until acceptance. For every
   executable card, Runner also posts a bounded readable, idempotent QA comment
   to its issue. The private feedback remains the authenticated
   implementation-to-QA channel if comment publication is temporarily
   unavailable. A human comment is optional, and can clarify or add requested
   work before the next `Ready` attempt.
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
   against the latest base; a clean update or conflict returns it through
   implementation and QA before it may reclaim integration. With automatic
   merge disabled, Runner leaves base-update decisions to the human reviewer.
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
pre-review snapshot records the candidate HEAD and tree. If the complete
snapshot, HEAD, and tree remain unchanged after acceptance, Runner writes an
exclusive private publication record keyed by the commit and binding the
approved item/content, commit/tree, base ref/OID, repository, and full branch
ref. An exact retry reuses that tuple; a conflicting collision fails closed.

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
rejected. Agent QA captures the implementation and active checkout
before and after review; a mismatch stops the workflow before Runner commits,
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
unfinished attempt. Runner also fails closed before the next agent call if
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
is reported with its exact Project item IDs and must be reviewed and removed by
the operator before replanning; Runner never silently combines or deletes it.
Retries within the original staging operation reuse the exact batch instead of
creating duplicates and reject changed or partially released children rather
than treating them as approved. The destination lane determines their role.

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
    "planning_support": "standard",
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
Implementer and reviewer roles also accept `planning_support`: `standard` uses
the ordinary concise planning contract, while `high` asks the planner for
smaller coherent slices, explicit boundaries and assumptions, literal acceptance
criteria, and observable proof obligations. It never reduces correctness or
verification requirements and does not add fields to the plan schema. Change
it with `role edit ROLE --planning-support standard|high`.
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
  --planning-support high
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

### Implementer ladder

`implementer_ladder` is optional. When omitted, Runner always launches the
`ready_lane` rule's configured implementer role. When present, it lists complete
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
inside the native filesystem sandbox, npm-registry and loopback network access
for implementers, and a pinned `runner_browser` server restricted to loopback
pages with external name resolution disabled. The browser uses a temporary
profile and mock keychain; it cannot attach to the operator's normal browser
profile. Runner does not download Chrome. Chrome or Chromium 149+ is required
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

If Agent QA reports unavailable browser capability, stop repeated retries. A
capability-blocked review does not increment the QA rejection count. On macOS,
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
against the current harness invocation. When Runner recorded the originating
agent lane, retry by exact title, Project item id, or source URL:

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

In a terminal, `cortexium-runner retry --config /absolute/operator/path/runner.json` presents the retryable blocked
cards as an arrow-key menu. The command preserves the previous result as attempt
history and moves only the selected card to its recorded lane. A running Runner
checks newly available work on its next poll without waiting for unrelated
harness actions to finish.

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
Planner output destinations and QA rejection routes must lead to implementer
lanes; branch-refresh outcomes likewise return through implementation and QA.
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

Setup refuses to overwrite a differing installed skill unless `--force` is
explicit during initialization or `doctor --fix` is explicit afterward. In
either case, only the embedded Runner-managed skills are replaced. Privileged
launches disable native skill discovery and inject the selected embedded copy,
so a modified installed copy cannot change execution. Role configuration
rejects skills outside this pinned catalog.

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
source-and-evidence audit without running dynamic checks. If any concrete proof
questions remain, Runner starts a fresh focused-verification call containing
only unresolved proof keys, even when another key already failed; that call
reuses the smallest relevant existing checks. It does not create tests,
benchmarks, a custom harness, or broad diagnostics unrelated to a concrete diff
concern. Roles do not assume a browser
or any other interface. Broad or long-running checks belong only at the narrowest
integration boundary that needs them. Time-based behavior uses controlled clocks
or ordinary fixed-size simulation steps executed without wall-clock pacing;
real-time smoke checks remain short and are required only when real scheduling,
pacing, or presentation integration is part of the claim.

When integration or release evidence cannot be established on the delivery
cards, a final project-readiness card depends on the relevant delivery paths.
Earlier cards run focused checks; the readiness card runs the repository's
complete established local suite once and the smallest required real-entrypoint
smoke. That card is the explicit local go-live gate. It does not require CI, and
it does not invent browser, deployment, or test-framework work absent from the
project contract.

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
  Every terminal path—success, command failure, timeout, or cancellation—reaps
  the direct process and terminates its owned process group, including child
  processes, while preserving output captured before termination. Failed
  harness diagnostics stay in local Runner output and are not copied to GitHub
  Project fields. Capacity, retry disposition, and retry timing come only from
  explicitly parsed adapter-owned structured evidence; phrases in model output,
  stdout, or stderr have no recovery authority. Project results use fixed,
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
  `approve` command. Retrying blocked work requires the explicit `retry`
  command. The latter two commands have read-only `--dry-run` forms.
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
harnesses and one or two repetitions. It runs exactly four scenarios for each
selected harness: three planner contracts plus one seeded-regression reviewer
contract. Each selected harness also proves implementation while preparing its
reviewer fixture. `--smoke` keeps only the most demanding planner contract, so
the smoke path makes three model calls per selected harness. Use one smoke run
of the affected harness while iterating; reserve the full matrix
twice from one clean candidate (24 scenario executions) for initial qualification
or harness-facing contract changes. It makes paid model calls, performs no
GitHub writes, and streams sanitized `EVAL_CASE`
start/completion records plus one `EVAL_SUMMARY` per run. Retained mode-`0600`
JSONL contains only candidate, case identity, fixed outcomes, duration, and
harness-reported usage; prompts, results, and diagnostics are never retained.

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
the configured counter. Reported tokens include cache reads and writes; the
operator selects a ceiling appropriate for the chosen providers and can use a
cost ceiling when spend is the concern. Normal tests validate the coordinator, corpus, budget refusal, and
safe reporting but skip all paid calls. The two live runs are an explicit
operator-controlled launch gate, never ordinary CI. Runner does not certify
model quality or prevent any supported harness from serving any role.

Public bug reports and feature proposals are welcome through GitHub Issues and
start in assessment. External pull requests are not accepted for now. See
[`CONTRIBUTING.md`](../CONTRIBUTING.md) and [`SECURITY.md`](../SECURITY.md).

Licensed under the [MIT License](../LICENSE).
