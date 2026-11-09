# Deterministic process

From the repository root, build and start Runner with a new private state path:

```sh
go build -o cortexium-runner ./cmd/cortexium-runner
./cortexium-runner serve --config examples/demo/instance.yaml --data /absolute/new/private-state
```

In another terminal:

```sh
./cortexium-runner start --data /absolute/new/private-state --project demo --inputs '{"name":"Hello, Dan"}'
```

The first step writes a retained file; the second consumes it and returns
`Hello, Dan`. This uses no model or external service. The role files demonstrate
YAML and JSON with the same schema. See the [operator guide](../../docs/operator-reference.md)
for inspection, cancellation, retries and configuration editing.
