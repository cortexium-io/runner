# Runner local operator guide

This guide describes the local engine shipped by `cortexium-runner`. GitHub
Projects is not required. The first replacement release is still a local
candidate until R5 qualification and an explicitly authorized publication.

Release archives include this guide as `OPERATOR.md` and a `runner.example.json`.
Extract them for offline use. The installer copies only the binary; it does not
initialize state, install skills, change services or migrate an old installation.

Runner records work locally, asks for exact approvals, implements in isolated
workspaces and reviews independently. `status` explains what needs you and what
can proceed. Only confirmed integration completes delivered work. Local delivery
retains a reviewed candidate at **Awaiting delivery**.

## 1. Build and configure fresh authority

Install the Go version in `go.mod`, Git and Codex CLI separately. Authenticate Codex
with your account and select a supported model/reasoning pair. This release
qualifies sandboxed Codex; it does not install tools, credentials or a service.
For GitHub delivery, also install `gh` and authenticate to github.com with access
to the destination repository. Projects access is unnecessary.

From the Runner checkout:

```sh
go build -o /absolute/tools/cortexium-runner ./cmd/cortexium-runner
/absolute/tools/cortexium-runner help
```

For an extracted archive, run its `cortexium-runner --version` and `--help`
directly. Source builds report `dev`; release builds report the injected version.
The private store format is **15**. Legacy Project configuration and experimental
store formats are deliberately rejected; there is no in-place migration. Keep
old binaries with their state/backups, reconcile any outstanding remote effects,
then use fresh config, state and workspace paths with new approvals. Never point
both runtimes at the same work or import old approval authority.

The output directory must exist. Use a committed target repository with a local
base branch and configured Git author identity. Uncommitted checkout edits are
excluded. Create a private JSON configuration at `/absolute/runner/runner.json`:

```json
{
  "format": "runner-local-v1",
  "repository": {
    "path": "/absolute/project",
    "base_ref": "refs/heads/main",
    "base_freshness": "recorded"
  },
  "state_dir": "/absolute/runner/state",
  "worktree_dir": "/absolute/runner/worktrees",
  "profiles": {
    "build": {"harness": "codex", "model": "gpt-6.1-sol", "reasoning": "high", "timeout": "20m"},
    "review": {"harness": "codex", "model": "gpt-6.1-sol", "reasoning": "high", "timeout": "20m"}
  },
  "roles": {"planner": "build", "implementer": "build", "reviewer": "review"},
  "policy": {
    "merge": "human",
    "max_parallelism": 1,
    "max_repairs": 1,
    "max_transient_retries": 1,
    "max_base_refreshes": 1,
    "implementation_ladder": []
  },
  "delivery": {"mode": "local"}
}
```

Replace all paths with canonical absolute paths without symlink components.
Repository, state and worktree roots must be separate and non-overlapping; keep
the config outside them. Use fresh state/worktree paths, make their parent
directories first, and restrict config access with `chmod 600`.

Choose delivery **before initialization**. To publish and integrate with GitHub,
replace `delivery` with `{"mode":"github-draft","repository":"OWNER/REPO"}`.
Approval then authorizes the exact candidate push and one draft PR in that
destination. Repository, roots, destination and base cannot change through policy
editing. Local mode has no integration receipt or manual completion command, so
dependent work cannot complete there.

For the remaining commands, define this shell helper in each terminal:

```sh
runner() { /absolute/tools/cortexium-runner --config /absolute/runner/runner.json "$@"; }
runner init
runner init --apply
```

The first command previews; the second checks Codex capabilities and creates
drained state. With no checks supplied, it says quality is unconfigured. No model
has run. Complete the agreement below before submitting work.

## 2. Agree on quality before work

The repository owner selects required checks and policy sources. The author or
planner defines the task's expected behavior, failure cases and verification
brief **before implementation**. The implementer reuses or extends the owning
suite; independent review checks adequacy and unnecessary complexity. A good
test detects a credible defect, checks observable behavior and fails for the
right reason. Existing sufficient evidence can mean no new test. Avoid duplicate
fixtures, implementation-mirroring assertions and arbitrary sleeps. Consolidate
equivalent cases when their distinct guarantees remain covered.

Review starts by auditing the exact candidate and retained evidence without
running repository commands. If a specific proof gap remains, focused verification
runs targeted checks in a disposable copy without changing source/tests. A known
defect can trigger bounded repair; missing evidence alone is not a code defect.
This keeps a second full suite from becoming an automatic review ritual.

Create `/absolute/runner/quality.json` using the repository's actual commands and
committed policy files. This example checks a tiny text-only rehearsal repository;
replace it for real work:

