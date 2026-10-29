# Go workspace process

This example accepts a change request for a small, committed Go project, runs a
coding step, retains its patch, then runs `go test ./...` without a model. It
contains configuration only; it does not depend on Runner's test fixtures.

Copy this example to your configuration location. Set `projects/seats/project.yaml`'s
`workspace` to your repository's absolute root and choose its starting `source.ref`.
Use a standard-library Go project with regular source files. The current writable
capability is qualified on the small local exercise described in the
[qualification record](../../docs/architecture.md#writable-go-qualification-29-october-2026),
not on arbitrary existing projects or this general request example.

`codex.read_only_paths` protects `go.mod` by default. Add the test files and other
source paths that your coding step must not change. The selected local Go binary,
Codex account, permissions and storage bounds are instance settings.

From the Runner repository, build and start the service with your configuration
and a short private state path outside system temporary directories:

```sh
go build -o cortexium-runner ./cmd/cortexium-runner
./cortexium-runner serve --config /absolute/path/to/config/instance.yaml --data "$HOME/.local/share/runner-go-example"
```

In another terminal, supply the actual task as a run input:

```sh
./cortexium-runner start --data "$HOME/.local/share/runner-go-example" --project seats --inputs '{"request":"Fix the behavior described by the existing failing tests."}'
./cortexium-runner inspect --data "$HOME/.local/share/runner-go-example" --run RUN_ID
./cortexium-runner wait --data "$HOME/.local/share/runner-go-example" --run RUN_ID
```

The role defines stable implementation instructions; `request` contains the
particular work. The worker returns a summary, and Runner produces the patch.
The check consumes that candidate, with read-only source and private scratch.
There is one attempt per step and no automatic repair. Completion requires
passing checks and retained source.

Inspect exposes the selected base, workspace, saved source and check result.
Successful/cancelled runs release settled worktrees after saving results. Failed
runs retain recoverable code until retry or history expiry. Unknown execution
holds its resources. Worker growth is monitored rather than governed by an OS
quota. See the [operator guide](../../docs/operator-reference.md#bounded-writable-go-execution)
for the supported lifecycle and limits.
