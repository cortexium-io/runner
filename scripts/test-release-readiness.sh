#!/bin/sh

set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
if [ "$#" -ne 0 ]; then
 printf '%s\n' 'usage: test-release-readiness.sh (local checks only; no model calls or publication)' >&2
 exit 2
fi

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
	unformatted=$(gofmt -l cmd internal scripts)
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
	go test -count=1 -race -timeout 15m ./...
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

printf '\n%s\n' 'Local release readiness passed.'
printf '%s\n' 'Covered: local-core race tests, static analysis, vulnerability and module checks, real local CLI smoke, four no-CGO archives and checksums.'
printf '%s\n' 'R5 still requires native macOS/Linux qualification, final evidence review and an approved cutover. No live model or GitHub calls, publication or operator installation occurred.'
