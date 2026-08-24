#!/bin/sh

set -eu

smoke_root=$(mktemp -d "${TMPDIR:-/tmp}/cortexium-runner-release.XXXXXX")
cleanup() {
  rm -rf "$smoke_root"
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$smoke_root/bin" "$smoke_root/home" "$smoke_root/cache" "$smoke_root/project"

go build -trimpath -o "$smoke_root/bin/cortexium-runner" ./cmd/cortexium-runner

cat >"$smoke_root/bin/gh" <<'EOF'
#!/bin/sh
case "$1 $2" in
  "--version ") printf '%s\n' 'gh version smoke-test' ;;
  "auth status") exit 0 ;;
	"project list") printf '%s\n' '[]' ;;
	"project link") exit 0 ;;
  "api user") printf '%s\n' 'smoke-user' ;;
  "api graphql")
    case "$*" in
      *"fields(first:100,after:"*) printf '%s\n' '{"data":{"node":{"fields":{"nodes":[{"__typename":"ProjectV2SingleSelectField","id":"F_status","name":"Status","dataType":"SINGLE_SELECT","options":[{"id":"O_assessment","name":"Needs assessment"},{"id":"O_backlog","name":"Backlog"},{"id":"O_plan","name":"Plan"},{"id":"O_ready","name":"Ready"},{"id":"O_running","name":"In Progress"},{"id":"O_qa","name":"Agent QA"},{"id":"O_pr_ready","name":"PR Ready"},{"id":"O_blocked","name":"Blocked"},{"id":"O_done","name":"Done"}]},{"__typename":"ProjectV2Field","id":"F_result","name":"Runner Result","dataType":"TEXT"},{"__typename":"ProjectV2Field","id":"F_approval","name":"Runner Approval","dataType":"TEXT"},{"__typename":"ProjectV2Field","id":"F_phase","name":"Runner Phase","dataType":"TEXT"},{"__typename":"ProjectV2Field","id":"F_activity","name":"Runner Activity","dataType":"TEXT"},{"__typename":"ProjectV2Field","id":"F_qa","name":"QA Failures","dataType":"NUMBER"},{"__typename":"ProjectV2Field","id":"F_branch","name":"Runner Branch","dataType":"TEXT"},{"__typename":"ProjectV2Field","id":"F_pr","name":"Pull Request","dataType":"TEXT"},{"__typename":"ProjectV2Field","id":"F_qa_commit","name":"QA Commit","dataType":"TEXT"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}' ;;
      *) printf '%s\n' '{"data":{"node":{"views":{"nodes":[{"id":"PVTV_board","name":"Board","layout":"BOARD_LAYOUT"}]}}}}' ;;
    esac ;;
  "project view") printf '%s\n' '{"id":"PVT_smoke","number":42}' ;;
  "repo view") printf '%s\n' '{"nameWithOwner":"example/runner","hasIssuesEnabled":true}' ;;
  "label list") printf '%s\n' '[{"name":"needs-assessment"}]' ;;
  *) printf '%s\n' "unexpected fake gh invocation: $*" >&2; exit 1 ;;
esac
EOF
chmod 700 "$smoke_root/bin/gh"

cat >"$smoke_root/bin/codex" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "--version" ]; then
  printf '%s\n' 'codex-cli smoke-test'
  exit 0
fi
if [ "${1:-}" = "--help" ] || { [ "${1:-}" = "exec" ] && [ "${2:-}" = "--help" ]; }; then
	printf '%s\n' '--ephemeral' '--json' '--cd' '--output-last-message' '--output-schema' '--config' '--model' '--sandbox' '--ask-for-approval' '--disable' '--strict-config' '--ignore-user-config' '--ignore-rules' '--skip-git-repo-check'
	exit 0
fi
printf '%s\n' "unexpected fake codex invocation: $*" >&2
exit 1
EOF
chmod 700 "$smoke_root/bin/codex"

export HOME="$smoke_root/home"
export XDG_CACHE_HOME="$smoke_root/cache"
export PATH="$smoke_root/bin:$PATH"

git init -q --bare "$smoke_root/origin.git"
git init -q -b main "$smoke_root/project"
git -C "$smoke_root/project" -c user.name='Smoke Test' -c user.email='smoke@example.invalid' commit --allow-empty -m initial >/dev/null
git -C "$smoke_root/project" config url."$smoke_root/origin.git".insteadOf https://github.com/example/runner.git
git -C "$smoke_root/project" remote add origin https://github.com/example/runner.git
git -C "$smoke_root/project" push -u origin main >/dev/null

dirty_file="$smoke_root/project/readiness-dirty.txt"
dirty_reference="$smoke_root/readiness-dirty.expected"
printf '%s\n' 'Runner readiness must preserve this dirty project file.' >"$dirty_reference"
cp "$dirty_reference" "$dirty_file"

config="$smoke_root/runner.config.json"
cortexium-runner init \
	--non-interactive \
  --owner example \
  --project-number 42 \
  --repository example/runner \
  --project-dir "$smoke_root/project" \
  --harness codex \
  --reasoning high \
  --base-update-review required \
  --config "$config"
cortexium-runner doctor --config "$config" --offline
cortexium-runner retry --help >/dev/null
doctor_output=$(cortexium-runner doctor --config "$config")
printf '%s\n' "$doctor_output"
printf '%s\n' "$doctor_output" | grep -Fqx 'Ready to run: yes'

test -f "$dirty_file"
cmp "$dirty_reference" "$dirty_file"

for skill in runner-planner runner-implementer runner-reviewer; do
  test -s "$HOME/.codex/skills/$skill/SKILL.md"
done

printf '%s\n' 'Packaged binary smoke test passed.'
