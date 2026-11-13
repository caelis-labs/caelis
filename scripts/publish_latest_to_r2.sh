#!/usr/bin/env bash

set -euo pipefail

: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
: "${GH_TOKEN:?GH_TOKEN is required}"
: "${R2_BUCKET:?R2_BUCKET is required}"
: "${R2_ENDPOINT:?R2_ENDPOINT is required}"

tag="${1:-${GITHUB_REF_NAME:-}}"
if [[ ! "${tag}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[^/[:space:]]+)?$ ]]; then
  echo "refusing to publish invalid release tag: ${tag}" >&2
  exit 1
fi

github_latest="$(gh api "/repos/${GITHUB_REPOSITORY}/releases/latest" --jq .tag_name)"
if [[ "${github_latest}" != "${tag}" ]]; then
  echo "${tag} is not GitHub's latest release (${github_latest}); leaving R2 unchanged"
  exit 0
fi

version="${tag#v}"
archives=(
  "caelis_${version}_darwin_amd64.tar.gz"
  "caelis_${version}_darwin_arm64.tar.gz"
  "caelis_${version}_linux_amd64.tar.gz"
  "caelis_${version}_linux_arm64.tar.gz"
  "caelis_${version}_windows_amd64.tar.gz"
  "caelis_${version}_windows_arm64.tar.gz"
)

work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

gh release download "${tag}" \
  --repo "${GITHUB_REPOSITORY}" \
  --dir "${work_dir}" \
  --pattern 'caelis_*.tar.gz' \
  --pattern checksums.txt

for asset in "${archives[@]}" checksums.txt; do
  if [[ ! -f "${work_dir}/${asset}" ]]; then
    echo "GitHub Release ${tag} is missing ${asset}" >&2
    exit 1
  fi
done

(
  cd "${work_dir}"
  sha256sum -c checksums.txt
)
bash "$(dirname "$0")/verify_release_archives.sh" "${version}" "${work_dir}"

r2() {
  aws "$@" --endpoint-url "${R2_ENDPOINT}"
}

versioned_prefix="releases/${tag}"
# Versioned objects are immutable. Only an explicit missing-key response permits
# creation, and the conditional write also protects against concurrent writers.
upload_immutable() {
  local asset="$1" content_type="$2" key="${versioned_prefix}/$1"
  if r2 s3api head-object --bucket "${R2_BUCKET}" --key "${key}" \
      > /dev/null 2> "${work_dir}/head-error"; then
    r2 s3 cp "s3://${R2_BUCKET}/${key}" "${work_dir}/existing" --no-progress
    if ! cmp -s "${work_dir}/${asset}" "${work_dir}/existing"; then
      echo "refusing to overwrite conflicting R2 object: ${key}" >&2
      return 1
    fi
    return 0
  fi
  if ! grep -Eq '^An error occurred \((404|NoSuchKey)\) when calling the HeadObject operation:' "${work_dir}/head-error"; then
    cat "${work_dir}/head-error" >&2
    echo "R2 object absence not established: ${key}" >&2
    return 1
  fi
  r2 s3api put-object --bucket "${R2_BUCKET}" --key "${key}" \
    --body "${work_dir}/${asset}" --if-none-match '*' \
    --content-type "${content_type}" \
    --cache-control 'public, max-age=31536000, immutable'
}
for archive in "${archives[@]}"; do
  upload_immutable "${archive}" application/gzip
done
upload_immutable checksums.txt 'text/plain; charset=utf-8'

for asset in "${archives[@]}" checksums.txt; do
  local_size="$(wc -c < "${work_dir}/${asset}" | tr -d '[:space:]')"
  remote_size="$(
    r2 s3api head-object \
      --bucket "${R2_BUCKET}" \
      --key "${versioned_prefix}/${asset}" \
      --query ContentLength \
      --output text
  )"
  if [[ "${local_size}" != "${remote_size}" ]]; then
    echo "R2 size mismatch for ${asset}: local=${local_size}, remote=${remote_size}" >&2
    exit 1
  fi
done

# Read the uploaded objects back. S3 ETags and size alone are not SHA256 proofs.
mkdir -p "${work_dir}/r2-readback"
for asset in "${archives[@]}" checksums.txt; do
  r2 s3 cp "s3://${R2_BUCKET}/${versioned_prefix}/${asset}" \
    "${work_dir}/r2-readback/${asset}" --no-progress
  cmp "${work_dir}/${asset}" "${work_dir}/r2-readback/${asset}"
done

printf '%s\n' "${tag}" > "${work_dir}/latest.txt"
r2 s3 cp "${work_dir}/latest.txt" "s3://${R2_BUCKET}/latest.txt" \
  --content-type 'text/plain; charset=utf-8' \
  --cache-control 'public, max-age=60, must-revalidate'

# Retain prior releases. Removing immutable artifacts requires separate approval.
echo "Published ${tag} to R2 and updated latest.txt"
