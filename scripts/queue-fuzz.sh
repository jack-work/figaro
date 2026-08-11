#!/usr/bin/env bash
# The four reported behaviours, driven end to end against a real provider.
#   A  four chained sends: one reply, then ONE combined inquiry
#   B  interrupt with three queued: combined AND ANSWERED, turn not abandoned
#   C  the pager: H stops the turn and stays; the messages render on separate lines
#   D  hup -d / X: the queue comes back rather than vanishing
set -uo pipefail

ROOT=/var/tmp/marcellina-fuzz
BIN=$ROOT/figaro
SOCK=figfuzz
SESS=fuzz

unset FIGARO_ARIA FIGARO_NO_BIND
export FIGARO_RUNTIME_DIR=$ROOT/run
export FIGARO_STATE_DIR=$ROOT/state

cleanup() {
  "$BIN" stop >/dev/null 2>&1 || true
  tmux -L "$SOCK" kill-server >/dev/null 2>&1 || true
}
trap cleanup EXIT

rm -rf "$ROOT"; mkdir -p "$ROOT/run" "$ROOT/state"
go build -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse HEAD)" \
  -o "$BIN" ./cmd/figaro || exit 1
echo "binary $(md5sum "$BIN" | cut -d' ' -f1)"

# turns <aria>: the IR as (turn, inquiry, node kinds, sealed).
turns() {
  "$BIN" show --id "$1" -j 2>/dev/null | python3 -c "
import json,sys
try: doc=json.load(sys.stdin)
except Exception: sys.exit(0)
for p in doc.get('parts',[]):
    kinds=[n.get('type') for n in (p.get('nodes') or [])]
    print(repr(p.get('inquiry','')), p.get('sealed'), kinds)
"
}

