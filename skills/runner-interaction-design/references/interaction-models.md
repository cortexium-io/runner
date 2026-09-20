# Interaction models, scope and recovery

## Predictability before decoration

A mental model is a user's working explanation of the product, not its internal
data model. Designers know hidden distinctions that users may not. Reusing a
familiar interaction can reduce learning, but a domain-specific alternative may
be better when the familiar one misrepresents the work. Identify the intended
users and their vocabulary; label assumptions about them as hypotheses rather
than research. [Source: NN/g, Mental Models](https://www.nngroup.com/articles/mental-models/).

Apply that distinction by tracing **object → available action → affected scope →
visible result**. Ask what someone could reasonably predict before acting. A
control can work correctly while suggesting the wrong target. In an editor,
focus, text selection, object selection and the inspector's target may differ;
the product need not collapse them into one state, but must make consequential
differences legible. A table, canvas and form may need different representations.

When a mode changes, inspect what persists and what changes: object identity,
selection, available commands, and the meaning of shortcuts. A test that asserts
an internal state transition cannot prove the user can perceive that transition.
Compare the resting and active states and one transition in both directions.
Do not prescribe persistent outlines or a particular toolbar position universally;
choose the smallest cue that communicates the actual distinction.

## Feedback and recovery

Nielsen's heuristics support visible system state, understandable language, error
prevention, and ways out of unwanted actions. They are broad diagnostic aids,
not component specifications. Ask which uncertainty the feedback resolves and
whether it arrives while that information is useful. More feedback can make a
product less clear when it competes with the task. [Source: NN/g, Usability
Heuristics](https://www.nngroup.com/articles/ten-usability-heuristics/).

Shneiderman connects informative feedback, completion of action sequences,
reversibility and user control. Apply them together: a successful cancellation
should leave a comprehensible state, and undo should have an understandable unit
of work. Tune these principles to the domain rather than implementing every
action with a confirmation dialog. [Source: Eight Golden Rules](https://www.cs.umd.edu/~ben/goldenrules.html).

For a risky action, reason about consequence, frequency and recoverability.
Reversible routine edits may suit immediate action plus undo; an irreversible
destructive action may warrant a scope-specific confirmation. State what cancel
means: abandon a draft, stop an operation, close a panel, or revert changes are
not interchangeable. Inspect interrupted, repeated and failed actions when those
states are relevant, including where focus and selection end up afterwards.

Retain both evidence and uncertainty: a screenshot can reveal an ambiguous scope
indicator, but cannot alone establish how undo behaves. Prefer one observed
journey with clear state transitions over many disconnected “green” screenshots.
