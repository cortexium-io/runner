# Visual structure and information density

## Hierarchy expresses priority

Visual hierarchy directs attention through relative emphasis: size, contrast,
placement and spacing work in combination. It should reflect the user's task,
not simply which component is easiest to decorate. If everything is prominent,
little is prioritized. [Source: NN/g, Visual Hierarchy](https://www.nngroup.com/articles/visual-hierarchy-ux-definition/).

Assess the composition at the intended viewport: what is noticed first, what
must be scanned, and what competes with the authored content or primary task?
Compare related states at the same scale. Typography should distinguish roles
without turning each label into a new style. Use the project's tokens and
components, but inspect their combined effect; individually consistent components
can still produce a crowded screen.

Density is a tradeoff, not a universal compactness target. Expert repetitive work
may benefit from many visible controls; occasional work may need more explanation.
Larger controls are not automatically clearer, and smaller controls are not
automatically efficient. Consider scanning, reach, label clarity and available
space together. Don't remove necessary meaning merely to make a screenshot clean.

## Grouping communicates relationships

Proximity makes nearby elements appear related. Spacing can establish groups
without adding boxes, while inconsistent spacing can imply relationships that do
not exist. [Source: NN/g, Proximity Principle](https://www.nngroup.com/articles/gestalt-proximity/).

Use this to inspect action-to-object relationships: can someone associate a
control with its target without tracing a long distance or recalling a previous
selection? Contextual controls and stable global toolbars have different benefits.
Choose based on task frequency, selection persistence and navigation cost, not a
blanket demand that every action sit next to its object. Test the full composition
with long labels, dense content and narrow layouts when those occur in the task.

## Disclosure manages complexity, not just clutter

Progressive disclosure presents frequent or essential choices first and makes
secondary detail available when needed. Its success depends on choosing the right
first layer and making the additional capabilities discoverable. Hiding commonly
needed work can increase effort rather than reduce it. [Source: NN/g,
Progressive Disclosure](https://www.nngroup.com/articles/progressive-disclosure/).

Distinguish inspecting a current value from editing it. A compact summary can
support inspection while a focused editor supports change, provided the summary
does not hide consequential state and entry/exit remain understandable. Similarly,
an empty state can explain the next action without giving every populated state
the same large explanation. Avoid prescribing one pattern for every entity.

When proposing a visual change, explain the relationship it clarifies and any
cost it adds. Separate a demonstrable obstruction or misleading hierarchy from
an aesthetic preference. The latter may be a worthwhile design direction, but it
needs product judgment, not a manufactured correctness failure.
