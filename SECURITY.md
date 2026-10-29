# Security policy

Runner's clean process-core slice is a local, single-owner service. The operator,
configuration and private state directory are trusted. It does not provide a
hostile multi-user boundary.

## Reporting

Use [GitHub private vulnerability reporting](https://github.com/cortexium-io/runner/security/advisories/new).
Include the affected version/commit, impact and safe reproduction steps. Do not
put credentials, private code or vulnerability details in public issues.

## Current boundary

The service accepts numeric loopback listeners only. MCP requires a private owner
bearer token, loopback Host and matching Origin when supplied. The CLI uses that
same MCP service; neither configuration files nor SQLite are a separate remote API.
There is no supported public proxy, remote OAuth/TLS or multiple-user access model.

The supported Codex profile executes text-only assignments through one private
`codex app-server` per instance. It verifies the selected model/reasoning, read-only
policy, empty task environments/workspace roots, disabled external integrations
and absent instruction-file discovery before submitting a turn. Repository coding,
arbitrary skills/tools and filesystem write access are unsupported. A role or task
cannot grant itself additional authority. These native controls were qualified
only for the documented fixture on CLI 0.164.0, not a hostile-code/multi-user sandbox.

The private native home replaces normal user configuration. Its account link lets
the trusted harness use the selected existing local credentials; account/runtime
links and the private socket alias are counted without following them. Links are
not accepted as artifacts. Cleanup removes owned links/data, preserving the source
account file. Native managed credential refresh remains normal harness behavior.

State is private to the owner account; an OS lock prevents duplicate coordinators.
Kernel socket-peer identity, rather than a saved PID alone, authorizes stopping
a reconnected native service. Native storage is monitored with a stop threshold;
it is not a hard filesystem quota or total token/cost cap.
SQLite snapshots and controlled files are bounded. Recovery reconciles uncertainty
before replacement, and Release waits for a saved terminal result. These controls
do not protect against a malicious owner changing files or deleting state.

This branch has a distinct database format and no migration from previous Runner
state. Keep installed binaries/state intact. Publication, installation, backup/
restore, remote control and production cutover require later qualification.
