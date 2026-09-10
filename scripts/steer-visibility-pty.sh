#!/usr/bin/env bash
# HOW LONG DOES A STEER SIT DURABLE AND INVISIBLE? -- the pty arm.
#
#   scripts/steer-visibility-pty.sh /path/to/figaro
#
# Times two things a reader can see: the steer's row leaving the queue drawer,
# and its text arriving as a steering node in the body. Before the journal
# (8ee80d7c) the second trails the first by the provider's time-to-first-token;
# after it, both land in the same 200ms poll.
#
# The in-process instrument is TestSteerVisibilityGap, which injects a known
# TTFT and needs no tokens. This one exists because that test can only say what
# the daemon broadcast, not what stood on the screen.
#
# THREE TRAPS, each of which cost a run:
#
#   THE STEER IS NOT LIFTED UNTIL THE TOOL ROUND ENDS. A 30s poll against a 40s
#   sleep photographs a message still in the drawer and concludes nothing.
#
#   THE DRAWER ROW IS NOT THE INLINE TRAILER. In the pager the row wears a pit
#   glyph (♭/𝄚), not the "· 1." of the inline view, so a grep written for the
#   trailer reports the drawer empty while the message is plainly on screen.
#
#   INVOKE BY ABSOLUTE PATH. `tmux new-session -e PATH=` is silently ignored,
#   which A/Bs the installed binary against itself. The md5 is printed so the
#   two arms in a report can be told apart.
set -u
BIN="$1"
TOKEN="${TOKEN:-SENTINEL}"
DIR=$(mktemp -d /tmp/steerpty.XXXXXX)
mkdir -p "$DIR/state" "$DIR/run" "$DIR/config"
cp -r "$HOME/.config/figaro/." "$DIR/config" 2>/dev/null
chmod -R go-rwx "$DIR/config"
export FIGARO_STATE_DIR="$DIR/state" FIGARO_RUNTIME_DIR="$DIR/run" FIGARO_CONFIG_DIR="$DIR/config"
SOCK="$DIR/tmux.sock"
T() { tmux -S "$SOCK" "$@"; }
# Kill the daemon as well as the server: an isolated daemon left running is
# still a running daemon, and seventeen of them once ate 1.2 GB of tmpfs.
cleanup() { "$BIN" stop --force >/dev/null 2>&1; T kill-server >/dev/null 2>&1; }
trap cleanup EXIT

echo "arm: $BIN ($(md5sum "$BIN" | cut -c1-12))"
T new-session -d -s m -x 100 -y 61 bash --norc
T set -g status off
T send-keys -l "$BIN send -- 'use bash to run: sleep 40, then say DONE'"
T send-keys Enter
sleep 12
T capture-pane -p | grep -qE 'sleep 40' ||
	{ echo "DECLINE: no tool was in flight at t=12s; there is nothing to steer"; exit 1; }

# One character per send: a human types one byte per read, and this is input.
LINE=":send -- $TOKEN"
for ((i = 0; i < ${#LINE}; i++)); do
	T send-keys -l "${LINE:$i:1}"
	sleep 0.12
done
T send-keys Enter
START=$(date +%s%N)

CLEAR=
VIS=
for i in $(seq 1 400); do
	MS=$((($(date +%s%N) - START) / 1000000))
	CAP=$(T capture-pane -p)
	Q=$(printf '%s' "$CAP" | grep -cE "^[[:space:]]*[♭𝄚][^↳]*$TOKEN")
	B=$(printf '%s' "$CAP" | grep -cE '↳ input')
	[ -z "$CLEAR" ] && [ "$Q" = 0 ] && [ "$i" -gt 3 ] && CLEAR=$MS
	[ -z "$VIS" ] && [ "$B" != 0 ] && VIS=$MS
	printf '%6dms drawer=%s body=%s\n' "$MS" "$Q" "$B"
	[ -n "$CLEAR" ] && [ -n "$VIS" ] && break
	sleep 0.2
done

echo "RESULT arm=$BIN t_clear=${CLEAR:-none}ms t_visible=${VIS:-none}ms gap=$((${VIS:-0} - ${CLEAR:-0}))ms"
echo "--- final pane ---"
T capture-pane -p | grep -vE '^[[:space:]]*$'
