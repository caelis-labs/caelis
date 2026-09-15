#!/usr/bin/env bash
set -euo pipefail

expect() {
  if [[ "$2" != "$3" ]]; then
    echo "$1: expected $3, got $2" >&2
    exit 1
  fi
}

expect changes "${CHANGES_RESULT}" success
case "${FULL}" in
  true) code_result=success ;;
  false) code_result=skipped ;;
  *) echo 'missing or invalid full-check classification' >&2; exit 1 ;;
esac
expect govulncheck "${VULN_RESULT}" "${code_result}"
expect go-quality "${GO_RESULT}" "${code_result}"
echo 'All checks selected for this change passed.'
