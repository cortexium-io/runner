# Cortexium Runner

Runner turns explicitly approved work into reviewed changes. One local coordinator
and a private transactional store own tasks, approvals, execution policy, evidence
and recovery. GitHub Projects is not required.

Enter a **Ready** task for implementation, or a **Plan** request for proposed,
dependency-aware tasks. Review the exact preview before approval; releasing a
plan separately authorizes its children. Runner implements in isolated worktrees,
runs the agreed checks and asks an independent reviewer to assess the result.

The replacement is now the sole `cortexium-runner` executable. This branch is a
local release candidate: R5 final qualification and cutover remain. Nothing in
this documentation publishes a release or upgrades an existing installation.

## Start

Install Git and an authenticated Codex CLI separately. To build, use the Go version
in `go.mod`:

```sh
go build -o ./cortexium-runner ./cmd/cortexium-runner
./cortexium-runner --version
./cortexium-runner --help
```

Follow the [operator guide](docs/operator-reference.md) and edit the
[example configuration](examples/runner.config.json) for fresh, non-overlapping
absolute paths. Preview initialization, approve a repository quality agreement,
enter work, then start the coordinator. No service is installed automatically.

```sh
./cortexium-runner --config /absolute/runner/runner.json init
./cortexium-runner --config /absolute/runner/runner.json status
```

Initialization needs its displayed preview before apply. Status never creates
authority. The guide covers the complete flow, configuration editing, limits and
escalation, interruptions, delivery and same-host backup/restore.

## Supported boundary

- Sandboxed Codex handles planning, implementation and independent review.
  Models, reasoning, timeouts, parallelism and bounded allowances are explicit.
- Local delivery retains an accepted candidate. Optional GitHub delivery creates
  a draft, verifies the current base, assesses pinned CI and performs bounded
  repair. External ready marking and host reviews remain human steps.
- Human merge is the default. Explicit or configured automatic merge requires
  guarded ordinary merges and supported branch protection. Completion requires
  an exact confirmed integration receipt.
- Boards, Trello, additional harnesses, automatic issue intake, CI reruns,
  background remote polling, services and self-update are outside this release.
  Narrow adapter contracts leave room for later integrations.
- Store format 15 and strict local configuration deliberately break compatibility.
  Preserve older binaries with their state and backups. Reconcile outstanding
  remote effects, initialize fresh authority and approve only remaining work.
  There is no import of old approvals or in-place upgrade.

## Local release checks and packaging

```sh
go test -race -timeout 15m ./...
go vet ./...
sh scripts/release-smoke-test.sh
sh scripts/build-release.sh v0.0.0-candidate /absolute/empty/release-output
```

`test-release-readiness.sh` combines the race suite, vet, pinned vulnerability
scan, module checks, real CLI smoke and four no-CGO archive builds. It makes no
model calls or GitHub mutations. [Behavior evaluations](docs/model-profile-evaluation.md)
are separate opt-in calls. R5 additionally requires native macOS/Linux evidence,
the packaged operator journey and final cutover review.

Archives support macOS/Linux on amd64/arm64 and contain the binary, license,
README, `OPERATOR.md` and `runner.example.json`; `SHA256SUMS` covers all four.
For offline instructions, open `OPERATOR.md` from the extracted archive.
The HTTPS/checksum-verifying installer copies only the verified binary to the
selected directory. It does not migrate configuration, install skills or change
services. Do not use it to cut over an existing installation before R5 approval.

Release publication remains an explicitly dispatched, reviewed-source workflow.
It requires an exact lightweight version tag on the reviewed main commit,
immutable verified build artifacts and an isolated publication job. Building local
archives creates no tag, release or installation.

See [architecture](docs/architecture.md), [ADR 0005](docs/decisions/0005-local-runner-core.md),
the [release checklist](docs/runner-redesign-proposal.md#frozen-first-release-checklist),
[contribution policy](CONTRIBUTING.md) and [security policy](SECURITY.md).
