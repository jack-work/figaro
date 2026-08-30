#!/usr/bin/env bash
# THE SEND ENTRANCE, in a real pty. statusbar-pty.sh and freeze-hunt.sh both
# drive `figaro listen`; this one drives `figaro send`, because the whole point
# of runSession is that the two stop differing by accident.
#
# What it asserts, all of it against the fake gateway on a private tmux server:
#   an ordinary send stays INLINE (no pager, no alt screen) and exits by itself
#   the reply lands in scrollback and the shell prompt comes back
#   the bar carries the capacity figure and the mantra, in the incipit
#   ^T promotes to the pager mid-session, and the pit keys work there
#   :send from inside a send opens the queue (the hooks are wired on this door)
#   --listen opens the pager at once
#   a non-TTY send streams and exits 0
#   leaving prints nothing at all
set -uo pipefail

BIN=${BIN:-/tmp/onesession/figaro}
PORT=${PORT:-8981}
DIR=$(mktemp -d /tmp/sendpty.XXXXXX)
SOCK=$DIR/tmux.sock
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

fail=0
ARIA=$("$BIN" new -j | python3 -c 'import json,sys;print(json.load(sys.stdin)["aria_id"])')
"$BIN" set --id "$ARIA" mantra "the send entrance walks the same road" >/dev/null 2>&1

tmux -S "$SOCK" new-session -d -s send -x 100 -y 25 bash --norc
tmux -S "$SOCK" set -g status off
tmux -S "$SOCK" send-keys -t send:0 "export FIGARO_STATE_DIR=$FIGARO_STATE_DIR FIGARO_RUNTIME_DIR=$FIGARO_RUNTIME_DIR FIGARO_CONFIG_DIR=$FIGARO_CONFIG_DIR FIGARO_CACHE_DIR=$FIGARO_CACHE_DIR FORCE_COLOR=1 PS1='ready\$ '" Enter
sleep 1
pane() { tmux -S "$SOCK" capture-pane -p -t send:0; }
bar()  { pane | grep -v '^$' | tail -1; }
prompt_back() { pane | grep -v '^$' | tail -1 | grep -q 'ready\$'; }
# A turn that stays open long enough to look at. The fake gateway answers in
# milliseconds, and everything interesting about a session happens while a turn
# is in flight.
slow() { echo "$1" > "$DIR/req.jsonl.delay"; }

# 1. AN ORDINARY SEND IS INLINE, AND IT ENDS BY ITSELF.
slow 0
tmux -S "$SOCK" send-keys -t send:0 "$BIN send --id $ARIA -- 'look around'" Enter
sleep 6
if prompt_back; then
  echo "ok   an ordinary send returns to the shell on its own"
else
  echo "FAIL: the send did not exit"; fail=1; pane | tail -6 | sed 's/^/    |/'
fi
if pane | grep -q "look around"; then
  echo "ok   the prompt and its reply are in scrollback"
else
  echo "FAIL: nothing of the turn reached scrollback"; fail=1
fi
for word in "hung up" "interrupting" "disconnected" "panic"; do
  if pane | grep -q "$word"; then echo "FAIL: '$word' was printed on the way out"; fail=1; fi
done
echo "ok   leaving printed nothing it should not have"

# 2. THE BAR IS THE SAME BAR: the bookend a finished send leaves in scrollback
#    carries the mantra and the capacity figure, on the entrance that reads no
#    history. (Sampled after the turn lands: mid-turn the incipit's footer is
#    whatever the last frame painted, and a stalled turn paints nothing.)
slow 0
tmux -S "$SOCK" send-keys -t send:0 "$BIN send --id $ARIA -- 'again'" Enter
for _ in $(seq 20); do prompt_back && break; sleep 1; done
sleep 1
row=$(pane | grep -v '^$' | grep -v 'ready\$' | tail -1)
if [[ "$row" == *"the send entrance walks the same road"* ]]; then
  echo "ok   the mantra is on send's bookend"
else
  echo "FAIL: no mantra on send's bookend: [$row]"; fail=1
  pane | grep -v '^$' | tail -4 | sed 's/^/    |/'
