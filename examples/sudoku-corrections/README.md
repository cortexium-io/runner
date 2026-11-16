# Linked Sudoku corrections with project-owned verification

This stages a new `correct-sudoku-preview` route beside the preserved historical
processes. It does not install configuration, submit work, edit the app, execute
product tests, replace the running preview or grant release authority.

The full owner JSON is preserved once through `stage-sudoku-goal`, a fixed,
network-disabled shared command. It receives the exact raw goal through `{goal}`
and retains those unchanged UTF-8 bytes as `owner-goal.json`. Its compact digest
summary says **owner-document-only**: staging authenticates neither source nor
product acceptance. It has one command and no model or child delegation.

```sh
node examples/sudoku-corrections/configure.mjs \
  --instance /absolute/current/config/instance.json \
  --parent-inspect /absolute/completed-parent-inspect.json \
  --goal /absolute/frozen-correction-goal.json \
  --capture-baseline /absolute/examples/project-verification/capture-baseline.mjs \
  --goal-stager /absolute/immutable/stage-goal.mjs \
  --root /absolute/new/staged-config
```

The exact completed-parent revision, source/check/handoff identities and nonempty,
unique correction IDs are checked before staging. The current goal has C01–C14;
later linked goals use their own IDs and requirements. Historical and new correction
parents preserve their accepted native profiles and pinned command runtimes. Normal
future linked intake reuses the configured route; the builder refuses to replace an
existing route. The overlay retains every existing project,
process and role by reference, selects the new future default, keeps profiles,
model effort, concurrency, local retries, timeouts and byte safeguards, and records
the authorized disabled aggregate-work and retained-run-count guards. The latter
requires the separately qualified runtime supporting `max_runs: 0` before install.

`requests/stage-goal.json` is ready for that first process. Its completed `goal`
artifact becomes the required `owner_goal` input of the main process.
`requests/correction.template.json` deliberately lacks that ID and is not ready to
submit. `correctionRequest(originalBytes, parentInspect, stageArtifactID)` binds
it to a real typed artifact. Choose the explicit new outer request ID when
continuing after a failed historical intake; never edit the preserved owner bytes
to generate an ID. The main `request:text` is only a small digest/purpose pointer.
Every child receives the same declared owner artifact, reads its relevant sections
and overall obligations, and verifies its digest. Baseline `--request` and verifier
`--goal` bind that exact file, never the compact pointer.

One fixed baseline child authenticates accepted source and preserves source-only
changes once. A separate read-only research role receives explicit network access
for requested current official sharing endpoints/constraints; it never posts or signs up.
This request's C13 requires fresh research before both writers. Later goals reuse
applicable evidence or omit irrelevant research.
Then a developer owns `frontend/src` and the exact design document, while an
independent verification author owns only
`frontend/verification/sudoku-corrections.mjs` and its adjacent
`frontend/verification/sudoku-corrections/` helper directory. Product/browser tests
are authored inside that future Runner task; none are supplied by this example.

The fixed model-free role invokes pinned Node directly through qualified
`command/shared/workspace-read`. Its script receives source, owned scratch, frozen
goal, baseline and supplied evidence paths. Fixed environment supplies pinned
Go/modules/Playwright/CFT/WebKit/frontend-helper paths and one isolated process-owned
preview root. The script partitions stopped builds by the exact goal SHA, binding
each manifest to source/test/goal/server/runtime identities and preserving other
goal partitions. Ordinary later linked goals reuse this profile without config
regeneration. The script owns source/test/runtime identity, meaningful numbered
assertions, proportionate interactions/images, exact relevant-input proof reuse,
stopped preview retention and finally cleanup. One bounded canonical archive is
returned on success or failure. `domain.json` contains exactly `outcome` and `reason`;
rich facts stay in canonical report/summary files. A passed archive preserves the
existing source-admission schema and flat JSON/PNG entry bounds. The author verifies
its final unchanged bytes through the existing read-only `admit-followup` helper
without `--request`; a failed compatibility probe returns to project test repair.
It does not encode old four-tool/hint-wizard gates
or require an arbitrary legacy geometry count.

Research returns full dated citations in `research:artifact`; independent source
and UX reviewers return complete per-ID reconciliation and actually viewed image
names in `assessment:artifact`. Their `report:text` summaries stay at most 1 KiB,
with full final envelopes within the unchanged 8 KiB limit. The lead and writers
read those canonical files rather than copying their contents into task plans.
No acceptance or proof is discarded to fit the operational record.

Source and visual reviewers are separate, read-only actors. They reconcile every
numbered requirement and personally inspect the current whole/open-panel images,
independently of aggregate test success. Only after matching current acceptance
does the trusted preview operator qualify replacement and private-LAN phone access
while retaining existing progress. Delivery stops for Dan inspection. Browser
emulation is not native/physical acceptance; no merge, production, restoration or
release-gate waiver is granted.

The exporter, profiles, optional count policy and synthetic runtime contracts must
be qualified before installation. A staged manifest explicitly records that work
as pending. No generic launcher, scheduler, DSL, cache daemon or task ledger is added.

Focused model-free checks:

```sh
node --test examples/sudoku-corrections/*.test.mjs
go test -race ./examples/sudoku-corrections -run '^TestGeneratedCorrection'
go test -race ./examples/sudoku-corrections -run '^TestWorkflowArtifactOwnerGoalResearchRepair$'
```

The workflow proof uses real Core/private SQLite with every execution intercepted.
It preserves a 17,294-byte owner artifact, admits the four-task plan, exhausts the
unchanged four research attempts, redelegates automatically and persists/reopens
the check/review history. It executes no product commands or models. Set
`RUNNER_SUDOKU_OWNER_GOAL` for the separately opt-in exact original-document digest
proof; ordinary tests use synthetic data.
