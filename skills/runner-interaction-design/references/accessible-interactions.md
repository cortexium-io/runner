# Accessible interactions

Apply the project's accessibility requirements and the relevant standard to the
actual control. A visual inspection is not a conformance audit. Use native
semantics where suitable; inspect behavior as well as attributes. The references
below explain specific criteria, not a claim of complete WCAG coverage.

## Focus, selection and keyboard behavior

Keyboard focus and selection are different concepts. A composite widget may use
arrow navigation internally while Tab moves between components; the appropriate
pattern depends on widget semantics. Focus should remain visible and predictable
as content changes. [Source: WAI-ARIA APG, Keyboard Interface](https://www.w3.org/WAI/ARIA/apg/practices/keyboard-interface/).

For the assigned journey, check entry, navigation, activation, dismissal and the
resulting focus destination. A mouse-only screenshot does not establish keyboard
support. Conversely, don't impose a custom composite-widget keyboard scheme on
ordinary native controls. Match the actual pattern before assessing it.

WCAG 2.2's Focus Not Obscured (Minimum) criterion concerns an element receiving
keyboard focus not being entirely hidden by author-created content. Partial
occlusion can still be a usability problem without proving failure of this exact
criterion. [Source: W3C, SC 2.4.11 explanation](https://www.w3.org/WAI/WCAG22/Understanding/focus-not-obscured-minimum.html).

## Targets and alternatives

WCAG 2.2 Target Size (Minimum) uses a 24-by-24 CSS pixel threshold with exceptions,
including spacing, equivalent controls, inline targets, user-agent control and
essential presentation. Do not demand that every inline indicator become a
24-pixel-high box, or infer CSS dimensions from a scaled image. Establish which
exception, if any, applies. [Source: W3C, SC 2.5.8 explanation](https://www.w3.org/WAI/WCAG22/Understanding/target-size-minimum.html).

Dragging Movements requires a single-pointer alternative without dragging unless
an exception applies. Keyboard support alone does not prove that alternative:
some touch users cannot use a physical keyboard. The keyboard requirement is
separate. [Source: W3C, SC 2.5.7 explanation](https://www.w3.org/WAI/WCAG22/Understanding/dragging-movements.html).

Choose representative evidence for the risk: a drag task may need pointer
placement, a non-drag path and keyboard behavior; a form change may instead need
labels, focus and error recovery. Do not run every input method for unrelated
changes or claim unobserved methods passed.

Responsive evaluation should preserve meaning, not merely fit pixels. Check
whether narrowed space separates actions from targets, conceals important state,
or makes disclosure inaccessible. Record viewport, zoom/input mode and the
specific observed state. Escalate missing required evidence within the assigned
review stage; do not compensate by assuming success from source code alone.
