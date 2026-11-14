#!/usr/bin/env bash
set -euo pipefail

packages=()
all=false
workflows=false
base=${1:-}
head=${2:-HEAD}

if [[ -z "$base" ]]; then
  all=true
  workflows=true
else
  changed=$(git diff --name-only "$base" "$head")
  while IFS= read -r file; do
    case "$file" in
      cmd/*/*)
        package=${file#cmd/}
        package=${package%%/*}
        if [[ ! "$package" =~ ^[A-Za-z0-9_-]+$ ]]; then exit 1; fi
        packages+=("./cmd/$package/...")
        if [[ ! -d "cmd/$package" ]]; then all=true; workflows=true; fi
        case "$package" in
          cortexium-runner|runner-go-check|runner-measure|runner-measure-history|runner-release)
            packages+=("./internal/runner/execution")
            workflows=true
            ;;
        esac
        ;;
      internal/runner/execution/*)
        packages+=("./internal/runner/execution")
        workflows=true
        ;;
      internal/*|go.mod|go.sum|testdata/*|examples/*|.github/*|Makefile|docs/runner-*)
        all=true
        workflows=true
        ;;
      *.md|LICENSE|.gitignore|.gitattributes|'') ;;
      *) all=true; workflows=true ;;
    esac
  done <<< "$changed"
fi

if $all; then
  printf 'packages=./...\n'
elif ((${#packages[@]})); then
  printf 'packages=%s\n' "$(printf '%s\n' "${packages[@]}" | sort -u | paste -sd ' ' -)"
else
  printf 'packages=\n'
fi
printf 'workflows=%s\n' "$workflows"
