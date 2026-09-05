# Open-source maintainer setup

Use this checklist before changing the repository to public.

## Repository settings

1. Keep Issues enabled.
2. Under **Settings → General → Features → Pull requests**, choose
   **Collaborators only**.
   Maintainers and authorized automation can create internal PRs; external PRs
   remain disabled.
3. Keep GitHub Actions enabled for `.github/workflows/ci.yml`.
4. Protect `main`: require a pull request and the lean `PR check` before merge.
   Keep the two-platform matrix and release-candidate job manual so they run
   once for an exact release candidate rather than for every PR.
5. Configure the protected `release` environment and scoped release credential
   described below. A pushed version tag never publishes by itself.
6. Enable private vulnerability reporting under **Settings → Security →
   Advanced Security** when the repository is public.

Issue forms apply `needs-assessment`. Runner creates that label when `init`
creates or synchronizes a Project.

## GitHub Project

Create a dedicated Project for the repository, or choose an existing Project
that is intended to control this Runner instance. Its primary view must use the
Kanban board layout with the configured workflow lanes. Board cards show
`Runner Activity` and `QA Failures` by default while preserving unrelated
visible card fields. The internal `Runner Phase` recovery and `Runner
Transition` lock fields remain hidden.
`init` generates:

| Status | Owner |
| --- | --- |
| `Needs assessment` | Human assessment; never executable. |
| `Backlog` | Human scheduling; approved but inactive. |
| `Plan` | Planner role. |
| `Ready` | Implementer role. |
| `In Progress` | Runner claim state. |
| `Agent QA` | Reviewer role. |
| `PR Ready` | Human pull-request gate, or GitHub requirements when automatic merge is explicitly enabled. |
| `Blocked` | Human input required after an error, closed-without-merge PR, request for information, or exhausted QA rejections. |
| `Done` | Planning finished or the PR was merged successfully. |

Preview adopting and synchronizing an existing board:

```bash
./cortexium-runner init \
  --config /absolute/operator/path/runner.json \
  --owner YOUR_GITHUB_OWNER \
  --project-number YOUR_PROJECT_NUMBER \
  --repository YOUR_GITHUB_OWNER/YOUR_REPOSITORY \
  --project-dir . \
  --harness codex \
  --reasoning high \
  --task-granularity standard \
  --max-parallelism 1 \
  --base-update-review required \
  --auto-merge=false \
  --dry-run
```

For a guided new setup, run `./cortexium-runner init --config
/absolute/operator/path/runner.json --create-project "Project title"` in a
terminal. Runner uses arrow-key menus for finite choices and asks for supported
harness/model/reasoning selections, downstream task sizing, and the maximum
number of independent cards that may run at once. For
scripts and repeatable setup, use `--non-interactive` and pass every required
option.

For a newly created Project, Runner replaces GitHub's initial views with one
fresh board view so view-only settings such as column limits are not inherited.
When adopting an existing Project, Runner preserves its views and only ensures
that a board view exists.

`init` performs its config-path, Git repository, GitHub remote/base-branch, CLI
authentication, repository, and Project-access checks before it changes GitHub.
Review the dry run, then rerun without `--dry-run`. Initialization synchronizes
the Project fields and statuses, writes the config, and installs bundled role
skills. When only one supported harness executable is available, `init` can
persist it for omitted roles; with multiple available harnesses, select one
shared default or pass `--harness`. Role-specific flags override that value.
Choose `--base-update-review required` during creation so every PR refresh
returns through implementation and QA before publication. Keep
`--auto-merge=false` for the normal maintainer gate; use `--auto-merge` only for
an intentionally autonomous repository. After
changing the config, use the same preview/synchronize pair:

```bash
./cortexium-runner init --config /absolute/operator/path/runner.json --dry-run
./cortexium-runner init --config /absolute/operator/path/runner.json
```

An empty remote has no real default branch even when GitHub is configured to
name the first branch `main`. Without authorization, `init` reports this
condition and points to `--bootstrap-base-branch`. With that option, it pushes
an existing local base branch or initializes and pushes a new one. The initial
commit is empty; other staged and untracked files remain untouched. It refuses to bootstrap a
missing base when the remote already contains other history.

Interactive initialization defaults to `.cortexium/runner.json` and ensures it
is ignored, adding its exact path to `.gitignore` when needed without staging or
committing that change. Users may instead choose an external path or deliberately
track the config. Runner rejects worktree-local, symlinked, incorrectly owned,
or group/other-writable config.

Configuration is additive by default. To remove obsolete empty Status columns,
run `init --config /absolute/operator/path/runner.json --prune --dry-run`, review the active and
archived usage counts, then rerun without `--dry-run`. An occupied Status option
blocks pruning; no cards or lifecycle fields are deleted.

Then prepare and verify the local machine:

