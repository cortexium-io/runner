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

## Onboard a Go repository

Use `develop-planned` for a complete local development task whose required proof
fits the fixed Go check. Read the repository's own instructions and select a
full committed base before submission. A small task may have one cohesive card;
parallel cards are useful only when their write scopes and dependencies allow it.

Copy this example into private operator configuration. Set the project ID and
owning repository path in `project.json`, and pin compatible Codex, Go and
`runner-go-check` executables. The repository's `go.mod` and toolchain directive
must work with that exact Go binary under `GOTOOLCHAIN=local`. Prepare required
dependencies in an operator-owned immutable module cache before admission;
configure `codex.go_module_cache`, the check's `GOMODCACHE`, and the helper/cache
read paths in `codex.command_read_paths`. Keep live credentials, personal runtime
state and dependency downloads out of the assigned source. Do not configure
delivery commands or a release operator for this local-only task.

Qualify the actual fixed command and its source/scratch permission boundary
without model turns before onboarding work. Configuration loading and service
status alone do not prove dependency availability or test execution. The sample
storage policy is an example, not a promise that every repository and retained
task family fits. Keep the selected policy explicit; capacity or unsupported
checks require a concrete resolution, not an automatic allowance increase.

Start one owner for the private state using the [service ownership
contract](../service/README.md). Choose an unused loopback endpoint when another
instance is already running. The following paths and request text are
placeholders for that qualified instance and the authorized task:

```sh
cortexium-runner run --data /private/state \
  --endpoint http://127.0.0.1:8788/mcp --project reviewed-go \
  --process develop-planned --source-ref FULL_COMMITTED_BASE \
  --request-id STABLE_AUTHORIZED_REQUEST_ID \
  --inputs '{"request":"AUTHORIZED_TASK_WITH_EXPLICIT_ACCEPTANCE_AND_WRITE_PATHS","package":"internal/parser","tests":"^TestParseCanonicalPaths$"}'
cortexium-runner result --data /private/state \
  --endpoint http://127.0.0.1:8788/mcp --run SAVED_RUN_ID --wait
```

Keep the saved run identity across disconnects. Runner owns planning, isolated
cards, checks, combined correction, independent review and local integration.
Completion returns the local candidate, accepted card manifest and scoped test
receipt. Confirm selected tests actually ran without missing or skipped proof;
name required new tests in the request and include them in the fixed selector.
An internal pin, compile-only check or zero executed tests is not behavioral
acceptance. Publication and any future merge/deployment remain separate work.
Node/frontend, browser, generated-app lifecycle or external database obligations
need their own supported complete process; a Go check cannot waive them.

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
