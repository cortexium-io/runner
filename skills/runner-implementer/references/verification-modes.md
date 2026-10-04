# Verification modes

Read only the section needed for the assigned change. These procedures do not add
features, execution capabilities or authority.

## UI and browser behavior

Use backend checks for validation/persistence, component checks for form logic and
a browser for interaction, rendering or integration that lower levels cannot prove.
Inspect the changed journey and representative rendered states before expensive
validation. A layout change does not waive interaction, source preservation or
undo/history requirements. Use the supplied interaction-design guidance when present.

Exercise the real application boundary when its wiring is at risk; keep permutations
in lower-level tests. Do not infer a browser requirement from the available tools.
Recheck actual capabilities before declaring a tool failure. Use a purpose-built
headless browser with a temporary profile, never the operator's normal profile;
Chromium on macOS uses `--use-mock-keychain`. Run compiler/type checks after type or
API changes before browser matrices. Diagnose one failing case and environment
before broadening, unless environment differences are the question.

## Time-based behavior

Use controlled time, deterministic synchronization and randomness where faithful.
Run fixed-size simulations without rendering or wall-clock pacing. Use a short
real-time smoke only when actual pacing or scheduler integration is being tested.

## Host-only and native-release proof

Use the approved sandbox verification path and host-only/native-release handoff.
Keep their proof distinct and require the applicable exact-candidate host receipt.
Do not repeatedly attempt a known unavailable operation or infer authority from a
running service. Report missing operator decisions or permissions through Runner's
supplied result contract while preserving partial work and completed evidence.
