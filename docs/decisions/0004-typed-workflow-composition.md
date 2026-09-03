# Decision: Typed workflow composition over three role contracts

Date: 2026-09-03

## Context

Runner's v4 workflow mixed state, role assignment, transition routing, and
entry actions inside lane objects while pull-request reactions used a separate
event shape. Custom role profiles could inherit planner, implementer, or
reviewer behavior, but the configuration did not expose one coherent event and
action model. Making configuration arbitrarily programmable would add unstable
plugin, sandbox, ordering, and compatibility contracts that Runner does not
need.

## Options

1. Keep the lane-specific v4 fields and add more special cases.
2. Compose a fixed catalog of typed triggers and actions, while retaining the
   three fixed role contracts and mandatory Runner safety invariants.
3. Add user-defined scripts, expressions, role contracts, and plugins.

## Decision

Choose option 2. Configuration v5 names Project lanes separately from typed
rules. Each rule binds one trigger to one action. Action outcomes transition to
another lane, whose `lane.entered` event can trigger the next action. The first
catalog contains:

- triggers: `lane.entered`, `pull_request.merged`,
  `pull_request.closed`, `pull_request.checks_failed`, and
  `pull_request.out_of_date`;
- actions: `run_role`, `transition`, `publish_pull_request`, and
  `update_branch`.

`run_role` accepts any configured role profile that resolves to the planner,
implementer, or reviewer contract. Multiple profiles of the same contract may
therefore occupy different lanes. `plan_lane` and `ready_lane` identify the
default human entry points without relying on map order or role-name guesses.

One action per rule is deliberate. It gives retries and ordering one obvious
meaning; larger flows compose through explicit transitions. Runner compiles
validated rules into its existing small coordinator model. Authentication,
dependency checks, resource conflicts, bounded concurrency and retries,
candidate integrity, independent review before publication, and serialized
integration cannot be disabled by configuration.

Recognized transient harness-provider failures are operational retries below
the configurable workflow graph: Runner retains the current role lane, exposes
the wait as activity, and uses a small bounded delay without consuming an Agent
QA rejection. Exhaustion enters the rule's ordinary error transition.

## Consequences

Lanes become simple external state names, and events and actions use one
configuration vocabulary. Operators can inspect the compiled meaning with
`cortexium-runner workflow validate` and `cortexium-runner workflow explain`.
Configuration v4 is intentionally not accepted at runtime; v5 removes its
`role`, `creates_in`, `transitions`, and `on_enter` lane fields and the separate
`events` collection.

Specialties such as security review, accessibility review, migration, or UI
implementation remain inherited profiles rather than new base contracts.
Publishing, merging, branch refresh, approval, and coordination remain
deterministic actions or human policy rather than agent roles. Arbitrary
expressions, shell commands, HTTP actions, plugins, and user-defined role
contracts are deferred.

## Revisit When

Add another typed primitive only after a concrete workflow cannot be expressed
by the current catalog. Consider a fourth read-only `analyst` contract only
after several real tasks need durable findings without cards, a candidate
change, or an acceptance verdict. Consider plugins only when independently
developed providers or actions are a core requirement and their permissions,
resource ownership, idempotency, and lifecycle can be specified stably.
