#!/usr/bin/env bash
# Enforce the repository-wide Go statement coverage gate.
set -euo pipefail

profile="${1:-coverage.out}"
threshold="${NULLKEY_COVERAGE_MIN:-90}"

# Exercise the real Keychain backend on macOS with a throwaway fake entry so the
# cgo adapter is measured. The test deletes the item and never reads real data.
if [ "$(uname -s)" = "Darwin" ]; then
  export NULLKEY_KEYCHAIN_TEST=1
fi

go test -count=1 -coverprofile="${profile}" ./...

total="$(go tool cover -func="${profile}" | awk '/^total:/ {print $3}' | tr -d '%')"
if [ -z "${total}" ]; then
  echo "coverage: could not read total from ${profile}" >&2
  exit 1
fi
echo "coverage: ${total}% (minimum ${threshold}%)"
awk -v total="${total}" -v min="${threshold}" 'BEGIN { exit !(total + 0 >= min + 0) }' || {
  echo "coverage gate failed: ${total}% < ${threshold}%" >&2
  exit 1
}
