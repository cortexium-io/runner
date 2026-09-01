# Claude Code quickstart

Claude Code is supported for planner, implementer, and reviewer roles. A
Claude-only installation can run the complete Runner workflow.

## Prerequisites

You need:

- macOS or Linux. Windows release artifacts are not published.
- A GitHub repository with Issues enabled and an `origin` remote.
- `git`, GitHub CLI (`gh`), Claude Code (`claude`), and `cortexium-runner` on
  `PATH`.
- Permission to create a GitHub Project for the chosen owner and to create
  issues, branches, and pull requests in the repository.

Authenticate both CLIs and give GitHub CLI Project scope:

```bash
gh auth login
gh auth refresh -s project
gh auth status
claude auth status
```

Confirm `claude auth status` reports `loggedIn: true`. Planner launches use a
disposable neutral directory with Runner's fixed read-only tool envelope.
Implementers start in a Runner-created task worktree; implementers and reviewers
use Claude's native OS sandbox by default, fail closed if it is unavailable,
and cannot fall back to unsandboxed Bash commands. Runner verifies repository
integrity before accepting evidence. Host access is available per role as an
explicit trusted-machine choice; read the README warning before enabling it.

## Create the Runner project

Run `init` from the repository root. In a terminal, omitted required choices
are collected interactively:

```bash
cortexium-runner init --create-project "My Runner project"
```

The guided flow asks for supported role harnesses, model and reasoning settings,
followed by the
maximum concurrent cards, base-update review policy, and whether pull requests
should wait for a human or use GitHub's automatic merge. Runner then
inspects the Git remote; only when it is actually empty does it ask whether to
push an existing local base branch or initialize it. By default the operator
config is `.cortexium/runner.json`; Runner ensures it is ignored, adding its
exact path to `.gitignore` when needed without staging or committing the
change. An external path is also supported.
Use the arrow keys and Enter for finite choices.
The Claude menu uses Claude Code's official rolling aliases, Opus and Sonnet;
it also offers the current harness-native selection and a custom-ID
escape hatch for pinned versions. Claude Code has no supported catalog-listing
command equivalent to Codex's local catalog, so Runner does not depend on its
private cache. Use `--dry-run` to preview the resulting local and GitHub
changes.

For scripts or repeatable setup, pass every required choice and disable
prompting explicitly:

```bash
cortexium-runner init \
  --non-interactive \
  --config /absolute/operator/path/runner.json \
  --owner YOUR_GITHUB_OWNER \
  --create-project "My Runner project" \
  --project-visibility private \
  --repository YOUR_GITHUB_OWNER/YOUR_REPOSITORY \
  --project-dir . \
  --harness claude \
  --model opus \
  --reasoning xhigh \
  --max-parallelism 1 \
  --base-update-review required \
  --auto-merge=false \
  --bootstrap-base-branch
```

The model and reasoning flags are expanded into explicit role definitions.
Add a role-specific flag such as
`--reviewer-model claude-opus-4-8` when one role should differ, or use
`cortexium-runner role edit ROLE` after initialization.

The safest default is `access: sandboxed` with `harness_config: isolated`.
Runner suppresses ambient Claude customization and uses Claude's native
filesystem/process sandbox. Set `harness_config: inherit` only when the role
must load native Claude user/project settings, hooks, tools, or MCP servers; the
same sandbox boundary still applies until `access` is explicitly changed to
`host`. Implementers run in the task worktree; reviewers use a private neutral directory with the
repository added read-only. Operator-home reads are denied except for the
assigned root and the implementer's npm cache. Claude still uses its existing
login.

Implementer and reviewer roles also inherit Runner's safe development tools:
bounded npm/loopback access and an isolated, headless `runner_browser` limited
to localhost pages. Verify the local prerequisites or opt out per role:

```bash
cortexium-runner doctor --config /absolute/operator/path/runner.json
cortexium-runner role edit reviewer \
  --config /absolute/operator/path/runner.json --no-safe-tools
```

`--max-parallelism 1` is the safest first run. Values above 1 execute only
cards whose declared dependencies are already `Done`; the planner is also
required to keep concurrently eligible cards non-overlapping and safe for
separate worktrees.

`--bootstrap-base-branch` is safe to include when the configured remote branch
already exists. When both repositories are empty, it creates an empty initial
commit without committing staged or untracked files, and pushes the configured base branch. It refuses to invent a
base branch when the remote already has other history. For a project-local
config, `init` modifies `.gitignore` but leaves that change for the user to
review and commit if desired.

The command creates or synchronizes the Project, writes the complete v2 config,
and installs readiness copies of the three bundled Runner skills. Privileged
launches disable native skill discovery and inject Runner's pinned embedded
copy. Init does not install or authenticate Claude Code.

## Prove readiness

