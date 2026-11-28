#!/usr/bin/env bash
# Publish only the generated pin on a version-specific branch from main.
set -euo pipefail

if [[ -z ${GH_TOKEN:-} ]]; then
  echo "::error::Configure the DTW_UPDATE_TOKEN Actions secret. See docs/testing.md." >&2
  exit 1
fi

tag=$1
if [[ ! $tag =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "Expected a stable Desktop World tag: $tag" >&2
  exit 1
fi
pin=control/plugin/builtin/desktop-world.json
if ! jq -e --arg tag "$tag" '.tag == $tag' "$pin" >/dev/null || git diff --quiet HEAD -- "$pin"; then
  echo "Expected a generated pin change for $tag." >&2
  exit 1
fi
branch="automation/desktop-world-$tag"
ref="refs/heads/$branch"
existing=$(gh api --method GET "repos/$GITHUB_REPOSITORY/pulls" \
  -f state=all -f "head=$GITHUB_REPOSITORY_OWNER:$branch" -f base=main \
  --jq '.[0].html_url // empty')
if [[ -n $existing ]]; then
  echo "Existing Desktop World update PR: $existing"
  exit 0
fi

git_authenticated() {
  git -c credential.helper= -c 'credential.helper=!gh auth git-credential' "$@"
}

remote_main=$(git_authenticated ls-remote --exit-code origin refs/heads/main | cut -f1)
if [[ $remote_main != "$(git rev-parse HEAD)" ]]; then
  echo "main advanced during generation. Run the update again against current main." >&2
  exit 1
fi

title="fix: bump built-in Desktop World to $tag"
if git_authenticated ls-remote --exit-code origin "$ref" >/dev/null; then
  # A previous run may have pushed before it could open the PR.
  git_authenticated fetch --no-tags origin "$ref"
  if [[ $(git rev-list --count HEAD..FETCH_HEAD) != 1 ||
        $(git diff-tree --no-commit-id --name-only -r FETCH_HEAD) != "$pin" ]] ||
       ! git merge-base --is-ancestor FETCH_HEAD^ HEAD ||
       ! git diff --quiet FETCH_HEAD -- "$pin"; then
    echo "Existing branch $branch does not contain just the generated pin update." >&2
    exit 1
  fi
else
  status=$?
  if [[ $status != 2 ]]; then
    exit "$status"
  fi
  git -c user.name='caelis-dtw-update' -c user.email='caelis-dtw-update@users.noreply.github.com' \
    commit --only -m "$title" -- "$pin"
  git_authenticated push origin "HEAD:$ref"
fi

gh pr create --repo "$GITHUB_REPOSITORY" --base main --head "$branch" --title "$title" \
  --body "Updates the built-in plugin pin from the [official Desktop World $tag release](https://github.com/caelis-labs/desktop-world/releases/tag/$tag). The pin generator verified the published archives, BUILD provenance, binaries, Skills and MCP launch contract for all Core-supported targets."
