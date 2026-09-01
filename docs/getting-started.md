# Getting started

This guide gives a new maintainer a bounded first look at Runner. The initial
checkpoint is local, offline, and non-destructive. The connected steps are
explicitly separated so nothing reaches GitHub or an AI model by surprise.

Allow about 15 minutes for the offline checkpoint and another 15 minutes for a
connected dry run if the prerequisites are already configured.

## What you will prove

By the end of the offline checkpoint, you will have:

- built the CLI from the current source;
- confirmed its version and command surface;
- seen the generated workflow without creating GitHub resources; and
- identified the project-local or external operator-config path for a connected run.

The optional connected checkpoint previews Project initialization. It does not
create a Project, write a config, install skills, or invoke a model.

## Minimum prerequisites

Installation and authentication commands for Git, GitHub CLI, Runner, Codex,
Claude Code, and Pi are kept in the main [README](../README.md#prerequisites).

For the offline checkpoint:

- macOS or Linux;
- Git;
- Go 1.26.6, or the version declared in [`../go.mod`](../go.mod); and
- a clean checkout of this repository.

For the connected dry run, also prepare:

- GitHub CLI (`gh`) authenticated for the repository and Projects;
- a GitHub repository with Issues enabled;
- a local checkout whose `origin` identifies that repository;
- at least one supported AI CLI installed and authenticated through its native
  setup; and
- a config path outside every Runner implementation-worktree root.

The connected preview inspects CLI and GitHub readiness but does not call the
configured AI model.

## Checkpoint 1: build and inspect locally

From the repository root:

```bash
go build -o cortexium-runner ./cmd/cortexium-runner
./cortexium-runner --version
./cortexium-runner --help
```

Expected results:

- the build exits successfully and creates `./cortexium-runner`;
- the source build prints a VCS-derived version when Go can identify the
  checkout, or `cortexium-runner dev` when it cannot; and
- help lists `init`, `doctor`, `plan`, `approve`, `retry`, `run`, `status`,
  `metrics`, and `role`.

Nothing is installed globally, started in the background, or sent over the
network by these commands.

Inspect a command-specific help page:

```bash
./cortexium-runner init --help
./cortexium-runner doctor --help
```

Expected result: each command prints its usage and exits without changing the
checkout or GitHub.

You can stop here for the fully offline, non-destructive checkpoint.

## Checkpoint 2: preview a private Project

Run this only from the local checkout that belongs to the repository named in
`--repository`. Replace every uppercase placeholder. This scripted example uses
an external config path; interactive `init` defaults to `.cortexium/runner.json`:

```bash
./cortexium-runner init \
  --non-interactive \
  --dry-run \
  --config /absolute/operator/path/runner.json \
  --owner YOUR_GITHUB_OWNER \
  --create-project "Repository development" \
  --project-visibility private \
  --repository YOUR_GITHUB_OWNER/YOUR_REPOSITORY \
  --project-dir . \
  --harness codex \
  --reasoning high \
  --planning-support standard \
  --max-parallelism 1 \
  --base-update-review required \
  --auto-merge=false
```

Expected result: Runner validates the local repository, remote, GitHub access,
base branch, config destination, and selected harness. It then prints the
Project/configuration plan. Because `--dry-run` is present, it does not create
or modify GitHub resources, write the config, or install skills.

If the preview is wrong, change the arguments and rerun it. Do not remove
`--dry-run` merely to diagnose a failed prerequisite.

## Continue to a real local Runner setup

This step changes GitHub and writes the config. Continue only when the
preview identifies the intended owner, repository, private Project, workflow,
and config path.

Rerun the same `init` command without `--dry-run`, then perform static local
validation:

```bash
./cortexium-runner doctor \
  --config /absolute/operator/path/runner.json \
  --offline
```

Expected state after successful initialization:

- the GitHub Project has Runner's workflow lanes and lifecycle fields;
- the repository is linked and the intake label exists;
- the operator config has private permissions and, by default, a project-local
  config is ignored without Runner staging or committing the `.gitignore` change;
  and
- bundled skills assigned to the configured roles are installed for their
  harnesses.

Normal `doctor` adds GitHub and executable checks. The explicit
`--probe-harnesses` option makes one minimal live call per distinct configured
harness profile; it is not required for the offline checkpoint.

## Expected work states

When work is later admitted and Runner is started, cards follow these states:

| State | Expected owner or event |
| --- | --- |
| `Needs assessment` | A human decides whether a public request is valid. |
| `Backlog` | A human has accepted the request but has not scheduled it. |
| `Plan` | The planner creates a bounded set of staged implementation cards. |
| `Ready` | An implementation card is approved for an implementer. |
| `In Progress` | Runner has claimed the card for one role attempt. |
| `Agent QA` | A reviewer checks the exact implementation worktree and diff. |
| `PR Ready` | The accepted branch is published and awaits the human gate. |
| `Blocked` | Human input or an explicit retry decision is required. |
| `Done` | Planning finished, or the pull request was merged or closed. |

Creating an ordinary card directly in `Ready` authorizes its exact current
snapshot for the implementer; Runner converts it to an issue in the configured
intake repository when necessary, then signs it before claim. Planned children
remain drafts while awaiting approval and become issues as part of the approved
batch release. Issue comments present at assignment time are supplied as bounded
historical context. Agent QA
posts readable requested-change notes to that issue and retains an authenticated
private copy, so its feedback is sufficient for a retry without a human comment.

A QA rejection returns the card to `Ready` until the configured rejection limit
is exhausted. If an optional implementer ladder is configured, each persisted
QA rejection advances to its next role profile; the final profile is reused for
any remaining allowed attempts. Errors and requests for input fail closed in
`Blocked`; they do not advance the ladder or become implicit approval.

## Safe reset

For the offline checkpoint, stop any foreground command with Ctrl-C and delete
only the `cortexium-runner` binary created in this checkout. No GitHub or model
state exists to reset.

The connected `--dry-run` also creates no state. If you completed real
initialization:

1. Stop every Runner process using the config.
2. Keep or archive the Project if it contains real work. Runner intentionally
   provides no destructive Project-reset command.
3. Remove the config only if you no longer want that Runner instance.
4. Remove an external worktree root only after `git worktree list` confirms it
   contains no active task worktrees.
5. Delete a throwaway GitHub Project manually only after confirming its owner,
   number, and contents in GitHub.

Never delete the repository checkout, `.git`, a broad parent directory, or an
unverified worktree root as a reset shortcut.

## Next reading

- [Operator reference](operator-reference.md) for complete behavior and flags.
- [Architecture](architecture.md) for trust and package boundaries.
- [Maintainer setup](open-source-maintainer-setup.md) before making a repository
  public or publishing a release.
- [Claude Code quickstart](claude-code-quickstart.md) when using Claude Code for
  planning or review.
