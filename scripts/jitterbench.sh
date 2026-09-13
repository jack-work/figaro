#!/usr/bin/env bash
# SUBMIT JITTER, measured: one scripted session, the same keystrokes every run.
#
# It drives the REAL pager in a real pty on a PRIVATE tmux server, against a
# gateway that streams slowly enough to have a thinking window, and records
# three things:
#
#   marks.jsonl  the mark sink (key, submit.qua, queue.row, runtime, frame)
#   pit.tsv      the queue pit as the SCREEN holds it, sampled ~30/s
#   rounds.tsv   when the driver pressed Enter, and into what state
#
# Usage: jitterbench.sh <label> [binary]
#   label   names the output dir /var/tmp/jitterbench.<label>
set -uo pipefail

LABEL=${1:?usage: jitterbench.sh <label> [binary]}
BIN=${2:-/var/tmp/jitterbench.$LABEL.bin/figaro}
PORT=${PORT:-8957}
OUT=/var/tmp/jitterbench.$LABEL
IDLE_ROUNDS=${IDLE_ROUNDS:-8}
BUSY_ROUNDS=${BUSY_ROUNDS:-3}

rm -rf "$OUT"; mkdir -p "$OUT"
SOCK=$OUT/tmux.sock
export FIGARO_STATE_DIR=$OUT/state FIGARO_RUNTIME_DIR=$OUT/run \
       FIGARO_CONFIG_DIR=$OUT/cfg FIGARO_CACHE_DIR=$OUT/cache
unset FIGARO_ARIA FIGARO_NO_BIND
mkdir -p "$FIGARO_STATE_DIR" "$FIGARO_RUNTIME_DIR" "$FIGARO_CONFIG_DIR"/{outfits,providers} "$FIGARO_CACHE_DIR"

cleanup() {
  [ -z "${SAMPLER:-}" ] || kill "$SAMPLER" 2>/dev/null
  tmux -S "$SOCK" kill-server 2>/dev/null
  "$BIN" stop >/dev/null 2>&1
  [ -z "${GW:-}" ] || kill "$GW" 2>/dev/null
}
trap cleanup EXIT

if (echo >/dev/tcp/127.0.0.1/$PORT) 2>/dev/null; then
  echo "FAIL: something already listens on :$PORT"; exit 1
fi
ROOT=$(git rev-parse --show-toplevel)
python3 "$ROOT/scripts/jitter-gateway.py" $PORT "$OUT/req.jsonl" & GW=$!
sleep 1

cfg() { printf '%s\n' "$1" > "$OUT/req.jsonl.cfg"; }
cfg '{"ttft":0.2,"chunks":30,"gap":0.05}'

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
tmux -S "$SOCK" new-session -d -s j -x 100 -y 30 bash --norc
tmux -S "$SOCK" set -g status off
tmux -S "$SOCK" send-keys -t j:0 "export FIGARO_STATE_DIR=$FIGARO_STATE_DIR FIGARO_RUNTIME_DIR=$FIGARO_RUNTIME_DIR FIGARO_CONFIG_DIR=$FIGARO_CONFIG_DIR FIGARO_CACHE_DIR=$FIGARO_CACHE_DIR FIGARO_MARKS=$MARKS FORCE_COLOR=1 PS1='ready\$ '" Enter
sleep 1
tmux -S "$SOCK" send-keys -t j:0 "$BIN listen $ARIA" Enter
sleep 4

# THE SCREEN IS THE WITNESS. Marks say what the renderer decided; this says
# what a reader could actually see, so a fix that only moves marks around
# cannot pass.
(
  while :; do
    # THE PIT NAMES ITSELF ON THE BAR: '𝄚' is the queue pit's glyph, and the
    # trailer under an incipit says the words. Either one on the screen means
    # a reader can see the queue.
    p=$(tmux -S "$SOCK" capture-pane -p -t j:0 2>/dev/null)
    printf '%s\t%s\n' "$(date +%s%N)" \
      "$(( $(grep -c '𝄚' <<<"$p") + $(grep -c 'queued messages' <<<"$p") ))"
    sleep 0.005
  done
) > "$OUT/pit.tsv" & SAMPLER=$!

: > "$OUT/rounds.tsv"
round() { printf '%s\t%s\t%s\n' "$(date +%s%N)" "$1" "$2" >> "$OUT/rounds.tsv"; }

