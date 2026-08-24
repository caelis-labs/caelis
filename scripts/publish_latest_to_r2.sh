#!/usr/bin/env bash

set -euo pipefail

: "${GITHUB_REF_NAME:?GITHUB_REF_NAME is required}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
: "${GH_TOKEN:?GH_TOKEN is required}"
: "${R2_BUCKET:?R2_BUCKET is required}"
: "${R2_ENDPOINT:?R2_ENDPOINT is required}"

tag="${GITHUB_REF_NAME}"
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

r2() {
  aws "$@" --endpoint-url "${R2_ENDPOINT}"
}

versioned_prefix="releases/${tag}"
for archive in "${archives[@]}"; do
  r2 s3 cp "${work_dir}/${archive}" "s3://${R2_BUCKET}/${versioned_prefix}/${archive}" \
    --content-type application/gzip \
    --cache-control 'public, max-age=31536000, immutable'
done
r2 s3 cp "${work_dir}/checksums.txt" "s3://${R2_BUCKET}/${versioned_prefix}/checksums.txt" \
  --content-type 'text/plain; charset=utf-8' \
  --cache-control 'public, max-age=31536000, immutable'

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

printf '%s\n' "${tag}" > "${work_dir}/latest.txt"
r2 s3 cp "${work_dir}/latest.txt" "s3://${R2_BUCKET}/latest.txt" \
  --content-type 'text/plain; charset=utf-8' \
  --cache-control 'public, max-age=60, must-revalidate'

mapfile -t r2_keys < <(
  r2 s3api list-objects-v2 \
    --bucket "${R2_BUCKET}" \
    --prefix releases/ \
    --query 'Contents[].Key' \
    --output text | tr '\t' '\n'
)
for key in "${r2_keys[@]}"; do
  if [[ -z "${key}" || "${key}" == None || "${key}" == "${versioned_prefix}/"* ]]; then
    continue
  fi
  if [[ ! "${key}" =~ ^releases/v[0-9]+\.[0-9]+\.[0-9]+(-[^/[:space:]]+)?/(caelis_[^/]+\.tar\.gz|checksums\.txt)$ ]]; then
    echo "refusing to delete unexpected R2 object: ${key}" >&2
    exit 1
  fi
  r2 s3 rm "s3://${R2_BUCKET}/${key}"
done

echo "Published ${tag} to R2 and updated latest.txt"
