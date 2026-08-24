#!/bin/sh
set -eu

usage() {
	printf '%s\n' 'usage: scripts/test-agent-behavior.sh --candidate SHA --repeat 1|2 --max-tokens N [--smoke] [--codex-model MODEL] [--claude-model MODEL] [--pi-model PROVIDER/MODEL --allow-pi-host] [--reasoning LEVEL] [--case-timeout-seconds N] [--max-seconds N] [--max-cost-usd N] HARNESS[,HARNESS...]' >&2
	printf '%s\n' '  HARNESS may be codex, claude, or pi; model overrides apply only to their selected harness.' >&2
	printf '%s\n' '  --allow-pi-host accepts that Pi implementer/reviewer calls are not OS-sandboxed.' >&2
	printf '%s\n' '  --smoke runs one demanding planner case plus the implementer/reviewer journey per harness.' >&2
}

candidate=
repeat=
case_timeout_seconds=1200
max_seconds=4500
max_tokens=
max_cost_usd=
codex_model=
claude_model=
pi_model=
reasoning=high
allow_pi_host=false
smoke=false

while [ "$#" -gt 0 ]; do
	case "$1" in
		--candidate) candidate=${2:-}; shift 2 ;;
		--repeat) repeat=${2:-}; shift 2 ;;
		--case-timeout-seconds) case_timeout_seconds=${2:-}; shift 2 ;;
		--max-seconds) max_seconds=${2:-}; shift 2 ;;
		--max-tokens) max_tokens=${2:-}; shift 2 ;;
		--max-cost-usd) max_cost_usd=${2:-}; shift 2 ;;
		--codex-model) codex_model=${2:-}; shift 2 ;;
		--claude-model) claude_model=${2:-}; shift 2 ;;
		--pi-model) pi_model=${2:-}; shift 2 ;;
		--reasoning) reasoning=${2:-}; shift 2 ;;
		--allow-pi-host) allow_pi_host=true; shift ;;
		--smoke) smoke=true; shift ;;
		--help|-h) usage; exit 0 ;;
		--) shift; break ;;
		-*) usage; exit 2 ;;
		*) break ;;
	esac
done

if [ "$#" -ne 1 ] || [ -z "$candidate" ] || [ -z "$max_tokens" ]; then
	usage
	exit 2
fi
case "$repeat" in
	1|2) ;;
	*) usage; exit 2 ;;
esac

harnesses=$1
case "$harnesses" in
	""|,*|*,|*,,*) usage; exit 2 ;;
esac
harness_count=0
includes_pi=false
includes_codex=false
includes_claude=false
seen_harnesses=,
remaining=$harnesses
while [ -n "$remaining" ]; do
	case "$remaining" in
		*,*) harness=${remaining%%,*}; remaining=${remaining#*,} ;;
		*) harness=$remaining; remaining= ;;
	esac
	case "$harness" in
		codex) includes_codex=true ;;
		claude) includes_claude=true ;;
		pi) includes_pi=true ;;
		*) usage; exit 2 ;;
	esac
	case "$seen_harnesses" in
		*,"$harness",*) usage; exit 2 ;;
	esac
	seen_harnesses=$seen_harnesses$harness,
	harness_count=$((harness_count + 1))
done
if [ -z "$reasoning" ]; then
	usage
	exit 2
fi
if [ "$includes_codex" != true ] && [ -n "$codex_model" ]; then
	printf '%s\n' '--codex-model is only valid when codex is selected' >&2
	exit 2
fi
if [ "$includes_claude" != true ] && [ -n "$claude_model" ]; then
	printf '%s\n' '--claude-model is only valid when claude is selected' >&2
	exit 2
