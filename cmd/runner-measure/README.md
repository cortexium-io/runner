# Bounded Go measurement

`runner-measure` runs the configured benchmark once with `go test -v`,
`-benchtime=1x`, `-count=1` and no ordinary tests. Runner owns subsequent rounds.
Verbose mode streams benchmark `B.Log` lines, avoiding Go's ten-line buffered-log
preview. The retained `measurements.json` still has a 256 KiB combined output
ceiling; overflow fails collection rather than becoming usable baseline feedback.

The original benchmark can be collected unchanged with `--baseline`. Its full
logs remain evidence even when it predates the research sidecar interface.
`evidence_status=incomplete` honestly records that no structured files were
provided; verbose logging does not invent them or change acceptance/routing.

New research collectors can write JSON files to the fresh
`RUNNER_MEASURE_EVIDENCE` directory. The helper retains at most 64 regular JSON
files and 8 MiB of sidecar data under the existing 16 MiB report ceiling.
`evidence_status=provided` means files were retained; configured independent review
must judge sample completeness, bounds and supported conclusions. Existing
release measurement acceptance and bounded-round semantics are unchanged.
