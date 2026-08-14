#!/usr/bin/env bash
set -euo pipefail

ROOT="${1:-}"
if [[ -z "${ROOT}" ]]; then
  ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
fi
cd "${ROOT}"

if command -v rg >/dev/null 2>&1; then
  PROD_CMD=(rg --files -g '*.go' -g '!**/*_test.go' -g '!vendor/**' -g '!.tmp/**')
  TEST_CMD=(rg --files -g '*_test.go' -g '!vendor/**' -g '!.tmp/**')
else
  PROD_CMD=(find . -type f -name '*.go' ! -name '*_test.go' ! -path './vendor/*' ! -path './.tmp/*')
  TEST_CMD=(find . -type f -name '*_test.go' ! -path './vendor/*' ! -path './.tmp/*')
fi
PROD_GO=()
while IFS= read -r file; do
  [[ -n "${file}" ]] || continue
  PROD_GO+=("${file#./}")
done < <("${PROD_CMD[@]}" | sed 's|^\./||' | LC_ALL=C sort)
TEST_GO=()
while IFS= read -r file; do
  [[ -n "${file}" ]] || continue
  TEST_GO+=("${file#./}")
done < <("${TEST_CMD[@]}" | sed 's|^\./||' | LC_ALL=C sort)

sum_lines() {
  if [[ "$#" -eq 0 ]]; then
    printf '0\n'
    return
  fi
  printf '%s\0' "$@" | xargs -0 wc -l | awk '$NF != "total" {sum += $1} END {print sum + 0}'
}

top_files() {
  if [[ "$#" -eq 0 ]]; then
    return
  fi
  printf '%s\0' "$@" | xargs -0 wc -l | awk '$NF != "total" {print $1 "\t" $2}' | sort -nr | head -20
}

top_packages() {
  if [[ "$#" -eq 0 ]]; then
    return
  fi
  printf '%s\0' "$@" |
    xargs -0 wc -l |
    awk '$NF != "total" {
      file = $2
      sub("/[^/]+$", "", file)
      lines[file] += $1
    } END {
      for (pkg in lines) print lines[pkg] "\t" pkg
    }' |
    sort -nr |
    head -20
}

file_size() {
  if stat -f '%z' "$1" >/dev/null 2>&1; then
    stat -f '%z' "$1"
  else
    stat -c '%s' "$1"
  fi
}

echo "Caelis size report"
echo
printf 'Go files: %d production, %d test\n' "${#PROD_GO[@]}" "${#TEST_GO[@]}"
printf 'Production Go lines: %s\n' "$(sum_lines "${PROD_GO[@]}")"
printf 'Test Go lines: %s\n' "$(sum_lines "${TEST_GO[@]}")"
echo

echo "Largest production Go files"
top_files "${PROD_GO[@]}"
echo

echo "Largest test Go files"
top_files "${TEST_GO[@]}"
echo

echo "Largest production packages"
top_packages "${PROD_GO[@]}"
echo

echo "Largest test packages"
top_packages "${TEST_GO[@]}"
echo

echo "Embedded resources"
if command -v rg >/dev/null 2>&1; then
  EMBED_LINES="$(rg -n '^//go:embed ' -g '*.go' -g '!vendor/**' -g '!.tmp/**' || true)"
else
  EMBED_LINES="$(grep -RIn '^//go:embed ' -- . 2>/dev/null || true)"
fi
if [[ -z "${EMBED_LINES}" ]]; then
  echo "none"
else
  while IFS= read -r line; do
    file="${line%%:*}"
    rest="${line#*:}"
    rest="${rest#*:}"
    dir="$(dirname "${file}")"
    patterns="${rest#//go:embed }"
    for pattern in ${patterns}; do
      while IFS= read -r resource; do
        [[ -e "${resource}" ]] || continue
        printf '%s\t%s\n' "$(file_size "${resource}")" "${resource}"
      done < <(find "${dir}" -path "${dir}/${pattern}" -type f 2>/dev/null || true)
    done
  done <<< "${EMBED_LINES}" | sort -nr | head -20
fi
echo

if [[ -x ./.tmp/bin/caelis ]]; then
  printf 'Local release binary: %s bytes (./.tmp/bin/caelis)\n' "$(file_size ./.tmp/bin/caelis)"
else
  echo "Local release binary: not built (run make build-cli)"
fi
if [[ -d npm ]]; then
  printf 'npm tree size: %s\n' "$(du -sh npm | awk '{print $1}')"
fi
if command -v go >/dev/null 2>&1; then
  direct_deps="$(go list -m -f '{{if and (not .Main) (not .Indirect)}}{{.Path}}{{end}}' all 2>/dev/null | sed '/^$/d' | wc -l | tr -d ' ')"
  printf 'Direct Go dependencies: %s\n' "${direct_deps:-unknown}"
fi