```bash
cortexium-runner doctor --config /absolute/operator/path/runner.json --offline
cortexium-runner doctor --config /absolute/operator/path/runner.json
cortexium-runner doctor --config /absolute/operator/path/runner.json --probe-harnesses
```

All three commands must succeed. The explicit probe makes a real minimal model
call and validates structured output. It does not edit a file, so complete one
throwaway end-to-end card before relying on a new environment.

If initialization says an installed `runner-planner`, `runner-implementer`, or
`runner-reviewer` skill differs, Runner leaves it unchanged. Review the reported
path, then repair the bundled Runner skills selected by this config and repeat
the checks:

```bash
cortexium-runner doctor --config /absolute/operator/path/runner.json --fix --offline
```

Ignoring the project-local config is the default, not a requirement; users may
deliberately track it. Symlinked, worktree-local, and insecurely permissioned
configs are rejected.

## Create work and run it

Preview a small project plan first:

```bash
cortexium-runner plan --config /absolute/operator/path/runner.json
```

Enter the idea over as many lines as needed, then press Ctrl-D at the empty
input prompt. Runner previews the plan and asks whether to create and approve the
proposed cards. Choose Yes once; Runner stages the complete batch, reloads and
revalidates it, then releases it to the configured work lane. `--idea`,
`--idea-file`, and explicit `--create` remain available for scripts.

For a non-interactive script, create and approve the implementation cards in one command:

```bash
cortexium-runner plan \
  --config /absolute/operator/path/runner.json \
  --idea "Create a small, documented hello-world feature with automated tests. Keep it to one independently reviewable implementation item." \
  --create
```

Runner stages, revalidates, and releases the complete batch to the configured
work lane. To require a separate human review instead, replace `--create` with
`--stage-only`; Runner then prints a fingerprint-bound `--approve-staged`
command that reviews and releases that exact batch.

Planner-agent batches use a second authority boundary: the planner stages all
children unapproved, and a maintainer previews the complete batch with
`cortexium-runner approve --config /absolute/operator/path/runner.json --item PLANNING_SOURCE --dry-run` before rerunning the
same command without `--dry-run`, reviewing the refreshed exact batch, and
explicitly choosing Yes to release it.

Start the foreground Runner:

```bash
cortexium-runner run --config /absolute/operator/path/runner.json
```

Leave that terminal running. Watch the Project card move through `Ready`,
`In Progress`, `Agent QA`, and `PR Ready`. By default, Runner stops at the pull
request for human review. For an autonomous PoC, initialize with `--auto-merge`
or set `github_project.auto_merge` to `true`; Runner then asks GitHub to merge
after its configured requirements pass, without bypassing branch protection.
Runner never deploys. `run` auto-detects the default project-local config and
accepts `--config` for other locations. It polls continuously with built-in
interval defaults. After Runner changes workflow state, it checks immediately for newly
available work; the polling delay and idle backoff apply only when no progress
was made. `run --once` performs one cycle for diagnostics or scripts.

## If something blocks

Run `doctor --probe-harnesses` again and read the local Runner error. Common
failures are missing GitHub Project scope, a repository remote that does not
match `--repository`, harness authentication, an installed CLI missing a
required non-interactive or structured-output flag, or a configured model the
user cannot access. For implementation failures, also inspect whether the
required native tool or browser is installed and usable. A browser-capability
block does not count as a QA rejection. On Ctrl-C, Runner verifies and retains
the isolated worktree with a fresh bounded cleanup context and returns the card
to the interrupted role lane.
For a recognized Claude Code session limit, the blocked card and `status`
display the reported reset time and the exact `cortexium-runner retry` command.
After the limit resets, run it by exact card title, item id, or issue URL; bare
`cortexium-runner retry --config /absolute/operator/path/runner.json` opens an arrow-key menu in a terminal. Retrying
`Agent QA` reuses the existing implementation branch; retrying `Ready` repeats
implementation. Earlier capability claims are historical and the new harness
invocation must inspect the capabilities that are now available.

## Skills and personal configuration

The generated workflow needs only `runner-planner`, `runner-implementer`, and `runner-reviewer`.
The configured `roles.*.skills` list is an allowlist over Runner's pinned
embedded catalog. Claude roles without safe development tools use safe mode.
Safe-tool implementers and reviewers instead use empty setting sources, strict
Runner-only MCP configuration, disabled hooks and auto-memory, and explicit
built-in and MCP tool allowlists because Claude safe mode disables even an
explicitly supplied browser server. Sandboxed implementers use Claude's native
filesystem/process sandbox; sandboxed reviewers additionally deny writes to the
assigned repository. Host mode uses Claude's non-interactive permission bypass
only after the operator sets that role's `access` to `host`. Runner injects the
selected embedded skill instructions directly and verifies repository integrity
after every implementation and review.