state() { "$BIN" status "$ARIA" -j 2>/dev/null | python3 -c 'import json,sys
try: print(json.load(sys.stdin).get("state",""))
except Exception: print("")'; }

wait_idle() {
  for _ in $(seq 1 "${1:-120}"); do
    [ "$(state)" = idle ] && return 0
    sleep 0.5
  done
  return 1
}

# THE PAGER HAS NO COMPOSER: a prompt leaves it through the ':' box, which is
# the send path under test. Typed as a burst, then Enter on its own, so the
# Enter mark is unambiguous.
type_prompt() {
  tmux -S "$SOCK" send-keys -t j:0 -l ":send -- $1"
  sleep 0.5
}

echo "== idle rounds =="
for i in $(seq 1 "$IDLE_ROUNDS"); do
  wait_idle || { echo "FAIL: never went idle before round $i"; exit 1; }
  sleep 0.8
  type_prompt "idle round $i"
  round idle "$i"
  tmux -S "$SOCK" send-keys -t j:0 Enter
  sleep 3
done
wait_idle

echo "== busy rounds =="
cfg '{"ttft":0.3,"chunks":90,"gap":0.09}'
type_prompt "the long one"
round anchor 0
tmux -S "$SOCK" send-keys -t j:0 Enter
sleep 2.5
cfg '{"ttft":0.2,"chunks":20,"gap":0.04}'
for i in $(seq 1 "$BUSY_ROUNDS"); do
  type_prompt "busy round $i"
  round busy "$i"
  tmux -S "$SOCK" send-keys -t j:0 Enter
  sleep 1.2
done
wait_idle 240

sleep 1
kill $SAMPLER 2>/dev/null; SAMPLER=
tmux -S "$SOCK" capture-pane -p -t j:0 > "$OUT/final-pane.txt"

# THE CORPUS VALIDATES ITSELF, OR THE RUN IS WITHDRAWN. The first published
# numbers were measured against a gateway that read Content-Length only: figaro
# streams chunked request bodies, so the fixture answered while the client was
# still writing and turns died on a broken pipe. A run whose turns have no
# replies is not slower or faster than anything, and a script that reports it
# anyway is worse than no script.
# PROMPTS, NOT TURNS. A prompt sent into an aria that is already working waits
# in the queue and is folded into the NEXT turn with whatever else is waiting:
# three busy sends are one turn, correctly. So the invariant is that every
# prompt reached the aria and every turn was answered.
WANT_PROMPTS=$OUT/prompts.txt
: > "$WANT_PROMPTS"
for i in $(seq 1 "$IDLE_ROUNDS"); do echo "idle round $i" >> "$WANT_PROMPTS"; done
echo "the long one" >> "$WANT_PROMPTS"
for i in $(seq 1 "$BUSY_ROUNDS"); do echo "busy round $i" >> "$WANT_PROMPTS"; done
"$BIN" show "$ARIA" -a -j | python3 -c 'import json,sys
want = [l.strip() for l in open(sys.argv[1]) if l.strip()]
parts = json.load(sys.stdin)["parts"]
empty = [p["turn"] for p in parts if not p.get("nodes")]
if empty:
    sys.exit("fixture: turns with no reply: %s" % empty)
asked = " \n ".join(p.get("inquiry") or "" for p in parts)
missing = [w for w in want if asked.count(w) != 1]
if missing:
    sys.exit("fixture: prompts that did not arrive exactly once: %s" % missing)
print("validated %d prompts over %d answered turns" % (len(want), len(parts)))' "$WANT_PROMPTS" || exit 1

# And the other end of the same defect: every request the gateway logged must
# carry the messages the client actually sent.
python3 -c 'import json,sys
rows = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
posts = [r for r in rows if isinstance(r.get("body"), dict)]
blank = [r for r in posts if not r["body"].get("messages")]
if not posts:
    sys.exit("fixture: the gateway logged no requests")
if blank:
    sys.exit("fixture: %d of %d requests arrived with no messages (chunked body unread?)" % (len(blank), len(posts)))
print("validated %d gateway requests, every one carrying messages" % len(posts))' "$OUT/req.jsonl" || exit 1
tmux -S "$SOCK" send-keys -t j:0 C-c
sleep 1
tmux -S "$SOCK" kill-server 2>/dev/null
"$BIN" stop >/dev/null 2>&1
kill "$GW" 2>/dev/null; GW=
echo "marks $(wc -l < "$MARKS") lines, pit $(wc -l < "$OUT/pit.tsv") samples -> $OUT"