Install and authenticate at least one of Codex CLI, Claude Code, or Pi CLI
using that product's normal setup flow. Runner does not install AI harnesses or
edit their saved permission configuration. Each harness can fill planner,
implementer, and reviewer roles. Work roles use sandboxed access with isolated
harness configuration by default; live probes always remain constrained. Host
access and ambient configuration inheritance are separate, explicit per-role
opt-ins. Pi has no native OS sandbox for shell/edit tools, so Pi implementation
and review require
`"access": "host"`; interactive init confirms it and non-interactive init
requires `--implementer-access host` and/or `--reviewer-access host`.
Pi also requires host access for `"harness_config": "inherit"`.
Sandboxed Codex uses scoped permission profiles with minimum runtime reads and
only the assigned repository/worktree. Sandboxed Claude denies operator-home
reads except for the assigned root and the implementer's npm cache.
Implementations run in the task worktree; reviewers use a private neutral
directory with a new detached checkout of the exact candidate added read-only.
Codex and Claude implementer and
reviewer roles inherit the bounded npm/loopback and isolated local-browser
profile unless `safe_tools` is explicitly disabled. The browser package uses
separate host-owned npm state that the role sandbox cannot write. Pi implementer and reviewer
roles receive the same pinned loopback-only browser through a temporary
Runner-generated extension, while their shell/edit boundary remains explicit
host access.

Use `harness_config: isolated` unless the role genuinely needs tools, MCP
servers, rules, skills, plugins, or hooks from the operator's native harness
setup. `access: host` plus `harness_config: inherit` is unrestricted agent
execution under the Runner OS account. `doctor` prints the effective policy for
every role and labels that combination as unrestricted.

```bash
./cortexium-runner doctor --config /absolute/operator/path/runner.json --offline
./cortexium-runner doctor --config /absolute/operator/path/runner.json
./cortexium-runner harness check --config /absolute/operator/path/runner.json
./cortexium-runner run --config /absolute/operator/path/runner.json
```

`harness check` exercises every configured planner,
implementer, and reviewer profile against a private temporary Git repository.
Add `--browser` when browser-backed verification is required. These checks do
not exercise GitHub card movement or PR publication, so complete one throwaway
end-to-end card when qualifying those integration paths on a new machine.
Use `doctor --probe-harnesses` instead when only a minimal authentication,
model-selection, invocation-flag, and structured-output probe is needed.
`run` auto-detects the default project-local config and accepts `--config` for
other locations. It polls until interrupted; use `run --once` only for a single
diagnostic or scripted cycle.

Run the complete deterministic release gate—and optionally repeat Doctor plus
the real model probe against an existing Project—with:

```bash
sh scripts/test-release-readiness.sh
sh scripts/test-release-readiness.sh \
  --live-config /absolute/path/to/runner.config.json
```

The harness runs the complete deterministic suite once with the race detector,
including fresh/empty and existing initialization paths backed by local Git
repositories and GitHub command doubles. It does not create or delete live
GitHub resources.

Maintainers can also exercise the exact planner, implementer, and reviewer
adapters against temporary local Git repositories. This opt-in check makes paid
model calls and performs no GitHub writes:

```bash
CORTEXIUM_RUNNER_LIVE_HARNESSES=codex \
  go test ./internal/execution -run '^TestLiveWorkspaceWriteHarness$' -v

# Run one browser-capability probe through Runner's pinned, isolated,
# loopback-only browser profile. This is paid and opt-in.
CORTEXIUM_RUNNER_LIVE_BROWSER_HARNESSES=codex,claude \
  go test ./internal/execution -run '^TestLiveReviewerBrowserHarness$' -timeout 4m -v

CORTEXIUM_RUNNER_LIVE_HARNESSES=codex,claude,pi \
  go test ./internal/engine -run '^TestLivePlannerWorkItemContractAcrossHarnesses$' -timeout 45m -v

candidate=$(git rev-parse HEAD)
sh scripts/test-agent-behavior.sh --candidate "$candidate" --repeat 1 --smoke \
	--codex-model gpt-5.6-luna --claude-model sonnet --reasoning medium \
	--pi-model lmstudio/qwen/qwen3.8-27b --allow-pi-host --max-tokens 1000000 \
  codex,claude,pi

sh scripts/test-agent-behavior.sh --candidate "$candidate" --repeat 2 \
  --pi-model lmstudio/qwen/qwen3.8-27b --allow-pi-host --max-tokens 3000000 \
  codex,claude,pi
```

