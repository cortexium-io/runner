# Getting useful work from smaller local models

Use smaller local models for small, well-specified pieces of work. This is our
recommended starting point for getting useful results from them: supply the
investigation, keep the relevant context short, define the output precisely, and
check the result independently. A stronger coordinator can prepare these tasks
when the data may be shared with that coordinator.

This is an operating recommendation supported by limited trials, not a guarantee
that every local model will perform best this way. Model, quantization, inference
server, task and harness all affect the result. Measure your own accepted outputs
before changing production defaults.

## Choose a task the worker can finish

Good initial candidates include a known one-function correction, a mechanical
edit across an explicitly listed set of files, implementing a supplied function
contract, or extracting specified fields from supplied documents. State the
boundary conditions, including missing values and malformed input.

Keep architecture decisions, broad repository investigation and ambiguous
requirements with the coordinator. A short request such as “fix validation” can
still leave a large investigation to the worker. A concrete request can be longer
and much easier to complete.

Prepare each assignment with:

- One outcome and the exact files or input records involved.
- Existing relevant source or a narrow location, plus facts already established.
- Allowed changes and behavior that must remain unchanged.
- Literal examples, including important boundary and error cases.
- A focused check or output schema and a clear stopping condition.

For example:

> Change only the existing feedback normalization function. Trim surrounding
> Unicode whitespace first. Reject an empty result. Accept at most 1,000 UTF-8
> bytes; reject longer results with the existing error. Preserve internal
> whitespace. Run the supplied failing test, add the missing check, rerun the
> focused tests and check the diff. Preserve the tests and all other files.
> Report the actual before/after result, then finish.

Supply a regression test when the coordinator already knows the failure. Record
that contribution separately from the worker's patch. For extraction, specify
field names, missing-value rules, ordering, and how later corrections override
earlier records.

## Verify the work and measure the whole job

Treat the worker's completion statement as a claim to verify. Check the actual
diff or output, unchanged supplied inputs/tests, and cases the worker did not see.
A model can satisfy a prepared test while introducing an adjacent regression.
Review matters even for small edits.

During a pilot, run one attempt at a time and diagnose a failure before admitting
another attempt. The initial expected failure of a supplied regression test is
part of reproduction, not a failed worker attempt. Record repairs and retries
separately instead of presenting the final passing result alone.

Measure:

- Accepted results and independent findings, including failed and unfinished work.
- Preparation, model execution, verification and review time separately.
- Exact model/variant, inference host, context, harness, reasoning settings,
  request counts and reported token usage.
- Human intervention, retries and monetary cost where actually available.

Local inference does not establish that the whole workflow is cheaper. Include
coordination, review, elapsed time and machine costs in that decision. Unknown
cost is not zero. Equal timeouts do not rank task quality or speed; retain the
actions and artifacts that explain where each attempt stopped.

For the Qwen configuration used in our trials, working requests use low reasoning
without a separate reasoning-token cap. A completion-token limit and an overall
task deadline still apply. Verify the actual request settings. Runner deliberately
disables thinking for its final structured formatting request; a benchmark monitor
must distinguish that stage from coding requests.

## Apply this to Runner

Write ordinary Ready cards with a narrow scope, explicit completion conditions
and focused proof obligations. Keep each card useful and reviewable on its own.
Use the existing planner and reviewer roles to prepare and assess the work, subject
to the data boundary below. Runner's normal role contracts, approvals and
verification requirements still apply.

These recommendations do not introduce a separate microtask dispatcher or change
Runner's retry/escalation policy. A direct Pi worker experiment measures a smaller
execution path than a complete Runner card lifecycle. Validate the full lifecycle
before relying on it unattended.

See the [operator reference](operator-reference.md),
the [profile evaluation guidance](model-profile-evaluation.md), and the
[Pi setup notes](../README.md).

## Keep sensitive work inside its intended boundary