idle() {  # wait until the aria stops working
  for _ in $(seq 1 90); do
    st=$("$BIN" list -j | python3 -c "
import json,sys
for a in json.load(sys.stdin):
    if a.get('id')=='$1': print(a.get('state'))
" 2>/dev/null)
    [ "$st" = "idle" ] && return 0
    sleep 1
  done
  return 1
}

echo
echo "=============== A. four chained sends against an idle aria ==============="
A=$("$BIN" new -j -- 'test1' | sed -E 's/.*"aria_id":"([^"]+)".*/\1/')
echo "aria $A"
"$BIN" send -f --id "$A" -- 'test2' >/dev/null
"$BIN" send -f --id "$A" -- 'test3' >/dev/null
"$BIN" send -f --id "$A" -- 'test4' >/dev/null
idle "$A" || echo "  (never went idle)"
turns "$A"
echo "-- expected: turn 1 inquiry 'test1'; turn 2 inquiry holding test2/3/4 separated by blank lines"

echo
echo "=============== B. interrupt with three queued ==============="
B=$("$BIN" new -j -- 'Using the bash tool, run these one at a time, waiting for each: `sleep 12`, then `sleep 12`, then `sleep 12`. Then say DONE.' \
  | sed -E 's/.*"aria_id":"([^"]+)".*/\1/')
echo "aria $B"
for i in $(seq 1 90); do
  sleep 1
  "$BIN" show --id "$B" -j 2>/dev/null | python3 -c "
import json,sys
doc=json.load(sys.stdin)
for p in doc.get('parts',[]):
    for n in (p.get('nodes') or []):
        if n.get('type')=='tool' and n.get('status')=='running': sys.exit(0)
sys.exit(1)
" && break
done
echo "tool running: yes"
"$BIN" send -f --id "$B" -- 'stop that' >/dev/null
"$BIN" send -f --id "$B" -- 'and instead' >/dev/null
"$BIN" send -f --id "$B" -- 'say BANANA and nothing else' >/dev/null
echo "queued: $("$BIN" queue --id "$B" -j | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["queue"]))')"
echo "-- hup (keep) --"
"$BIN" hup "$B"
idle "$B" || echo "  (never went idle)"
turns "$B"
echo "-- expected: the interrupted turn sealed, then a NEW turn whose inquiry holds all three, ANSWERED (a prose node)"
"$BIN" show --id "$B" -n 1 2>/dev/null | tail -5

echo
echo "=============== D. hup -d returns the queue ==============="
D=$("$BIN" new -j -- 'Using the bash tool run `sleep 25`, then say DONE.' | sed -E 's/.*"aria_id":"([^"]+)".*/\1/')
sleep 6
"$BIN" send -f --id "$D" -- 'dropme one' >/dev/null
"$BIN" send -f --id "$D" -- 'dropme two' >/dev/null
echo "-- hup -d -j --"
"$BIN" hup -d -j "$D"
echo "-- queue after (expect empty) --"
"$BIN" queue --id "$D" -j
idle "$D" >/dev/null

echo
echo "=============== C. the pager: H mid-turn, and the rendering ==============="
C=$("$BIN" new -j -- 'Using the bash tool, run these one at a time, waiting for each: `sleep 12`, then `sleep 12`. Then say DONE.' \
  | sed -E 's/.*"aria_id":"([^"]+)".*/\1/')
tmux -L "$SOCK" new-session -d -s "$SESS" -x 100 -y 41
tmux -L "$SOCK" send-keys -t "$SESS" \
  "FIGARO_RUNTIME_DIR=$FIGARO_RUNTIME_DIR FIGARO_STATE_DIR=$FIGARO_STATE_DIR $BIN listen $C" Enter
for i in $(seq 1 90); do
  sleep 1
  "$BIN" show --id "$C" -j 2>/dev/null | python3 -c "
import json,sys
doc=json.load(sys.stdin)
for p in doc.get('parts',[]):
    for n in (p.get('nodes') or []):
        if n.get('type')=='tool' and n.get('status')=='running': sys.exit(0)
sys.exit(1)
" && break
done
"$BIN" send -f --id "$C" -- 'first queued' >/dev/null
"$BIN" send -f --id "$C" -- 'second queued' >/dev/null
"$BIN" send -f --id "$C" -- 'third queued: say CHERRY and nothing else' >/dev/null
sleep 1
tmux -L "$SOCK" send-keys -t "$SESS" H
sleep 2
echo "--- right after H ---"
tmux -L "$SOCK" capture-pane -p -t "$SESS" | grep -vE '^\s*$' | tail -8
idle "$C" || echo "  (never went idle)"
sleep 2
echo "--- after the queued turn ran ---"
tmux -L "$SOCK" capture-pane -p -S - -t "$SESS" | grep -vE '^\s*$' | tail -14
echo "--- pane alive? ---"
tmux -L "$SOCK" list-panes -t "$SESS" -F 'cmd=#{pane_current_command} dead=#{pane_dead}'
echo "--- IR ---"
turns "$C"

echo
echo "=============== E. the pager: X drops the queue and prints it ==============="
E=$("$BIN" new -j -- 'Using the bash tool, run these one at a time, waiting for each: `sleep 12`, then `sleep 12`. Then say DONE.' \
  | sed -E 's/.*"aria_id":"([^"]+)".*/\1/')
tmux -L "$SOCK" new-session -d -s drop -x 100 -y 41
tmux -L "$SOCK" send-keys -t drop \
  "FIGARO_RUNTIME_DIR=$FIGARO_RUNTIME_DIR FIGARO_STATE_DIR=$FIGARO_STATE_DIR $BIN listen $E" Enter
for i in $(seq 1 90); do
  sleep 1
  "$BIN" show --id "$E" -j 2>/dev/null | python3 -c "
import json,sys
doc=json.load(sys.stdin)
for p in doc.get('parts',[]):
    for n in (p.get('nodes') or []):
        if n.get('type')=='tool' and n.get('status')=='running': sys.exit(0)
sys.exit(1)
" && break
done
"$BIN" send -f --id "$E" -- 'doomed one' >/dev/null
"$BIN" send -f --id "$E" -- 'doomed two' >/dev/null
sleep 1
tmux -L "$SOCK" send-keys -t drop X
sleep 3
echo "--- status + notice after X ---"
tmux -L "$SOCK" capture-pane -p -t drop | grep -vE '^\s*$' | tail -6
echo "--- pane alive? ---"
tmux -L "$SOCK" list-panes -t drop -F 'cmd=#{pane_current_command} dead=#{pane_dead}'
echo "--- queue after X (expect empty) ---"
"$BIN" queue --id "$E" -j
idle "$E" >/dev/null
echo "--- IR (expect NO new turn from the dropped messages) ---"
turns "$E"
