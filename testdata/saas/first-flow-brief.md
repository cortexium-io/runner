# First platform flow: delivery brief

Status: chosen Genny foundation with selective reuse; implementation not admitted

Observed: 2026-11-12, 09:21 UTC. Coordination: [Operations #65](https://github.com/techbase/operations/issues/65).

## Outcome

Make Cortexium useful as a multi-user work platform: configure a reusable flow,
run it in a permitted workspace, inspect its progress and accept its exact result.
Handler, Notes and social publishing are later applications of that platform.

The bounded first proof is an ordered flow:

**Agent reviews supplied text → named function packages the report → human
accepts or requests revision → saved result.**

Use a small step-list editor with typed inputs/outputs and frozen component
versions. Run the same saved flow on two different inputs. The Agent step uses
the existing local Worker (the current implementation of a Cortexium proxy).
The first function can be an allowlisted, deterministic report formatter;
general user-authored function execution is later work.

Use Genny for accounts, personal/shared workspaces and scoped application data.
Reuse Cortexium's Agent/Skill configuration, execution evidence, revision and
acceptance behavior where their meanings fit. The run view must show the current
step, responsible party, selected versions, results and any failure or required
human decision. A local proof with isolated fixture state is sufficient initially.

## Reuse and missing pieces

| Capability | Existing evidence | Needed for this slice |
| --- | --- | --- |
| SaaS/workspaces | Genny v0.4.0: auth, memberships, personal tenant creation, workspace selection and scoped models | Adopt in Cortexium; explicitly scope existing resources and bind Worker access to the trusted workspace |
| Agents/Skills | App: editable settings, explicit model/reasoning, immutable versions | Port these through project-owned services |
| Work and overview | App: durable assignments/attempts, reports, revision, cancellation, exact-result acceptance and attention/history views | Versioned ordered flows, step progress and typed handoffs |
| Proxy execution | Worker: supplied-text review, connection enrollment, journal/outbox, receipt recovery and uncertainty | Tenant-bound dispatch; demonstrate the retained review capability without broadening its grants |
| Functions/tools | No general function or MCP/API catalogue in current App | One named versioned deterministic function; authoring, external connections and credentials follow later |
| Broader platform | Supplied documents only; no Cortexium model gym, knowledge/context engine or general scheduler | Keep [Gym](gym.md), evaluations, memory/context, teams, API/MCP tools, triggers and richer proxy capabilities in the platform direction; none is implied complete by this proof |

Source inventory is not runtime qualification. The App is currently a single
local-operator Go/Vue application using SQLite, without Genny adoption. Genny's
tenant support does not automatically isolate its existing tables. Genny's
named jobs are not a scheduler. Worker currently advertises supplied-text review
only; its code-change executor/lifecycle work ([#15](https://github.com/cortexium-io/worker/issues/15),
[#16](https://github.com/cortexium-io/worker/issues/16)) is not required here.

## Completion evidence

- Two fixture users exercise personal and shared membership. Session-derived
  workspace identity and Worker binding govern reads, mutations, artifacts and
  dispatch. Cross-tenant access is denied; membership/grant removal is enforced
  before another protected action.
- A user configures, saves and runs the flow twice with different supplied text.
  Each run retains its input, component versions, step evidence and exact result.
- The human can request revision and accept the resulting exact version. The
  overview identifies whose action is needed and distinguishes awaiting acceptance,
  failed, cancelled and accepted work.
- A deterministic function failure stops progression with an inspectable reason.
  Unsupported Worker capability is rejected before dispatch. Restart or a lost
  receipt rejoins retained work without duplicating a completed model invocation.
- Runner supplies narrow headless Chrome for Testing proof of the successful
  journey, revision/failure handling and tenant denial, using deterministic Worker
  fixtures. Reuse relevant App/Worker/Protocol coverage and run required repository
  gates inside Runner; do not broaden tests without a concrete risk.
- A separate bounded local Worker/model proof demonstrates the real execution
  path after its existing host prerequisites are verified. No provider, Keychain
  or authentication changes are authorized by this brief.

## Chosen foundation and preserved boundaries

**Use a fresh Genny foundation in the owning App repository, with selective
reuse of Cortexium's working components.** Genny supplies accounts, workspaces,
memberships and generated scoped model handling. Port versioned Agents/Skills,
Worker integration and exact-result review through ordinary project-owned services.

This direction follows Dan's 12 November instruction in the Codex chat
“Plan the Handler vision” (`01a1b050-999e-72c1-aedc-fa962dad0934`):
“use Genny for the parts that it's good for” and “tenancy and user owned model
handling.” He repeated it in “Restore Techbase and Runner flow”
(`01a1c9d3-ddca-7751-b9f4-d2b11366a236`, user message
`01a1cf16-9186-7072-b155-1a6d13b4b306`), where this bounded route was selected.

Preserve current App/main, Worker, source history and existing data. Prove the new
foundation with isolated fixture state; no data migration or public deployment is
authorized. Generated model scoping supplies the standard CRUD boundary. Custom
endpoints, artifacts and Worker dispatch still require trusted tenant checks;
flow transitions, acceptance and proxy permissions remain Cortexium behavior.

Genny metadata/overwrite guards remain intact. No forced generation, invented
adoption manifest or dependency patch is part of this direction. Request a
Genny-owned change only if its supported extension points demonstrably cannot
meet the need.

## Runner admission prerequisite

Prepare this brief while Operations completes the existing Runner reliability,
integration and attention-notification work. The Jack's Break frontend delivery
route was qualified on 11 November, but the observed process-core configuration
contains only Jack's Break: **no Cortexium route is admitted yet**. Legacy
Cortexium GitHub Project registrations do not establish that route.

Before submitting implementation, the owning process must cover the chosen App
integration, pinned Genny lifecycle/ownership, required Go toolchain, Vue checks,
headless browser evidence and any declared Worker/Protocol boundaries. A generic
Go example or the Jack's Break default alone is insufficient. Public contract
changes belong in Protocol and must preserve existing supported clients.
Refresh relevant source references at admission. Submit this outcome once through
the qualified complete process, retain its run identity, and keep planning,
correction, review and integration inside it. No implementation was launched by
this brief; release and migration require their own authorization.

## Reproducible source checkpoint

Fetched repository refs on 12 November. App/Worker/Protocol `main` were clean and
equal to `origin/main`:

| Source | Full base commit |
| --- | --- |
| App | `6ad7f7f26c82a28521cb6f8131bcef7aa0318694` |
| Worker | `452308d4705e8b27c8f4519f82d86dd6b19cb6f0` |
| Protocol | `a7eb7b7e5ab3b46c66df62ef65aaeae893453438` |
| Docs local | `5347d898d71b9db5724c2bfb09d071cad1256048` |
| Docs origin/main | `e27dbb77fd3f4246b13c7cbe9b5fd2c23dd130c3` |
| Genny pin: v0.4.0 | `86fe57147760ad741303620610575ba107eead84` |

Genny v0.4.0 is annotated: tag object
`34bb6a21b6fd2f4773cf195033dbb38eca1e609f` peels to the commit pinned above.

Docs local was 15 commits ahead with an identical tree to origin/main; the
pre-existing untracked `.DS_Store` is unrelated. Do not reset or push that history
as part of this brief. Genny's installed CLI reports v0.4.0, but its local checkout
is older (`6bfc2c6`); fetched origin/main is `86fe571`. Use the explicit pin above.

Legacy status reads showed App/Worker/Docs stopped with zero active work. Worker
has blocked executor work and a dependent waiting card; Docs retains
[#83 / PR #84](https://github.com/cortexium-io/docs/pull/84) for Attempt history.
Preserve those outcomes and historical worktrees. Runner's separate maintenance
worktree has active unrelated edits; it was not modified or tested here.
