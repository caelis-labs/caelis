#!/usr/bin/env bash
set -euo pipefail

version=${1:?release version required}
dist=${2:?archive directory required}
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo 'invalid release version' >&2; exit 1; }
dist=$(cd "$dist" && pwd)
archives=(
  "caelis_${version}_darwin_amd64.tar.gz"
  "caelis_${version}_darwin_arm64.tar.gz"
  "caelis_${version}_linux_amd64.tar.gz"
  "caelis_${version}_linux_arm64.tar.gz"
  "caelis_${version}_windows_amd64.tar.gz"
  "caelis_${version}_windows_arm64.tar.gz"
)
for archive in "${archives[@]}"; do
  [[ -f "$dist/$archive" ]] || { echo "missing release archive: $archive" >&2; exit 1; }
done
shopt -s nullglob
found=("$dist"/caelis_*.tar.gz)
[[ "${#found[@]}" == "${#archives[@]}" ]] || { echo 'unexpected release archive count' >&2; exit 1; }
[[ "${3:-}" != --names-only ]] || exit 0
[[ -f "$dist/checksums.txt" ]] || { echo 'missing final checksums' >&2; exit 1; }
[[ "$(wc -l < "$dist/checksums.txt" | tr -d '[:space:]')" == "${#archives[@]}" ]] || {
  echo 'checksums must cover exactly six archives' >&2; exit 1;
}
for archive in "${archives[@]}"; do
  awk -v name="$archive" '$2 == name { count++ } END { exit count != 1 }' "$dist/checksums.txt" || {
    echo "checksums must name $archive exactly once" >&2; exit 1;
  }
done
(
  cd "$dist"
  shasum -a 256 -c checksums.txt
)
