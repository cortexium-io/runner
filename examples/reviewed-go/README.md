# Reviewed Go development

One process owns implementation, focused verification, independent read-only
review and integration into a local candidate branch. The external chat submits
the authorized request and reports the compact result. It does not perform any
of those steps itself. See [the coordinator contract](../../docs/coordinator.md).

Copy this directory to private configuration. Set the executable paths in
`instance.json` and `roles/verify.json`, the repository and source ref in
`project.json`, and any read-only dependency cache in
`codex.command_read_paths`. The command's environment can select that cache via
`GOMODCACHE`; it cannot download dependencies. No dependencies are installed.

```sh
cortexium-runner serve --config /private/config/instance.json --data /private/state
cortexium-runner run --data /private/state --project reviewed-go \
  --source-ref COMMITTED_BASE --request-id USER_REQUEST_ID \
  --inputs '{"request":"Fix the described parser behavior in the named file and lines; preserve the existing contract.","package":"internal/parser","tests":"^TestParse$"}'
```

`package` is one repository-relative Go package without the `./` prefix. `tests`
is an explicit Go test regular expression. Empty `tests` selects that package's
ordinary tests; `^$` is a compile-only check and cannot establish behavior. A
small task may select a named test; a larger integration can explicitly select
`package: "..."`. There is no model-selected shell command. Text substitutions
are literal argv elements and are not expanded recursively.

The reviewer receives the original request, retained patch, implementation
summary and actual check result/scope. It can read source and write scratch;
source, Git metadata, private files and network access remain restricted. It
returns `APPROVE` or `BLOCKED` plus concrete findings. It never edits or retests.

`git-candidate` requires `APPROVE`. It reconstructs the retained patch from its
saved base using a temporary index, commits exactly that tree and creates
`codex/runner/RUN_ID` locally. It leaves the user's checkout/index alone and
never merges, pushes or deploys. A lost result reuses the same commit/ref. A
conflicting branch is preserved and reported. An unchanged Runner-owned branch
expires with run retention; a branch advanced or checked out by a user is preserved.

There are at most four attempts, one per step, with no automatic retry or
repair. A failed check or blocked review stops the process and surfaces its
reason. A human decision is required before extra work or allowance. Successful
completion means a checked, reviewed local candidate, not a production release.

This configuration supports scoped Go work. Frontend/build/browser work needs
an explicitly configured process and its required capabilities. Do not substitute
this Go check for those obligations or have the coordinator perform them outside
Runner. Browser verification must be headless.

`develop-planned` adds source-aware planning and up to eight dependency cards.
Its new planner has an 8KiB response budget; card coding, combined correction
and independent review use 8KiB input and 512B responses. The instance explicitly
sets `task_concurrency: 2` while root `concurrency` stays one. Cards have isolated
workspaces and literal write scopes; an internal task pin is unreviewed source,
never an approval. The source join retains the accepted plan/card manifest as an
artifact for combined correction and review. Initial work proceeds directly to
the fixed scoped Go check. Corrections preserve completed cards and rerun the
combined check/review before the existing reviewed local candidate integration.
The new 64-attempt ceiling is shared by the whole root family, including cards,
corrections and infrastructure recovery; children do not each receive 64 tries.
Malformed or conflicting unaccepted plans return bounded feedback to the same
planner at most twice, before any card starts; accepted cards are never replanned.

Configure the absolute `runner-go-check` and Go paths in `roles/planned-check.json`
and the existing immutable runtime read paths before selecting this process.
It retains actual selected-test execution as an artifact; inconclusive or missing
execution blocks. This local process ends at a reviewed candidate and grants no
publication, merge or production authority. Existing processes and their budgets
remain available unchanged.

## Iterative and planned development

`develop-iterative` adds bounded verification/review corrections, a project-owned
benchmark, an approval boundary and delivery through a fixed project tool.
`develop-tasks` first plans a small task collection, executes its fixed
implementation template in dependency order, then checks and reviews the combined
candidate. Initial work skips its later correction owner. Corrections return to
that owner without replanning or repeating the completed collection.

Before selecting either process, configure the additional roles' absolute tool
paths and allowed read paths. The benchmark tool writes `domain.json` with only
`outcome`/`reason` and `measurements.json` containing its actual measurements.
`continue` requests another owner/measurement cycle within the declared limit;
`ready` proceeds to review. The tool owns workload identity and measurement, not
scheduling. A passing unchanged offline check can be reused inside the run.

The delivery tool must own the project's real publication and delivered-result
verification. Its fixed command is invoked only after the saved decision is
approved. Registering an example or accepting a local candidate provides no
production release authority. Leave delivery unconfigured until its supported
capability and target are qualified. Select `develop` when the requested finish
is the existing local reviewed candidate.

The example's 128KiB record allowance accommodates retained role definitions,
expanded tasks and bounded attempt evidence. It is an example policy, not a
change to any installed instance. Time, attempt and repetition allowances remain
explicit in each process.