fi
if [ "$includes_pi" = true ]; then
	case "$pi_model" in
		*/*) ;;
		*) usage; exit 2 ;;
	esac
	if [ "$allow_pi_host" != true ]; then
		printf '%s\n' 'Pi implementer/reviewer evaluation requires --allow-pi-host because Pi has no native OS sandbox.' >&2
		exit 2
	fi
elif [ -n "$pi_model" ]; then
	printf '%s\n' '--pi-model is only valid when pi is selected' >&2
	exit 2
elif [ "$allow_pi_host" = true ]; then
	printf '%s\n' '--allow-pi-host is only valid when pi is selected' >&2
	exit 2
fi
cases_per_harness=4
if [ "$smoke" = true ]; then
	cases_per_harness=2
fi
expected_cases=$((harness_count * cases_per_harness))
expected_total=$((expected_cases * repeat))

head_sha=$(git rev-parse HEAD)
if [ "$head_sha" != "$candidate" ]; then
	printf '%s\n' "candidate $candidate does not match HEAD $head_sha" >&2
	exit 2
fi
if [ -n "$(git status --porcelain --untracked-files=all)" ]; then
	printf '%s\n' 'live evaluation requires a clean candidate worktree' >&2
	exit 2
fi

artifact_dir=$(mktemp -d "${TMPDIR:-/tmp}/cortexium-runner-eval.XXXXXX")
chmod 700 "$artifact_dir"
printf '%s\n' "Live evaluation candidate: $candidate"
printf '%s\n' "Sanitized artifacts: $artifact_dir"

run=1
while [ "$run" -le "$repeat" ]; do
	current_sha=$(git rev-parse HEAD)
	if [ "$current_sha" != "$candidate" ] || [ -n "$(git status --porcelain --untracked-files=all)" ]; then
		printf '%s\n' "run $run requires the unchanged clean candidate $candidate" >&2
		exit 2
	fi
	artifact=$artifact_dir/run-$run.jsonl
	export CORTEXIUM_RUNNER_EVAL_HARNESSES=$harnesses
	export CORTEXIUM_RUNNER_EVAL_CANDIDATE=$candidate
	export CORTEXIUM_RUNNER_EVAL_RUN=$run
	export CORTEXIUM_RUNNER_EVAL_ARTIFACT=$artifact
	export CORTEXIUM_RUNNER_EVAL_CODEX_MODEL=$codex_model
	export CORTEXIUM_RUNNER_EVAL_CLAUDE_MODEL=$claude_model
	export CORTEXIUM_RUNNER_EVAL_REASONING=$reasoning
	if [ "$includes_pi" = true ]; then
		export CORTEXIUM_RUNNER_EVAL_PI_MODEL=$pi_model
		export CORTEXIUM_RUNNER_EVAL_ALLOW_PI_HOST=1
	else
		unset CORTEXIUM_RUNNER_EVAL_PI_MODEL || true
		unset CORTEXIUM_RUNNER_EVAL_ALLOW_PI_HOST || true
	fi
	export CORTEXIUM_RUNNER_EVAL_CASE_TIMEOUT_SECONDS=$case_timeout_seconds
	export CORTEXIUM_RUNNER_EVAL_MAX_SECONDS=$max_seconds
	export CORTEXIUM_RUNNER_EVAL_MAX_TOKENS=$max_tokens
	if [ "$smoke" = true ]; then
		export CORTEXIUM_RUNNER_EVAL_SMOKE=1
	else
		unset CORTEXIUM_RUNNER_EVAL_SMOKE || true
	fi
	if [ -n "$max_cost_usd" ]; then
		export CORTEXIUM_RUNNER_EVAL_MAX_COST_USD=$max_cost_usd
	else
		unset CORTEXIUM_RUNNER_EVAL_MAX_COST_USD || true
	fi
	printf '%s\n' "Starting live matrix run $run/$repeat for $harnesses"
	go test ./internal/engine -run '^TestLiveRunnerBehaviorEval$' -timeout 80m -count=1 -v
	completed=$(grep -c '"event":"completed"' "$artifact")
	if [ "$completed" -ne "$expected_cases" ] || ! grep -q '"event":"summary".*"passed":true' "$artifact"; then
		printf '%s\n' "run $run did not produce a complete sanitized $expected_cases-case summary" >&2
		exit 1
	fi
	run=$((run + 1))
done

printf '%s\n' "Live evaluation passed $expected_total/$expected_total scenarios for $harnesses at $candidate"
