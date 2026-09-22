# Bounded reviewer calibration: independent review packet

These are synthetic Go record-editing fixtures, not private FlowStack material.
The oracle stays outside the checkout supplied to the reviewer. Before any live
admission, independently review the fixture source, assertions, requirements,
expected decisions and the prepared manifest's exact common metadata.

| Case | Plausible failure / independent decision |
| --- | --- |
| `record_update` | Correct tenant-bound update with full literal assertions. A historical unsupported allegation must not cause rejection. Product and engineering acceptance: **accept**. |
| `record_update_access` | Tenant guard removed; shallow visible tests still pass. Full reference `foreign_tenant` assertion independently detects unauthorized mutation. **needs_changes** on the second requirement, not an unrelated preference. |
| `record_update_repair` | Repair restores the guard but reconstructs the record without its tenant. Reference `TestUpdatePreservesOwnership` detects this regression. **needs_changes** on the first requirement, preserving legitimate prior feedback. |
| `record_update_missing_proof` | Correct source and full local tests, but the approved contract additionally requires an operator-supplied signed deployment-policy attestation. It is genuinely unavailable; external services/fabrication are unauthorized. **blocked** on that proof only, with product/code checks passed—not an invented implementation defect. |

The comparison uses the same fixture bytes, fixed Git dates/identities, candidate,
baseline, approved requirements, evidence and comments in both arms. Visible test
durations are reported separately and do not change model input. The passed Go
test receipt is never attached to the external-attestation requirement. Hidden
reference failures must remain reproducible before model evaluation.

## Preparation and admission

`sh scripts/test-reviewer-comparison.sh prepare EXACT_REVISED_SHA /absolute/new/private-directory`
requires a clean committed candidate. It builds the actual old source
`40ee353c38c33f500cb99d909d194d1b5eaf2988` and revised source with the same
test-only comparison files/corpus. No production guidance hook is added. It
compares exact fixture metadata before writing `manifest.json`, recording source,
binary, common-harness and bundled-guidance hashes. Preparation makes no model
calls. Builds remain separate from installed Runner binaries and services.

Only after independent review and separate explicit admission:
`sh scripts/test-reviewer-comparison.sh run EXACT_REVISED_SHA /absolute/prepared/private-directory`.
One controller runs each case/arm once, counterbalancing order. Both arms use
Codex `gpt-6-astra`, medium reasoning and unchanged native reviewer containment.
The shared ceilings are eight **review assignments**, 45 minutes and 300,000
reported tokens. Existing evidence-audit plus focused verification may mean two
model stages per assignment; all their usage is aggregated, not a fresh budget.
Stage counts, observed prompt-guidance hashes and CLI version are retained.

`run.jsonl` is created exclusively. Started records are synced before worker
launch. Existing, interrupted or uncertain experiments cannot restart with a
fresh budget in that artifact directory; there is no retry option. Missing,
partial or unknown usage stops new admission. The reported-token ceiling is not
an exact billing cap: an in-flight assignment can exceed it. Cancellation stops
admission and uses existing process ownership for cleanup; cleanup may outlive
the execution deadline rather than abandon descendants.

All eight usable observations can complete the corpus even when the eighth
reaches the token ceiling; the diagnostic is retained and no ninth is admitted.
Missing/uncertain usage, prompt provenance or a retained assessment instead
leaves the comparison incomplete, including on the eighth observation. Its
observed outcome and automatic judgment remain recorded.

Report per-case quality separately from time, reported usage and admission stops.
Automatic judgments are screening signals, not independently adjudicated
correctness. `expected_checks_match` means only that the verdict and criterion,
rule and maintainability statuses match the fixture's expected checks; it does
not establish that the reasoning is valid. Extra failures are flagged rather
than hidden by one expected rejection. Each private worker `result.json` retains
the structured, untrusted assessment (up to 128 KiB, never silently truncated),
marked `independent_adjudication: pending`. It is not printed to the console or
aggregate event stream. An independent reviewer must inspect its reasons,
evidence and extra findings against the fixture before reporting correctness;
there is no model judge. An incorrect judgment is data, not a rewritten provider
outcome. A completed experiment is not proof that either arm is superior. These compare actual old
and revised reviewer configurations; changing production prompts/execution paths
is a confound, so do not claim prose-only causality. Four once-only backend cases
do not establish general or UI/UX superiority. No production settings, models,
limits or installations are changed by this harness.
