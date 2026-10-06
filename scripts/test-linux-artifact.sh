#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
binary="${1:-$root/dist/relay-server-linux-amd64}"
test_root="$(mktemp -d /tmp/relay-linux-test.XXXXXX)"
port="${RELAY_TEST_PORT:-18088}"
pid=""

cleanup() {
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then kill "$pid" 2>/dev/null || true; fi
  case "$test_root" in /tmp/relay-linux-test.*) rm -rf -- "$test_root" ;; esac
}
trap cleanup EXIT INT TERM

mkfifo "$test_root/input"
exec 3<>"$test_root/input"
export RELAY_SUPABASE_URL="http://127.0.0.1:1"
export RELAY_SUPABASE_PUBLISHABLE_KEY="test-publishable"

start_app() {
  log="$1"
  "$binary" --config "$test_root/relay.json" --data-dir "$test_root/data" --listen 127.0.0.1 --port "$port" <&3 >"$log" 2>&1 &
  pid=$!
  attempt=0
  until curl -fsS "http://127.0.0.1:$port/health" >"$test_root/health.json" 2>/dev/null; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 50 ]; then cat "$log"; return 1; fi
    sleep 0.1
  done
  grep -q '"status":"ok"' "$test_root/health.json"
}

start_app "$test_root/console.log"
printf '%s\n' status >&3
printf '%s\n' stop >&3
wait "$pid"
pid=""
grep -q 'RELAY READY .*version=0.7.1' "$test_root/console.log"
grep -q 'STATUS ready=true' "$test_root/console.log"
grep -q 'shutdown complete' "$test_root/console.log"
test -s "$test_root/data/relay.db"

start_app "$test_root/sigint.log"
kill -INT "$pid"
wait "$pid"
pid=""
grep -q 'shutdown complete' "$test_root/sigint.log"

start_app "$test_root/sigterm.log"
kill -TERM "$pid"
wait "$pid"
pid=""
grep -q 'shutdown complete' "$test_root/sigterm.log"

printf 'Linux artifact: version=0.7.1 health=ok console=ok SIGINT=ok SIGTERM=ok restart=ok\n'
