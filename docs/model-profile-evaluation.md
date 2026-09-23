# Evaluating model profiles

Choose an operator-approved **model and reasoning pair** for the task. Runner
does not infer prices or capability order from names. Recommendations are
starting hypotheses, not proof that a model is always best or that a deployment
has become faster, cheaper or more accurate.

## Recommended operating profiles

For demanding Codex projects, start with Astra/medium for planning, independent
card QA and whole-plan QA. Use Sol/high for well-specified implementation with
applicable checks. Select Astra upfront for ambiguous diagnosis, consequential
architecture, difficult UI behavior or high-risk interacting requirements; start
at medium and explicitly choose high when the reasoning demands justify it.
Luna/high is an explicit choice for bounded mechanical implementation with
inspected examples and checks that can detect mistakes, not the general default.

These are risk-based starting choices, not a measured Runner quality improvement.
The earlier Sol/high planning and card-QA recommendation was a cost-saving
hypothesis, not demonstrated equivalence to Astra. OpenAI positions Astra as its
strongest model overall and Sol as a capability/cost tradeoff; more reasoning does
not establish equivalent capability. See the official
[GPT-6 Sol and Luna comparison](https://openai.com/index/introducing-gpt-6-sol-and-luna/).
Measure total delivery cost, including planning, repair, QA, validation and human
correction; cheaper first calls can produce expensive outcomes.

Keep the existing three-step implementation ladder:

`Luna/high → Sol/high → Astra/medium`

The general Ready default can remain Sol/high in the middle of that ladder.
The planner must give a concrete reason for selecting a profile or skipping the
bounded rung. Size of the diff and generic importance are insufficient reasons.
Only a valid QA `needs_changes` advances the ladder; retained work and feedback
follow the next attempt. The in-attempt corrective pass stays on the same profile
and original deadline. Infrastructure, capacity, permissions and missing proof
do not authorize capability escalation. The configured rejection/admission
limits still apply; no extra rung resets them. Planner/reviewer escalation is
not automatic. An omitted requirement or a reviewer falsely accepting a defect
creates no escalation signal. Stronger whole-plan QA alone can find these mistakes
only after implementation; protect the initial planning and card-review decisions
as well. Do not require a failed lower-profile attempt before choosing an approved
Astra profile for difficult work, or silently raise reasoning on existing work.

This fragment illustrates the explicit additions/overrides to an existing Codex
configuration; it is not a complete config or a migration command:

```json
{
  "roles": {
    "planner": {"model": "gpt-6-astra", "reasoning": "medium"},
    "implementer": {"model": "gpt-6-sol", "reasoning": "high", "description": "Well-specified implementation with applicable checks; explain the hardest invariant and proof."},
    "implementer_luna": {"extends": "implementer", "model": "gpt-6-luna", "reasoning": "high", "description": "Bounded work with inspected examples and reliable affected checks; not ambiguous behavior merely described as small."},
    "implementer_astra": {"extends": "implementer", "model": "gpt-6-astra", "reasoning": "medium", "description": "Difficult diagnosis, consequential uncertainty or high-risk interacting requirements; explain why Sol is insufficient."},
    "reviewer": {"model": "gpt-6-astra", "reasoning": "medium"}
  },
  "planner_implementers": ["implementer_luna", "implementer", "implementer_astra"],
  "implementer_ladder": ["implementer_luna", "implementer", "implementer_astra"],
  "plan_delivery": {"enabled": true, "complete_verification": "EXISTING_REVIEWED_ENTRY"}
}
```

Preserve all other role settings, the reviewed complete gate, permissions, tools,
timeouts, parallelism, automatic integration and QA limits. The ordinary QA lane
and authority stay unchanged. With the same Astra settings for card and parent
QA, no additional reviewer profile is needed. `plan_delivery.reviewer_role` can
select one existing reviewer profile for authenticated parent QA and its
failed-gate classification;
it adds neither a lane nor another review. Omission preserves the ordinary review
profile. Doctor inspects the selected profile, and retained parent progress binds
the actual execution profile/settings. Changing them cannot reuse its acceptance.

For new Claude setup, the suggested pair is explicit `claude-opus-5-5`/medium,
not a moving alias with blanket high effort. Pi stays provider-neutral: select an
available provider-qualified model, then a supported effort for that model. No
Claude/Pi substitution is implied for an OpenAI-only project. See the official
[Codex effort guidance](https://learn.chatgpt.com/docs/agent-configuration/subagents#reasoning-effort-model_reasoning_effort)
and [Claude model configuration](https://code.claude.com/docs/en/model-config).
These sources inform starting choices, not a Runner-specific comparison result.

### Existing project activation

Do not rewrite saved projects on upgrade. Preview the exact model/effort/profile
delta and resolve active approved plans before changing it. A plan binds its
selected implementation profile and every reachable escalation profile, not just
their names: replacing a model under the same role name invalidates that approval.
Integrated-but-undelivered children still belong to a live plan. Finish it with
its approved settings, or use an explicitly authorized amendment; do not patch
approval assertions, reset counters or silently reapprove work. Drain before
replacing the saved configuration, preserve backups/operator edits, run validation
and normal Doctor, then verify the restarted service and fresh poll. This is not
permission for a paid evaluation or additional product work.

## What external benchmarks tell us

The September 5, 2026 [Artificial Analysis coding-agent results](https://artificialanalysis.ai/agents/coding-agents)
suggest testing Luna Max against Terra Medium, and Sol Medium against Sol High.
More reasoning is not uniformly better across the component scores. The coding
snapshot has Astra Max, not Astra Medium: using Astra Medium for uncertain work
remains a local operating hypothesis, not a measured coding-benchmark result.

Read the [benchmark methodology](https://artificialanalysis.ai/methodology/coding-agents-benchmarking)
before transferring those results to Runner. The index equally weights software
engineering, repository Q&A, and terminal work. Its three attempts are independent
pass@1 observations, not a sequence of QA repairs. Cost and time pool attempts,
while missing telemetry is excluded. Reported cost accounts for cached-token
pricing, but is not a subscription bill; agent time excludes environment setup
and judging. Check component results, harness versions, settings, and telemetry
coverage. An aggregate score divided by average cost is not cost per accepted
Runner card.

## Evaluate process changes before changing models

First compare the next completed batch in each participating project with its
own baseline. Keep model/reasoning profiles, retry limits, global concurrency,
permissions, and acceptance requirements fixed. Record the Runner and skill
versions, project-policy changes, approved card IDs, starting bases, and any
operator interventions in the existing trial evidence. Different projects and
different task mixes are not interchangeable control groups.

For prompt or skill changes, also retain each stage's `prompt_contexts` layout
and `guidance_digest`. Compare assembled prompts for the same assignment and
stage to detect duplicated or misplaced guidance. Fewer bytes or words prove
only a smaller prompt, not fewer provider tokens, better cache reuse, lower
total cost, or unchanged quality. Check those outcomes using the next approved
real work and the evidence below; do not create extra product work just to
exercise a prompt change.

Use the existing read-only commands; exporting metrics does not run a model or
retry work. Keep exports private because attempt evidence can contain project
details:

```bash
# Choose a new batch directory under an existing private, durable parent.
# Do not use /tmp or an execution worktree for the only retained copy.
trial_dir=/absolute/private/trials/UNIQUE_BATCH_ID
umask 077
mkdir "$trial_dir"
cortexium-runner metrics --config /absolute/operator/path/runner.json --json > "$trial_dir/metrics-before.json"
cortexium-runner metrics --config /absolute/operator/path/runner.json --item CARD_ID
```

Save the planner's JSON proposal there when planning is already authorized; do
not rerun planning to recreate missing evidence. Retain an after-batch metrics
export, exact approved item IDs, source and candidate/merge commits, and any
needed check reports in the same private trial location. Use the existing trial
record for each human intervention: time, card/attempt/candidate, reason, action,
and result, distinguishing diagnosis, product repair, scope amendment, retry,
environment repair, and Runner upgrade. Record human time only when measured.
Runner does not capture work done outside its assignments or copy temporary
reports automatically. Keep diagnostic summaries self-contained and redact
credentials and customer payloads rather than retaining raw sessions.

New attempt records include `run_context`: Runner build, bundled skill version,
and a digest of the loaded config including CLI overrides. These are captured
at execution, not export. Retain relevant operator settings and changes in the
private trial record as well: a digest cannot reconstruct them, identify an
unlabelled development binary, or detect changed native harness configuration,
environment, or referenced files. Use `prompt_contexts` for actual supplied
guidance; the bundled version alone does not prove which skills were installed.
Missing historical identity stays unknown. No current version is backfilled.
These telemetry fields are not injected into model prompts. The stable-first
prompt layout is unchanged; a revised skill changes the shared guidance prefix,
not a per-attempt timestamp or run identity in that prefix. Cache reuse across
models or harness sessions is still not guaranteed.

Filter the export to the approved batch's exact item IDs, retaining every
attempt through its observed terminal state. Include blocked/exhausted work and
manual recoveries, rather than comparing only the successful attempts. Account
for planning at batch level, including failed staging and any paid replanning;
a saved-plan staging retry is not another planner invocation.

| Question | Evidence to use | Interpretation boundary |
| --- | --- | --- |
| Did delivery get faster or cheaper? | All batch attempts, reported usage/cost, and the observed release-to-Done/Blocked timeline | Attempt durations exclude gaps between attempts and may overlap across cards. Missing monetary cost is unavailable, not zero. |
| Are tests being repeated unnecessarily? | Retained per-check results, candidate/environment identity, start/end times where recorded, and reuse explanations | A stage interval is not test time. A renewed receipt is not a fresh test execution. Distinguish justified reruns after relevant changes from repetitions on unchanged inputs. |
| Did quality improve? | Recorded `review_verdict`, exact candidate, private findings, and the relevant repair diff | Rejection exhaustion is still `needs_changes`; accepted QA followed by publication failure is not a code rejection. Distinguish unresolved findings, repair regressions, and late discoveries from new requirements. |
| Are setup and publication costs hidden by eventual success? | Failed/blocked stages, publication attempt counts and failing operations, plus retained failure/fallback reports | Count failures inside successful attempts. A shared failure label or a later success does not establish its cause. |
| Did planning improve? | The actual proposal, approved scope, dependencies, and observed repairs | Judge coherent behavior/risk boundaries and real dependencies, not a target card count. Record human amendments; do not attribute them to the planner. |

Do not rerun completed checks merely to obtain missing timings. Mark measurement
coverage explicitly, and retain named failure/rerun reports when checks do run.
For focused QA, inspect the original verification request and audit context
retained in the final private evidence as well as the resolved result. Older
results may omit the request; without it, an unchanged-candidate rerun's reason
may remain unknown rather than demonstrably necessary or unnecessary.
Keep commands, arguments, and raw diagnostics in appropriately private project
evidence rather than adding them to Runner's structured stage telemetry. A
high cache-read count does not by itself establish low end-to-end cost; preserve
the reported counters and the provider's definitions without guessing prices.

Summarize time, usage, unnecessary repetition, setup failures, QA findings, and
human intervention together. One before/after batch is an operational signal,
not a causal A/B result. Keep improvements that remove demonstrated waste
without weakening proof; investigate apparent savings accompanied by missing
evidence or later defects. Only then start a separate model-profile comparison.
Recurring observations can inform the existing private `guidance` drafts, but
must be independently reviewed before becoming repository or skill instructions.

## Test quality and verification cost

### Bounded reviewer-guidance calibration — 2026-09-22

The single authorized comparison of old source `40ee353` and revised source
`77a82dd` stopped after **4 of 8 review assignments**, covering **2 of 4 cases**.
Both used Codex CLI 0.155.1, `gpt-6-astra`, medium reasoning. Independent review
of the actual private assessments found both arms correctly accepted the sound
record-update case and rejected the tenant-access defect. This was reasoning
and evidence adjudication, not just matching verdict labels. The repair-regression
and missing-proof cases were not run. No assignment remained unfinished.

Observed harness time was **179.622s** and wall time **182.58s**. The four reports
contained **251,118 inclusive input + 3,547 output = 254,665 tokens**. Their
**140,800 cache-read tokens are an input subset**. The old accounting incorrectly
added that subset again, producing **395,465** and triggering the 300,000-token
admission stop after the fourth assignment. Deterministic tests now protect the
correct total and historical-provider normalization; this correction does not
retroactively alter the recorded stop decision.

The comparison is permanently closed for this authorization. Raw artifacts and
independent adjudication are preserved unchanged; there is no resume, rerun or
automatic budget extension. This partial corpus establishes neither a quality
nor performance advantage, and reported tokens are not a billing claim. Any
future experiment needs separate approval. See the
[comparison protocol](../internal/engine/testdata/reviewer/COMPARISON.md) and
[accounting definitions](../internal/metrics/USAGE.md).

### Verification methods

Use the existing behavior evaluator's reviewer corpus before interpreting a
prompt change as a quality improvement. It contains correct record editing
with an unsubstantiated prior security allegation that must be independently checked,
a tenant-access defect with passing shallow tests, and a repair that drops
ownership data. Expected judgments are specified independently of model output;
ordinary Go tests validate the candidates against literal reference assertions.
See the [operator reference](operator-reference.md#development) for bounded
live commands and reporting. Compare the same cases, profiles, and environments.
Count false acceptance, unnecessary rejection, missed defects, incomplete
reviews, and execution failures alongside time and usage. Passing the small
corpus is evidence for those cases, not a certification of reviewer quality.

For repository test costs, capture JSON from a required run rather than adding
another measurement pass. For Runner's PR gate, an uncached measurement is:

```bash
go test -race -json -count=1 ./... > "$trial_dir/tests.jsonl"
```

Use package completion events for package durations and top-level test
completion events to identify expensive tests. Do not sum a parent and its
subtests, or sum concurrent package times and call it wall time. Measure command
wall time separately; retain OS, Go version, race mode, candidate, and cache
conditions. A single before/after sample is diagnostic, not a speedup claim.
Keep the existing PR race and vet gates. Measure the effect of a simpler test
with the same scenarios and execution mode before changing any gate.
The [maintainer guide](open-source-maintainer-setup.md#go-ci-caches) describes
the opt-in baseline/refreshed CI cache comparison; it keeps race-test execution
fresh and includes cache transfer overhead in the comparison.

On 2026-09-17, two paired Linux x64/Go 1.26.6 runs at `db6f3bc` used
`go test -count=1 -race ./...` and vet with identical source and checks:

| Run | Cache policy | Race command | Complete job |
| --- | --- | --- | --- |
| [First](https://github.com/cortexium-io/runner/actions/runs/35197754732) | Existing snapshot | 70.20 s | 93 s |
| First | Refreshed, initial cache miss | 101.93 s | 132 s |
| [Second](https://github.com/cortexium-io/runner/actions/runs/35198001630) | Existing snapshot | 94.71 s | 123 s |
| Second | Refreshed, previous snapshot restored | 45.44 s | 62 s |

All four jobs passed. The existing policy restored the same approximately
38 MiB snapshot and skipped saving both times; the refreshed policy restored
its approximately 73 MiB first snapshot and saved an updated one. Job durations
include setup and cache transfer/save, but exclude queue time. The cold-start
penalty and baseline variation matter: this is a bounded positive signal for
retaining the cache change, not a universal speedup estimate. This comparison
does not measure macOS, release-build performance, or model-token caching.

Prefer focused backend tests for record edits and their validation/persistence
permutations. Use component tests for form logic and browser checks only where
interaction, rendering, or integration requires them. Preserve representative
boundary checks when lower-level tests cannot establish the contract. For Go,
use tables for common test logic and separate tests for distinct workflows;
keep expectations visible and avoid adding a configurable harness to save a
little duplication.

## Opt-in comparison profiles

These are example hypotheses for a separately approved comparison, not an
authorization for extra model runs or an extension of a closed evaluation budget.

| Profile | Model / reasoning | Task-selection hypothesis |
| --- | --- | --- |
| `trial_luna_high` | `gpt-6-luna` / `high` | Bounded mechanical changes with applicable examples and reliable checks |
| `trial_sol_medium` | `gpt-6-sol` / `medium` | Clear implementation contracts using established patterns; compare with Sol High |
| `trial_sol_high` | `gpt-6-sol` / `high` | General implementation baseline; compare total outcome cost, not just first-call cost |
| `trial_astra_medium` | `gpt-6-astra` / `medium` | Uncertain contracts, security-sensitive behavior, or difficult diagnosis |

A small diff can still have a difficult contract. For example, absence from one
paginated response does not establish global absence. Profile selection should
consider contract clarity, evidence that examples apply, whether checks can
detect mistakes, and their consequences. Slow tests or missing browser tools do
not establish a need for a stronger model.

The planner should name the hardest card-owned invariant and why the selected
profile can handle it. A small UI diff can still involve source preservation,
occurrence identity, or interacting selection/history. Conversely, a consumer
of a fixed, verified prerequisite contract can be much more bounded than the
card that establishes that contract. Require applicable evidence, not merely
"established patterns" in the selection reason. Resolve inspectable contract
questions in planning; moving an undocumented assumption to a stronger model
does not resolve it. This guidance changes no configured profile or ladder.

Use a separate operator-owned test configuration for an isolated test project.
Do not run a second coordinator against the same live Project. The commands below
assume its implementer already uses a configured Codex harness with approved
permissions and tools. Each role inherits those settings; only the named model,
reasoning, and description differ. Check account/harness support before trials.
Do not run these commands against active approved cards: allowlist changes can
invalidate their selections.

```bash
trial_config=/absolute/operator/path/runner-trial.json

cortexium-runner role add trial_luna_high --config "$trial_config" \
  --extends implementer --model gpt-6-luna --reasoning high \
  --description 'Experimental: bounded mechanical work with applicable examples and reliable checks.'
cortexium-runner role add trial_sol_medium --config "$trial_config" \
  --extends implementer --model gpt-6-sol --reasoning medium \
  --description 'Experimental: clear contracts and established implementation patterns.'
cortexium-runner role add trial_sol_high --config "$trial_config" \
  --extends implementer --model gpt-6-sol --reasoning high \
  --description 'Experimental: interacting states and non-obvious edge cases; compare with Sol Medium.'
cortexium-runner role add trial_astra_medium --config "$trial_config" \
  --extends implementer --model gpt-6-astra --reasoning medium \
  --description 'Local hypothesis: uncertain contracts, security-sensitive behavior, or difficult diagnosis.'

cortexium-runner role edit implementer --config "$trial_config" --clear-implementer-ladder
cortexium-runner role edit planner --config "$trial_config" \
  --implementer-profile trial_luna_high \
  --implementer-profile trial_sol_medium --implementer-profile trial_sol_high \
  --implementer-profile trial_astra_medium
cortexium-runner role list --config "$trial_config" --json
```

This exposes choices to the planner; it does **not** randomize assignments or
ensure that every profile is used. Compare paired runs explicitly. No ladder is
configured here, so a selected profile stays fixed on retries. This avoids
confusing a comparison set with a mandatory escalation order. An empty profile
still uses the workflow's default implementer. Existing ladder behavior remains
available, but changing it is a separate operator decision; Runner does not
automatically classify a rejection and choose its next model.

## Measure the whole outcome

- Keep the approved task, starting base, harness version, tools, permissions,
  timeout, task granularity, and verification requirements equal. Run each
  candidate independently; do not seed one comparison with another's solution.
- Keep planner and reviewer fixed at the trial's approved profiles.
  Change only one comparison at a time: Luna Max versus Terra Medium, or Sol
  Medium versus Sol High. Include representative tasks, not just easy successes.
- Record first-pass acceptance and total implementation, QA, repair, and required
  integration work through acceptance; account for planning costs at batch level.
  Include blocked and exhausted cards;
  separate environment failures from code failures without hiding their cost.
- Retain input, cached input, output, reported monetary cost when available,
  elapsed time, and human intervention. Missing cost is unknown, not zero.
  Cache state and model switches are comparison conditions, not guaranteed reuse.
- Use the implementer's retry `work_done` note to distinguish preserving and
  improving an approach from partly or largely replacing it. Check the diff
  when that distinction matters; self-reporting does not prove causal savings.

Use existing per-card attempt metrics and review evidence. There is no automatic
A/B scheduler, causal cost estimator, or numerical code-reuse metric. Run repeated
matched tasks before changing defaults; four different successful cards cannot
establish which profile would have been cheapest on the same work.

## Skill rollout

After installing a build with the updated bundled skills, stop or pause Runner
at an idle boundary and run `cortexium-runner doctor --fix --offline --config PATH`, then
run Doctor again and restart only when ready. This refreshes Runner-managed
skills; it does not enable the experimental profiles or alter model settings.
The guidance uses the shared bundled skills and applies across supported
harnesses, but the example model IDs above are specifically for Codex.
