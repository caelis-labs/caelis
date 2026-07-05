#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "${ROOT}"

SDK_SRC="${ROOT}/agent-sdk"

CACHE_ROOT="${CACHE_ROOT:-${ROOT}/.tmp/cache}"
export GOMODCACHE="${GOMODCACHE:-${CACHE_ROOT}/gomod}"
export GOCACHE="${GOCACHE:-${CACHE_ROOT}/gocache}"
export GOTMPDIR="${GOTMPDIR:-${CACHE_ROOT}/gotmp}"
export GOWORK=off
mkdir -p "${GOMODCACHE}" "${GOCACHE}" "${GOTMPDIR}"

CAELIS_MODULE="github.com/caelis-labs/caelis"
SDK_MODULE="github.com/caelis-labs/caelis/agent-sdk"

RELEASE_ARTIFACTS=(go.mod go.sum README.md LICENSE)

require_regular_file() {
  local label="$1"
  local path="$2"
  if [[ ! -e "${path}" ]]; then
    echo "sdk-standalone-check: missing ${label}: ${path}" >&2
    exit 1
  fi
  if [[ -L "${path}" ]]; then
    echo "sdk-standalone-check: ${label} must be a regular file, not a symlink: ${path}" >&2
    exit 1
  fi
  if [[ ! -f "${path}" ]]; then
    echo "sdk-standalone-check: ${label} must be a regular file: ${path}" >&2
    exit 1
  fi
}

require_release_artifacts() {
  local dir="$1"
  local phase="$2"
  local artifact
  for artifact in "${RELEASE_ARTIFACTS[@]}"; do
    require_regular_file "${phase} ${artifact}" "${dir}/${artifact}"
  done
}

