#!/usr/bin/env bash
# Shared scaffolding for the fork-leak hunt: an isolated figaro (own config,
# state, runtime, cache), a scripted gateway, no credentials, no network.
# Nothing here can reach the real daemon or the real store.
set -uo pipefail

# ROOT is where the boxes, the binary and the gateways live. Override it to
# put the scratch somewhere other than the repo.
ROOT=${FORKLEAK_ROOT:-/var/tmp/forkleak}
BIN=${FORKLEAK_BIN:-$ROOT/bin/figaro}
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PORT=${PORT:-8931}

box_up() {
  BOX=$1
  rm -rf "$BOX"; mkdir -p "$BOX"/{cfg/outfits,cfg/providers,state,rt,cache}
  cat > "$BOX/cfg/providers/gateway.toml" <<EOF
base_url = "http://127.0.0.1:$PORT/v1"
EOF
  printf 'You are a test agent.\n' > "$BOX/cfg/credo.md"
  cat > "$BOX/cfg/outfits/probe.toml" <<'EOF'
duke-title = "prober"

[system]
provider   = "gateway"
model      = "auto"
max_tokens = 64
credo      = { fileName = "credo.md" }
EOF
  cat > "$BOX/cfg/config.toml" <<'EOF'
default_outfit = "probe"
interactive = false
EOF
  export FIGARO_CONFIG_DIR=$BOX/cfg FIGARO_STATE_DIR=$BOX/state \
         FIGARO_RUNTIME_DIR=$BOX/rt FIGARO_CACHE_DIR=$BOX/cache FIGARO_NO_BIND=1
}

gw_up() {
  # GW picks the gateway: our tagging one, or the repo's tool-calling script
  # (several LTs per turn, which is where turn-vs-LT arithmetic shows).
  python3 "${GW:-$HERE/gw.py}" "$PORT" "${BOX:-/tmp}/requests.jsonl" >"${BOX:-/tmp}/gw.log" 2>&1 &
  GW_PID=$!
  for _ in $(seq 40); do
    curl -s -o /dev/null "http://127.0.0.1:$PORT/v1/models" && return 0
    sleep 0.25
  done
  echo "gateway never came up" >&2; return 1
}

gw_down() { kill "${GW_PID:-0}" 2>/dev/null; }

wait_idle() { # wait_idle <aria> [secs]
  local id=$1 secs=${2:-30} st
  for _ in $(seq $((secs * 2))); do
    st=$("$BIN" status "$id" -j 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin).get("state",""))' 2>/dev/null)
    [[ "$st" == "idle" ]] && return 0
    sleep 0.5
  done
  return 1
}

aria_new() { "$BIN" new -j 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin)["aria_id"])'; }
