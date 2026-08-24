#!/bin/sh

set -eu

fail() {
	printf '%s\n' "cortexium-runner installer: $*" >&2
	exit 1
}

if [ "$#" -gt 1 ]; then
	printf '%s\n' 'usage: install.sh [vMAJOR.MINOR.PATCH]' >&2
	exit 2
fi

releases_url=${CORTEXIUM_RUNNER_RELEASES_URL:-https://github.com/cortexium-io/runner/releases}
releases_url=${releases_url%/}
version=${1:-}

command -v curl >/dev/null 2>&1 || fail 'curl is required'
command -v tar >/dev/null 2>&1 || fail 'tar is required'

if [ -z "$version" ]; then
	latest_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$releases_url/latest") || fail 'could not resolve the latest release'
	version=${latest_url##*/}
fi
printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || fail "unsupported release version: $version"

case "$(uname -s)" in
	Darwin) platform=darwin ;;
	Linux) platform=linux ;;
	*) fail "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
	x86_64|amd64) architecture=amd64 ;;
	arm64|aarch64) architecture=arm64 ;;
	*) fail "unsupported architecture: $(uname -m)" ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
	sha256_file() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256_file() { shasum -a 256 "$1" | awk '{print $1}'; }
else
	fail 'sha256sum or shasum is required'
fi

if [ -n "${CORTEXIUM_RUNNER_INSTALL_DIR:-}" ]; then
	install_dir=$CORTEXIUM_RUNNER_INSTALL_DIR
else
	[ -n "${HOME:-}" ] || fail 'HOME is required unless CORTEXIUM_RUNNER_INSTALL_DIR is set'
	install_dir=$HOME/.local/bin
fi

package=cortexium-runner-$version-$platform-$architecture
archive=$package.tar.gz
download_root=$(mktemp -d "${TMPDIR:-/tmp}/cortexium-runner-install.XXXXXX")
install_temp=
cleanup() {
	if [ -n "$install_temp" ]; then
		rm -f "$install_temp"
	fi
	rm -rf "$download_root"
}
trap cleanup EXIT HUP INT TERM

archive_path=$download_root/$archive
checksums_path=$download_root/SHA256SUMS
download_base=$releases_url/download/$version

curl -fsSL "$download_base/$archive" -o "$archive_path" || fail "could not download $archive"
curl -fsSL "$download_base/SHA256SUMS" -o "$checksums_path" || fail 'could not download SHA256SUMS'

expected_checksum=$(awk -v archive="$archive" '$2 == archive { count++; checksum = $1 } END { if (count == 1) print checksum }' "$checksums_path")
[ -n "$expected_checksum" ] || fail "SHA256SUMS does not contain exactly one entry for $archive"
printf '%s\n' "$expected_checksum" | grep -Eq '^[0-9a-fA-F]{64}$' || fail "invalid checksum for $archive"
actual_checksum=$(sha256_file "$archive_path")
[ "$actual_checksum" = "$expected_checksum" ] || fail "checksum verification failed for $archive"

entries_path=$download_root/entries
tar -tzf "$archive_path" >"$entries_path" || fail "could not inspect $archive"
while IFS= read -r entry; do
	case "$entry" in
		"$package"|"$package/"|"$package/cortexium-runner"|"$package/LICENSE"|"$package/README.md") ;;
		*) fail "unexpected archive entry: $entry" ;;
	esac
done <"$entries_path"

unpacked=$download_root/unpacked
mkdir "$unpacked"
tar -xzf "$archive_path" -C "$unpacked" || fail "could not extract $archive"
binary=$unpacked/$package/cortexium-runner
[ -f "$binary" ] && [ ! -L "$binary" ] || fail 'release archive does not contain a regular cortexium-runner binary'
chmod 755 "$binary"
[ "$("$binary" --version)" = "cortexium-runner $version" ] || fail 'downloaded binary reported an unexpected version'

mkdir -p "$install_dir" || fail "could not create install directory: $install_dir"
install_temp=$(mktemp "$install_dir/.cortexium-runner.XXXXXX") || fail "could not create a temporary file in $install_dir"
cp "$binary" "$install_temp" || fail 'could not copy the verified binary'
chmod 755 "$install_temp"
mv -f "$install_temp" "$install_dir/cortexium-runner" || fail "could not install into $install_dir"
install_temp=

printf '%s\n' "Installed cortexium-runner $version to $install_dir/cortexium-runner"
case ":${PATH:-}:" in
	*":$install_dir:"*) ;;
	*) printf '%s\n' "Add $install_dir to PATH to run cortexium-runner directly." ;;
esac
