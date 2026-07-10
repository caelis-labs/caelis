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
      "${SDK_PREFIX}"|"${SDK_PREFIX}"/*|"${SDK_PREFIX}.test")
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

public_packages=()
while IFS= read -r package; do
  case "${package}" in
    */internal|*/internal/*)
      ;;
    *)
      public_packages+=("${package}")
      ;;
  esac
done < <(go list -f '{{.ImportPath}}' ./agent-sdk/...)

if ((${#public_packages[@]} == 0)); then
  echo "sdk-boundary-check: no public agent-sdk packages found" >&2
  exit 1
fi

consumer_dir="$(mktemp -d "${TMPDIR:-/tmp}/caelis-sdk-consumer.XXXXXX")"
cleanup() {
  rm -rf "${consumer_dir}"
}
trap cleanup EXIT

root_go_version="$(go list -m -f '{{.GoVersion}}')"
cp "${ROOT}/go.sum" "${consumer_dir}/go.sum"
(
  cd "${consumer_dir}"
  go mod init example.com/caelis-sdk-consumer >/dev/null
  go mod edit -go="${root_go_version}"
  go mod edit -require="${ROOT_MODULE}@v0.0.0"
  go mod edit -replace="${ROOT_MODULE}=${ROOT}"

  {
    printf 'package consumer\n\nimport (\n'
    for package in "${public_packages[@]}"; do
      printf '\t_ "%s"\n' "${package}"
    done
    printf ')\n'
  } >imports_test.go

  go mod tidy
  go test ./...
  check_sdk_closure "external consumer" -test ./...
)

echo "sdk-boundary-check: passed (${package_count} packages, ${#public_packages[@]} public imports)"
