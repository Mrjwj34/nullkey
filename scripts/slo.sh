#!/usr/bin/env bash
# Release SLO gate. Runs the SLO assertions in test/acceptance.
#
# The full S1-S19 set needs the native runtime, PTY, gateway, and tunnel. Until
# those land, NULLKEY_RELEASE_STRICT=1 (default) fails unimplemented gates
# instead of reporting fabricated numbers. Set NULLKEY_RELEASE_STRICT=0 for a
# local partial run.
set -euo pipefail

: "${NULLKEY_RELEASE_STRICT:=1}"
export NULLKEY_RELEASE_STRICT

exec go test \
  -tags 'acceptance release' \
  -count=1 \
  -run '^TestSLO_' \
  -v \
  -timeout 30m \
  ./test/acceptance