reject_symlinks_in_tree() {
  local root="$1"
  local symlinks=()
  while IFS= read -r -d '' link; do
    symlinks+=("${link}")
  done < <(find "${root}" -type l -print0)

  if ((${#symlinks[@]} > 0)); then
    echo "sdk-standalone-check: agent-sdk tree must not contain symlinks" >&2
    printf '  %s\n' "${symlinks[@]}" >&2
    exit 1
  fi
}

reject_sdk_module_workspace_hygiene() {
  local root="$1"
  local phase="$2"
  local violations=()
  local path

  while IFS= read -r -d '' path; do
    if [[ "${path}" != "${root}/go.mod" ]]; then
      violations+=("${path}")
    fi
  done < <(find "${root}" -name go.mod -print0)

  while IFS= read -r -d '' path; do
    violations+=("${path}")
  done < <(find "${root}" \( -name go.work -o -name go.work.sum \) -print0)

  if ((${#violations[@]} > 0)); then
    echo "sdk-standalone-check: agent-sdk tree must not contain nested go.mod, go.work, or go.work.sum (${phase})" >&2
    printf '  %s\n' "${violations[@]}" >&2
    exit 1
  fi
}

require_release_artifacts "${SDK_SRC}" "source"
reject_symlinks_in_tree "${SDK_SRC}"
reject_sdk_module_workspace_hygiene "${SDK_SRC}" "source"

TMP_PARENT="${TMPDIR:-/tmp}"
STANDALONE_DIR="$(mktemp -d "${TMP_PARENT}/caelis-agent-sdk-standalone.XXXXXX")"
STANDALONE_DIR="$(cd "${STANDALONE_DIR}" && pwd)"
cleanup() {
  rm -rf "${STANDALONE_DIR}"
}
trap cleanup EXIT

case "${STANDALONE_DIR}" in
"${ROOT}"/*)
  echo "sdk-standalone-check: temp dir must be outside repository root" >&2
  exit 1
  ;;
esac

cp -R "${SDK_SRC}/." "${STANDALONE_DIR}/"

reject_symlinks_in_tree "${STANDALONE_DIR}"
reject_sdk_module_workspace_hygiene "${STANDALONE_DIR}" "standalone copy"
require_release_artifacts "${STANDALONE_DIR}" "standalone copy"

if grep -Eq '^[[:space:]]*replace[[:space:]]' "${STANDALONE_DIR}/go.mod"; then
  echo "sdk-standalone-check: agent-sdk/go.mod must not contain replace directives" >&2
  exit 1
fi

normalize_abs_dir() {
  (cd "${1}" && pwd)
}

path_is_under() {
  local child="$1"
  local parent="$2"
  case "${child}" in
  "${parent}"|"${parent}"/*)
    return 0
    ;;
  *)
    return 1
    ;;
  esac
}

normalize_import_path() {
  local import_path="$1"
  local normalized="${import_path%% \[*}"
  normalized="${normalized%% }"
  printf '%s' "${normalized}"
}

is_sdk_import_path() {
  local import_path="$1"
  local normalized
  normalized="$(normalize_import_path "${import_path}")"
  [[ "${normalized}" == "${SDK_MODULE}" || "${normalized}" == "${SDK_MODULE}/"* || "${normalized}" == "${SDK_MODULE}.test" ]]
}

is_allowed_caelis_dep() {
  local dep="$1"
  is_sdk_import_path "${dep}"
}

check_sdk_module_resolution() {
  local module_path module_dir
  local list_output

  if ! list_output="$(go list -m -f '{{.Path}}' 2>&1)"; then
    echo "sdk-standalone-check: SDK module resolution check failed (module Path)" >&2
    printf '%s\n' "${list_output}" >&2
    return 1
  fi
  module_path="${list_output}"

  if ! list_output="$(go list -m -f '{{.Dir}}' 2>&1)"; then
    echo "sdk-standalone-check: SDK module resolution check failed (module Dir)" >&2
    printf '%s\n' "${list_output}" >&2
    return 1
  fi
  module_dir="${list_output}"

  if [[ "${module_path}" != "${SDK_MODULE}" ]]; then
    echo "sdk-standalone-check: current module Path is ${module_path}, expected ${SDK_MODULE}" >&2
    return 1
  fi

  module_dir="$(normalize_abs_dir "${module_dir}")"
  if [[ "${module_dir}" != "${STANDALONE_DIR}" ]]; then
    echo "sdk-standalone-check: ${SDK_MODULE} module Dir resolves to ${module_dir}, expected ${STANDALONE_DIR}" >&2
    return 1
  fi
}

check_sdk_package_resolution_for_graph() {
  local graph_label="$1"
  local with_test="$2"
  local import_path pkg_dir
  local violations=()
  local deps_output
  local list_args=(-deps -f '{{.ImportPath}}{{"\t"}}{{.Dir}}' ./...)

  if [[ "${with_test}" == "1" ]]; then
    list_args=(-deps -test -f '{{.ImportPath}}{{"\t"}}{{.Dir}}' ./...)
  fi

  if ! deps_output="$(go list "${list_args[@]}" 2>&1)"; then
    echo "sdk-standalone-check: SDK package resolution check failed (${graph_label})" >&2
    printf '%s\n' "${deps_output}" >&2
    return 1
  fi

  while IFS=$'\t' read -r import_path pkg_dir; do
    [[ -z "${import_path}" ]] && continue
    if ! is_sdk_import_path "${import_path}"; then
      continue
    fi
    pkg_dir="$(normalize_abs_dir "${pkg_dir}")"
    if path_is_under "${pkg_dir}" "${STANDALONE_DIR}"; then
      continue
    fi
    violations+=("${import_path} -> ${pkg_dir}")
  done <<< "${deps_output}"

  if ((${#violations[@]} > 0)); then
    echo "sdk-standalone-check: SDK packages must resolve under standalone copy ${STANDALONE_DIR} (${graph_label})" >&2
    local violation
    for violation in "${violations[@]}"; do
      echo "  ${violation} (outside standalone copy)" >&2
    done
    return 1
  fi
}

check_sdk_package_resolution() {
  check_sdk_package_resolution_for_graph "production" 0
}

check_sdk_test_package_resolution() {
  check_sdk_package_resolution_for_graph "test" 1
}

check_module_graph() {
  local mod
  local forbidden=()
  local list_output

  if ! list_output="$(go list -m -f '{{.Path}}' all 2>&1)"; then
    echo "sdk-standalone-check: module graph check failed" >&2
    printf '%s\n' "${list_output}" >&2
    return 1
  fi

  while IFS= read -r mod; do
    if [[ "${mod}" == "${CAELIS_MODULE}" ]]; then
      forbidden+=("${mod}")
    fi
  done <<< "${list_output}"

  if ((${#forbidden[@]} > 0)); then
    echo "sdk-standalone-check: module graph must not include Caelis root module ${CAELIS_MODULE}" >&2
    printf '  %s\n' "${forbidden[@]}" >&2
    return 1
  fi
}

check_package_deps_for_graph() {
  local graph_label="$1"
  local with_test="$2"
  local dep normalized
  local forbidden=()
  local deps_output
  local list_args=(-deps -f '{{.ImportPath}}' ./...)

  if [[ "${with_test}" == "1" ]]; then
    list_args=(-deps -test -f '{{.ImportPath}}' ./...)
  fi

  if ! deps_output="$(go list "${list_args[@]}" 2>&1)"; then
    echo "sdk-standalone-check: package deps check failed (${graph_label})" >&2
    printf '%s\n' "${deps_output}" >&2
    return 1
  fi

  while IFS= read -r dep; do
    [[ -z "${dep}" ]] && continue
    normalized="$(normalize_import_path "${dep}")"
    if [[ "${normalized}" == "${CAELIS_MODULE}" || "${normalized}" == "${CAELIS_MODULE}/"* ]]; then
      if ! is_allowed_caelis_dep "${dep}"; then
        forbidden+=("${dep}")
      fi
    fi
  done <<< "${deps_output}"

  if ((${#forbidden[@]} > 0)); then
    echo "sdk-standalone-check: package deps must not include non-SDK Caelis packages (${graph_label})" >&2
    printf '  %s\n' "${forbidden[@]}" >&2
    return 1
  fi
}

check_package_deps() {
  check_package_deps_for_graph "production" 0
}

check_test_package_deps() {
  check_package_deps_for_graph "test" 1
}

GO_TEST_TIMEOUT="${GO_TEST_TIMEOUT:-5m}"
SDK_STANDALONE_RUN_TESTS="${SDK_STANDALONE_RUN_TESTS:-0}"
SDK_STANDALONE_COMPILE_TESTS="${SDK_STANDALONE_COMPILE_TESTS:-0}"
echo "sdk-standalone-check: checking copied module at ${STANDALONE_DIR}"
(
  cd "${STANDALONE_DIR}"
  check_sdk_module_resolution
  check_module_graph
  check_sdk_package_resolution
  check_sdk_test_package_resolution
  check_package_deps
  check_test_package_deps
  if [[ "${SDK_STANDALONE_RUN_TESTS}" == "1" ]]; then
    go test -timeout "${GO_TEST_TIMEOUT}" -count=1 ./...
  elif [[ "${SDK_STANDALONE_COMPILE_TESTS}" == "1" ]]; then
    go test -timeout "${GO_TEST_TIMEOUT}" -run '^$' ./...
  else
    go build ./...
  fi
)

echo "sdk-standalone-check: passed"