```json
{
  "definition": "Satisfy the approved outcome; preserve unrelated behavior; reuse sufficient existing tests.",
  "sources": [],
  "checks": [{
    "id": "greeting",
    "command": "test -s greeting.txt",
    "directory": ".",
    "prerequisites": ["sh"],
    "timeout": "1m"
  }]
}
```

For GitHub delivery, also include `required_ci`, with each exact check name and
its observed GitHub app identity, for example
`[{"kind":"check","name":"fixture","app_id":15368}]`. That identity is an
example from the qualification repository, not a universal setting. Commit
statuses use `kind: "status"` and omit `app_id`. An empty list is unconfigured,
not permission to skip CI; duplicate matching results require inspection.

```sh
runner quality preview --file /absolute/runner/quality.json
runner quality check --file /absolute/runner/quality.json --preview BASELINE_PREVIEW
runner quality preview --report REPORT_ID
runner quality apply --report REPORT_ID --preview AGREEMENT_PREVIEW
runner quality show
```

Use `preview` from each JSON preview and `id` from the returned report. Checks run
in fresh source copies with private HOME/TMPDIR, fixed PATH and no inherited
credentials or network. Declare tools in `prerequisites`; keep generated output
under `.runner-output/`. Exit 0 passes, 1–124 reports failure, 125–127 signals an
unavailable prerequisite. Source/Git modifications, timeouts and unresolved
processes are unavailable. A report is evidence, not automatic agreement approval.

For an observed, stable baseline defect, the owner may supply an exceptions file:
`[{"check_id":"legacy","rationale":"Existing unrelated defect","scope":["legacy.txt"]}]`.
Pass `--exceptions PATH` to both report preview and apply. The candidate must pass
or reproduce the exact failure with scoped files unchanged; variable timestamps
or diagnostics prevent matching. Never weaken an assertion to accept a failure.

New agreements affect new work. Existing tasks retain their revision until
`task adopt-quality ID --version N --preview ADOPTION_PREVIEW`, shown by
`task show`. Adoption revokes the old approval/candidate and creates an unapproved
version; inspect and approve it again. `quality show --revision DIGEST` reads old
agreements. Released plans cannot adopt as a batch. Required checks run after
implementation without a model call; saved complete observations are reused.

## 3. Enter Ready work or ask for a Plan

For one specified change, create a task file such as:

```markdown
## Outcome
Change the greeting to hello.
## Scope
greeting.txt only.
## Acceptance
The file contains exactly hello followed by one newline.
## Verification
Read the file and compare the exact bytes. The repository check covers presence;
this tiny text change needs no new test suite. Do not change unrelated files.
```

```sh
runner task add --file /absolute/runner/task.md
runner task show TASK_ID
runner task approve TASK_ID --version N --preview APPROVAL_PREVIEW
runner run
```

Copy the ID from `add` and the exact version/approval command from `show`, after
reviewing content, quality revision, base and destination. The displayed preview
is not approval by itself. Missing Verification requires a configured planner and
adds a preparation call. A sufficient Ready brief normally needs implementation
and audit; focused review or repairs can add calls.

For a larger request, use `task add --plan --file REQUEST.md`, inspect and approve
as above. This approval authorizes **planning only**. In another terminal:

```sh
runner task show PLAN_ID
runner task release PLAN_ID --version N --preview RELEASE_PREVIEW
```

Release only after inspecting the full proposed cards, verification briefs and
dependencies. It authorizes that exact batch. Independent children can run within
capacity; a dependent waits for its exact prerequisite version's confirmed
integration. A draft PR or accepted local candidate does not release it. The
parent completes only after all children integrate.

If a question appears, put the answer into the complete request file, then:

```sh
runner task replace TASK_ID --version N --file /absolute/runner/clarified.md
runner task show TASK_ID
runner task approve TASK_ID --version NEW_VERSION --preview NEW_APPROVAL_PREVIEW
```

The original request/evidence remain. Editing the file alone changes nothing.
Replacing text retains the quality revision. A released plan cannot be replaced;
stop the batch and submit a new plan if its requirements must change.

## 4. Follow work and recover deliberately

`run` stays in the foreground. Use a second terminal for:

```sh
runner status
runner status --watch 2s
runner task show TASK_ID
runner task show TASK_ID --json
```

Status groups **Needs you**, **Working**, **Waiting**, **Retained** in that order.
It shows cause, actual profile, elapsed time, evidence, remaining allowances and
next action. JSON supplies the same advice. Watch only observes; Ctrl-C stops the
watch, not the coordinator. `task show` includes the request, questions, proof,
all attempts, candidate and delivery history. Read `Recovery role`, `Preserved`
and `Repeats` before recovery.

