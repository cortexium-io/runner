# Contributing

Runner is a local task engine. Public contributions start as GitHub Issues.
Use the bug or feature form and include enough sanitized evidence for assessment.
The `needs-assessment` label is triage metadata: an issue or board entry grants
no execution authority. A maintainer explicitly enters and approves local work.

## Development

Use the Go version in `go.mod`. Build `./cmd/cortexium-runner`; see the
[operator guide](docs/operator-reference.md) for a fresh local configuration.
The [redesign plan](docs/runner-redesign-proposal.md) owns the frozen release work
and testing policy. Define supported behavior and the verification brief before
editing. Extend the owning suite for credible regression risks; preserve useful
coverage when retiring old paths.

Run `go test -race -timeout 15m ./...` and `go vet ./...`. Release tooling is
documented in the [README](README.md). Live model evaluations are separate,
explicitly paid runs; the normal suite skips them. Report actual evidence and
any unverified boundary.

## Pull requests

External pull requests are not accepted for now. The repository restricts pull
request creation to collaborators so maintainers and the autonomous runner can
still use reviewed branches internally. Please do not prepare a code change in
expectation that it will be merged; open an issue first.

This policy may change when the project has a contributor review and ownership
model that can safely coexist with autonomous development.

## Security reports

Do not disclose vulnerabilities in a public issue. Follow [SECURITY.md](SECURITY.md)
to report them privately.

## Community behavior

Participation in Issues and other project spaces is governed by
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
