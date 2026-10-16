#!/bin/sh
# Exercise the shipped entry point without credentials, a model call or GitHub.
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
root=$(mktemp -d "${TMPDIR:-/tmp}/cortexium-runner-smoke.XXXXXX")
root=$(cd "$root" && pwd -P)
trap 'rm -rf "$root"' EXIT HUP INT TERM
cd "$repository_root"

binary=${1:-"$root/cortexium-runner"}
if [ "$#" -gt 1 ]; then
 printf '%s\n' 'usage: release-smoke-test.sh [ABSOLUTE_BINARY]' >&2
 exit 2
fi
if [ "$#" -eq 0 ]; then
 go build -trimpath -ldflags="-X main.version=v0.0.0-smoke" -o "$binary" ./cmd/cortexium-runner
 test "$("$binary" --version)" = 'cortexium-runner v0.0.0-smoke'
fi
"$binary" --version | grep -Eq '^cortexium-runner [^ ]+$'
"$binary" --help >"$root/help"
"$binary" >"$root/noargs"
cmp "$root/help" "$root/noargs"
grep -q 'backup' "$root/help"
grep -q 'task' "$root/help"
if grep -Eq 'doctor|runner-next|Projects|self-update' "$root/help"; then
 printf '%s\n' 'legacy interface leaked into help' >&2; exit 1
fi

git init --quiet --initial-branch=main "$root/repo"
git -C "$root/repo" -c user.name=Smoke -c user.email=smoke@example.invalid commit --quiet --allow-empty -m Initial
sed "s|/absolute/project|$root/repo|g; s|/absolute/runner/state|$root/state|g; s|/absolute/runner/worktrees|$root/worktrees|g" examples/runner.config.json >"$root/runner.json"
chmod 600 "$root/runner.json"
"$binary" --config "$root/runner.json" init >"$root/preview"
grep -q 'Preview only' "$root/preview"
test ! -e "$root/state"
test ! -e "$root/worktrees"
if "$binary" --config "$root/runner.json" status >"$root/status" 2>&1; then
 printf '%s\n' 'status unexpectedly created authority' >&2; exit 1
fi
grep -q 'inspection never creates it' "$root/status"
test ! -e "$root/state"

if "$binary" --config "$root/runner.json" doctor >"$root/retired-error" 2>&1; then
 printf '%s\n' 'retired command was accepted' >&2; exit 1
fi
grep -q 'unknown command' "$root/retired-error"
test ! -e "$root/state"

printf '%s\n' '{"project":{"owner":"example","number":1}}' >"$root/legacy.json"
if "$binary" --config "$root/legacy.json" init >"$root/legacy-error" 2>&1; then
 printf '%s\n' 'legacy configuration was accepted' >&2; exit 1
fi
grep -q 'unknown field' "$root/legacy-error"
printf '%s\n' 'Local CLI smoke passed: version, help, setup preview, strict config and read-only missing authority.'
