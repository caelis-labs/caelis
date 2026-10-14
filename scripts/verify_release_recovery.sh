#!/usr/bin/env bash
# This one-off recovery uses the unpublished build from the failed v0.71.1 run.
# Do not broaden these constants into a general manual release entry point.
set -euo pipefail

tag=v0.71.1
tag_sha=5e232e817cdd62186a19dd1d3a43aa5476326a74
source_run=38420720149
archive_digest=sha256:82c56458a1d95e591b288d69b1860f2cc777031d873d217e6b5fe7e9fe3dab6e
repo=caelis-labs/caelis

[[ "${GITHUB_EVENT_NAME:-}" == workflow_dispatch && "${GITHUB_REF:-}" == refs/heads/main &&
   "${GITHUB_REPOSITORY:-}" == "$repo" && "${RELEASE_TAG:-}" == "$tag" &&
   "${GITHUB_RUN_ATTEMPT:-}" == 1 && "${GITHUB_RUN_ID:-}" =~ ^[0-9]+$ ]] || {
  echo 'recovery requires its first workflow_dispatch attempt on Caelis main' >&2
  exit 1
}
: "${GH_TOKEN:?GitHub read token required}"
cd "$(dirname "$0")/.."

[[ "$(git rev-parse "refs/tags/$tag^{commit}")" == "$tag_sha" ]] || {
  echo 'v0.71.1 tag commit changed; refusing recovery' >&2
  exit 1
}
git merge-base --is-ancestor "$tag_sha" HEAD || {
  echo 'original tag is not an ancestor of this reviewed main' >&2
  exit 1
}
git diff --quiet "$tag_sha" HEAD -- npm .goreleaser.yaml \
  scripts/verify_release_archives.sh scripts/publish_release_assets.sh || {
  echo 'package or asset publication inputs differ from the original tag' >&2
  exit 1
}
printf '%s  %s\n' \
  '76660222f7ecd3f9f0c0ce5e47658ed611a9d68d2d7b7282e3588a85a5845222' scripts/sign_notarize_cli.sh \
  '69121275b1ddc5cce677adc9a2b9193c4c214abbde60da32c0d03e35d51b372f' scripts/publish_latest_to_r2.sh |
  shasum -a 256 --check >/dev/null || {
  echo 'reviewed signing or R2 script changed; refusing recovery' >&2
  exit 1
}

run=$(gh api "/repos/$repo/actions/runs/$source_run")
jq -e --arg sha "$tag_sha" '
  .head_sha == $sha and .head_branch == "v0.71.1" and .event == "push" and
  .status == "completed" and .conclusion == "failure" and
  .path == ".github/workflows/release.yml"
' >/dev/null <<< "$run" || {
  echo 'source release run does not match the failed v0.71.1 tag run' >&2
  exit 1
}

jobs=$(gh api "/repos/$repo/actions/runs/$source_run/jobs?per_page=100")
jq -e '
  any(.jobs[]; .name == "build" and .conclusion == "success") and
  any(.jobs[]; .name == "notarize-darwin" and .conclusion == "failure") and
  any(.jobs[]; .name == "release" and .conclusion == "skipped") and
  any(.jobs[]; .name == "publish-r2" and .conclusion == "skipped")
' >/dev/null <<< "$jobs" || {
  echo 'source job results do not match the reviewed failure boundary' >&2
  exit 1
}

artifacts=$(gh api "/repos/$repo/actions/runs/$source_run/artifacts?per_page=100")
jq -e --arg digest "$archive_digest" '
  .total_count == 1 and (.artifacts | length) == 1 and
  .artifacts[0].name == "unpublished-archives" and
  .artifacts[0].digest == $digest and .artifacts[0].size_in_bytes == 184477179 and
  .artifacts[0].expired == false
' >/dev/null <<< "$artifacts" || {
  echo 'original unpublished archive artifact is missing, expired or changed' >&2
  exit 1
}

release=$(gh api "/repos/$repo/releases/tags/$tag")
jq -e '.tag_name == "v0.71.1" and .draft == false and .prerelease == false and (.assets | length) == 0' \
  >/dev/null <<< "$release" || {
  echo 'release already has assets; inspect and resume without re-signing' >&2
  exit 1
}
latest=$(gh api "/repos/$repo/releases/latest")
jq -e '.tag_name == "v0.71.1"' >/dev/null <<< "$latest" || {
  echo 'v0.71.1 is no longer the latest release; refusing R2 recovery' >&2
  exit 1
}

# A second dispatch could re-sign and submit new bytes after an unknown Apple
# result. Publication-only retries use the successful artifact in the same run.
current_visible=false
for attempt in 1 2 3 4 5 6; do
  recoveries=$(gh api "/repos/$repo/actions/workflows/release.yml/runs?event=workflow_dispatch&per_page=100")
  if jq -e --argjson current "$GITHUB_RUN_ID" '
    .total_count == 1 and (.workflow_runs | length) == 1 and
    .workflow_runs[0].id == $current
  ' >/dev/null <<< "$recoveries"; then
    current_visible=true
    break
  fi
  if jq -e --argjson current "$GITHUB_RUN_ID" '
    .total_count > 100 or any(.workflow_runs[]; .id != $current)
  ' >/dev/null <<< "$recoveries"; then
    echo 'a recovery run already exists; inspect its exact signed ZIP and Apple receipt' >&2
    exit 1
  fi
  sleep 5
done
[[ "$current_visible" == true ]] || {
  echo 'current recovery run is not visible in the GitHub API; refusing submission' >&2
  exit 1
}

echo "Verified $tag at $tag_sha and unpublished archives from failed run $source_run."
