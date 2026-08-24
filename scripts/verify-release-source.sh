#!/bin/sh

set -eu

version=${1:-}
reviewed_commit=${2:-}
remote=${3:-origin}
expected_commit=${4:-}

fail() {
	printf '%s\n' "release source verification failed: $*" >&2
	exit 1
}

if [ "$#" -lt 2 ] || [ "$#" -gt 4 ]; then
	printf '%s\n' 'usage: verify-release-source.sh vMAJOR.MINOR.PATCH REVIEWED_COMMIT [REMOTE [EXPECTED_COMMIT]]' >&2
	exit 2
fi

if ! printf '%s\n' "$version" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
	fail "invalid version tag: $version"
fi
if ! printf '%s\n' "$reviewed_commit" | grep -Eq '^[0-9a-f]{40}$'; then
	fail 'reviewed commit must be a full lowercase SHA-1'
fi
if [ -n "$expected_commit" ] && ! printf '%s\n' "$expected_commit" | grep -Eq '^[0-9a-f]{40}$'; then
	fail 'expected tag commit must be a full lowercase SHA-1'
fi

resolved_reviewed_commit=$(git rev-parse --verify "$reviewed_commit^{commit}" 2>/dev/null) ||
	fail "reviewed commit is unavailable: $reviewed_commit"
if [ "$resolved_reviewed_commit" != "$reviewed_commit" ]; then
	fail "reviewed commit does not resolve exactly: $reviewed_commit"
fi
if [ "$(git rev-parse --verify HEAD)" != "$reviewed_commit" ]; then
	fail 'the reviewed checkout does not match the dispatch commit'
fi

tag_ref="refs/tags/$version"
remote_lines=$(git ls-remote --refs "$remote" "$tag_ref") ||
	fail "cannot inspect $tag_ref on $remote"
line_count=$(printf '%s\n' "$remote_lines" | awk 'NF { count++ } END { print count + 0 }')
if [ "$line_count" -ne 1 ]; then
	fail "remote lightweight tag is missing or ambiguous: $tag_ref"
fi
remote_object=$(printf '%s\n' "$remote_lines" | awk -v ref="$tag_ref" '$2 == ref && NF == 2 { print $1 }')
if ! printf '%s\n' "$remote_object" | grep -Eq '^[0-9a-f]{40}$'; then
	fail "remote returned an invalid object for $tag_ref"
fi

git fetch --quiet --no-tags --force "$remote" "$tag_ref" ||
	fail "cannot fetch $tag_ref from $remote"
fetched_object=$(git rev-parse --verify FETCH_HEAD 2>/dev/null) ||
	fail "cannot resolve fetched $tag_ref"
if [ "$fetched_object" != "$remote_object" ]; then
	fail "$tag_ref moved while it was being verified"
fi
object_type=$(git cat-file -t "$fetched_object" 2>/dev/null) ||
	fail "cannot inspect fetched object $fetched_object"
if [ "$object_type" != commit ]; then
	fail "$tag_ref is not a lightweight tag"
fi
if [ -n "$expected_commit" ] && [ "$fetched_object" != "$expected_commit" ]; then
	fail "$tag_ref moved from expected commit $expected_commit to $fetched_object"
fi
if [ "$fetched_object" != "$reviewed_commit" ]; then
	fail "$tag_ref does not identify the exact reviewed dispatch commit"
fi

confirmed_lines=$(git ls-remote --refs "$remote" "$tag_ref") ||
	fail "cannot recheck $tag_ref on $remote"
if [ "$confirmed_lines" != "$remote_object$(printf '\t')$tag_ref" ]; then
	fail "$tag_ref moved after it was fetched"
fi

printf 'tag=%s\n' "$version"
printf 'commit_sha=%s\n' "$fetched_object"
