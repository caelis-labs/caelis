#!/usr/bin/env bash
# Attach only missing assets to the release-please release. Existing bytes are
# immutable: a full rerun that re-signs Darwin differently must fail closed.
set -euo pipefail

tag=${1:?release tag required}
dist=${2:?archive directory required}
repo=${GITHUB_REPOSITORY:?GitHub repository required}
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo 'invalid release tag' >&2; exit 1; }
dist=$(cd "$dist" && pwd)
bash "$(dirname "$0")/verify_release_archives.sh" "${tag#v}" "$dist" >/dev/null

files=("$dist"/caelis_*.tar.gz "$dist/checksums.txt")
declare -A remote_digest remote_state
# A failed metadata read must never look like an empty release.
read_assets() {
  local assets
  assets=$(gh api "repos/$repo/releases/tags/$tag" --jq '.assets[] | [.name, .digest, .state] | @tsv')
  remote_digest=()
  remote_state=()
  while IFS=$'\t' read -r name digest state; do
    [[ -n "$name" ]] || continue
    remote_digest["$name"]=$digest
    remote_state["$name"]=$state
  done <<< "$assets"
}

declare -A expected_digest
missing=()
read_assets
for file in "${files[@]}"; do
  name=${file##*/}
  expected_digest["$name"]="sha256:$(shasum -a 256 "$file" | awk '{print $1}')"
  if [[ -n "${remote_digest[$name]+x}" ]]; then
    if [[ "${remote_state[$name]}" != uploaded || "${remote_digest[$name]}" != "${expected_digest[$name]}" ]]; then
      echo "GitHub release asset $name differs from the verified local bytes or is not uploaded" >&2
      exit 1
    fi
    echo "Verified existing GitHub asset $name"
  else
    missing+=("$file")
  fi
done

for file in "${missing[@]}"; do
  gh release upload "$tag" "$file" --repo "$repo"
done

read_assets
for file in "${files[@]}"; do
  name=${file##*/}
  if [[ "${remote_state[$name]:-}" != uploaded || "${remote_digest[$name]:-}" != "${expected_digest[$name]}" ]]; then
    echo "GitHub release asset $name did not retain the verified SHA256" >&2
    exit 1
  fi
done
echo 'GitHub release assets match all seven verified local files.'
