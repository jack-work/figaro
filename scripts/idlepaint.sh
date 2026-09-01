#!/usr/bin/env bash
# WHAT THE PAGER WRITES WHILE NOTHING IS HAPPENING, in bytes.
#
# An idle pager should write nothing at all. Two things used to make it write
# anyway -- a queue poll that rendered on every answer including "no change",
# and a periodic resync that erased and rewrote every row to re-earn a screen
# it already had -- and neither is visible on a local terminal, because a
# synchronized update coalesces the frame and the eye never sees it.
#
# On a terminal that does not implement synchronized updates (WSL under a
# remote desktop, which is where this was reported) the same frame is an erase
# and a rewrite of every row, twice a second: a blink, on a pager nobody is
# touching. So the measurement is the test.
#
# script(1) gives the pager a pty and tees everything it writes to a file; the
# assertion is the growth of that file across a quiet interval.
set -uo pipefail

BIN=${BIN:-/tmp/statusbar/figaro}
IDLE=${IDLE:-12}
BUDGET=${BUDGET:-200}   # bytes across the whole idle window; 0 is the intent
DIR=$(mktemp -d /tmp/idlepaint.XXXXXX)
SOCK=$DIR/tmux.sock
PORT=${PORT:-8931}
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

printf 'base_url = "http://127.0.0.1:%s/v1"\n' "$PORT" > "$FIGARO_CONFIG_DIR/providers/gateway.toml"
printf 'test agent\n' > "$FIGARO_CONFIG_DIR/credo.md"
cat > "$FIGARO_CONFIG_DIR/outfits/bar.toml" <<'EOG'
duke-title = "bar"
[system]
provider = "gateway"
model = "auto"
max_tokens = 64
credo = { fileName = "credo.md" }
EOG
printf 'default_outfit = "bar"\ninteractive = false\n' > "$FIGARO_CONFIG_DIR/config.toml"

fail=0
ARIA=$("$BIN" new -j | python3 -c 'import json,sys;print(json.load(sys.stdin)["aria_id"])')
"$BIN" send -f --id "$ARIA" -- "look around" >/dev/null 2>&1
sleep 4

tmux -S "$SOCK" new-session -d -s idle -x 100 -y 30 bash --norc
tmux -S "$SOCK" set -g status off
tmux -S "$SOCK" send-keys -t idle:0 "export FIGARO_STATE_DIR=$FIGARO_STATE_DIR FIGARO_RUNTIME_DIR=$FIGARO_RUNTIME_DIR FIGARO_CONFIG_DIR=$FIGARO_CONFIG_DIR FIGARO_CACHE_DIR=$FIGARO_CACHE_DIR FORCE_COLOR=1" Enter
tmux -S "$SOCK" send-keys -t idle:0 "script -qfc '$BIN listen $ARIA' $DIR/typescript" Enter
sleep 5
tmux -S "$SOCK" send-keys -t idle:0 C-t   # the pager, explicitly
sleep 3

size() { stat -c%s "$DIR/typescript"; }
erases() { python3 -c "print(open('$DIR/typescript','rb').read().count(b'\x1b[2K'))"; }

before=$(size); ebefore=$(erases)
echo "== idling for ${IDLE}s with the pager up and nothing to say"
sleep "$IDLE"
after=$(size); eafter=$(erases)
wrote=$((after - before)); erased=$((eafter - ebefore))

echo "idle write: $wrote bytes over ${IDLE}s ($((wrote / IDLE)) B/s), $erased line erases"
if (( wrote <= BUDGET )); then
  echo "ok   an idle pager writes nothing worth seeing (<= $BUDGET bytes)"
else
  echo "FAIL: an idle pager wrote $wrote bytes in ${IDLE}s"; fail=1
fi
if (( erased == 0 )); then
  echo "ok   and erases no rows at all"
else
  echo "FAIL: an idle pager erased $erased rows -- that is the flicker"; fail=1
fi

# AND IT STILL PAINTS WHEN THERE IS SOMETHING TO PAINT: the same measurement
# with a turn in flight must be loud, or the test above is passing for the
# wrong reason (a pager that has died writes nothing either).
mid=$(size)
"$BIN" send -f --id "$ARIA" -- "say something" >/dev/null 2>&1
sleep 6
live=$(( $(size) - mid ))
echo "live write: $live bytes for one turn"
if (( live > 500 )); then
  echo "ok   a turn still paints"
else
  echo "FAIL: a turn painted only $live bytes -- is the pager alive?"; fail=1
fi

tmux -S "$SOCK" send-keys -t idle:0 q; sleep 1
(( fail )) && echo FAILED || echo "all good"
exit $fail
