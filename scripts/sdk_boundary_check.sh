#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "${ROOT}"

ROOT_MODULE="github.com/caelis-labs/caelis"
SDK_PREFIX="${ROOT_MODULE}/agent-sdk"
CACHE_ROOT="${CACHE_ROOT:-${ROOT}/.tmp/cache}"
export GOPATH="${SDK_BOUNDARY_GOPATH:-${CACHE_ROOT}/gopath}"
export GOMODCACHE="${GOMODCACHE:-${CACHE_ROOT}/gomod}"
export GOCACHE="${GOCACHE:-${CACHE_ROOT}/gocache}"
export GOTMPDIR="${GOTMPDIR:-${CACHE_ROOT}/gotmp}"
export GOWORK=off

if [[ -n "${GOFLAGS:-}" ]]; then
  export GOFLAGS="${GOFLAGS} -buildvcs=false"
else
  export GOFLAGS="-buildvcs=false"
fi
mkdir -p "${GOPATH}" "${GOMODCACHE}" "${GOCACHE}" "${GOTMPDIR}"

metadata_files=()
while IFS= read -r -d '' metadata; do
  metadata_files+=("${metadata}")
done < <(find agent-sdk -type f \( -name go.mod -o -name go.sum -o -name go.work -o -name go.work.sum \) -print0)

if ((${#metadata_files[@]} > 0)); then
  echo "sdk-boundary-check: agent-sdk must be a package tree in the root module; found nested module metadata" >&2
  printf '  %s\n' "${metadata_files[@]}" >&2
  exit 1
fi

module_path="$(go list -m -f '{{.Path}}')"
if [[ "${module_path}" != "${ROOT_MODULE}" ]]; then
  echo "sdk-boundary-check: root module is ${module_path}, expected ${ROOT_MODULE}" >&2
  exit 1
fi

package_count="$(go list -f '{{.ImportPath}}' ./agent-sdk/... | awk 'NF { count++ } END { print count + 0 }')"
if [[ "${package_count}" -eq 0 ]]; then
  echo "sdk-boundary-check: no agent-sdk packages found" >&2
  exit 1
fi

check_sdk_closure() {
  local label="$1"
  shift

  local dependencies
  dependencies="$(
    go list -deps -f \
      '{{if .Module}}{{if eq .Module.Path "github.com/caelis-labs/caelis"}}{{.ImportPath}}{{end}}{{end}}' \
      "$@" | sort -u
  )"

  local violations=()
  local dependency
  while IFS= read -r dependency; do
    dependency="${dependency%% \[*}"
    if [[ -z "${dependency}" ]]; then
      continue
    fi
    case "${dependency}" in
      "${SDK_PREFIX}"|"${SDK_PREFIX}"/*|"${SDK_PREFIX}.test"|"${SDK_PREFIX}_test"|"${SDK_PREFIX}"/*_test)
        ;;
      *)
        violations+=("${dependency}")
        ;;
    esac
  done <<<"${dependencies}"

  if ((${#violations[@]} > 0)); then
    echo "sdk-boundary-check: ${label} depends on non-SDK packages from the Caelis root module" >&2
    printf '  %s\n' "${violations[@]}" >&2
    exit 1
  fi
}

check_sdk_closure "agent-sdk production code" ./agent-sdk/...
check_sdk_closure "agent-sdk tests" -test ./agent-sdk/...

allowlist="agent-sdk/supported-packages.txt"
if [[ ! -f "${allowlist}" ]]; then
  echo "sdk-boundary-check: supported package allowlist is missing: ${allowlist}" >&2
  exit 1
fi

supported_packages=()
has_seen_package() {
  local needle="$1"
  local p
  for p in "${supported_packages[@]+${supported_packages[@]}}"; do
    if [[ "${p}" == "${needle}" ]]; then
      return 0
    fi
  done
  return 1
}
while IFS= read -r package; do
  package="${package%%#*}"
  package="$(printf '%s' "${package}" | xargs)"
  if [[ -z "${package}" ]]; then
    continue
  fi
  case "${package}" in
    "${SDK_PREFIX}"|"${SDK_PREFIX}"/*)
      ;;
    *)
      echo "sdk-boundary-check: unsupported allowlist entry ${package}" >&2
      exit 1
      ;;
  esac
  if has_seen_package "${package}"; then
    echo "sdk-boundary-check: duplicate allowlist entry ${package}" >&2
    exit 1
  fi
  resolved="$(go list -f '{{.ImportPath}}' "${package}" 2>/dev/null || true)"
  if [[ "${resolved}" != "${package}" ]]; then
    echo "sdk-boundary-check: allowlisted package does not exist: ${package}" >&2
    exit 1
  fi
  supported_packages+=("${package}")
done <"${allowlist}"

if ((${#supported_packages[@]} == 0)); then
  echo "sdk-boundary-check: no supported agent-sdk packages found" >&2
  exit 1
fi

sorted_supported="$(printf '%s\n' "${supported_packages[@]}" | LC_ALL=C sort)"
listed_supported="$(printf '%s\n' "${supported_packages[@]}")"
if [[ "${sorted_supported}" != "${listed_supported}" ]]; then
  echo "sdk-boundary-check: supported package allowlist must be sorted" >&2
  exit 1
fi

consumer_dir="$(mktemp -d "${TMPDIR:-/tmp}/caelis-sdk-consumer.XXXXXX")"
cleanup() {
  rm -rf "${consumer_dir}"
}
trap cleanup EXIT

root_go_version="$(go list -m -f '{{.GoVersion}}')"
cp "${ROOT}/go.sum" "${consumer_dir}/go.sum"
cp "${ROOT}/scripts/testdata/sdk_consumer/quickstart_test.go" "${consumer_dir}/quickstart_test.go"
(
  cd "${consumer_dir}"
  go mod init example.com/caelis-sdk-consumer >/dev/null
  go mod edit -go="${root_go_version}"
  go mod edit -require="${ROOT_MODULE}@v0.0.0"
  go mod edit -replace="${ROOT_MODULE}=${ROOT}"

  {
    printf 'package consumer\n\nimport (\n'
    for package in "${supported_packages[@]}"; do
      printf '\t_ "%s"\n' "${package}"
    done
    printf ')\n'
  } >imports_test.go

  go mod tidy
  go test ./...
  check_sdk_closure "external consumer" -test ./...
)

echo "sdk-boundary-check: passed (${package_count} packages, ${#supported_packages[@]} supported imports)"
