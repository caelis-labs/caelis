#!/usr/bin/env bash
# The source release completed signing and publication; only R2 failed.
set -euo pipefail

repo=caelis-labs/caelis
tag=v0.79.1
sha=048c0d479f663caa31880fdb3c6d5e33734de8be
source_run=43425108989

[[ "${GITHUB_EVENT_NAME:-}" == push && "${GITHUB_REF:-}" == refs/heads/main &&
   "${GITHUB_REPOSITORY:-}" == "$repo" ]] || {
  echo 'R2 recovery requires reviewed Caelis main' >&2
  exit 1
}
[[ "$(git rev-parse "refs/tags/$tag^{commit}")" == "$sha" ]] || {
  echo 'release tag identity changed; refusing R2 recovery' >&2
  exit 1
}
git merge-base --is-ancestor "$sha" HEAD

gh api "repos/$repo/actions/runs/$source_run" | jq -e --arg sha "$sha" '
  .head_sha == $sha and .head_branch == "v0.79.1" and .event == "push" and
  .status == "completed" and .conclusion == "failure" and
  .path == ".github/workflows/release.yml"
' > /dev/null
gh api "repos/$repo/actions/runs/$source_run/jobs?per_page=100" | jq -e '
  any(.jobs[]; .name == "build" and .conclusion == "success") and
  any(.jobs[]; .name == "notarize-darwin" and .conclusion == "success") and
  any(.jobs[]; .name == "release" and .conclusion == "success") and
  any(.jobs[]; .name == "publish-r2" and .conclusion == "failure")
' > /dev/null

# The publisher independently requires this to remain GitHub latest, verifies
# all archive checksums, and reconciles immutable R2 bytes before updating latest.
echo "Verified R2-only recovery for $tag at $sha from run $source_run."
