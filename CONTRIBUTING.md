# Contributing

Runner is a configurable process engine. Public contributions start as GitHub
Issues; an issue or board entry does not authorize execution. Include enough
sanitized evidence for assessment. External pull requests are not accepted for
now; maintainers use reviewed branches internally.

Use the Go version in `go.mod` and build `./cmd/cortexium-runner`. The
[README](README.md), [architecture](docs/architecture.md) and
[process-core design](docs/process-core-design.md) describe the clean first slice.
Earlier runtime plans and qualification records are historical references.

Define the behavior and concrete regression risk before editing. Extend the
owning suite, consolidate related cases where useful and avoid duplicate checks.
Run `go test -race -timeout 15m ./...` and `go vet ./...`. Normal checks make no
model calls. Report actual evidence and unverified boundaries.

Native Codex qualification is separately opt-in and spends model usage. Its
initial three-task allowance has been used; obtain a new explicit bounded
authorization before enabling it. Extend focused offline adapter coverage instead
of repeating paid executions or the entire core matrix for each protocol change.

Report vulnerabilities privately through [SECURITY.md](SECURITY.md).
Participation is governed by [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
