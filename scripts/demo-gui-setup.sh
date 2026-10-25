#!/usr/bin/env bash
# Prepare a GUI-friendly auth-injection demo:
#   fake authenticated model upstream + isolated nullkeyd + fake credential.
# The demo itself happens in the GUI; this script only prepares the backend.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP="${NULLKEY_APP:-/Applications/Nullkey.app}"
DEMO_HOME="${NULLKEY_DEMO_HOME:-/tmp/nullkey-gui-demo}"
WORKSPACE="${NULLKEY_DEMO_WORKSPACE:-/tmp/nullkey-gui-demo-ws}"
FAKE_PORT="${NULLKEY_DEMO_FAKE_PORT:-49960}"
CONTROL_PORT="${NULLKEY_DEMO_CONTROL_PORT:-49152}"
GATEWAY_PORT="${NULLKEY_DEMO_GATEWAY_PORT:-49153}"
PROXY_PORT="${NULLKEY_DEMO_PROXY_PORT:-49154}"
TOKEN_FILE="$DEMO_HOME/daemon-token"
FAKE_LOG="$DEMO_HOME/fake.log"
STARTED_DAEMON=""
STARTED_FAKE=""

cleanup_on_error() {
  [ -n "$STARTED_DAEMON" ] && kill "$STARTED_DAEMON" 2>/dev/null || true
  [ -n "$STARTED_FAKE" ] && kill "$STARTED_FAKE" 2>/dev/null || true
}
trap cleanup_on_error ERR

for cmd in python3 curl lsof codex; do
  command -v "$cmd" >/dev/null || { echo "missing $cmd" >&2; exit 1; }
done
[ -x "$APP/Contents/Resources/nullkeyd" ] || {
  echo "$APP does not contain Contents/Resources/nullkeyd; install v0.1.3 or newer" >&2
  exit 1
}
[ -x "$APP/Contents/MacOS/Nullkey" ] || {
  echo "$APP is not a Nullkey.app bundle" >&2
  exit 1
}
[ -s "$HOME/.codex/models_cache.json" ] || {
  echo "missing ~/.codex/models_cache.json; run codex once on the host first" >&2
  exit 1
}

for port in "$FAKE_PORT" "$CONTROL_PORT" "$GATEWAY_PORT" "$PROXY_PORT"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "port $port is already in use; stop the old daemon/GUI and retry" >&2
    exit 1
  fi
done
if pgrep -f "$APP/Contents/MacOS/Nullkey" >/dev/null 2>&1; then
  echo "Nullkey GUI is already running; quit it before starting the demo" >&2
  exit 1
fi

echo "==> release daemon: $("$APP/Contents/Resources/nullkeyd" --version)"
rm -rf "$DEMO_HOME" "$WORKSPACE" "$FAKE_LOG"
mkdir -p "$DEMO_HOME" "$WORKSPACE"

echo "==> fake authenticated upstream :$FAKE_PORT"
python3 "$ROOT/scripts/e2e-fake-model.py" \
  --port "$FAKE_PORT" --log "$FAKE_LOG" \
  >"$DEMO_HOME/fake.stdout.log" 2>&1 &
STARTED_FAKE=$!
echo "$STARTED_FAKE" >"$DEMO_HOME/fake.pid"

echo "==> isolated nullkeyd on $CONTROL_PORT/$GATEWAY_PORT/$PROXY_PORT"
env -u NULLKEY_DAEMON_TOKEN -u NULLKEY_DAEMON_URL -u NULLKEY_TOKEN_FILE \
  NULLKEY_HOME="$DEMO_HOME" \
  NULLKEY_SECRET_BACKEND=file \
  "$APP/Contents/Resources/nullkeyd" \
  --listen "127.0.0.1:$CONTROL_PORT" \
  --gateway-listen "127.0.0.1:$GATEWAY_PORT" \
  --proxy-listen "127.0.0.1:$PROXY_PORT" \
  >"$DEMO_HOME/daemon.log" 2>&1 &
STARTED_DAEMON=$!
echo "$STARTED_DAEMON" >"$DEMO_HOME/daemon.pid"

for _ in $(seq 1 100); do
  if curl -s -m 1 "http://127.0.0.1:$CONTROL_PORT/api/v1/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
if ! curl -s -m 1 "http://127.0.0.1:$CONTROL_PORT/api/v1/health" >/dev/null 2>&1; then
  echo "daemon did not start:" >&2
  cat "$DEMO_HOME/daemon.log" >&2
  exit 1
fi
for _ in $(seq 1 50); do
  [ -s "$TOKEN_FILE" ] && break
  sleep 0.1
done
[ -s "$TOKEN_FILE" ] || { echo "daemon did not write $TOKEN_FILE" >&2; exit 1; }
TOKEN="$(cat "$TOKEN_FILE")"

echo "==> fake credential + workspace"
curl -sS -f -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{
    \"provider_id\": \"codex\",
    \"label\": \"Demo ChatGPT\",
    \"auth_type\": \"oauth\",
    \"value\": \"fake-oauth-token\",
    \"base_url\": \"http://127.0.0.1:$FAKE_PORT/backend-api/codex\"
  }" \
  "http://127.0.0.1:$CONTROL_PORT/api/v1/credentials" >"$DEMO_HOME/credential.json"

curl -sS -f -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{
    \"name\": \"demo\",
    \"root_path\": \"$WORKSPACE\",
    \"storage_kind\": \"managed\",
    \"default_mode\": \"rw\"
  }" \
  "http://127.0.0.1:$CONTROL_PORT/api/v1/workspaces" >"$DEMO_HOME/workspace.json"

trap - ERR
cat >"$DEMO_HOME/demo.env" <<EOF
DEMO_HOME=$DEMO_HOME
WORKSPACE=$WORKSPACE
CONTROL_PORT=$CONTROL_PORT
FAKE_PORT=$FAKE_PORT
FAKE_LOG=$FAKE_LOG
DAEMON_PID=$STARTED_DAEMON
FAKE_PID=$STARTED_FAKE
EOF

echo
echo "Demo backend is ready."
echo "  fake log:     tail -f $FAKE_LOG"
echo "  demo home:    $DEMO_HOME"
echo
echo "Launch the GUI with the SAME home (do not use open):"
echo
if [ "$CONTROL_PORT" = "49152" ]; then
  echo "  NULLKEY_HOME=$DEMO_HOME $APP/Contents/MacOS/Nullkey"
else
  echo "  NULLKEY_HOME=$DEMO_HOME NULLKEY_DAEMON_URL=http://127.0.0.1:$CONTROL_PORT $APP/Contents/MacOS/Nullkey"
fi
echo
echo "Then: 连接 Codex 已勾选 -> 检查安装 -> 选择 demo 工作区 -> 启动 Codex"
echo "In Codex, type: hello"
echo
echo "Stop everything afterwards:"
echo "  $ROOT/scripts/demo-gui-cleanup.sh"
