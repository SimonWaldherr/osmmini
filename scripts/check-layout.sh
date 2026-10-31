#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." >/dev/null && pwd -P)"
shopt -s dotglob nullglob
entries=("$root"/*)
if (( ${#entries[@]} > 20 )); then
    printf 'Repository root has %s entries; the limit is 20:\n' "${#entries[@]}" >&2
    printf '  %s\n' "${entries[@]##*/}" >&2
    exit 1
fi
printf 'Repository root: %s/20 entries (including hidden files).\n' "${#entries[@]}"
