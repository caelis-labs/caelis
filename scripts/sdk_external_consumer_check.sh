#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "${ROOT}"

SDK_SRC="${ROOT}/agent-sdk"
if [[ ! -f "${SDK_SRC}/go.mod" ]]; then
  echo "sdk-external-consumer-check: missing ${SDK_SRC}/go.mod" >&2
  exit 1
fi

CACHE_ROOT="${CACHE_ROOT:-${ROOT}/.tmp/cache}"
export GOPATH="${CACHE_ROOT}/gopath"
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

CAELIS_MODULE="github.com/caelis-labs/caelis"
SDK_MODULE="github.com/caelis-labs/caelis/agent-sdk"
CONSUMER_MODULE="example.com/caelis-sdk-consumer"

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
    echo "sdk-external-consumer-check: agent-sdk tree must not contain nested go.mod, go.work, or go.work.sum (${phase})" >&2
    printf '  %s\n' "${violations[@]}" >&2
    exit 1
  fi
}

reject_sdk_module_workspace_hygiene "${SDK_SRC}" "source"

TMP_PARENT="${TMPDIR:-/tmp}"
WORKDIR="$(mktemp -d "${TMP_PARENT}/caelis-sdk-external-consumer.XXXXXX")"
WORKDIR="$(cd "${WORKDIR}" && pwd)"
cleanup() {
  rm -rf "${WORKDIR}"
}
trap cleanup EXIT

