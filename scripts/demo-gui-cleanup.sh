#!/usr/bin/env bash
# Stop the GUI auth-injection demo and remove its temporary home/workspace.
set -euo pipefail

DEMO_HOME="${NULLKEY_DEMO_HOME:-/tmp/nullkey-gui-demo}"
WORKSPACE="${NULLKEY_DEMO_WORKSPACE:-/tmp/nullkey-gui-demo-ws}"
CONTROL_PORT="${NULLKEY_DEMO_CONTROL_PORT:-49152}"
TOKEN_FILE="$DEMO_HOME/daemon-token"
DAEMON_PID_FILE="$DEMO_HOME/daemon.pid"
FAKE_PID_FILE="$DEMO_HOME/fake.pid"

if [ -s "$TOKEN_FILE" ] && curl -s -m 1 "http://127.0.0.1:$CONTROL_PORT/api/v1/health" >/dev/null 2>&1; then
  token="$(cat "$TOKEN_FILE")"
  ids="$(curl -sS -H "Authorization: Bearer $token" \
    "http://127.0.0.1:$CONTROL_PORT/api/v1/sandboxes" |
    python3 -c 'import json,sys; print("\n".join(i["id"] for i in json.load(sys.stdin).get("items",[])))' 2>/dev/null || true)"
  for id in $ids; do
    curl -s -X POST -H "Authorization: Bearer $token" \
      "http://127.0.0.1:$CONTROL_PORT/api/v1/sandboxes/$id/stop" >/dev/null 2>&1 || true
    curl -s -X DELETE -H "Authorization: Bearer $token" \
      "http://127.0.0.1:$CONTROL_PORT/api/v1/sandboxes/$id" >/dev/null 2>&1 || true
  done
fi

for file in "$DAEMON_PID_FILE" "$FAKE_PID_FILE"; do
  if [ -s "$file" ]; then
    pid="$(cat "$file")"
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
done

rm -rf "$DEMO_HOME" "$WORKSPACE"

echo "demo environment stopped; quit the GUI window if it is still open."
