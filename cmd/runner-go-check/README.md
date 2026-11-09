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

A research process can instead request one declared scope with `--run`. It is
exclusive with `--regression-run` and `--development-run` and launches Go once.
This mode also accepts `--package ...` or `./...` for the repository package
set; a bare `...` becomes `./...`. Other paths remain repository-relative.

```sh
runner-go-check --go /absolute/pinned/go --package ... \
  --run '^TestNonogramResearch' --timeout 10m \
  --scratch /owned/scratch --output /owned/scratch/proof.json
```

The single-scope artifact has `package` and `scope`, with the literal selector,
actual test outcomes keyed by package and name, every observed package's terminal
outcome, counts and child exit/settlement facts. An unfinished test or package is
marked `incomplete`. Packages with no selected tests may finish without tests;
the whole scope must still execute at least one passing leaf test to pass.
Explicit compiler failures retain `build_failures` with bounded diagnostics and
the failing package's `failed_build` reference. They never count as executed
assertions. Receipt JSON is capped at the existing 16 MiB artifact ceiling.

Single-scope exit codes are 0 for passed proof, 1 for conclusive source feedback
(settled Go exit 1, complete events, no selected skipped tests, and an actual
failing test or explicit linked build failure with diagnostics), and 2 for
inconclusive collection, missing/skipped proof, runtime/start/timeout or report
failure. A configured process can map 1 to repair and 2 to blocked. A build failure
without the supported Go build/package event linkage remains inconclusive.
The existing two-scope mode retains its exit behavior: 0 only when both pass,
otherwise 1.

New planned checks also pass all three optional prepared-input flags together:

```sh
  --source-base <Runner-resolved-full-base> \
  --input-patch-id <literal-declared-patch-artifact-id> \
  --input-patch /owned/scratch/inputs/patch
```

The optional `prepared_input` object records `source_base`, `input_patch_id`,
`input_patch_sha256`, `input_patch_bytes`, `input_verified`, and
`runtime.{go_version,goos,goarch}`. The helper hashes the bounded regular patch
before and after settled Go execution and rejects file identity or byte drift.
It queries the configured child Go executable with `go env -json GOVERSION GOOS
GOARCH` in the same environment and shared timeout as the test command. Runtime
query or input-verification failure cannot produce a green check or conclusive
source feedback. Legacy invocations omit this object and keep their behavior.

This is prepared-input provenance, not a full source digest or an approval.
Runner's fixed command supplies the resolved workspace base, the typed literal
patch artifact ID, and its staged bytes after authenticating that patch against
the prepared source. A standalone caller can supply its own labels; those labels
alone do not establish Runner authority or freshness. Retained receipts remain
context from their original producer and require its owning provenance; no old
receipt is retroactively certified by the new fields.

The private JSON artifact lists actual terminal test/subtest outcomes and counts,
including parents, plus passing executed leaf tests, elapsed/exit/settlement facts
and the first failing test's diagnostic head and tail, at most 2 KiB combined.
Any selected skipped case is missing required proof. No tests, skipped proof, incomplete
evidence, command failure or timeout fails the check. Development remains
explicitly `not_run` when regression fails. Stdout also preserves proof JSON up to
48 KiB so failed command logs retain outcomes. Above that cap it explicitly marks
test names omitted and keeps aggregate counts, first failure and reasons; the
file still has full names. In single-scope mode, the preview also explicitly marks
omitted evidence lists; the artifact keeps all package/build details. A short
summary includes each scope's result/reason.

Events above 1 MiB, malformed JSON, names above 512 bytes or more than 1024 test
identities are drained and mark evidence incomplete; package/build identities use
the same 512-byte name and 1024-identity bounds. No full log is saved. Go and its
tests inherit the caller-owned process group so Runner's group cancellation
reaches them. Go's explicit test timeout gets two seconds of settlement grace
before the reporter cancels its direct child; it waits for that child's exit.
Missing or timeout evidence remains failed/incomplete. The reporter does not own
or promise cleanup of detached processes; the outer worker owns group settlement.
SIGKILL of the reporter can prevent a final receipt. The caller supplies the pinned binary, selected source,
owned scratch and cache; this tool neither creates workspaces nor changes policy.