The behavioral script accepts any non-empty subset of `codex`, `claude`, and
`pi`, plus one or two repetitions from one clean candidate SHA. Optional
`--codex-model`, `--claude-model`, and `--reasoning` overrides make an explicit
model tier reproducible without changing Runner defaults. A tool-capable
Pi model and `--allow-pi-host` are required when Pi is selected. The evaluator
uses Pi's fixed host-access profile in a disposable worktree because Pi has no
native OS sandbox; use a trusted machine or external sandbox. Codex and Claude
remain natively sandboxed. Each selected harness evaluates
three planner contracts and one seeded-regression reviewer contract; each
selected harness also proves its implementer contract while preparing that
fixture. `--smoke` keeps only the most demanding planner contract, for three
model calls per harness. Use one smoke repetition of only the affected harness
while iterating. Reserve the full
three-harness, two-run command above for initial qualification or changes to
skills, prompts, schemas, execution profiles, or harness adapters. The script
streams sanitized `EVAL_CASE` progress and `EVAL_SUMMARY` aggregates and retains
only a private JSONL summary. Per-case and aggregate time are always bounded.
The required token ceiling and optional cost ceiling fail closed when reported
usage is unavailable. Reported tokens include cache reads and writes, so choose
the ceiling from the selected providers' observed counters; use the cost ceiling
when spend is the concern. Normal tests and Doctor never run these paid probes.

Harness and model quality remain an operator decision. These probes provide
evidence for that decision; they do not create a Runner allowlist or prevent a
user from assigning any supported harness to any role.

Runner installs and verifies its three bundled role skills, disables native
skill discovery and automatic project-instruction discovery for isolated
launches, and injects the selected pinned embedded instructions. Reviewers
can inspect repository rules through their explicit read root. Implementers run
inside their assigned worktree. Sandboxed roles use the native Codex or Claude
isolation boundary. Isolated configuration retains fixed tools and suppressed
ambient customization; inherited configuration deliberately loads native
customization. A host-access role is not contained from other local resources.
Mandatory Runner integrity checks follow both modes. Review operator-selected
models, access, permissions, and skills before sharing a machine or reusable
environment image.

Do not run the same Project on multiple machines. The local process lock cannot
provide a distributed GitHub Project claim.

## Assessment and execution authority

Opening or editing a public issue never grants execution authority. A maintainer
must inspect clarity, safety, scope, repository ownership, and dependencies.
Preview the exact authenticated action, then approve it:

```bash
./cortexium-runner approve --config /absolute/operator/path/runner.json --item ISSUE_URL --dry-run
./cortexium-runner approve --config /absolute/operator/path/runner.json --item ISSUE_URL
```

Approval removes `needs-assessment`, records `Runner Approval`, and moves
the item to `Backlog`. A maintainer later moves it to `Plan` when it should run.
Moving an ordinary unsigned card to `Plan` asks the planner to shape that exact
snapshot; moving one to `Ready` is the explicit implementer authorization.
Runner signs the snapshot before claiming it. These are the only direct agent
intake lanes, and unreleased planner children cannot bypass complete-batch
approval. The same events can be created while Runner is active with
`add plan --title TITLE --body-file PATH` and
`add ready --title TITLE --body-file PATH`. Editing content covered by an
existing nonempty
approval invalidates it rather than causing Runner to replace it. Approval and
other multi-field transitions set the hidden `Runner Transition` lock before
their first write and clear it only after the final status and authority agree.
Ordinary cards therefore remain in their real lane during the update. After an
interruption, Runner clears a completed lock in place; an incomplete or invalid
transition moves the card to genuine assessment without leaving executable work.
An ordinary card can declare cross-batch prerequisites before authorization with
an exact `## Dependencies` bullet list of Project item IDs or GitHub issue URLs.
Only Runner-authenticated successful outcomes satisfy those references; moving a
prerequisite to `Done` manually does not.

Every planner path first stages one identified child batch. A 1,000-item
emergency ceiling bounds pathological model output and staging loops but never
guides task sizing. Interactive direct planning and scripted `plan --create`
then revalidate and release that complete batch.
For a Project planning source, preview the exact complete batch and destination with
`approve --item PLANNING_SOURCE --dry-run`, then rerun without `--dry-run`.
The second command requires a terminal, repeats the exact preview, and defaults
to No until the maintainer explicitly chooses Yes. For
a scripted direct CLI plan, use `plan --create` to create and release the
complete batch. Use `plan --stage-only` when a separate review is desired, then
run the displayed fingerprint-bound `plan --approve-staged` command in a
terminal and explicitly accept the repeated preview. Runner revalidates the
accepted preview before release; changing it leaves the children non-executable.
Interrupted recovery reuses the exact batch and rejects changed or partial
children instead of accepting duplicates. Each child retains the original
request and project-wide success contract as well as its local criteria.
Implementation success goes to
`Agent QA`; QA rejection returns it to `Ready` until
`max_qa_rejections`, then `Blocked`. A value of 3 means the third rejection
blocks the card. QA acceptance publishes a PR and moves the card
to `PR Ready`, then removes the local task worktree while retaining
the task branch. A plan with unresolved `open_decisions` creates no cards and
moves the planning item through `needs_input` to `Blocked`.

