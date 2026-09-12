# Evaluating model profiles

Choose an operator-approved **model and reasoning pair** for the task. Runner
does not infer prices or capability order from names. These are experimental
starting points, not new defaults or a claim that any model is always best.

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

Use the existing read-only commands; exporting metrics does not run a model or
retry work. Keep exports private because attempt evidence can contain project
details:

```bash
umask 077
cortexium-runner metrics --config /absolute/operator/path/runner.json --json > metrics.json
cortexium-runner metrics --config /absolute/operator/path/runner.json --item CARD_ID
```

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

## Opt-in comparison profiles

| Profile | Model / reasoning | Task-selection hypothesis |
| --- | --- | --- |
| `trial_luna_max` | `gpt-5.6-luna` / `max` | Bounded mechanical changes with applicable examples and reliable checks |
| `trial_terra_medium` | `gpt-5.6-terra` / `medium` | Comparison baseline for the same mechanical tasks |
| `trial_sol_medium` | `gpt-5.6-sol` / `medium` | Clear implementation contracts using established patterns |
| `trial_sol_high` | `gpt-5.6-sol` / `high` | Interacting states and less obvious edge cases; compare with Sol Medium |
| `trial_astra_medium` | `gpt-6-astra` / `medium` | Uncertain contracts, security-sensitive behavior, or difficult diagnosis |

A small diff can still have a difficult contract. For example, absence from one
paginated response does not establish global absence. Profile selection should
consider contract clarity, evidence that examples apply, whether checks can
detect mistakes, and their consequences. Slow tests or missing browser tools do
not establish a need for a stronger model.

Use a separate operator-owned test configuration for an isolated test project.
Do not run a second coordinator against the same live Project. The commands below
assume its implementer already uses a configured Codex harness with approved
permissions and tools. Each role inherits those settings; only the named model,
reasoning, and description differ. Check account/harness support before trials.
Do not run these commands against active approved cards: allowlist changes can
invalidate their selections.

```bash
trial_config=/absolute/operator/path/runner-trial.json

cortexium-runner role add trial_luna_max --config "$trial_config" \
  --extends implementer --model gpt-5.6-luna --reasoning max \
  --description 'Experimental: bounded mechanical work with applicable examples and reliable checks.'
cortexium-runner role add trial_terra_medium --config "$trial_config" \
  --extends implementer --model gpt-5.6-terra --reasoning medium \
  --description 'Comparison baseline for the same bounded mechanical tasks as Luna Max.'
cortexium-runner role add trial_sol_medium --config "$trial_config" \
  --extends implementer --model gpt-5.6-sol --reasoning medium \
  --description 'Experimental: clear contracts and established implementation patterns.'
cortexium-runner role add trial_sol_high --config "$trial_config" \
  --extends implementer --model gpt-5.6-sol --reasoning high \
  --description 'Experimental: interacting states and non-obvious edge cases; compare with Sol Medium.'
cortexium-runner role add trial_astra_medium --config "$trial_config" \
  --extends implementer --model gpt-6-astra --reasoning medium \
  --description 'Local hypothesis: uncertain contracts, security-sensitive behavior, or difficult diagnosis.'

cortexium-runner role edit implementer --config "$trial_config" --clear-implementer-ladder
cortexium-runner role edit planner --config "$trial_config" \
  --implementer-profile trial_luna_max --implementer-profile trial_terra_medium \
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
