#!/usr/bin/env bash
set -euo pipefail

package="${1:-}"
selector="${2:-}"
label="${3:-regression}"

if [[ -z "${package}" || -z "${selector}" ]]; then
  echo "go-test-nonempty: ${label} requires a package and non-empty selector" >&2
  exit 1
fi

matches="$(go test "${package}" -list "${selector}" | awk '/^Test/ { print }')"
if [[ -z "${matches}" ]]; then
  echo "go-test-nonempty: ${label} selector ${selector} matched no tests in ${package}" >&2
  exit 1
fi

# Anchored lists name individual contracts. A surviving name must not hide a
# renamed or deleted sibling. Other regular expressions keep normal Go matching.
literal_list='^\^\((Test[[:alnum:]_]+(\|Test[[:alnum:]_]+)*)\)\$$'
if [[ "${selector}" =~ ${literal_list} ]]; then
  IFS='|' read -r -a expected <<< "${BASH_REMATCH[1]}"
  for name in "${expected[@]}"; do
    if ! grep -Fxq "${name}" <<< "${matches}"; then
      echo "go-test-nonempty: ${label} missing ${name} in ${package}" >&2
      exit 1
    fi
  done
fi

echo "go-test-nonempty: ${label} matched:"
printf '%s\n' "${matches}"
go test -timeout "${GO_TEST_TIMEOUT:-5m}" "${package}" -run "${selector}" "${@:4}"
