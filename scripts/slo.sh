#!/usr/bin/env bash
# SLO benchmark placeholder (P9). It intentionally exits non-zero instead of
# reporting fabricated numbers. See docs/11-kernel-implementation-plan.md §11-12
# and docs/12-acceptance.md (AC-SLO-*).
set -euo pipefail

cat >&2 <<'MSG'
nullkey SLO benchmark is not implemented yet (P9).

Expected measurements:
  S1/S2  POST /sandboxes -> terminal ticket -> WS ready -> input enabled
  S3/S4  daemon RSS / per-sandbox RSS
  S8/S9  terminal RTT / gateway overhead against a controlled upstream stub
  S16-19 tunnel RTT/throughput, artifact listing, concurrency

Run `make acceptance` for functional assertions; SLO results must be recorded
with the macOS version, chip, harness version, sample count, and failures.
MSG
exit 2