| What you see | What to do |
| --- | --- |
| Question or rejected requirement | Clarify the request, replace its displayed version, inspect and approve the new version. |
| Dependency wait | Inspect the named prerequisite/version and finish its integration; do not reapprove or remove the dependency to bypass it. |
| Capacity or retry backoff | Wait; unrelated eligible work continues within configured limits. |
| Interrupted audit | Use the displayed `task resume ID --attempt ATTEMPT_ID`. It refuses a repeat unless you add `--allow-new-invocation`; that explicitly permits one call of the interrupted role with its pinned profile. The candidate remains retained. |
| Saved result awaiting continuation | Use the displayed resume or run command. Integrity checks and deterministic continuation reuse the saved result without another model call. |
| Unknown process ownership | Inspect the recorded process/evidence and resolve ownership before reconciliation. Do not replay work or kill an unrelated PID. |
| Failed/unavailable repository check | Inspect the retained check log. Correct the problem or agreement and explicitly adopt/approve when required; this is not an automatic code-repair loop. |
| Repair, retry, refresh or invocation limit | Inspect consumption and next profile with `config explain`; explicitly change/apply the relevant limit if more work is warranted. Counters never reset. |

To stop admitting work without canceling active actions:

```sh
runner drain
runner status
runner run
```

Drain waits for the coordinator to settle; it does not wait for every task to
finish. The next explicit `run` reconciles retained work and clears drain. Use
`task stop ID` to revoke a task or a released plan's batch. Cancellation can take
time, and a sent remote effect may still settle. Evidence and remote artifacts
remain; stopping does not delete branches or close PRs.

## 5. Change execution policy

`config edit` provides an interactive editor over the same JSON file. `done`
previews and offers to save; `cancel`, EOF or declining save leaves it unchanged.
For repeatable commands, preview and save the same settings:

```sh
runner config edit --set parallelism=2 --set repairs=1 --set transient-retries=1 \
  --set base-refreshes=2 --set invocations=8 \
  --set 'profile.careful=gpt-6.1-sol high 30m' --set escalation=careful
runner config edit --set parallelism=2 --set repairs=1 --set transient-retries=1 \
  --set base-refreshes=2 --set invocations=8 \
  --set 'profile.careful=gpt-6.1-sol high 30m' --set escalation=careful \
  --save --preview SAVE_PREVIEW
runner config explain
runner config apply --preview APPLY_PREVIEW
```

Save changes the file; apply changes running policy. `config explain --json`
uses **`apply_preview`**, while task JSON uses **`approval_preview`** or
**`release_preview`**. Stale previews are refused. Saved but unapplied edits are
visible in status and block new mutating commands except drain/stop and policy
application; already admitted work retains the applied policy.

| Setting | Meaning |
| --- | --- |
| `profile.NAME=MODEL REASONING TIMEOUT` | A complete Codex profile. Role settings `planner`, `implementer`, `reviewer` select its name; `planner=none` removes planning. |
| `parallelism=1..16` | Global admission limit shared by roles and delivery actions. Lowering it does not cancel active work. |
| `repairs=N` | Shared bounded allowance for confirmed rejection, eligible CI failure and supported text-conflict repair. |
| `transient-retries=N` | Bounded retry of recognized transient failures; interruptions need explicit recovery. |
| `base-refreshes=N` | Allowance for changed-base verification cycles, separate from code repairs. |
| `invocations=N` or `unlimited` | Per-task model-call ceiling; admission consumes a call even if it later fails. |
| `escalation=careful,next` or `none` | Successors after the starting implementer. Confirmed review rejection advances the ladder; the last profile remains selected within the repair limit. CI/conflict repair and transient retry do not independently advance it. |
| `merge-policy=human` or `automatic` | GitHub merge authority; see below before applying automatic policy. |

The example's successor keeps the same model/reasoning and gives it more time;
select a different supported model/reasoning if desired. Active attempts and
pending retries/resumes retain their admitted profiles even if those names change.
Explain shows pinned work and effects on future admissions. Apply starts no
coordinator and grants no fresh allowance to an exhausted counter.

## 6. Deliver and inspect the result

With `github-draft`, accepted work publishes one draft, verifies the current
remote base, refreshes if needed, synchronizes the same draft and assesses pinned
CI once. A changed candidate runs checks and independent review again. Supported
text conflicts and explicit CI failures use the shared repair budget; unavailable
or incomplete evidence requires inspection. Local mode stops with its retained
candidate and requires no GitHub connection.

