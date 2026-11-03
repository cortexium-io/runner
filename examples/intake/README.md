# Restricted chat intake

This is a client profile, not a Runner instance. The only exposed tools submit,
rejoin and inspect Runner work. Editing, shell, browsing, apps, plugins, host
skills and subagents are disabled. It cannot approve its own delivery decision.
Copy it to private operator configuration and select the actual loopback endpoint.
Keep the owner token outside source; the launcher supplies it through the process
environment. Existing general desktop chats retain their capabilities; directing
new execution through this fresh profile enforces the narrower tool boundary.

```sh
go run ./cmd/runner-intake --execute \
  --cli /absolute/path/to/qualified/codex \
  --config /private/intake/config.toml \
  --auth /private/normal-account/auth.json \
  --token /private/runner-state/owner.token \
  --request /private/authorized-request.txt \
  --deadline FUTURE_ABSOLUTE_RFC3339_TIME
```

The request supplies the explicit project/process, stable request ID, source ref,
original task and acceptance conditions. Scope-specific process configuration
belongs in Runner, not in the request's conversational history. Already-specified
requests can use the ordinary Runner CLI/MCP directly and spend no intake model
turn. User decisions resume through the owner interface using the saved ID.

The launcher requires explicit instance/account/executable paths so the portable
tool cannot silently choose a personal pilot. It uses an empty private read-only
workspace, retains bounded usage/result diagnostics and cleans its temporary data
only after child settlement. Disconnecting intake never cancels Runner work.
