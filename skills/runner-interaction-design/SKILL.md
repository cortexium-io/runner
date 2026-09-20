---
name: runner-interaction-design
description: Plan, implement, or review user-facing interactions using design theory and observed journey evidence. Complements the assigned Runner role; does not replace its contract or apply to unrelated backend work.
---

# Interaction design

Make the user's next action, its target, and its outcome understandable in the
actual product. Technical correctness and visual polish alone do not establish
this. Preserve the approved scope, project design language, and assigned role's
tool and verification boundaries. This skill grants no redesign, approval,
publication, or additional work authority.

## Choose the relevant knowledge

Read only references needed for the current decision, from this skill's directory
(Runner supplies the bundled reference root during execution):

- [Interaction models](references/interaction-models.md): object identity,
  selection versus focus, modes, action scope, feedback, cancellation and undo.
- [Visual structure](references/visual-structure.md): hierarchy, grouping,
  typography, density and contextual disclosure.
- [Accessible interactions](references/accessible-interactions.md): keyboard,
  pointer/touch alternatives, focus, targets and responsive states.
- [Evaluation](references/evaluation.md): journey-based critique, evidence limits,
  prioritization and finding disposition. Read when reviewing an interaction.

These are contextual reasoning aids, not a style prescription. Distinguish a
standard's applicable requirement from a heuristic, project convention, or
personal preference. An unfamiliar design is not automatically a bad design.

## Apply within the assigned role

**Planner:** For consequential UI work, define a compact interaction contract in
the existing card: user and goal, affected objects/actions, meaningful states and
transitions, constraints, and observable completion evidence. Inspect adjacent
behavior so locally correct changes form a coherent journey. Identify unresolved
product choices before implementation; do not paste this handbook into cards.

**Implementer:** Use the contract to choose and explain material tradeoffs. Inspect
the rendered states and a representative journey early enough to correct design
mistakes before expensive final validation. Reuse project components where their
meaning fits. Evidence should identify the candidate, viewport/input mode,
observed states, result and remaining uncertainty—not merely assert “looks good.”

**Reviewer:** Retain the ordinary reviewer's correctness and safety obligations.
Assess whether the experience supports the approved goal, not whether you would
have styled it differently. During evidence audit, inspect retained screenshots,
interaction evidence and code; do not launch the app or tests. Request only the
unresolved, required observations for focused verification. There, respect the
assigned proof keys and permitted tools; do not start a second full QA cycle.

## Report decisions, not a checklist score

For material findings, connect the observed state to the user's likely difficulty,
the applicable requirement or principle, and a proportionate remedy. Separate
**requirement violation**, **improvement**, **preference**, and **unobserved**.
Only required proof failures or unresolved required evidence affect the existing
QA verdict; suggestions are not invented acceptance criteria. Use the existing
result/feedback fields, not a new output schema.

Keep material concerns visible as fixed, explicitly accepted by the authorized
human, deferred with a concrete existing follow-up, or unresolved. Do not claim
acceptance/deferment without evidence. A cross-card concern is not automatically
waived, nor does finding it authorize changing another card. Escalate conflicting
approved requirements rather than quietly rewriting them. Human judgment remains
necessary for subjective quality; heuristic review is not user research.
