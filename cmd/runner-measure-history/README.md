# Complete measurement history

This fixed Go command combines explicit complete candidate measurement files for
independent review. It does not run benchmarks, discover Core artifacts, read
Runner state or judge performance, correctness, coverage or scientific claims.
The original pristine baseline remains a separate process input.

```sh
runner-measure-history --source /owned/source --checks /inputs/checks.json \
  --measurement /inputs/round1.json --measurement /inputs/round2.json \
  --measurement /inputs/profiled-before.json --measurement /inputs/current.json \
  --output /owned/scratch/measurement-history.json

runner-measure-history --source /owned/source \
  --previous /inputs/measurement-history.json --measurement /inputs/new.json \
  --output /owned/scratch/new-history.json
```

`--previous` may instead name the initial complete raw candidate report. At most
eight `--measurement` flags and eight distinct retained reports are supported;
the complete output remains within 16 MiB. Paths must be explicit, clean absolute
paths with canonical directory parents. Inputs must be regular files without
links. Output cannot alias an input; an existing different output is preserved.
Output publication is atomic, private and synchronized. No record is dropped to
make room: malformed evidence or an exceeded bound fails collection.

The `runner-measure-history-v1` JSON contains `entries` in supplied evidence order.
Each entry has a sequence number, workflow-local round, package/selector/start
metadata, source stamp, byte size, SHA256 and `measurement`: a JSON string holding
the **exact original report bytes**. Decode that string to inspect the report.
Appending deduplicates identical bytes only. Instrumentation/source changes and
round resets in a continuation remain distinct; sequence numbers never relabel
their original rounds or invent a historical run identity.

Candidate reports require complete console bytes/hash, an actual benchmark,
nonempty safe structured JSON evidence and the verified versioned source stamp.
Failed, skipped, baseline, incomplete or unverified reports are refused. This is
collection completeness, not semantic acceptance or evidence that old incidents
were reproduced. Every original report and sidecar still needs independent review.

With `--source`, the command recomputes the same versioned literal source snapshot
used by `runner-measure` and requires **the last retained report** to match its
hash, executable modes, paths, file count and byte count. Hashing and verification
share a ten-minute bound; existing configured command/attempt limits still apply.
`source_bindings` retains links to reports matched against an actual workspace.
Later appends preserve earlier reports and source bindings. No caller-supplied
checksum can substitute for reading the explicit workspace.

Optional `checks` preserves exact original Go-check JSON as `{kind, bytes, sha256,
json}` context. It never certifies that check as passing or bound to current source.
An original check without source identity must have its existing producer/source
records verified by the authorized submitting operator during intake. This helper
does not automate or certify that cross-run provenance; it enforces only the last
measurement's current-source snapshot when `--source` is supplied. Existing check
context cannot be replaced by different bytes.
