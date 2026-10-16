#!/bin/sh
# Explicit opt-in: these cases invoke the authenticated Codex CLI and incur usage.
set -eu
usage() {
 printf '%s\n' 'usage: test-agent-behavior.sh ready|plan|combined|ci-repair|conflict-repair MODEL REASONING NEW_EVIDENCE_DIR'
}
if [ "$#" -eq 1 ] && { [ "$1" = --help ] || [ "$1" = -h ]; }; then usage; exit 0; fi
if [ "$#" -ne 4 ]; then usage >&2; exit 2; fi
case "$1" in
 ready) test_name=TestLiveReadyReviewBehavior ;;
 plan) test_name=TestLivePlanningBehavior ;;
 combined) test_name=TestLiveCombinedOutcomeBehavior ;;
 ci-repair) test_name=TestLiveCIRepairBehavior ;;
 conflict-repair) test_name=TestLiveConflictRepairBehavior ;;
 *) usage >&2; exit 2 ;;
esac
model=$2
reasoning=$3
evidence=$4
[ -n "$model" ] && [ -n "$reasoning" ] || { usage >&2; exit 2; }
[ ! -e "$evidence" ] || { printf '%s\n' 'evidence directory must be new' >&2; exit 2; }
# Keep live evidence bound to one clean candidate.
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$script_dir/.."
repository_root=$(pwd -P)
if [ -n "$(git status --porcelain --untracked-files=all)" ]; then
 printf '%s\n' 'live evaluation requires a clean candidate worktree' >&2; exit 2
fi
candidate=$(git rev-parse HEAD)
case "$evidence" in
 /*) ;;
 *) printf '%s\n' 'evidence directory must be an absolute path outside the checkout' >&2; exit 2 ;;
esac
umask 077
mkdir -p "$evidence"
evidence=$(cd "$evidence" && pwd -P)
case "$evidence/" in
 "$repository_root/"*)
 printf '%s\n' 'evidence directory must be outside the checkout' >&2; exit 2 ;;
esac
{
 printf 'candidate=%s\nsuite=%s\nmodel=%s\nreasoning=%s\n' "$candidate" "$test_name" "$model" "$reasoning"
} >"$evidence/source.txt"
result=0
RUNNER_LOCAL_LIVE_MODEL="$model" RUNNER_LOCAL_LIVE_REASONING="$reasoning" RUNNER_LOCAL_LIVE_DIR="$evidence/cases" \
 go test -count=1 -timeout 40m -v ./internal/runner/execution -run "^$test_name$" >"$evidence/evaluation.log" 2>&1 || result=$?
cat "$evidence/evaluation.log"
if [ "$(git rev-parse HEAD)" != "$candidate" ] || [ -n "$(git status --porcelain --untracked-files=all)" ]; then
 printf '%s\n' 'candidate changed during evaluation; results are not release evidence' >&2; exit 1
fi
exit "$result"
