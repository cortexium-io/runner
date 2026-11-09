# Model-free command

This example runs one explicit command against a committed repository's README.
It needs the installed Codex sandbox and Go runtime, but submits no model turn.
It contains no source fixture; select your repository in `project.yaml`.

Copy this directory to your private configuration location. Set the repository's
absolute path and choose `source.ref`. The shared workspace implementation currently
requires concurrency one, an explicit Go binary and short private state paths
outside system temporary directories.

```sh
go build -o cortexium-runner ./cmd/cortexium-runner
./cortexium-runner serve --config /absolute/path/to/config/instance.yaml --data "$HOME/.local/share/runner-command"
```

In another terminal:

```sh
./cortexium-runner start --data "$HOME/.local/share/runner-command" --project command-demo --inputs '{}'
./cortexium-runner wait --data "$HOME/.local/share/runner-command" --run RUN_ID
```

The result reports exit status and a short output tail. Change the role's purpose
and argv to select a relevant check; a command role does not decide test scope.
Commands must be synchronous and keep their children in the same process group.
The sandbox grants read-only source and writable attempt scratch, with no network.
Runtime dependencies need explicit instance `codex.command_read_paths` entries.
See the [operator contract](../../docs/operator-reference.md#configured-command-execution)
for artifact handoff, environment substitution, recovery and platform limits.
