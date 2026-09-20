# Evaluating a journey

## Start from a real task

Name the intended user, goal, starting state and relevant constraints. Follow the
journey far enough to observe its result and any required recovery. Inspect
adjacent UI when it affects that journey, without treating the whole application
as authorized redesign scope. Ask whether the person can locate the action,
understand its target, predict the consequence and recognize the outcome.

Heuristics are lenses for interpreting evidence, not acceptance tests by
themselves. Their broad nature requires contextual judgment. A technically
passing implementation can still be confusing, while a design that breaks a
convention can be justified. [Basis: Nielsen's usability heuristics](https://www.nngroup.com/articles/ten-usability-heuristics/).

## Respect what the evidence can establish

- A retained screenshot supports claims about its visible state, not unseen
  transitions, timing, focus movement or another viewport.
- A recording or reproducible interaction receipt can support a state sequence;
  identify its candidate and environment before reusing it.
- Source and tests help explain behavior but may encode the design being
  questioned. Do not use implementation-derived expectations as independent
  proof of usability.
- A heuristic review predicts likely difficulty. It is not evidence of measured
  user success, user preference, conversion improvement or accessibility
  conformance. Reserve those claims for appropriate observations or research.

Within Runner's evidence-audit stage, inspect available artifacts and mark missing
required observations for focused verification. Do not run tests or a browser
merely because this reference describes an interaction walkthrough. During
focused verification, close the assigned gaps with the smallest useful checks;
reuse valid evidence rather than repeating a complete validation suite.

## Make findings actionable and proportionate

Use the existing response contract. A concise material finding should explain
the observed state, artifact/candidate, user consequence, applicable requirement
or principle, confidence and a proportionate next action. Avoid arbitrary scores
and fixed finding counts. No finding is a valid outcome when evidence supports it.

Classify the claim before deciding its disposition:

- **Requirement violation:** connect it to approved behavior or applicable
  repository policy. Report the relevant proof failure through normal QA.
- **Improvement:** explain a plausible benefit and tradeoff; propose it without
  expanding implementation scope or withholding acceptance on a new criterion.
- **Preference:** label the subjective choice. Offer alternatives when useful;
  don't disguise taste as a universal law.
- **Unobserved:** state precisely what is unknown. Required missing evidence
  remains unresolved; optional unknowns do not automatically block the card.

Prioritize by harm, frequency and ability to recover, with uncertainty explicit.
Material concerns remain visible as fixed, human-accepted, deferred to an
identified existing follow-up, or unresolved. A neighboring component's issue is
not silently dismissed, but it does not authorize editing it. A conflict between
the approved design and a plausible better design is a human product decision.

For comparative trials, retain total review/implementation time and available
usage, useful corrections, rejected suggestions, rework and later user-discovered
issues. Count the design pass's overhead too. Small uncontrolled samples suggest
what to investigate; they do not prove causal improvements.
