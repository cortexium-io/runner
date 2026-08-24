#!/bin/sh

set -eu

version=${1:-}
output_dir=${2:-}

case "$version" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) printf '%s\n' 'usage: build-release.sh vMAJOR.MINOR.PATCH OUTPUT_DIR' >&2; exit 2 ;;
esac
case "$version" in
  *[!A-Za-z0-9._-]*) printf '%s\n' 'release version contains unsupported characters' >&2; exit 2 ;;
esac
if [ -z "$output_dir" ]; then
  printf '%s\n' 'usage: build-release.sh vMAJOR.MINOR.PATCH OUTPUT_DIR' >&2
  exit 2
fi
if [ -d "$output_dir" ] && [ -n "$(find "$output_dir" -mindepth 1 -maxdepth 1 -print -quit)" ]; then
  printf '%s\n' "release output directory must be empty: $output_dir" >&2
  exit 2
fi
mkdir -p "$output_dir"
output_dir=$(cd "$output_dir" && pwd)

build_root=$(mktemp -d "${TMPDIR:-/tmp}/cortexium-runner-build.XXXXXX")
cleanup() {
  rm -rf "$build_root"
}
trap cleanup EXIT HUP INT TERM

host_goos=$(go env GOOS)
host_goarch=$(go env GOARCH)
go build -o "$build_root/package-release" ./scripts/package-release.go

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
  goos=${target%/*}
  goarch=${target#*/}
  package="cortexium-runner-$version-$goos-$goarch"
  package_dir="$build_root/$package"
  binary="$package_dir/cortexium-runner"
  mkdir -p "$package_dir"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build \
    -buildvcs=false \
    -trimpath \
    -ldflags="-s -w -X main.version=$version" \
    -o "$binary" \
    ./cmd/cortexium-runner
  if [ "$goos" = "$host_goos" ] && [ "$goarch" = "$host_goarch" ]; then
    test "$("$binary" --version)" = "cortexium-runner $version"
  fi
  cp LICENSE README.md "$package_dir/"

  "$build_root/package-release" tar.gz "$package_dir" "$output_dir/$package.tar.gz"
done

(
  cd "$output_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum cortexium-runner-* >SHA256SUMS
  else
    shasum -a 256 cortexium-runner-* >SHA256SUMS
  fi
)

printf '%s\n' "Release assets written to $output_dir"
