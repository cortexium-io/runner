# Focused Go proof

This configured command runs one regression scope, then one disjoint development
scope, in the caller's existing working directory and the same environment/cache.
It launches fixed argv once per scope: `go test -json -mod=readonly -count=1` with
the explicit package, literal selector argument and timeout. No shell or retries.
Package directories are workspace-relative: `internal/puzzles` and
`./internal/puzzles` both select `./internal/puzzles`; `.` selects the root.
Bare directories are never passed to Go as dependency import paths.

```sh
runner-go-check --go /absolute/pinned/go --package ./internal/puzzles \
  --regression-run '^TestRegression$' \
  --development-run '^TestDevelopment$/^(16|20|30)$/^easy$' \
  --timeout 5m --scratch /owned/scratch --output /owned/scratch/proof.json
```

Regression selectors cannot contain `/`, so selected parents keep all children.
Development selectors start with one exact `^TestName$`; optional subtest regex
components must be anchored. The regression regex must exclude that development
function. The tool does not infer what a test proves from diagnostic text.

The private JSON artifact lists actual terminal test/subtest outcomes and counts,
including parents, plus passing executed leaf tests, elapsed/exit/settlement facts
and the first failing test's diagnostic head and tail, at most 2 KiB combined.
Any selected skipped case is missing required proof. No tests, skipped proof, incomplete
evidence, command failure or timeout fails the check. Development remains
explicitly `not_run` when regression fails. Stdout also preserves proof JSON up to
48 KiB so failed command logs retain outcomes. Above that cap it explicitly marks
test names omitted and keeps aggregate counts, first failure and reasons; the
file still has full names. A short summary includes each scope's result/reason.

Events above 1 MiB, malformed JSON, names above 512 bytes or more than 1024 test
names are drained and mark evidence incomplete; no full log is saved. Go and its
tests inherit the caller-owned process group so Runner's group cancellation
reaches them. Go's explicit test timeout gets two seconds of settlement grace
before the reporter cancels its direct child; it waits for that child's exit.
Missing or timeout evidence remains failed/incomplete. The reporter does not own
or promise cleanup of detached processes; the outer worker owns group settlement.
SIGKILL of the reporter can prevent a final receipt. The caller supplies the pinned binary, selected source,
owned scratch and cache; this tool neither creates workspaces nor changes policy.
