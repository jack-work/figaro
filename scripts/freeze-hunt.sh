#!/usr/bin/env bash
# freeze-hunt.sh -- try to make the pager stop answering, and catch it when it does.
#
# The report: "under certain conditions the UI locks up totally, does not
# respond, does not resize, and may thrash the CPU." That is one of two
# animals, and this harness does not care which: it drives a REAL pager in a
# REAL pty through resize storms, key floods and a talkative aria, and after
# every burst it asks two questions the freeze cannot answer:
#
#   ALIVE?    press a key that must change the screen (? opens help), and see
#             whether the screen changes within a second.
#   RESIZE?   change the pane width, and see whether the bar follows.
#
# On a trip it sends SIGUSR1 (goroutine dump), SIGUSR2 (CPU profile), saves the
# pane, and stops -- because the first freeze is the interesting one and a
# harness that keeps hammering a corpse only overwrites the evidence.
#
# Usage:
#   nix develop -c bash scripts/freeze-hunt.sh            # 200 bursts
#   STEPS=1000 SEED=3 nix develop -c bash scripts/freeze-hunt.sh
set -uo pipefail

BIN=${BIN:-/tmp/freeze/figaro}
STEPS=${STEPS:-200}
SEED=${SEED:-1}
PORT=${PORT:-8961}
DIR=$(mktemp -d /tmp/freeze-hunt.XXXXXX)
SOCK=$DIR/tmux.sock
export FIGARO_STATE_DIR=$DIR/state FIGARO_RUNTIME_DIR=$DIR/run \
       FIGARO_CONFIG_DIR=$DIR/cfg FIGARO_CACHE_DIR=$DIR/cache
mkdir -p "$FIGARO_STATE_DIR" "$FIGARO_RUNTIME_DIR" "$FIGARO_CONFIG_DIR"/{outfits,providers} "$FIGARO_CACHE_DIR"
OUT=${OUT:-/tmp/freeze-hunt}; mkdir -p "$OUT"

cleanup() {
  tmux -S "$SOCK" kill-server 2>/dev/null
  "$BIN" stop >/dev/null 2>&1
  kill "${GW:-0}" 2>/dev/null
  rm -rf "$DIR"
}
trap cleanup EXIT

python3 "$(git rev-parse --show-toplevel)/scripts/fake-gateway-tools.py" $PORT "$DIR/req.jsonl" & GW=$!
sleep 1
cat > "$FIGARO_CONFIG_DIR/providers/gateway.toml" <<EOF
base_url = "http://127.0.0.1:$PORT/v1"
EOF
printf 'test agent\n' > "$FIGARO_CONFIG_DIR/credo.md"
cat > "$FIGARO_CONFIG_DIR/outfits/bar.toml" <<'EOF'
duke-title = "bar"
[system]
provider = "gateway"
model = "auto"
max_tokens = 64
credo = { fileName = "credo.md" }
EOF
printf 'default_outfit = "bar"\ninteractive = false\n' > "$FIGARO_CONFIG_DIR/config.toml"

ARIA=$("$BIN" new -j | python3 -c 'import json,sys;print(json.load(sys.stdin)["aria_id"])') || exit 1
# A conversation with something in it: history to page through, holes to want.
for i in 1 2 3 4 5 6; do "$BIN" send -f --id "$ARIA" -- "turn $i" >/dev/null 2>&1; done
sleep 6

tmux -S "$SOCK" new-session -d -s hunt -x 100 -y 30 bash --norc
tmux -S "$SOCK" set -g status off
tmux -S "$SOCK" send-keys -t hunt:0 "export FIGARO_STATE_DIR=$FIGARO_STATE_DIR FIGARO_RUNTIME_DIR=$FIGARO_RUNTIME_DIR FIGARO_CONFIG_DIR=$FIGARO_CONFIG_DIR FIGARO_CACHE_DIR=$FIGARO_CACHE_DIR FORCE_COLOR=1" Enter
tmux -S "$SOCK" send-keys -t hunt:0 "$BIN listen $ARIA" Enter
sleep 4

PID=$(pgrep -f "$BIN listen $ARIA" | head -1)
[ -n "$PID" ] || { echo "FAIL: no pager process"; exit 1; }
echo "== pager pid $PID, seed $SEED, $STEPS bursts"

