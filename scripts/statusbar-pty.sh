#!/usr/bin/env bash
# The status row's state token, in a real pty on a PRIVATE tmux server.
#
# The server is private (-S on its own socket) for the reason the smoke harness
# gives: a shared server leaks sessions between runs, and killing Gluck's tmux
# would be unforgivable.
#
# What it looks at: the succinct glyph is what the bar draws by default, and
# the ONLY place that is true is a terminal -- a unit test sees the string the
# renderer returned, not the row the terminal kept.
set -uo pipefail

BIN=${BIN:-/tmp/statusbar/figaro}
DIR=$(mktemp -d /tmp/statusbar-pty.XXXXXX)
SOCK=$DIR/tmux.sock
PORT=${PORT:-8921}
export FIGARO_STATE_DIR=$DIR/state FIGARO_RUNTIME_DIR=$DIR/run \
       FIGARO_CONFIG_DIR=$DIR/cfg FIGARO_CACHE_DIR=$DIR/cache
mkdir -p "$FIGARO_STATE_DIR" "$FIGARO_RUNTIME_DIR" "$FIGARO_CONFIG_DIR"/{outfits,providers} "$FIGARO_CACHE_DIR"

cleanup() {
  tmux -S "$SOCK" kill-server 2>/dev/null
  "$BIN" stop >/dev/null 2>&1
  kill "${GW:-0}" 2>/dev/null
  rm -rf "$DIR"
}
trap cleanup EXIT

if (echo >/dev/tcp/127.0.0.1/$PORT) 2>/dev/null; then
  echo "FAIL: something already listens on :$PORT"; exit 1
fi
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

# THE PAGER MUST BE WATCHING WHEN THE TURN RUNS. The first draft of this script
# prompted first and attached afterwards, then asserted a ✓ that was never
# going to be there: a session that never saw a turn reports `idle`, which is
# the catch-all doing exactly its job. The state is a property of what THIS
# CLI watched, not of the aria's history.
ARIA=$("$BIN" new -j | python3 -c 'import json,sys;print(json.load(sys.stdin)["aria_id"])')

# -y h+1: tmux subtracts the status row AT CREATION, and turning the bar off
# afterwards does not give it back. Then read back what we actually got.
tmux -S "$SOCK" new-session -d -s bar -x 100 -y 25 bash --norc
tmux -S "$SOCK" set -g status off
H=$(tmux -S "$SOCK" display -p -t bar:0 '#{pane_height}')
tmux -S "$SOCK" send-keys -t bar:0 "export FIGARO_STATE_DIR=$FIGARO_STATE_DIR FIGARO_RUNTIME_DIR=$FIGARO_RUNTIME_DIR FIGARO_CONFIG_DIR=$FIGARO_CONFIG_DIR FIGARO_CACHE_DIR=$FIGARO_CACHE_DIR FORCE_COLOR=1" Enter
tmux -S "$SOCK" send-keys -t bar:0 "$BIN listen $ARIA" Enter
sleep 2

# Now prompt it from OUTSIDE the pane, and watch the row move.
"$BIN" send -f --id "$ARIA" -- "look around" >/dev/null 2>&1
sleep 1
thinking=$(tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -v '^$' | tail -1)
echo "mid-turn row: [$thinking]"
if [[ "$thinking" =~ [⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏] ]]; then
  echo "ok   a spinner frame is on the row while the turn runs"
else
  echo "note: no spinner frame caught (the fake gateway answers fast)"
fi
for _ in $(seq 60); do
  [[ "$("$BIN" status "$ARIA" -j 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin).get("state",""))')" == idle ]] && break
  sleep 0.5
done
sleep 2

bar() { tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -v '^$' | tail -1; }

fail=0
row=$(bar)
echo "pane height $H"
echo "status row: [$row]"

# 1. The succinct glyph is on the row, and the old fused word is not.
if [[ "$row" != *"✓"* ]]; then
  echo "FAIL: no done glyph on the status row"; fail=1
else
  echo "ok   the done glyph ✓ is on the row"
fi
# THE NAMES ARE WHAT VERBOSE ADDS, so the default row must carry NONE of them:
# the old fused spellings, and equally the new ones. The first draft of this
# script forbade only the old words, so a canary that forced verbose on the
# default row sailed straight through it -- the assertion was testing that a
# rename had happened, not that succinct was succinct.
for word in completed interrupted disconnected done hup error detached idle; do
  if [[ "$row" == *"$word"* ]]; then
    echo "FAIL: the default row carries the state NAME '$word'; succinct is glyph-only"; fail=1
  fi
done
[[ $fail == 0 ]] && echo "ok   the default row carries no state name at all"

# 2. The panel is where words live: '!' opens it and it names the state.
tmux -S "$SOCK" send-keys -t bar:0 '!'
sleep 1
panel=$(tmux -S "$SOCK" capture-pane -p -t bar:0)
if [[ "$panel" == *"done ✓"* ]]; then
  echo "ok   the ! panel names the state: done ✓"
else
  echo "FAIL: the ! panel does not name the state"; fail=1
  echo "$panel" | tail -8
fi

# 3. It survives a resize, which is where a width-sensitive row goes wrong.
tmux -S "$SOCK" send-keys -t bar:0 Escape
tmux -S "$SOCK" resize-window -t bar -x 46 -y 25 2>/dev/null
sleep 1
narrow=$(bar)
echo "narrow row: [$narrow]"
if [[ "$narrow" == *"✓"* ]]; then
  echo "ok   the glyph survives a 46-column pane"
else
  echo "FAIL: the state fell off a narrow row"; fail=1
fi

tmux -S "$SOCK" send-keys -t bar:0 q
sleep 1
exit $fail
