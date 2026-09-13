#!/usr/bin/env bash
# THE OTHER SURFACE: an ordinary inline `figaro send`, which draws the queue as
# a trailer under the incipit rather than as a pit. Sends into an IDLE aria and
# into a BUSY one, sampling the pane as fast as capture-pane goes, and prints
# how long the trailer was on screen each time.
set -uo pipefail
BIN=${BIN:-/var/tmp/jitterbench.before.bin/figaro}
LABEL=${LABEL:-before}
OUT=/var/tmp/jitterinline.$LABEL
PORT=${PORT:-8959}
rm -rf "$OUT"; mkdir -p "$OUT"
SOCK=$OUT/tmux.sock
export FIGARO_STATE_DIR=$OUT/state FIGARO_RUNTIME_DIR=$OUT/run \
       FIGARO_CONFIG_DIR=$OUT/cfg FIGARO_CACHE_DIR=$OUT/cache
unset FIGARO_ARIA FIGARO_NO_BIND
mkdir -p "$FIGARO_STATE_DIR" "$FIGARO_RUNTIME_DIR" "$FIGARO_CONFIG_DIR"/{outfits,providers} "$FIGARO_CACHE_DIR"
cleanup() {
  kill "${SAMPLER:-0}" 2>/dev/null
  tmux -S "$SOCK" kill-server 2>/dev/null
  "$BIN" stop >/dev/null 2>&1
  kill "${GW:-0}" 2>/dev/null
}
trap cleanup EXIT
python3 "$(git rev-parse --show-toplevel)/scripts/jitter-gateway.py" $PORT "$OUT/req.jsonl" & GW=$!
sleep 1
cfg() { printf '%s\n' "$1" > "$OUT/req.jsonl.cfg"; }
cfg '{"ttft":0.2,"chunks":25,"gap":0.05}'
cat > "$FIGARO_CONFIG_DIR/providers/gateway.toml" <<EOF
base_url = "http://127.0.0.1:$PORT/v1"
EOF
printf 'test agent\n' > "$FIGARO_CONFIG_DIR/credo.md"
cat > "$FIGARO_CONFIG_DIR/outfits/bench.toml" <<'EOF'
duke-title = "bench"
[system]
provider = "gateway"
model = "auto"
max_tokens = 512
max_context_tokens = 200000
credo = { fileName = "credo.md" }
EOF
printf 'default_outfit = "bench"\ninteractive = false\n' > "$FIGARO_CONFIG_DIR/config.toml"
ARIA=$("$BIN" new -j | python3 -c 'import json,sys;print(json.load(sys.stdin)["aria_id"])')
echo "aria $ARIA"
MARKS=$OUT/marks.jsonl
tmux -S "$SOCK" new-session -d -s i -x 100 -y 30 bash --norc
tmux -S "$SOCK" set -g status off
tmux -S "$SOCK" send-keys -t i:0 "export FIGARO_STATE_DIR=$FIGARO_STATE_DIR FIGARO_RUNTIME_DIR=$FIGARO_RUNTIME_DIR FIGARO_CONFIG_DIR=$FIGARO_CONFIG_DIR FIGARO_CACHE_DIR=$FIGARO_CACHE_DIR FIGARO_MARKS=$MARKS FORCE_COLOR=1 PS1='ready\$ '" Enter
sleep 1
(
  while :; do
    p=$(tmux -S "$SOCK" capture-pane -p -t i:0 2>/dev/null)
    printf '%s\t%s\t%s\n' "$(date +%s%N)" \
      "$(grep -c 'queued messages' <<<"$p")" "$(grep -c '𝄚' <<<"$p")"
    sleep 0.005
  done
) > "$OUT/pit.tsv" & SAMPLER=$!
: > "$OUT/rounds.tsv"
round() { printf '%s\t%s\t%s\n' "$(date +%s%N)" "$1" "$2" >> "$OUT/rounds.tsv"; }
state() { "$BIN" status "$ARIA" -j 2>/dev/null | python3 -c 'import json,sys
try: print(json.load(sys.stdin).get("state",""))
except Exception: print("")'; }
wait_idle() { for _ in $(seq 1 "${1:-120}"); do [ "$(state)" = idle ] && return 0; sleep 0.5; done; return 1; }

echo "== inline sends into an idle aria =="
for i in $(seq 1 "${IDLE_ROUNDS:-6}"); do
  wait_idle || { echo "never idle"; exit 1; }
  sleep 0.6
  round idle "$i"
  tmux -S "$SOCK" send-keys -t i:0 "$BIN send --id $ARIA -- 'idle inline $i'" Enter
  sleep 4
done
wait_idle

echo "== inline sends into a busy aria =="
cfg '{"ttft":0.3,"chunks":80,"gap":0.09}'
"$BIN" send -f --id "$ARIA" -- 'the long one' >/dev/null 2>&1
sleep 2
cfg '{"ttft":0.2,"chunks":15,"gap":0.04}'
for i in $(seq 1 "${BUSY_ROUNDS:-2}"); do
  round busy "$i"
  tmux -S "$SOCK" send-keys -t i:0 "$BIN send --id $ARIA -- 'busy inline $i'" Enter
  sleep 3
done
wait_idle 240
sleep 1
kill $SAMPLER 2>/dev/null; SAMPLER=
tmux -S "$SOCK" kill-server 2>/dev/null
"$BIN" stop >/dev/null 2>&1
kill "$GW" 2>/dev/null; GW=
python3 - "$OUT" <<'PY'
import sys
out = sys.argv[1]
pit = []
for line in open(f"{out}/pit.tsv"):
    p = line.split()
    if len(p) == 3 and p[0].isdigit():
        pit.append((int(p[0]), int(p[1]) + int(p[2])))
rounds = [(int(a), b, c) for a, b, c in (l.split() for l in open(f"{out}/rounds.tsv"))]
eps, start = [], None
for t, n in pit:
    if n and start is None:
        start = t
    elif not n and start is not None:
        eps.append((start, t)); start = None
if start is not None:
    eps.append((start, pit[-1][0]))
gaps = [(b - a) / 1e6 for a, b in zip([p[0] for p in pit], [p[0] for p in pit[1:]])]
gaps.sort()
print(f"{len(pit)} samples, median period {gaps[len(gaps)//2]:.1f} ms")
print(f"queue trailer visible: {len(eps)} episodes")
for a, b in eps:
    kind = next((f"{k}{n}" for t, k, n in reversed(rounds) if t <= a), "?")
    print(f"   {(b-a)/1e6:8.0f} ms   after {kind}")
PY
