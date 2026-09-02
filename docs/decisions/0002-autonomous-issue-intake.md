# Decision: Trust-gated autonomous issue intake

Date: 2026-09-02

## Context

Runner already synchronizes labeled GitHub issues into `Needs assessment`, but
every issue and every planner batch requires a separate human approval. Private
repositories often use issues as direct instructions, while public issue text
is untrusted input. The runner needs to act on clear trusted requests, ask for
human decisions when necessary, and retain one coherent planning and authority
model.

## Options

1. Add a separate triage agent that chooses planner, implementer, or human.
2. Route apparently simple issues directly to `Ready` with deterministic text
   heuristics.
3. Reuse the planner for every trusted issue and automate only the existing
   approval boundaries.

## Decision

Choose option 3.

This decision narrows ADR 0001's human-approval rule for explicitly configured
trusted issue intake; all other intake keeps that rule.

Autonomous intake is explicit opt-in configuration. A labeled issue is trusted
when the configured intake repository is private, or when its author is on the
configured allowlist for a public repository. GitHub Project visibility is not
a trust signal. Runner verifies repository and issue identity, previews the
ordinary approval, rechecks trust immediately before mutation, and routes the
exact signed issue to `Plan`.

The planner remains the only classification boundary. It either returns open
decisions, which Runner posts to the issue before blocking for human retry, or
stages one or more dependency-aware implementation cards. If the source is
still trusted, Runner rechecks it and releases the exact authenticated batch
through the existing approval path. Untrusted issue intake retains human
assessment and batch approval.

Runner does not place source-closing keywords in child pull requests. It closes
each implementation issue only after reconciling that issue's authenticated
merged-pull-request outcome. It closes the original planning issue only after
every exact child in the authenticated released batch has the same successful
outcome. A planning card reaching `Done` means planning finished; it does not by
itself mean the original request is complete.

## Consequences

There is no second triage prompt, role, or direct-to-implementer heuristic.
Simple issues may incur a planner call, but authorization, ambiguity handling,
decomposition, and dependency creation remain one coherent path. Deterministic
trust, approval, and issue-completion reconciliation does not consume harness
parallelism. Multiple child PRs may merge in any order without closing the
source early, and a closure API failure is retryable housekeeping rather than a
workflow-wide failure.

Private-repository trust is broad by design, so operators must leave the option
disabled when issue creation in that repository is not an acceptable work
authorization boundary. Public autonomy requires maintaining an explicit author
allowlist.

## Revisit When

Revisit if measured planner cost or latency makes clear single-card requests
materially inefficient, or if GitHub exposes a stronger repository-native
authorization signal that can replace the explicit author allowlist without
weakening the trust boundary.