Runner records the originating agent lane on new blocked transitions. After the
human or environmental blocker is resolved, move the card to `Ready` to retry
through implementation while preserving the recorded result and QA failure
count. To restore its recorded lane instead, preview and apply the CLI retry:

```bash
./cortexium-runner retry --config /absolute/operator/path/runner.json --item "Exact card title" --dry-run
./cortexium-runner retry --config /absolute/operator/path/runner.json --item "Exact card title"
```

A maintainer can merge the PR to finish it. Closing without merge moves the card
to `Blocked` and does not release dependent work. To request revisions,
comment on the PR and move the card from `PR Ready` to `Ready`; Runner imports
the feedback, resets QA rejections, and recreates the deterministic task
workspace from the retained branch. Comments alone do not restart work.

Manual-review PRs stay at the human gate when another merge advances the base;
Runner does not create update churn on the maintainer's behalf. Automatic merge
is available only when `github_project.auto_merge` is explicitly true. In that
mode, reconciliation selects one PR per repository/base, updates only that
candidate without force-pushing, and sends either a clean update or conflict
back through implementation and QA. Direct PR-head mutation also invalidates
QA. After a clean reviewed candidate is current, Runner asks GitHub to merge
after repository requirements pass and never bypasses them. Integration is
lifecycle reconciliation, not an agent attempt: it consumes no
`max_parallelism` slot or admission budget. Runner never deploys.

Do not make the repository public until the default branch contains the MIT
license, contribution and security policies, issue forms, and passing CI.

## Publishing a version

Before the first release, protect `main` with pull requests and the lean
`PR check`. When another maintainer is available, also require an approving
review. On the exact reviewed release candidate, manually dispatch CI once and
require both platform checks plus the release-candidate job to pass as launch
evidence. Do not make those manual jobs permanent PR requirements. Then create
a **lightweight** version tag from that reviewed commit on `main` and push it.
Annotated tags and versions outside strict `vMAJOR.MINOR.PATCH` syntax are
rejected.

Before creating the first version tag, add an active repository tag ruleset for
`refs/tags/v*` that restricts updates and deletions with no bypass actors. Also
enable immutable releases under **Settings → General → Releases**. The ruleset
closes the check-to-publication interval; release immutability locks each
published tag and asset set and supplies GitHub's release attestation.

```bash
git tag v0.1.0
git push origin v0.1.0
```

Create an environment named `release` under **Settings → Environments**. It must:

- restrict deployment branches to protected branches so only protected `main`
  can dispatch publication; and
- when the GitHub plan and maintainer roster support deployment reviewers,
  require a designated maintainer, prevent self-review, and prevent
  administrators from bypassing the protection rules.

The current private single-maintainer boundary cannot provide independent
deployment approval. In that boundary, the administrator's explicit manual
workflow dispatch is the human release approval, and the environment's
protected-branch rule remains mandatory. Do not claim independent review. Add
the reviewer, self-review, and no-bypass controls before granting another
operator release authority or when the repository plan exposes them.

Do not create a release PAT or `RELEASE_TOKEN` secret. The isolated publication
job uses GitHub's ephemeral repository-scoped workflow token. Only that job has
`contents: write`; it checks out no repository source and runs no dependency,
test, or build code.

GitHub must apply those environment protections before the publication job is
sent to a runner. Immediately before every real release, a maintainer must
inspect the live `main` ruleset, required checks and any configured reviews, the
environment branch and reviewer controls available for the supported operator
boundary, and the publish job's permissions. The current repository settings
are not proven by source-controlled tests, so the first real release is blocked
until that inspection succeeds.

From the Actions page, choose the **Release** workflow while viewing `main`, use
**Run workflow**, keep the branch set to `main`, enter the pushed tag (for
example `v0.1.0`), and start the run. The workflow rejects every dispatch whose
recorded ref is not exactly `refs/heads/main`. Its read-only build resolves the
remote tag, rejects missing or annotated tags, proves the tagged commit belongs
to the exact reviewed dispatch commit, checks out that exact commit without persisted
credentials, runs release readiness, and builds the four platform archives plus
`SHA256SUMS`.

The protected publication job downloads only the immutable artifact ID produced
by that build, fails on a digest mismatch, and rechecks the exact five-file asset
set, all checksums, and the remote tag identity. Its fixed metadata check uses
the ephemeral workflow token only to read the artifact record; the final
`gh release create` step exports the same job token as `GH_TOKEN`. The isolated
publish job performs no checkout, dependency installation, tests, or build.
Treat a failed workflow as a failed release. Do not move the tag or assemble and
publish a different asset set manually under the same tag.
