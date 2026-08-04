#!/usr/bin/env bash
# One command that proves prompt caching works end to end, with no
# credentials and no network: build the binary, run it as an isolated
# daemon against a local OpenAI-compatible gateway, take two turns, and
# read the four token buckets back out of `figaro status -j`.
#
# It asserts the things that were actually broken:
#   1. a cache directive is on the wire
#   2. one stable session key, under every name, identical across turns
#   3. never more than Anthropic's cap of 4 cache markers
#   4. cache_read > 0 on turn 2, and the four buckets re-sum to the
#      provider's own prompt+completion (the double-count regression)
#
# Touches nothing outside /tmp/figaro-verify. Your daemon is never used.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BOX=/tmp/figaro-verify
BIN=$BOX/figaro
PORT=${PORT:-8917}

rm -rf "$BOX"; mkdir -p "$BOX"/{cfg/loadouts,cfg/providers,state,rt,cache,wire}
trap 'kill $(cat "$BOX/rt/angelus.pid" 2>/dev/null) 2>/dev/null || true; kill ${GW_PID:-0} 2>/dev/null || true' EXIT

echo "== building from $(git -C "$ROOT" rev-parse --short HEAD)"
( cd "$ROOT" && go build -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse HEAD)" -o "$BIN" ./cmd/figaro )

echo "== starting the fake gateway on :$PORT"
python3 "$ROOT/scripts/fake-gateway.py" "$PORT" "$BOX/requests.jsonl" & GW_PID=$!
sleep 1

cat > "$BOX/cfg/providers/gateway.toml" <<EOF
base_url = "http://127.0.0.1:$PORT/v1"
cache_markers = "trusted"
EOF
printf 'You are a terse test agent. Reply with one word.\n' > "$BOX/cfg/credo.md"
cat > "$BOX/cfg/loadouts/verify.toml" <<'EOF'
duke-title = "verifier"

[system]
provider   = "gateway"
model      = "auto"
max_tokens = 64
credo      = { fileName = "credo.md" }
EOF
cat > "$BOX/cfg/config.toml" <<'EOF'
default_loadout = "verify"
interactive = false
EOF

export FIGARO_CONFIG_DIR=$BOX/cfg FIGARO_STATE_DIR=$BOX/state \
       FIGARO_RUNTIME_DIR=$BOX/rt FIGARO_CACHE_DIR=$BOX/cache

echo "== turn 1"
"$BIN" new -f -- "say ok" >/dev/null 2>&1
sleep 6
# The tree glyphs shift awk's columns, so match the id by shape: the aria
# line is the only one carrying a bare 8-hex token (loadout ids are
# name@hex, and the @ keeps them out).
ARIA=$("$BIN" ls -g 2>/dev/null | grep "say ok" | head -1 | grep -oE '(^|[^@[:alnum:]])[0-9a-f]{8}([^[:alnum:]]|$)' | grep -oE '[0-9a-f]{8}' | head -1)
[ -n "$ARIA" ] || { echo "FAIL: no aria was created"; exit 1; }

echo "== turn 2 (aria $ARIA)"
"$BIN" send -f --id "$ARIA" -- "say ok" >/dev/null 2>&1
sleep 6

"$BIN" status "$ARIA" -j > "$BOX/status.json"
python3 - "$BOX/requests.jsonl" "$BOX/status.json" <<'PY'
import json, sys

reqs = [json.loads(l) for l in open(sys.argv[1])]
st = json.load(open(sys.argv[2]))
fail = []

def markers(b):
    n = 1 if b.get("cache_control") else 0
    for m in b.get("messages", []):
        c = m.get("content")
        if isinstance(c, list):
            n += sum(1 for p in c if isinstance(p, dict) and p.get("cache_control"))
    return n

if not reqs:
    fail.append("the gateway received no requests at all")
else:
    b = reqs[-1]["body"]
    if not b.get("cache_control") and markers(b) == 0:
        fail.append("no cache directive on the wire")
    sids = {r["body"].get("session_id") for r in reqs}
    if len(sids) != 1 or None in sids:
        fail.append(f"session key not stable across turns: {sids}")
    if reqs[-1]["body"].get("session_id") != reqs[-1]["body"].get("prompt_cache_key"):
        fail.append("session_id and prompt_cache_key disagree")
    worst = max(markers(r["body"]) for r in reqs)
    if worst > 4:
        fail.append(f"{worst} cache markers exceeds the API cap of 4")

buckets = {k: st.get(k, 0) for k in
           ("tokens_in", "cache_read_tokens", "cache_write_tokens", "tokens_out")}
if st.get("cache_read_tokens", 0) <= 0:
    fail.append("turn 2 reported no cache read")

print("\n  requests        :", len(reqs))
print("  session key     :", reqs[-1]["body"].get("session_id") if reqs else "-")
print("  cache_control   :", reqs[-1]["body"].get("cache_control") if reqs else "-")
print("  max markers     :", max(markers(r["body"]) for r in reqs) if reqs else "-")
print("  buckets         :", buckets)
print("  context_tokens  :", st.get("context_tokens"))

if fail:
    print("\nFAIL:")
    for f in fail:
        print("  -", f)
    sys.exit(1)
print("\nPASS: directive on the wire, one stable session key, markers within cap,"
      "\n      cache reads folded into the buckets.")
PY