```sh
runner task show TASK_ID
runner task delivery TASK_ID --reconcile
runner task verify-base TASK_ID
runner task assess-ci TASK_ID
runner task integrate TASK_ID
```

These are **next-action alternatives**, not a sequence to run blindly. Follow
status: settle publication/synchronization first, verify a stale base, reassess
settled CI, then inspect integration. Run the coordinator if absent or drained.
There is no remote polling, CI rerun or ready-state write. A ready PR must be
returned to draft externally before Runner can synchronize another candidate.
An uncertain PR creation is inspected and never blindly repeated.

A human marks the PR ready and satisfies host reviews. Under default `human`
policy, merge through GitHub using an **ordinary merge commit**, then request
`task integrate ID`. Runner confirms exact candidate/tree, merge parents, current
base ancestry and pinned CI before completion. Squash/rebase receipts are not
supported. A closed PR or an unmatched merge needs attention.

Alternatively, `task merge ID` explicitly authorizes one guarded ordinary merge.
Runner requires a classic branch protection rule with strict required checks and
admin enforcement, ordinary merge enabled, no bypass/force-push/deletion allowance,
no auto-merge/queue, and satisfied host merge/review conditions. Ruleset-only
repositories use human integration. Runner never changes these protections.

Applying `merge-policy=automatic` through the same edit/save/explain/apply steps
authorizes guarded merge for **existing and future approved work** after a ready
PR observation. `task integrate` can then trigger a write. It still needs external
ready marking/reviews and explicit reassessment after waits. Disabling the policy
blocks unsent automatic writes. A consumed merge dispatch is never repeated;
use `task integrate` to inspect an uncertain outcome, with human resolution if
it remains open. Host destination/protection must stay stable during dispatch.

After confirmation, `task show` displays **Completed**, `Confirmed integration`,
the PR URL, merge commit and retained base proof. It also retains all calls,
checks, publication/CI observations and any merge dispatch. Inspect the parent
and dependent to confirm progress. Acceptance, passing CI and a successful merge
API response alone are not completion. Do not claim token usage or cost when the
provider did not report them; inspect retained attempt evidence for actual usage.

## 7. Back up and restore on the same host

Finish other commands and stop watches. Keep editors/Git tools from changing the
captured roots. Preserve the exact binary separately, then:

```sh
runner drain
runner status
runner backup --to /absolute/backups/new-snapshot
runner restore --from /absolute/backups/new-snapshot
```

The backup directory must be new and outside source roots. It includes the exact
config, consistent SQLite state/evidence, repository/Git administration, workspaces
and recorded inputs. Keep the complete private bundle; `manifest.json` is written
last. Credentials/tools are not collected separately; files inside captured roots
are copied as-is. Symlinks are copied literally, special files/Git object alternates
are unsupported, and the manifest limit is 64 MiB. This is not an encrypted archive.

Restore requires the same binary, host, OS user and original config/path roots.
Keep the original **`STATE_DIR.guard` in place**, outside the backup. Read-only
inspection preserves freshness. A writable command, config save, newer backup or
restore consumes/invalidates the token; even a refused writable command may do so.
Copying an old guard cannot establish freshness.

The restore preview lists **all destinations and blockers**, including retained
temporary execution inputs. While stopped, preserve existing roots by moving
each listed destination to a distinct name; never move/replace the guard. All
destinations must be absent and their parent directories must exist. The config
itself may be absent: restore reads it from the bundle. Repeat preview, then:

```sh
runner restore --from /absolute/backups/new-snapshot --apply
runner status
runner task show TASK_ID
```

An untouched original guard permits `inspection_only: false`. The restored
authority starts **drained**; inspect pending config, tasks, counters and remote
history before explicit `run`. Existing candidate/approval identities and consumed
writes remain intact.

If freshness is lost, `inspection_only: true` preserves history but blocks run,
task mutations, config save/apply and recovery. There is no override. Reconcile
retained and current remote evidence, then initialize fresh authority at distinct
paths and approve only remaining work. Do not restart an old approval after later
remote effects. Losing the disk/guard also requires this explicit fresh recovery.

An interrupted restore may leave installed roots and sibling `.runner-restore-*`
staging directories. Inspect and preserve partial roots before retrying. Authority
is installed last under a hold; a retry cannot regain consumed freshness. Nothing
is overwritten. Recreate an incomplete backup from a valid drained installation;
do not patch its manifest. Online backup, relocation and cross-version recovery
are outside this release.

The [release checklist and qualification evidence](https://github.com/cortexium-io/runner/blob/main/docs/runner-redesign-proposal.md#frozen-first-release-checklist)
record what was exercised, actual calls/interventions and remaining release gates.
