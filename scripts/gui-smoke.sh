#!/usr/bin/env bash
# Start the packaged GUI without a daemon (autostart disabled) and catch
# bootstrap errors that widget tests hide by initializing Flutter's binding
# before the entry point.
set -euo pipefail

app="${1:?usage: gui-smoke.sh /path/to/Nullkey.app}"
work="$(mktemp -d)"
pid=""
cleanup() {
  if [ -n "$pid" ]; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT
mkdir -p "$work/home"

NULLKEY_HOME="$work/home" NULLKEY_DAEMON_TOKEN="" \
  NULLKEY_DAEMON_URL="http://127.0.0.1:1" NULLKEY_DEMO=0 \
  NULLKEY_DISABLE_AUTOSTART=1 \
  "$app/Contents/MacOS/Nullkey" > "$work/startup.log" 2>&1 &
pid=$!

for ((i = 0; i < 100; i++)); do
  if ! kill -0 "$pid" 2>/dev/null; then
    cat "$work/startup.log"
    echo "GUI exited during startup" >&2
    exit 1
  fi
  if grep -Eq 'Unhandled Exception|Error while initializing the Dart VM' "$work/startup.log"; then
    cat "$work/startup.log"
    echo "GUI raised an unhandled startup error" >&2
    exit 1
  fi
  sleep 0.1
done
echo "GUI startup smoke passed: stayed alive without unhandled Dart errors"
