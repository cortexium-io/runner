# Input-only Codex process

Build Runner, then start it using your existing Codex account and a new private
state path:

```sh
go build -o cortexium-runner ./cmd/cortexium-runner
./cortexium-runner serve --config examples/codex/instance.yaml --data /absolute/new/codex-state
```

In another terminal:

```sh
./cortexium-runner start --data /absolute/new/codex-state --project demo --inputs '{"document":"Booking Desk opens Tuesday at noon."}'
```

The first step uses Sol / Extra High to summarize the supplied text. The second
consumes that result without a model. No workspace or task tools are enabled.
Starting this example submits model work; ordinary repository tests do not.
See the [operator guide](../../docs/operator-reference.md#input-only-codex-execution)
for supported settings and recovery behavior.