For privacy-sensitive work, keep the relevant inputs, planning, inference, tools,
logs, output and review local. A cloud coordinator or reviewer also receives data
when it prepares or checks a local worker's assignment. Share only information
approved for that destination; keep sensitive preparation and review local too.

Runner normally coordinates through GitHub Projects and can publish issues,
pull requests and review evidence. Selecting a local model does not make that
workflow offline. Avoid placing confidential content in cloud-backed cards or
reports when the requirement is local-only processing; such work may need a
separate local workflow.

Pi's host-access tools also retain their host permissions. A local endpoint,
disabled network-oriented extensions, or a prompt saying “stay local” is not an
OS-enforced network boundary. Use an appropriate execution environment when strict
isolation is required. Start privacy-workflow evaluations with synthetic data and
check the complete data path before using confidential material.

## What the trials establish

In one assisted Qwen 3.8 27B 6-bit MLX trial, a coordinator supplied a failing
regression test, exact filter functions and message examples. The model edited
production code after about 16 minutes and passed the prepared and independent
predeclared checks. Additional review found that it discarded malformed message
envelopes. The final handoff was interrupted by a benchmark-monitor mistake,
so full completion was not established.

Earlier hour-long attempts on that historical bug, with less assistance, produced
no edits. This suggests that concrete preparation deserves further evaluation.
It does not isolate which part of the assistance helped or establish a speed
ranking. The adjacent regression is direct evidence for keeping independent
review in the workflow.

The bounded-task pilot below evaluates a smaller worker interface separately
from those full-role trials. It uses synthetic or isolated fixtures and does not
establish confidential-data isolation, production reliability or a cloud cost
advantage.

### Bounded-task pilot, 4 October 2026

Both tasks completed on their first attempt and passed independent checks and
coordinator review. No coordinator edits or repair attempts were needed.

| Task | Worker elapsed | Independent acceptance |
| --- | --- | --- |
| Add a byte-limit guard to one normalization function | 2m47s | Three-line patch; 7 supplied and 6 withheld cases passed; only the allowed production file changed |
| Extract open tasks from synthetic internal notes | 2m26s | Four exact JSON records; later corrections, reopened/closed tasks, missing values and ordering handled correctly; source unchanged |

The coding fixture isolated Runner's existing 1,000-byte retry-feedback rule.
It supplied the function, error values and failing tests; it was not a replay of
the whole GitHub integration change. The extraction fixture contained six invented
tasks and four later corrections. Its expected result stayed outside the worker's
workspace. Grading was calibrated against correct and deliberately incorrect
results before inference.

Conditions were Pi 1.0.0, Qwen 3.8 27B 6-bit MLX through local LM Studio, an Apple
M4 Pro with 48 GiB memory, 80,896-token loaded context and one inference request
at a time. All 12 observed requests used low reasoning with no separate reasoning
budget. The overall completion limit remained 16,384 tokens, with a one-hour
deadline per task. Codemode, automatic retries and ambient extensions were
disabled. Each task used a 66-word worker instruction and a 148- or 153-word brief;
tool definitions and subsequent tool results added context.

The coding case used read, edit and shell tools; extraction used only read and
write. Reported input/output usage was 19,232/1,250 tokens for coding and
6,530/1,201 for extraction. Input totals include repeated context across requests;
reported output includes reasoning.

Worker elapsed time includes the Pi process and its tools. Model loading took
another 13.6s and 11.8s respectively. Independent automated checks took 0.6s and
0.1s. Measured fixture preparation took 5m15s, excluding earlier task selection
and discussion; it included benchmark setup and calibration. Coordinator review
was completed but its active effort was not separately metered. No complete
cost-per-accepted-task comparison is available.

These are two feasibility results for one model, not a reliability estimate for
smaller models generally. There was no cloud baseline, matched full-role arm,
network-isolation test or measured energy/hardware cost. The machine's caches and
other workloads were not controlled. Use this as evidence that tightly prepared
tasks can complete successfully, then evaluate representative work and the full
cost of acceptance before scaling the workflow.
