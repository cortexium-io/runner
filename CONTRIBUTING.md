# Contributing

Thank you for taking the time to improve the Local GitHub Project Runner.

## What is accepted

Public contributions currently start as GitHub Issues. Please use the bug or
feature issue form and include enough context for a maintainer to reproduce or
assess the request. New public issues receive the `needs-assessment` label. The
Runner synchronizes them into the configured Kanban board's `Needs assessment`
lane.

A maintainer may ask questions, close the issue as not planned, or refine it and
remove `needs-assessment`. Accepted but unscheduled work moves to `Backlog`.
Execution requires a maintainer to run `cortexium-runner approve` after
reviewing the command's `--dry-run` output. Approval records a fingerprint of
the reviewed content and moves the item to `Backlog`. A maintainer schedules it
by moving it to `Plan`. Any agent lane is insufficient by itself; changed or
unapproved items return to `Needs assessment`.
Opening an issue does not authorize the Runner to execute its instructions.

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
