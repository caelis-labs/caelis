#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "${ROOT}"

SDK_SRC="${ROOT}/agent-sdk"
if [[ ! -f "${SDK_SRC}/go.mod" ]]; then
  echo "sdk-external-replace-check: missing ${SDK_SRC}/go.mod" >&2
  exit 1
fi
if [[ ! -f "${ROOT}/go.mod" || ! -f "${ROOT}/go.sum" ]]; then
  echo "sdk-external-replace-check: missing root go.mod or go.sum" >&2
  exit 1
fi

CACHE_ROOT="${CACHE_ROOT:-${ROOT}/.tmp/cache}"
export GOMODCACHE="${GOMODCACHE:-${CACHE_ROOT}/gomod}"
export GOCACHE="${GOCACHE:-${CACHE_ROOT}/gocache}"
export GOTMPDIR="${GOTMPDIR:-${CACHE_ROOT}/gotmp}"
export GOWORK=off
mkdir -p "${GOMODCACHE}" "${GOCACHE}" "${GOTMPDIR}"

SDK_MODULE="github.com/caelis-labs/caelis/agent-sdk"

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
    echo "sdk-external-replace-check: agent-sdk tree must not contain nested go.mod, go.work, or go.work.sum (${phase})" >&2
    printf '  %s\n' "${violations[@]}" >&2
    exit 1
  fi
}

reject_sdk_module_workspace_hygiene "${SDK_SRC}" "source"

TMP_PARENT="${TMPDIR:-/tmp}"
WORKDIR="$(mktemp -d "${TMP_PARENT}/caelis-sdk-external-replace.XXXXXX")"
WORKDIR="$(cd "${WORKDIR}" && pwd)"
cleanup() {
  rm -rf "${WORKDIR}"
}
trap cleanup EXIT