pane() { tmux -S "$SOCK" capture-pane -p -t hunt:0; }
cpu()  { ps -o %cpu= -p "$PID" 2>/dev/null | tr -d ' '; }

trip() { # trip <what>
  echo "TRIPPED: $1"
  echo "   cpu=$(cpu)%"
  kill -USR1 "$PID" 2>/dev/null; sleep 1
  kill -USR2 "$PID" 2>/dev/null; sleep 11
  cp "$FIGARO_STATE_DIR"/freeze/* "$OUT"/ 2>/dev/null
  pane > "$OUT/pane-at-trip.txt"
  cp "$FIGARO_STATE_DIR"/telemetry/logs.jsonl "$OUT/" 2>/dev/null
  echo "   evidence in $OUT:"; ls -la "$OUT" | tail -5
  exit 1
}

# ALIVE: '?' opens help and must change the screen; Esc closes it again.
alive() {
  local before after
  before=$(pane | md5sum)
  tmux -S "$SOCK" send-keys -t hunt:0 '?'
  for _ in $(seq 10); do
    sleep 0.15
    after=$(pane | md5sum)
    [ "$after" != "$before" ] && { tmux -S "$SOCK" send-keys -t hunt:0 Escape; sleep 0.2; return 0; }
  done
  return 1
}

# RESIZE: the bar must follow the pane width.
resized() {
  local w=$1 row
  tmux -S "$SOCK" resize-window -t hunt -x "$w" -y 30 2>/dev/null
  for _ in $(seq 12); do
    sleep 0.15
    row=$(pane | grep -v '^$' | tail -1)
    (( ${#row} <= w )) && (( ${#row} > 0 )) && return 0
  done
  return 1
}

RANDOM=$SEED
KEYS=(j k u d G g Escape C-n C-p y m F T S Q '?' '!' / : n N)
hot=0
for step in $(seq 1 "$STEPS"); do
  case $((RANDOM % 6)) in
  0) # a resize storm: no settle between changes
     for _ in 1 2 3 4 5; do
       tmux -S "$SOCK" resize-window -t hunt -x $((40 + RANDOM % 120)) -y $((8 + RANDOM % 30)) 2>/dev/null
     done ;;
  1) # a key flood
     for _ in $(seq 1 12); do
       tmux -S "$SOCK" send-keys -t hunt:0 "${KEYS[$((RANDOM % ${#KEYS[@]}))]}" 2>/dev/null
     done ;;
  2) # a turn arriving under all of it
     "$BIN" send -f --id "$ARIA" -- "step $step" >/dev/null 2>&1 ;;
  3) # scroll into history: the paging path, which fetches
     for _ in $(seq 1 8); do tmux -S "$SOCK" send-keys -t hunt:0 u; done
     tmux -S "$SOCK" send-keys -t hunt:0 g; tmux -S "$SOCK" send-keys -t hunt:0 g ;;
  4) # the command box, opened and abandoned mid-word
     tmux -S "$SOCK" send-keys -t hunt:0 ':'
     tmux -S "$SOCK" send-keys -t hunt:0 -l "form sh"
     tmux -S "$SOCK" send-keys -t hunt:0 Escape ;;
  5) # a search that walks history
     tmux -S "$SOCK" send-keys -t hunt:0 /
     tmux -S "$SOCK" send-keys -t hunt:0 -l "turn"
     tmux -S "$SOCK" send-keys -t hunt:0 Enter ;;
  esac

  # Sustained CPU is the other freeze: sample twice, a second apart.
  c=$(cpu); c=${c%%.*}
  if [ -n "$c" ] && [ "$c" -ge 80 ]; then
    hot=$((hot + 1))
    [ "$hot" -ge 6 ] && trip "cpu pinned at ${c}% across $hot samples"
  else
    hot=0
  fi

  if (( step % 10 == 0 )); then
    alive   || trip "no repaint after '?' (step $step)"
    resized $((60 + RANDOM % 60)) || trip "the bar did not follow a resize (step $step)"
    printf '  %3d/%s alive, resizes, cpu %s%%\n' "$step" "$STEPS" "${c:-?}"
  fi
done

echo "no freeze in $STEPS bursts (seed $SEED)"
