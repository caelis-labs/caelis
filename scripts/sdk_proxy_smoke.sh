#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "${ROOT}"

MODULE="github.com/caelis-labs/caelis"
default_proxy_version() {
  local exact tag
  exact="$(git describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null || true)"
  while IFS= read -r tag; do
    if [[ -n "${tag}" && "${tag}" != "${exact}" ]]; then
      printf '%s\n' "${tag}"
      return 0
    fi
  done < <(git tag --merged HEAD --sort=-v:refname --list 'v[0-9]*')
  echo "sdk-proxy-smoke: no previous semantic release tag is reachable from HEAD" >&2
  return 1
}

VERSION="${SDK_PROXY_VERSION:-$(default_proxy_version)}"
PROXY="${SDK_PROXY_URL:-https://proxy.golang.org}"
if [[ -z "${VERSION}" || "${VERSION}" != v* ]]; then
  echo "sdk-proxy-smoke: SDK_PROXY_VERSION must be a semantic release tag" >&2
  exit 1
fi
if [[ -z "${PROXY}" || "${PROXY}" == "off" || "${PROXY}" == "direct" || "${PROXY}" == *","* || "${PROXY}" == *"|"* ]]; then
  echo "sdk-proxy-smoke: SDK_PROXY_URL must name exactly one module proxy with no direct/off/pipe fallback" >&2
  exit 1
fi

smoke_root="$(mktemp -d "${TMPDIR:-/tmp}/caelis-sdk-proxy-consumer.XXXXXX")"
smoke_root="$(cd "${smoke_root}" && pwd -P)"
consumer_dir="${smoke_root}/consumer"
consumer_modcache="${smoke_root}/cache/gomod"
consumer_gocache="${smoke_root}/cache/gobuild"
consumer_gotmp="${smoke_root}/cache/gotmp"
mkdir -p "${consumer_dir}"
cleanup() {
	chmod -R u+w "${smoke_root}" 2>/dev/null || true
  rm -rf "${smoke_root}"
}
trap cleanup EXIT

if git cat-file -e "${VERSION}:scripts/testdata/sdk_consumer/quickstart_test.go" 2>/dev/null &&
  git cat-file -e "${VERSION}:agent-sdk/supported-packages.txt" 2>/dev/null; then
  git show "${VERSION}:scripts/testdata/sdk_consumer/quickstart_test.go" >"${consumer_dir}/quickstart_test.go"
  git show "${VERSION}:agent-sdk/supported-packages.txt" >"${consumer_dir}/supported-packages.txt"
else
  # This fallback applies only to immutable historical tags that predate
  # supported-packages.txt. Current and future releases must use the fixture
  # and supported-package manifest in the branch above; api.txt is not a
  # current-tree compatibility contract.
  git show "${VERSION}:agent-sdk/example_external_test.go" |
    sed '1s/^package .*/package consumer/' >"${consumer_dir}/quickstart_test.go"
  git show "${VERSION}:agent-sdk/api.txt" |
    awk '/^package / { print $2 }' >"${consumer_dir}/supported-packages.txt"
fi
tagged_go_version="$(git show "${VERSION}:go.mod" | awk '$1 == "go" { print $2; exit }')"
if [[ -z "${tagged_go_version}" ]]; then
  echo "sdk-proxy-smoke: ${VERSION} fixture has no Go version" >&2
  exit 1
fi
(
  cd "${consumer_dir}"
  export GOWORK=off
  export GOPROXY="${PROXY}"
	# Ambient private-module policy must not bypass the evidence proxy for the
	# target module. Explicit empty/none values override user and CI GOENV.
	export GOPRIVATE=
	export GONOPROXY=none
	export GONOSUMDB=none
	export GOVCS='*:off'
  export GOMODCACHE="${consumer_modcache}"
  export GOCACHE="${consumer_gocache}"
  export GOTMPDIR="${consumer_gotmp}"
  export GOFLAGS="${GOFLAGS:-} -buildvcs=false"
  mkdir -p "${GOMODCACHE}" "${GOCACHE}" "${GOTMPDIR}"
  go mod init example.com/caelis-sdk-proxy-consumer >/dev/null
  go mod edit -go="${tagged_go_version}"
  go mod edit -require="${MODULE}@${VERSION}"

  download="$(go mod download -json "${MODULE}@${VERSION}")"
  downloaded_dir="$(printf '%s' "${download}" | go run "${ROOT}/scripts/sdk_proxy_download_dir.go")"
  downloaded_dir="$(cd "${downloaded_dir}" && pwd -P)"
  isolated_modcache="$(cd "${GOMODCACHE}" && pwd -P)"
  case "${downloaded_dir}" in
    "${isolated_modcache}"/*) ;;
    *)
      echo "sdk-proxy-smoke: module was not downloaded into the isolated module cache" >&2
      exit 1
      ;;
  esac

  {
    printf 'package consumer\n\nimport (\n'
    while IFS= read -r package; do
      package="${package%%#*}"
      package="$(printf '%s' "${package}" | xargs)"
      if [[ -n "${package}" ]]; then
        printf '\t_ "%s"\n' "${package}"
      fi
    done <supported-packages.txt
    printf ')\n'
  } >imports_test.go

  go mod tidy
  if grep -Eq '^[[:space:]]*replace([[:space:]]|\()' go.mod; then
    echo "sdk-proxy-smoke: consumer go.mod contains a replace directive" >&2
    exit 1
  fi
  resolved="$(go list -m -f '{{.Version}}|{{if .Replace}}{{.Replace.Path}}{{end}}' "${MODULE}")"
  if [[ "${resolved}" != "${VERSION}|" ]]; then
    echo "sdk-proxy-smoke: resolved ${resolved}, want ${VERSION} with no replacement" >&2
    exit 1
  fi
  go mod tidy
  go test -mod=readonly ./...
)

echo "sdk-proxy-smoke: passed ${MODULE}@${VERSION} via ${PROXY} without replace"
