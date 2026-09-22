#!/bin/sh
set -eu

usage() {
	printf '%s\n' 'usage: sh scripts/test-reviewer-comparison.sh prepare|run EXACT_REVISED_SHA ABSOLUTE_ARTIFACT_DIRECTORY' >&2
	printf '%s\n' 'prepare: clean committed source, local fixtures/builds only, no model calls.' >&2
	printf '%s\n' 'run: requires independent fixture review and explicit admission; Codex gpt-6-astra/medium, at most 8 reviewer assignments total, 45m shared wall time, 300000 reported tokens; no retries.' >&2
}
if [ "$#" -ne 3 ]; then usage; exit 2; fi
case "$1" in prepare) test_name=TestPrepareReviewerComparison ;; run) test_name=TestLiveReviewerComparison ;; *) usage; exit 2 ;; esac
case "$3" in /*) ;; *) usage; exit 2 ;; esac
if [ "$(git rev-parse HEAD)" != "$2" ] || [ -n "$(git status --porcelain --untracked-files=all)" ]; then
	printf '%s\n' 'comparison requires the exact unchanged clean reviewed commit' >&2
	exit 2
fi
export CORTEXIUM_RUNNER_REVIEW_COMPARISON_MODE=$1
export CORTEXIUM_RUNNER_REVIEW_COMPARISON_APPROVED_CANDIDATE=$2
export CORTEXIUM_RUNNER_REVIEW_COMPARISON_DIRECTORY=$3
# No --repeat, model override, harness matrix, planner or implementer path.
go test ./internal/engine -run "^${test_name}$" -count=1 -timeout 50m -v