case "${WORKDIR}" in
"${ROOT}"/*)
  echo "sdk-external-consumer-check: temp dir must be outside repository root" >&2
  exit 1
  ;;
esac

EXTERNAL_SDK_DIR="${WORKDIR}/agent-sdk"
CONSUMER_DIR="${WORKDIR}/caelis-sdk-consumer"

mkdir -p "${EXTERNAL_SDK_DIR}" "${CONSUMER_DIR}"
cp -R "${SDK_SRC}/." "${EXTERNAL_SDK_DIR}/"

reject_sdk_module_workspace_hygiene "${EXTERNAL_SDK_DIR}" "external SDK copy"

EXTERNAL_SDK_DIR="$(cd "${EXTERNAL_SDK_DIR}" && pwd)"
CONSUMER_DIR="$(cd "${CONSUMER_DIR}" && pwd)"

case "${EXTERNAL_SDK_DIR}" in
"${ROOT}"/*)
  echo "sdk-external-consumer-check: external SDK copy must be outside repository root" >&2
  exit 1
  ;;
esac

is_public_sdk_package() {
  local pkg="$1"
  if [[ "${pkg}" != "${SDK_MODULE}" && "${pkg}" != "${SDK_MODULE}/"* ]]; then
    return 1
  fi
  local rest="${pkg#${SDK_MODULE}}"
  if [[ -z "${rest}" ]]; then
    return 0
  fi
  rest="${rest#/}"
  local segment
  IFS='/' read -ra segments <<< "${rest}"
  for segment in "${segments[@]}"; do
    if [[ "${segment}" == "internal" ]]; then
      return 1
    fi
  done
  return 0
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

check_sdk_module_resolution() {
  local replace_path replace_dir module_dir
  local list_output

  if ! list_output="$(go list -m -f '{{if .Replace}}{{.Replace.Path}}{{end}}' "${SDK_MODULE}" 2>&1)"; then
    echo "sdk-external-consumer-check: SDK module resolution check failed (replace Path)" >&2
    printf '%s\n' "${list_output}" >&2
    return 1
  fi
  replace_path="${list_output}"

  if ! list_output="$(go list -m -f '{{if .Replace}}{{.Replace.Dir}}{{end}}' "${SDK_MODULE}" 2>&1)"; then
    echo "sdk-external-consumer-check: SDK module resolution check failed (replace Dir)" >&2
    printf '%s\n' "${list_output}" >&2
    return 1
  fi
  replace_dir="${list_output}"

  if ! list_output="$(go list -m -f '{{.Dir}}' "${SDK_MODULE}" 2>&1)"; then
    echo "sdk-external-consumer-check: SDK module resolution check failed (module Dir)" >&2
    printf '%s\n' "${list_output}" >&2
    return 1
  fi
  module_dir="${list_output}"

  if [[ -z "${replace_dir}" && -z "${replace_path}" ]]; then
    echo "sdk-external-consumer-check: ${SDK_MODULE} has no replace directive in consumer module graph" >&2
    return 1
  fi

  if [[ -n "${replace_dir}" ]]; then
    replace_dir="$(normalize_abs_dir "${replace_dir}")"
    if [[ "${replace_dir}" != "${EXTERNAL_SDK_DIR}" ]]; then
      echo "sdk-external-consumer-check: ${SDK_MODULE} replace Dir resolves to ${replace_dir}, expected ${EXTERNAL_SDK_DIR}" >&2
      return 1
    fi
  elif [[ -n "${replace_path}" ]]; then
    if [[ "${replace_path}" != "${EXTERNAL_SDK_DIR}" ]]; then
      replace_path="$(normalize_abs_dir "${replace_path}")"
      if [[ "${replace_path}" != "${EXTERNAL_SDK_DIR}" ]]; then
        echo "sdk-external-consumer-check: ${SDK_MODULE} replace Path resolves to ${replace_path}, expected ${EXTERNAL_SDK_DIR}" >&2
        return 1
      fi
    fi
  fi

  module_dir="$(normalize_abs_dir "${module_dir}")"
  if [[ "${module_dir}" != "${EXTERNAL_SDK_DIR}" ]]; then
    echo "sdk-external-consumer-check: ${SDK_MODULE} module Dir resolves to ${module_dir}, expected ${EXTERNAL_SDK_DIR}" >&2
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
    echo "sdk-external-consumer-check: SDK package resolution check failed (${graph_label})" >&2
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
    echo "sdk-external-consumer-check: SDK packages must resolve under external SDK copy ${EXTERNAL_SDK_DIR} (${graph_label})" >&2
    local violation
    for violation in "${violations[@]}"; do
      echo "  ${violation} (outside external SDK copy)" >&2
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

enumerate_public_packages() {
  local pkg
  local packages=()
  local list_output
  if ! list_output="$(cd "${EXTERNAL_SDK_DIR}" && go list "${SDK_MODULE}/..." 2>&1)"; then
    echo "sdk-external-consumer-check: public package enumeration failed in external SDK copy ${EXTERNAL_SDK_DIR}" >&2
    printf '%s\n' "${list_output}" >&2
    return 1
  fi
  while IFS= read -r pkg; do
    [[ -z "${pkg}" ]] && continue
    if is_public_sdk_package "${pkg}"; then
      packages+=("${pkg}")
    fi
  done <<< "${list_output}"

  packages+=("${SDK_MODULE}")

  if ((${#packages[@]} == 0)); then
    echo "sdk-external-consumer-check: public package enumeration found no importable SDK packages" >&2
    return 1
  fi

  local sorted=()
  while IFS= read -r pkg; do
    sorted+=("${pkg}")
  done < <(printf '%s\n' "${packages[@]}" | LC_ALL=C sort -u)

  printf '%s\n' "${sorted[@]}"
}

write_consumer_import_test() {
  local test_file="$1"
  shift
  local packages=("$@")

  {
    printf '%s\n' "// Code generated by scripts/sdk_external_consumer_check.sh; DO NOT EDIT."
    printf '%s\n' "package consumer_test"
    printf '%s\n'
    printf '%s\n' "import ("
    printf '\t"testing"\n'
    printf '%s\n'
    local pkg
    for pkg in "${packages[@]}"; do
      printf '\t_ "%s"\n' "${pkg}"
    done
    printf '%s\n' ")"
    printf '%s\n'
    printf '%s\n' "func TestSDKPublicPackagesCompile(t *testing.T) {}"
  } >"${test_file}"
}

check_consumer_dependency_graph_for_graph() {
  local graph_label="$1"
  local with_test="$2"
  local dep normalized
  local forbidden=()
  local deps_output
  local list_args=(-deps -f '{{.ImportPath}}' ./...)

  if [[ "${with_test}" == "1" ]]; then
    list_args=(-deps -test -f '{{.ImportPath}}' ./...)
  fi

  if ! deps_output="$(cd "${CONSUMER_DIR}" && go list "${list_args[@]}" 2>&1)"; then
    echo "sdk-external-consumer-check: consumer dependency graph check failed (${graph_label})" >&2
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
    echo "sdk-external-consumer-check: consumer module must not depend on non-SDK Caelis packages (${graph_label})" >&2
    local leak
    for leak in "${forbidden[@]}"; do
      echo "  non-SDK Caelis dependency leak: ${leak}" >&2
    done
    return 1
  fi
}

check_consumer_dependency_graph() {
  check_consumer_dependency_graph_for_graph "production" 0
}

check_consumer_test_dependency_graph() {
  check_consumer_dependency_graph_for_graph "test" 1
}

GO_TEST_TIMEOUT="${GO_TEST_TIMEOUT:-5m}"
echo "sdk-external-consumer-check: external SDK copy at ${EXTERNAL_SDK_DIR}"
echo "sdk-external-consumer-check: consumer module at ${CONSUMER_DIR}"

packages_output="$(enumerate_public_packages)"
public_packages=()
while IFS= read -r pkg; do
  [[ -z "${pkg}" ]] && continue
  public_packages+=("${pkg}")
done <<< "${packages_output}"

(
  cd "${CONSUMER_DIR}"
  go mod init "${CONSUMER_MODULE}" >/dev/null
  go mod edit -require="${SDK_MODULE}@v0.0.0"
  go mod edit -replace="${SDK_MODULE}=${EXTERNAL_SDK_DIR}"
)

if grep -Fq '=> ./agent-sdk' "${CONSUMER_DIR}/go.mod"; then
  echo "sdk-external-consumer-check: consumer go.mod must not replace SDK with ./agent-sdk" >&2
  exit 1
fi
if ! grep -Fq "=> ${EXTERNAL_SDK_DIR}" "${CONSUMER_DIR}/go.mod"; then
  echo "sdk-external-consumer-check: consumer go.mod must replace SDK with external copy ${EXTERNAL_SDK_DIR}" >&2
  exit 1
fi

TEST_FILE="${CONSUMER_DIR}/sdk_public_imports_test.go"
write_consumer_import_test "${TEST_FILE}" "${public_packages[@]}"

if ! (
  cd "${CONSUMER_DIR}"
  go mod tidy 2>&1
); then
  echo "sdk-external-consumer-check: consumer go mod tidy failed while resolving SDK public package imports" >&2
  exit 1
fi

(
  cd "${CONSUMER_DIR}"
  check_sdk_module_resolution
  check_sdk_package_resolution
  check_sdk_test_package_resolution
)

check_consumer_dependency_graph
check_consumer_test_dependency_graph

echo "sdk-external-consumer-check: compiling consumer blank imports for ${#public_packages[@]} public SDK packages"
if ! (
  cd "${CONSUMER_DIR}"
  go test -timeout "${GO_TEST_TIMEOUT}" -count=1 ./...
); then
  echo "sdk-external-consumer-check: consumer import compile failed for one or more public SDK packages" >&2
  exit 1
fi

echo "sdk-external-consumer-check: passed"