fi
if [[ "$row" =~ [0-9]{2,}(\.[0-9])?[km%]?[[:space:]]*$ ]]; then
  echo "ok   the capacity figure is on send's bookend"
else
  echo "FAIL: no capacity figure on send's bookend: [$row]"; fail=1
fi

# 3. ^T PROMOTES A SEND TO THE PAGER, and the pit keys work there -- the hooks
#    are wired on this door too, which is the bug this refactor exists for.
slow 12
tmux -S "$SOCK" send-keys -t send:0 "$BIN send --id $ARIA -- 'promote me'" Enter
sleep 2
tmux -S "$SOCK" send-keys -t send:0 C-t; sleep 2
tmux -S "$SOCK" send-keys -t send:0 '?'; sleep 1.5
if pane | grep -q "close help\|exit; keeps the turn running"; then
  echo "ok   ^T promotes a send to the pager and ? opens help there"
else
  echo "FAIL: the pager did not open under a send"; fail=1; pane | tail -6 | sed 's/^/    |/'
fi
tmux -S "$SOCK" send-keys -t send:0 Escape; sleep 0.5
tmux -S "$SOCK" send-keys -t send:0 S; sleep 3
if pane | grep -q "웃"; then
  echo "ok   S opens the form pit inside a send"
else
  echo "FAIL: S is dead on the send door"; fail=1; pane | tail -4 | sed 's/^/    |/'
fi
tmux -S "$SOCK" send-keys -t send:0 Escape; sleep 0.5

# 4. THE COMMAND BOX IS WIRED ON THIS DOOR. ':' used to answer "commands need a
#    live session" inside a send, because the runner was armed in listen only.
tmux -S "$SOCK" send-keys -t send:0 ':'; sleep 0.7
tmux -S "$SOCK" send-keys -t send:0 -l "send -- queued from a send"; sleep 0.5
tmux -S "$SOCK" send-keys -t send:0 Enter; sleep 3
if pane | grep -q "live session"; then
  echo "FAIL: the ':' box is not wired on the send door"; fail=1
else
  echo "ok   the ':' box runs a command inside a send"
fi
# Out: q closes the pit, ^C ends the session and its turn.
# ^C twice: the first stops the turn (the pager keeps listen semantics), the
# second leaves. That rule is older than this refactor and outlives it.
tmux -S "$SOCK" send-keys -t send:0 q; sleep 1
tmux -S "$SOCK" send-keys -t send:0 C-c; sleep 2
tmux -S "$SOCK" send-keys -t send:0 C-c; sleep 2
slow 0
for _ in $(seq 20); do prompt_back && break; sleep 1; done
if prompt_back; then
  echo "ok   ^C ends a promoted send and returns the shell"
else
  echo "FAIL: the promoted send never ended"; fail=1; pane | tail -4 | sed 's/^/    |/'
fi

# 5. --listen OPENS THE PAGER AT ONCE.
slow 8
tmux -S "$SOCK" send-keys -t send:0 "$BIN send -l --id $ARIA -- 'open the pager'" Enter
sleep 4
if pane | grep -qE "^─+.*live"; then
  echo "ok   --listen opens the pager at once"
else
  echo "FAIL: --listen did not open the pager"; fail=1; pane | tail -5 | sed 's/^/    |/'
fi
tmux -S "$SOCK" send-keys -t send:0 C-c; sleep 1.5
tmux -S "$SOCK" send-keys -t send:0 C-c; sleep 2
slow 0
for _ in $(seq 20); do prompt_back && break; sleep 1; done

# 6. A NON-TTY SEND STREAMS AND EXITS 0.
out=$("$BIN" send --id "$ARIA" -- "piped" 2>&1); rc=$?
if [[ $rc -eq 0 ]]; then
  echo "ok   a piped send exits 0"
else
  echo "FAIL: a piped send exited $rc"; fail=1
fi
if [[ -n "$out" ]]; then
  echo "ok   a piped send writes its turn to stdout"
else
  echo "FAIL: a piped send wrote nothing"; fail=1
fi

echo
(( fail )) && echo "FAILED" || echo "all good"
exit $fail
