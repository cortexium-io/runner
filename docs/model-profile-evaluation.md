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
- Keep planner and reviewer fixed (Astra Medium for the current trial baseline).
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
