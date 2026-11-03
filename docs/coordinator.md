# External coordinator contract

Runner owns the configured delivery process. An external chat owns understanding
the user's intent, submitting authorized work and reporting results or decisions.
It must not become a second worker or scheduler when something fails.

The coordinator may:

- Clarify a material missing decision and resolve the target project/process.
- Submit a concise request, original acceptance conditions, relevant file/line
  references and the process's declared inputs. Keep reusable guidance in roles.
- Call `runner_run` once with a stable request ID. Rejoin with `runner_result`
  and `wait: true` after a disconnect; a client timeout never authorizes a new run.
- Supply the agreed checkpoint as `source_ref`. Subsequent dependent work can
  select the latest accepted Runner candidate commit; do not submit against an
  older base and describe separate branches as combined integration.
- Read compact status when asked and report actual completion, resource usage,
  missing proof and cleanup/settlement limits.
- Present an unexpected blocker, its consequence, a recommended resolution and
  the specific user decision required. Retain the existing run identity.

It must not edit product code, run product tests/builds, integrate patches, launch
repair agents, alter configuration, change permissions, increase budgets, retry
or bypass a failed gate unless the user explicitly authorizes that operator action.
Correction, benchmark repetition and recovery already configured and authorized
in Runner do not need a new decision. The chat does not coordinate those iterations.
Missing process/capability is a blocker, never permission for external execution.
Explicit user requests to investigate or maintain Runner itself are separate work.

Use a fresh short intake with only `runner_run`, `runner_result` and
`runner_status` exposed through the existing native Codex MCP configuration.
Disable shell, editing, browsing, subagents, apps, skill/plugin discovery and
unrelated MCP servers. Give it an empty read-only workspace. Supply the private
owner token through the process environment, never its prompt or tracked files.
This enforces the boundary for that intake; instructions in a general desktop
chat cannot revoke its already-available tools. Tell such a chat the contract,
then use the restricted intake for new work.
The portable [intake profile and launcher](../examples/intake/README.md) require
explicit operator paths and spend no extra coordination turns between stages.

The [reviewed Go example](../examples/reviewed-go/README.md) supplies the complete
first configured delivery process. Verification uses fixed argv; review is
read-only; local candidate integration requires an approving review. The same
core follows bounded outcome routes and expands declared task templates; task
data cannot supply new roles or permissions. Model choice, repetition and step
limits remain explicit configuration. Saved decisions are resumed only by an
explicitly authorized owner action through `runner_decide`; a restricted intake
must not approve its own work.

Completion means the process's declared outcome. Report a local reviewed
candidate as ready for integration/release decisions; never describe it as
merged, deployed or fully qualified beyond the actual proof. Coordinator token
usage remains additional to the Runner result and must be included in any
end-to-end benchmark.

The [approved release process](../examples/approved-release/README.md) adds the
model-free Jack's Break release operator. It owns qualification reuse, protected
merge, backups, explicit migration, Compose deployment and live verification. Two
saved decisions separate merge from production approval. Intake presents the
receipt and exact decision ID to the owner and must never approve it. Operator
configuration/credentials remain separate from coding roles. Uncertain remote
effects require inspection; a client disconnect grants no replacement operation.
