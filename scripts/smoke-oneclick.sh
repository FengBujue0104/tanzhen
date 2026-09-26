#!/bin/sh
# Local one-click deploy smoke test (prefix install, no root required).
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
SMOKE="${SMOKE_DIR:-/workspace/tanzhen-smoke}"
ASSET_PORT="${ASSET_PORT:-19090}"
HUB_PORT="${HUB_PORT:-18080}"
PASS="${ADMIN_PASSWORD:-SmokeTestPass123}"

echo "== prepare $SMOKE =="
rm -rf "$SMOKE"
mkdir -p "$SMOKE/bin" "$SMOKE/etc" "$SMOKE/data" "$SMOKE/log" \
  "$SMOKE/agent-bin" "$SMOKE/agent-etc" "$SMOKE/agent-log"

# Kill anything left on our ports
if command -v fuser >/dev/null 2>&1; then
  fuser -k "${ASSET_PORT}/tcp" 2>/dev/null || true
  fuser -k "${HUB_PORT}/tcp" 2>/dev/null || true
fi

echo "== asset server on :$ASSET_PORT =="
python3 -m http.server "$ASSET_PORT" --directory "$ROOT/releases" \
  >"$SMOKE/log/asset-server.log" 2>&1 &
echo $! >"$SMOKE/asset-server.pid"
sleep 0.4
curl -fsS -o /dev/null "http://127.0.0.1:$ASSET_PORT/tanzhen-hub-linux-amd64"

echo "== hub one-click (install-hub.sh, prefix) =="
ADMIN_PASSWORD="$PASS" \
TANZHEN_PORT="$HUB_PORT" \
PUBLIC_URL="http://127.0.0.1:$HUB_PORT" \
TANZHEN_DATA_DIR="$SMOKE/data" \
INSTALL_DIR="$SMOKE/bin" \
CONFIG_DIR="$SMOKE/etc" \
LOG_FILE="$SMOKE/log/hub.log" \
PID_FILE="$SMOKE/hub.pid" \
TANZHEN_BASE_URL="http://127.0.0.1:$ASSET_PORT" \
  sh "$ROOT/cmd/hub/static/install-hub.sh" | tee "$SMOKE/log/hub-install.log"

test -x "$SMOKE/bin/tanzhen-hub"
test -f "$SMOKE/etc/hub.env"
test -f "$SMOKE/hub.pid"
curl -fsS "http://127.0.0.1:$HUB_PORT/healthz" | grep -q ok
curl -fsS -o /dev/null -w "admin_html=%{http_code}\n" "http://127.0.0.1:$HUB_PORT/admin"
echo "HUB_OK"

echo "== create node via admin API =="
cj="$SMOKE/cj"
curl -fsS -c "$cj" -X POST "http://127.0.0.1:$HUB_PORT/api/admin/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$PASS\"}" >/dev/null
NODE_JSON="$(curl -fsS -b "$cj" -X POST "http://127.0.0.1:$HUB_PORT/api/admin/nodes" \
  -H 'Content-Type: application/json' \
  -d '{"name":"smoke-local","meta":{"location":"box"}}')"
echo "$NODE_JSON" | tee "$SMOKE/log/create-node.json"
TOKEN="$(printf '%s' "$NODE_JSON" | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')"
test -n "$TOKEN"
echo "TOKEN_OK len=${#TOKEN}"

echo "== agent one-click (install.sh against local hub) =="
# Prefer hub-served script + binaries (production path).
curl -fsSL "http://127.0.0.1:$HUB_PORT/install.sh?hub=http://127.0.0.1:$HUB_PORT&token=$TOKEN" \
  -o "$SMOKE/install.sh"
INSTALL_DIR="$SMOKE/agent-bin" \
CONFIG_DIR="$SMOKE/agent-etc" \
LOG_FILE="$SMOKE/agent-log/agent.log" \
PID_FILE="$SMOKE/agent.pid" \
  sh "$SMOKE/install.sh" --hub "http://127.0.0.1:$HUB_PORT" --token "$TOKEN" \
  | tee "$SMOKE/log/agent-install.log"

test -x "$SMOKE/agent-bin/tanzhen-agent"
test -f "$SMOKE/agent-etc/token"
test -f "$SMOKE/agent.pid"
echo "AGENT_INSTALLED"

echo "== wait for online + metrics =="
i=0
ONLINE=0
while [ "$i" -lt 30 ]; do
  STATUS="$(curl -fsS "http://127.0.0.1:$HUB_PORT/api/status" || true)"
  echo "$STATUS" >"$SMOKE/log/status.json"
  if printf '%s' "$STATUS" | python3 -c '
import sys,json
d=json.load(sys.stdin)
nodes=d.get("nodes") or []
ok=any(n.get("online") and n.get("metrics") for n in nodes)
sys.exit(0 if ok else 1)
'; then
    ONLINE=1
    break
  fi
  i=$((i+1))
  sleep 1
done

if [ "$ONLINE" != 1 ]; then
  echo "FAIL: agent did not come online"
  echo "--- hub log ---"; tail -50 "$SMOKE/log/hub.log" || true
  echo "--- agent log ---"; tail -50 "$SMOKE/agent-log/agent.log" || true
  exit 1
fi
echo "AGENT_ONLINE"
python3 -c "
import json
d=json.load(open('$SMOKE/log/status.json'))
for n in d.get('nodes') or []:
  m=n.get('metrics') or {}
  print(f\"node={n.get('name')} online={n.get('online')} cpu={m.get('cpu_pct')} mem={m.get('mem_pct')}\")
"

echo "== appearance API smoke =="
curl -fsS -b "$cj" "http://127.0.0.1:$HUB_PORT/api/admin/appearance" | tee "$SMOKE/log/appearance.json"
curl -fsS "http://127.0.0.1:$HUB_PORT/api/appearance" >/dev/null

echo "== agent uninstall (scope check) =="
INSTALL_DIR="$SMOKE/agent-bin" \
CONFIG_DIR="$SMOKE/agent-etc" \
LOG_FILE="$SMOKE/agent-log/agent.log" \
PID_FILE="$SMOKE/agent.pid" \
  sh "$SMOKE/install.sh" --uninstall | tee "$SMOKE/log/agent-uninstall.log"
test ! -f "$SMOKE/agent-etc/token"
test -f "$SMOKE/etc/hub.env"
echo "AGENT_UNINSTALL_OK hub.env preserved"

echo "== hub uninstall --purge =="
INSTALL_DIR="$SMOKE/bin" \
CONFIG_DIR="$SMOKE/etc" \
TANZHEN_DATA_DIR="$SMOKE/data" \
LOG_FILE="$SMOKE/log/hub.log" \
PID_FILE="$SMOKE/hub.pid" \
  sh "$ROOT/cmd/hub/static/install-hub.sh" --uninstall --purge \
  | tee "$SMOKE/log/hub-uninstall.log"
test ! -f "$SMOKE/bin/tanzhen-hub"
test ! -d "$SMOKE/data" -o ! -e "$SMOKE/data/tanzhen.db"
echo "HUB_UNINSTALL_OK"

kill "$(cat "$SMOKE/asset-server.pid")" 2>/dev/null || true
echo ""
echo "SMOKE PASS"