case "${WORKDIR}" in
"${ROOT}"/*)
  echo "sdk-external-replace-check: temp dir must be outside repository root" >&2
  exit 1
  ;;
esac

PRODUCT_HOST_DIR="${WORKDIR}/caelis-product-host"
EXTERNAL_SDK_DIR="${WORKDIR}/agent-sdk"

mkdir -p "${PRODUCT_HOST_DIR}" "${EXTERNAL_SDK_DIR}"

shopt -s nullglob
for entry in "${ROOT}"/*; do
  name="$(basename "${entry}")"
  case "${name}" in
  agent-sdk)
    continue
    ;;
  esac
  cp -R "${entry}" "${PRODUCT_HOST_DIR}/"
done
shopt -u nullglob

cp -R "${SDK_SRC}/." "${EXTERNAL_SDK_DIR}/"

reject_sdk_module_workspace_hygiene "${EXTERNAL_SDK_DIR}" "external SDK copy"

if [[ -e "${PRODUCT_HOST_DIR}/agent-sdk" ]]; then
  echo "sdk-external-replace-check: product host copy must not contain agent-sdk/" >&2
  exit 1
fi

EXTERNAL_SDK_DIR="$(cd "${EXTERNAL_SDK_DIR}" && pwd)"
PRODUCT_HOST_DIR="$(cd "${PRODUCT_HOST_DIR}" && pwd)"

case "${EXTERNAL_SDK_DIR}" in
"${PRODUCT_HOST_DIR}"/*)
  echo "sdk-external-replace-check: external SDK copy must not live inside product host copy" >&2
  exit 1
  ;;
esac

(
  cd "${PRODUCT_HOST_DIR}"
  go mod edit -replace="${SDK_MODULE}=${EXTERNAL_SDK_DIR}"
)

if ! grep -Fq "=> ${EXTERNAL_SDK_DIR}" "${PRODUCT_HOST_DIR}/go.mod"; then
  echo "sdk-external-replace-check: failed to point ${SDK_MODULE} replace at external SDK copy" >&2
  exit 1
fi
if grep -Fq '=> ./agent-sdk' "${PRODUCT_HOST_DIR}/go.mod"; then
  echo "sdk-external-replace-check: product host go.mod must not replace SDK with ./agent-sdk" >&2
  exit 1
fi

normalize_abs_dir() {
  (cd "${1}" && pwd)
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

check_sdk_module_resolution() {
  local replace_path replace_dir module_dir
  local list_output

  if ! list_output="$(go list -m -f '{{if .Replace}}{{.Replace.Path}}{{end}}' "${SDK_MODULE}" 2>&1)"; then
    echo "sdk-external-replace-check: SDK module resolution check failed (replace Path)" >&2
    printf '%s\n' "${list_output}" >&2
    return 1
  fi
  replace_path="${list_output}"

  if ! list_output="$(go list -m -f '{{if .Replace}}{{.Replace.Dir}}{{end}}' "${SDK_MODULE}" 2>&1)"; then
    echo "sdk-external-replace-check: SDK module resolution check failed (replace Dir)" >&2
    printf '%s\n' "${list_output}" >&2
    return 1
  fi
  replace_dir="${list_output}"

  if ! list_output="$(go list -m -f '{{.Dir}}' "${SDK_MODULE}" 2>&1)"; then
    echo "sdk-external-replace-check: SDK module resolution check failed (module Dir)" >&2
    printf '%s\n' "${list_output}" >&2
    return 1
  fi
  module_dir="${list_output}"

  if [[ -z "${replace_dir}" && -z "${replace_path}" ]]; then
    echo "sdk-external-replace-check: ${SDK_MODULE} has no replace directive in Go module graph" >&2
    return 1
  fi

  if [[ -n "${replace_dir}" ]]; then
    replace_dir="$(normalize_abs_dir "${replace_dir}")"
    if [[ "${replace_dir}" != "${EXTERNAL_SDK_DIR}" ]]; then
      echo "sdk-external-replace-check: ${SDK_MODULE} replace Dir resolves to ${replace_dir}, expected ${EXTERNAL_SDK_DIR}" >&2
      return 1
    fi
  elif [[ -n "${replace_path}" ]]; then
    if [[ "${replace_path}" != "${EXTERNAL_SDK_DIR}" ]]; then
      replace_path="$(normalize_abs_dir "${replace_path}")"
      if [[ "${replace_path}" != "${EXTERNAL_SDK_DIR}" ]]; then
        echo "sdk-external-replace-check: ${SDK_MODULE} replace Path resolves to ${replace_path}, expected ${EXTERNAL_SDK_DIR}" >&2
        return 1
      fi
    fi
  fi

  module_dir="$(normalize_abs_dir "${module_dir}")"
  if [[ "${module_dir}" != "${EXTERNAL_SDK_DIR}" ]]; then
    echo "sdk-external-replace-check: ${SDK_MODULE} module Dir resolves to ${module_dir}, expected ${EXTERNAL_SDK_DIR}" >&2
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
    echo "sdk-external-replace-check: SDK package resolution check failed (${graph_label})" >&2
    printf '%s\n' "${deps_output}" >&2
    return 1
  fi

  while IFS=$'\t' read -r import_path pkg_dir; do
    [[ -z "${import_path}" ]] && continue
    if ! is_sdk_import_path "${import_path}"; then
      continue
    fi
    pkg_dir="$(normalize_abs_dir "${pkg_dir}")"
    if path_is_under "${pkg_dir}" "${EXTERNAL_SDK_DIR}"; then
      continue
    fi
    violations+=("${import_path} -> ${pkg_dir}")
  done <<< "${deps_output}"

  if ((${#violations[@]} > 0)); then
    echo "sdk-external-replace-check: SDK packages must resolve under external SDK copy ${EXTERNAL_SDK_DIR} (${graph_label})" >&2
    local violation
    for violation in "${violations[@]}"; do
      local resolved="${violation#* -> }"
      if path_is_under "${resolved}" "${PRODUCT_HOST_DIR}"; then
        echo "  ${violation} (inside product-host copy)" >&2
      else
        echo "  ${violation} (outside external SDK copy)" >&2
      fi
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

GO_TEST_TIMEOUT="${GO_TEST_TIMEOUT:-5m}"
SDK_EXTERNAL_REPLACE_RUN_TESTS="${SDK_EXTERNAL_REPLACE_RUN_TESTS:-0}"
SDK_EXTERNAL_REPLACE_COMPILE_TESTS="${SDK_EXTERNAL_REPLACE_COMPILE_TESTS:-0}"
echo "sdk-external-replace-check: building product host copy without in-tree SDK at ${PRODUCT_HOST_DIR}"
echo "sdk-external-replace-check: external SDK copy at ${EXTERNAL_SDK_DIR}"
(
  cd "${PRODUCT_HOST_DIR}"
  check_sdk_module_resolution
  check_sdk_package_resolution
  check_sdk_test_package_resolution
  go build ./...
  if [[ "${SDK_EXTERNAL_REPLACE_RUN_TESTS}" == "1" ]]; then
    go test -timeout "${GO_TEST_TIMEOUT}" ./...
  elif [[ "${SDK_EXTERNAL_REPLACE_COMPILE_TESTS}" == "1" ]]; then
    go test -timeout "${GO_TEST_TIMEOUT}" -run '^$' ./...
  fi
)

echo "sdk-external-replace-check: passed"
