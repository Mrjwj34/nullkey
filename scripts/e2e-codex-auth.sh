#!/usr/bin/env bash
# Reproducible local end-to-end check:
#   credential -> Codex sandbox start -> Codex model call with gateway-injected auth.
#
# Requirements: Go, Python 3, curl, lsof, and a host Codex install that has been
# run once (so ~/.codex/models_cache.json exists). Nothing touches the normal
# Nullkey home or the default 49152-49154 ports.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TOKEN="e2e-nullkey-token-0123456789abcdef"
CRED_TOKEN="fake-oauth-token"
FAKE_PORT=49960
CONTROL_PORT=49961
GATEWAY_PORT=49962
PROXY_PORT=49963
WORK="$(mktemp -d "${TMPDIR:-/tmp}/nullkey-e2e.XXXXXX")"
DAEMON_PID=""
FAKE_PID=""
SANDBOX_ID=""
KEEP="${KEEP:-0}"

cleanup() {
  if [ -n "$SANDBOX_ID" ]; then
    curl -s -X POST -H "Authorization: Bearer $TOKEN" \
      "http://127.0.0.1:$CONTROL_PORT/api/v1/sandboxes/$SANDBOX_ID/stop" >/dev/null 2>&1 || true
    curl -s -X DELETE -H "Authorization: Bearer $TOKEN" \
      "http://127.0.0.1:$CONTROL_PORT/api/v1/sandboxes/$SANDBOX_ID" >/dev/null 2>&1 || true
  fi
  if [ -n "$DAEMON_PID" ]; then
    kill "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
  fi
  if [ -n "$FAKE_PID" ]; then
    kill "$FAKE_PID" 2>/dev/null || true
    wait "$FAKE_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

for pid in $FAKE_PORT $CONTROL_PORT $GATEWAY_PORT $PROXY_PORT; do
  if lsof -nP -iTCP:"$pid" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "port $pid is already in use; stop the listener and retry" >&2
    exit 1
  fi
done
for cmd in go python3 curl lsof; do
  command -v "$cmd" >/dev/null || { echo "missing $cmd" >&2; exit 1; }
done
command -v codex >/dev/null || { echo "missing codex on PATH" >&2; exit 1; }
if [ ! -s "$HOME/.codex/models_cache.json" ]; then
  echo "missing ~/.codex/models_cache.json; run codex once on the host first" >&2
  exit 1
fi

echo "==> build local binaries"
(cd "$ROOT" && go build -o "$WORK/nullkeyd" ./cmd/nullkeyd)
(cd "$ROOT" && go build -o "$WORK/e2e-terminal" ./scripts/e2e-terminal)

echo "==> start fake authenticated upstream on 127.0.0.1:$FAKE_PORT"
python3 "$ROOT/scripts/e2e-fake-model.py" --port "$FAKE_PORT" --log "$WORK/fake.log" \
  >"$WORK/fake.stdout.log" 2>&1 &
FAKE_PID=$!

echo "==> start isolated nullkeyd (minimal Finder-like PATH)"
mkdir -p "$WORK/home" "$WORK/workspace"
env -i HOME="$HOME" PATH=/usr/bin:/bin:/usr/sbin:/sbin \
  NULLKEY_HOME="$WORK/home" \
  NULLKEY_DAEMON_TOKEN="$TOKEN" \
  NULLKEY_SECRET_BACKEND=file \
  "$WORK/nullkeyd" \
  --listen "127.0.0.1:$CONTROL_PORT" \
  --gateway-listen "127.0.0.1:$GATEWAY_PORT" \
  --proxy-listen "127.0.0.1:$PROXY_PORT" \
  >"$WORK/daemon.log" 2>&1 &
DAEMON_PID=$!

api() {
  curl -sS -H "Authorization: Bearer $TOKEN" "$@"
}
json_field() {
  python3 -c 'import json,sys; print(json.load(sys.stdin)[sys.argv[1]])' "$1"
}

for _ in $(seq 1 100); do
  if curl -s -m 1 "http://127.0.0.1:$CONTROL_PORT/api/v1/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
curl -s -m 1 "http://127.0.0.1:$CONTROL_PORT/api/v1/health" >/dev/null || {
  echo "daemon did not start" >&2
  cat "$WORK/daemon.log" >&2
  exit 1
}

echo "==> configure a fake ChatGPT OAuth credential (base_url points at the fake upstream)"
api -H 'Content-Type: application/json' \
  -d "{\"provider_id\":\"codex\",\"label\":\"e2e chatgpt\",\"auth_type\":\"oauth\",\"value\":\"$CRED_TOKEN\",\"base_url\":\"http://127.0.0.1:$FAKE_PORT/backend-api/codex\"}" \
  "http://127.0.0.1:$CONTROL_PORT/api/v1/credentials" >"$WORK/credential.json"

echo "==> install check finds host Codex with the minimal PATH"
api "http://127.0.0.1:$CONTROL_PORT/api/v1/harnesses" >"$WORK/harnesses.json"
python3 - "$WORK/harnesses.json" <<'PY'
import json, sys
items = json.load(open(sys.argv[1]))["items"]
codex = [i for i in items if i["id"] == "codex"]
if not codex or not codex[0]["installed"]:
    raise SystemExit("codex is not reported installed")
print("codex:", codex[0]["version"])
PY

echo "==> create workspace"
rm -rf "$WORK/workspace"
mkdir -p "$WORK/workspace"
api -H 'Content-Type: application/json' \
  -d "{\"name\":\"e2e\",\"root_path\":\"$WORK/workspace\",\"storage_kind\":\"managed\",\"default_mode\":\"rw\"}" \
  "http://127.0.0.1:$CONTROL_PORT/api/v1/workspaces" >"$WORK/workspace.json"
WS_ID="$(json_field id <"$WORK/workspace.json")"

echo "==> start a Codex harness sandbox (workspace lock + generated CODEX_HOME)"
api -H 'Content-Type: application/json' \
  -d "{\"harness_id\":\"codex\",\"workspace_id\":\"$WS_ID\",\"runtime\":\"native\",\"network_mode\":\"filtered\"}" \
  "http://127.0.0.1:$CONTROL_PORT/api/v1/sandboxes" >"$WORK/codex-sandbox.json"
SANDBOX_ID="$(json_field id <"$WORK/codex-sandbox.json")"

for _ in $(seq 1 50); do
  if grep -q "GET /backend-api/codex/models" "$WORK/fake.log" 2>/dev/null; then
    break
  fi
  sleep 0.1
done
grep -q "GET /backend-api/codex/models.*auth=Bearer $CRED_TOKEN" "$WORK/fake.log" || {
  echo "Codex did not call the authenticated model catalog" >&2
  cat "$WORK/fake.log" >&2
  cat "$WORK/daemon.log" >&2
  exit 1
}
CODEX_PID="$(json_field pid <"$WORK/codex-sandbox.json")"
kill -0 "$CODEX_PID" 2>/dev/null || {
  echo "Codex harness exited during startup" >&2
  cat "$WORK/daemon.log" >&2
  exit 1
}
echo "codex sandbox $SANDBOX_ID is running; model catalog call was authenticated"

api -X POST "http://127.0.0.1:$CONTROL_PORT/api/v1/sandboxes/$SANDBOX_ID/stop" >/dev/null
api -X DELETE "http://127.0.0.1:$CONTROL_PORT/api/v1/sandboxes/$SANDBOX_ID" >/dev/null
SANDBOX_ID=""

echo "==> start a shell sandbox and run codex exec through the gateway"
api -H 'Content-Type: application/json' \
  -d "{\"harness_id\":\"shell\",\"workspace_id\":\"$WS_ID\",\"runtime\":\"native\",\"network_mode\":\"filtered\"}" \
  "http://127.0.0.1:$CONTROL_PORT/api/v1/sandboxes" >"$WORK/shell-sandbox.json"
SANDBOX_ID="$(json_field id <"$WORK/shell-sandbox.json")"
api -X POST "http://127.0.0.1:$CONTROL_PORT/api/v1/sandboxes/$SANDBOX_ID/terminal" >"$WORK/ticket.json"
WS_URL="$(json_field ws_url <"$WORK/ticket.json")"
TICKET="$(json_field ticket <"$WORK/ticket.json")"

cat >"$WORK/command.txt" <<'EOS'
mkdir -p "$HOME/.codex"
cat > "$HOME/.codex/config.toml" <<'TOML'
model_provider = "nullkey"
chatgpt_base_url = "https://127.0.0.1:49962/backend-api"
approval_policy = "never"
sandbox_mode = "danger-full-access"
[model_providers.nullkey]
name = "Nullkey"
base_url = "https://127.0.0.1:49962/openai"
env_key = "NULLKEY_TOKEN"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
[features]
daemon_auto_start = false
plugins = false
apps = false
respect_system_proxy = false
TOML
codex exec --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox "hello"
EOS
"$WORK/e2e-terminal" -url "$WS_URL" -ticket "$TICKET" \
  -send "$(cat "$WORK/command.txt")"$'\r' -wait 20s >"$WORK/terminal.out"

if ! grep -q 'pong from fake upstream' "$WORK/terminal.out"; then
  echo "codex exec did not receive the fake upstream response" >&2
  cat "$WORK/terminal.out" >&2
  cat "$WORK/daemon.log" >&2
  exit 1
fi
grep -q "POST /backend-api/codex/responses auth=Bearer $CRED_TOKEN" "$WORK/fake.log" || {
  echo "codex exec did not call the authenticated responses endpoint" >&2
  cat "$WORK/fake.log" >&2
  exit 1
}

echo
echo "--- fake upstream saw the injected credential ---"
grep -E 'GET /backend-api/codex/models|POST /backend-api/codex/responses' "$WORK/fake.log" || true
echo "--- codex exec output ---"
python3 - "$WORK/terminal.out" <<'PY'
import json
import re
import sys

text = ""
for line in open(sys.argv[1]):
    try:
        message = json.loads(line)
    except Exception:
        continue
    if message.get("type") == "output":
        text += message.get("data", "")
text = re.sub(r"\x1b\[[0-9;?]*[ -/]*[@-~]", "", text).replace("\r", "")
print(text[-1200:])
PY

echo
echo "PASS"
echo "  harness login: fake ChatGPT OAuth credential"
echo "  workspace:     $WS_ID"
echo "  codex start:   authenticated GET /backend-api/codex/models"
echo "  codex exec:    authenticated POST /backend-api/codex/responses"
echo "  response:      pong from fake upstream"

if [ "$KEEP" = "1" ]; then
  trap - EXIT
  echo
  echo "KEEP=1: leaving the demo environment running."
  echo "  work dir:     $WORK"
  echo "  fake log:     tail -f $WORK/fake.log"
  echo "  daemon pid:   $DAEMON_PID (ports $CONTROL_PORT/$GATEWAY_PORT/$PROXY_PORT)"
  echo "  fake pid:     $FAKE_PID"
  echo "  sandbox:      $SANDBOX_ID"
  echo "  token:        $TOKEN"
  echo
  echo "stop it with:"
  echo "  curl -s -X POST -H 'Authorization: Bearer $TOKEN' http://127.0.0.1:$CONTROL_PORT/api/v1/sandboxes/$SANDBOX_ID/stop"
  echo "  kill $DAEMON_PID $FAKE_PID; rm -rf $WORK"
fi
