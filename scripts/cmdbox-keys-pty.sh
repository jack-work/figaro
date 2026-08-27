#!/usr/bin/env bash
# A real pty, a real binary, a real daemon: the ':' box's readline keys.
#
# No provider tokens are spent: `figaro new` with no prompt mints an aria and
# takes no turn, and every key under test edits a LINE rather than sending it.
set -uo pipefail

BIN=${BIN:-/tmp/cmdkeys/figaro}
DIR=$(mktemp -d /tmp/cmdkeys-pty.XXXXXX)
SOCK=$DIR/tmux.sock
export FIGARO_STATE_DIR=$DIR/state FIGARO_RUNTIME_DIR=$DIR/run FIGARO_CONFIG_DIR=$DIR/config
mkdir -p "$FIGARO_STATE_DIR" "$FIGARO_RUNTIME_DIR" "$FIGARO_CONFIG_DIR"
cp -r "$HOME/.config/figaro/." "$FIGARO_CONFIG_DIR" 2>/dev/null
chmod 700 "$FIGARO_CONFIG_DIR"

cleanup() {
  tmux -S "$SOCK" kill-server 2>/dev/null
  "$BIN" stop >/dev/null 2>&1
  rm -rf "$DIR"
}
trap cleanup EXIT

ARIA=$("$BIN" new -j | head -1 | python3 -c 'import json,sys; print(json.load(sys.stdin)["aria_id"])') || {
  echo "could not mint an aria"; exit 1; }
echo "aria $ARIA in $DIR"

tmux -S "$SOCK" new-session -d -s box -x 100 -y 31 bash --norc
tmux -S "$SOCK" set -g status off
H=$(tmux -S "$SOCK" display -p -t box:0 '#{pane_height}')
echo "pane height $H"
tmux -S "$SOCK" send-keys -t box:0 "export FIGARO_STATE_DIR=$FIGARO_STATE_DIR FIGARO_RUNTIME_DIR=$FIGARO_RUNTIME_DIR FIGARO_CONFIG_DIR=$FIGARO_CONFIG_DIR" Enter
tmux -S "$SOCK" send-keys -t box:0 "$BIN listen $ARIA" Enter
sleep 3

# type() sends one byte per read, as a human does (trap 5).
type_slow() {
  local s=$1 i c
  for ((i = 0; i < ${#s}; i++)); do
    c=${s:i:1}
    tmux -S "$SOCK" send-keys -t box:0 -l -- "$c"
    sleep 0.03
  done
}
raw() { tmux -S "$SOCK" send-keys -t box:0 -H "$@"; sleep 0.15; }
line() { tmux -S "$SOCK" capture-pane -p -t box:0 | grep -m1 '^:' ; }

fail=0
# tmux strips trailing whitespace out of a captured row, so every expectation
# here is written without it. That is a property of the INSTRUMENT, not of the
# box: the cursor block after a trailing space is drawn, it just does not
# survive capture-pane.
expect() { # expect <what> <wanted line>
  local got; got=$(line)
  if [[ "$got" != "$2" ]]; then
    echo "FAIL $1: got [$got] want [$2]"; fail=1
  else
    echo "ok   $1: $got"
  fi
}

type_slow ":send --id abc12345 -- hello"
expect "typed" ":send --id abc12345 -- hello"

raw 1b 62                # M-b: back to the start of "hello"
raw 1b 64                # M-d: kill it forward
expect "M-b M-d" ":send --id abc12345 --"

raw 17                   # ^W: kill the shell word behind the cursor
expect "^W" ":send --id abc12345"

# ^Y NOW PASTES BOTH KILLS, joined in reading order: a run of kills accumulates
# into one ring entry, and the forward kill (M-d, "hello") went on the end while
# the backward one (^W, "-- ") went on the front. That is readline's rule, and
# the first draft of this script asserted the naive answer instead.
raw 19
expect "^Y" ":send --id abc12345 -- hello"

raw 01                   # ^A: to the start
raw 1b 66                # M-f: past "send"
raw 1b 75                # M-u: upcase the next word, "id"
expect "M-u" ":send --ID abc12345 -- hello"

raw 1f                   # ^_ : undo the case change
expect "^_" ":send --id abc12345 -- hello"

raw 05 04                # ^E then ^D at the end of the line: nothing to delete
expect "^D at eol" ":send --id abc12345 -- hello"

raw 05 1b 7f             # ^E then M-DEL: kill the readline word back
expect "M-DEL" ":send --id abc12345 --"

# ^S REACHES US AT ALL only because MakeRaw clears IXON (internal/term):
# with flow control on, the tty eats it and stops the output instead.
raw 13
got=$(tmux -S "$SOCK" capture-pane -p -t box:0 | grep -m1 'i-search')
if [[ "$got" != *"i-search"* ]]; then
  echo "FAIL ^S: no i-search prompt (flow control ate it?) got [$got]"; fail=1
else
  echo "ok   ^S: $got"
fi
raw 07                   # ^G out again

raw 12 12                # ^R twice with an empty history: the prompt says so
got=$(tmux -S "$SOCK" capture-pane -p -t box:0 | grep -m1 'i-search')
if [[ "$got" != *"reverse-i-search"* ]]; then
  echo "FAIL ^R: no i-search prompt (got [$got])"; fail=1
else
  echo "ok   ^R: $got"
fi
raw 07                   # ^G: abandon the search, keep the line
expect "^G out of a search" ":send --id abc12345 --"

raw 15                   # ^U: kill to start
expect "^U" ":"

raw 04                   # ^D on an empty line: closes the box
if tmux -S "$SOCK" capture-pane -p -t box:0 | grep -q '^:'; then
  echo "FAIL ^D-empty: the box is still open"; fail=1
else
  echo "ok   ^D-empty closed the box"
fi
if ! tmux -S "$SOCK" list-panes -t box:0 >/dev/null 2>&1; then
  echo "FAIL ^D-empty: the pane died (it detached instead of closing the box)"; fail=1
fi

# The escape hatch is one press further away, not gone: ^D again detaches.
raw 04
sleep 1
if tmux -S "$SOCK" capture-pane -p -t box:0 | grep -q '? help'; then
  echo "FAIL ^D-again: still in the pager"; fail=1
else
  echo "ok   ^D-again left the pager"
fi

# Re-open, and prove Esc/^[ and history.
tmux -S "$SOCK" send-keys -t box:0 "$BIN listen $ARIA" Enter
sleep 3
type_slow ":open deadbeef"
raw 1b                   # Esc closes the box
if tmux -S "$SOCK" capture-pane -p -t box:0 | grep -q '^:open'; then
  echo "FAIL Esc: the box is still open"; fail=1
else
  echo "ok   Esc closed the box"
fi
raw 3a                   # ':' again
raw 1b 5b 39 31 3b 35 75 # CSI-u Ctrl-[
if tmux -S "$SOCK" capture-pane -p -t box:0 | grep -q '^:'; then
  echo "FAIL ^[: the box is still open"; fail=1
else
  echo "ok   CSI-u ^[ closed the box"
fi

echo "--- final screen ---"
tmux -S "$SOCK" capture-pane -p -t box:0 | tail -6
exit $fail
