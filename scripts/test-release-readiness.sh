#!/bin/sh

set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
live_config=""

usage() {
	printf '%s\n' 'usage: test-release-readiness.sh [--live-config PATH]'
	printf '%s\n' '  --live-config PATH  also run Runner Doctor with a real harness model probe'
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--live-config)
			if [ "$#" -lt 2 ] || [ -z "$2" ]; then
				usage >&2
				exit 2
			fi
			live_config=$2
			shift 2
			;;
		--help|-h)
			usage
			exit 0
			;;
		*)
			printf '%s\n' "unknown option: $1" >&2
			usage >&2
			exit 2
			;;
	esac
done

readiness_root=$(mktemp -d "${TMPDIR:-/tmp}/cortexium-runner-readiness.XXXXXX")
cleanup() {
	rm -rf "$readiness_root"
}
trap cleanup EXIT HUP INT TERM

cd "$repository_root"

run_step() {
	name=$1
	shift
	printf '\n%s\n' "$name"
	"$@"
}

verify_source_format() {
	unformatted=$(gofmt -l cmd internal scripts skills)
	if [ -n "$unformatted" ]; then
		printf '%s\n' 'Go files require formatting:' >&2
		printf '%s\n' "$unformatted" >&2
		return 1
	fi
	git diff --check
	sh -n scripts/build-release.sh
	sh -n scripts/install.sh
	sh -n scripts/release-smoke-test.sh
	sh -n scripts/verify-release-source.sh
	sh -n scripts/test-release-readiness.sh
	sh -n scripts/test-agent-behavior.sh
}

run_step 'Source formatting and script syntax' verify_source_format
run_step 'Race-enabled test suite' \
	go test -count=1 -race ./...
run_step 'Static analysis' \
	go vet ./...
run_step 'Known-vulnerability scan' \
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
run_step 'Module checksum verification' \
	go mod verify
run_step 'Packaged binary smoke test' \
	sh scripts/release-smoke-test.sh
run_step 'Cross-platform release build' \
	sh scripts/build-release.sh v0.0.0-readiness "$readiness_root/release"

asset_count=$(find "$readiness_root/release" -maxdepth 1 -type f | wc -l | tr -d ' ')
checksum_count=$(wc -l <"$readiness_root/release/SHA256SUMS" | tr -d ' ')
if [ "$asset_count" -ne 5 ] || [ "$checksum_count" -ne 4 ]; then
	printf '%s\n' "unexpected release asset set: files=$asset_count checksums=$checksum_count" >&2
	exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
	(cd "$readiness_root/release" && sha256sum --check SHA256SUMS)
else
	(cd "$readiness_root/release" && shasum -a 256 --check SHA256SUMS)
fi

if [ -n "$live_config" ]; then
	case "$live_config" in
		/*) ;;
		*) live_config=$(CDPATH= cd -- "$(dirname -- "$live_config")" && pwd)/$(basename -- "$live_config") ;;
	esac
	if [ ! -f "$live_config" ]; then
		printf '%s\n' "live config does not exist: $live_config" >&2
		exit 1
	fi
	run_step 'Build live-probe binary' \
		go build -trimpath -o "$readiness_root/cortexium-runner" ./cmd/cortexium-runner
	run_step 'Live GitHub and harness probe' \
		"$readiness_root/cortexium-runner" doctor --config "$live_config" --probe-harnesses
fi

printf '\n%s\n' 'Release readiness passed.'
printf '%s\n' 'Covered: new and existing Projects, empty and initialized remotes, local-config Git protection and repair, dry-run and apply, interactive and scripted init, blocked-item retry, role overrides, review policies, deterministic two-card parallel execution, automatic-merge safety, Project repair, packaged binary startup, and every published platform.'
if [ -z "$live_config" ]; then
	printf '%s\n' 'Live GitHub mutations are intentionally not performed. Pass --live-config to add a read-only GitHub inspection and real model probe.'
fi
