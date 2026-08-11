#!/usr/bin/env bash
# H-key proof: hang up from the pager and STAY. Real binary, real pty, real
# provider, private tmux socket, isolated runtime+state (never the live daemon).
set -uo pipefail

ROOT=/var/tmp/marcellina
BIN=$ROOT/figaro
SOCK=figmar                      # PRIVATE tmux server; never the default one
SESS=hup

# This script may be run from inside an aria's own shell, where FIGARO_ARIA
# names THAT aria and FIGARO_NO_BIND forbids binding. Both must go: an
# unqualified verb would otherwise address the live aria running the script.
unset FIGARO_ARIA FIGARO_NO_BIND

export FIGARO_RUNTIME_DIR=$ROOT/run
export FIGARO_STATE_DIR=$ROOT/state
# config + hush are INHERITED on purpose: a real provider is the point.

cleanup() {
  "$BIN" stop >/dev/null 2>&1 || true          # trap 10: kill the daemon, not just the pane
  tmux -L "$SOCK" kill-server >/dev/null 2>&1 || true
}
trap cleanup EXIT

rm -rf "$ROOT"; mkdir -p "$ROOT/run" "$ROOT/state"

# Stamped: a plain build in a WORKTREE records no revision at all (trap: the
# CLI/daemon build handshake then has nothing to compare).
go build -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse HEAD)" \
  -o "$BIN" ./cmd/figaro || exit 1
echo "binary: $(md5sum "$BIN" | cut -d' ' -f1)  $("$BIN" version | head -1)"

# A turn long enough to interrupt in the middle of.
# A TOOL-BOUND turn: item 4 says the hangup may have to pause for a tool in
# flight, so the proof has to have one in flight. A model that answers from
# memory finishes before the keystroke and proves nothing (it did, once).
ARIA=$("$BIN" new -j -- 'Using the bash tool, run these one at a time, waiting for each to finish: `sleep 12`, then `sleep 12`, then `sleep 12`. Then say DONE.' | tee /dev/stderr | sed -E 's/.*"aria_id":"([^"]+)".*/\1/')
[ -n "$ARIA" ] || { echo "no aria"; exit 1; }
echo "aria: $ARIA"

# Ask for h+1 rows (trap 1: -y N gives pane height N-1), then read it back.
tmux -L "$SOCK" new-session -d -s "$SESS" -x 100 -y 41
echo "pane height: $(tmux -L "$SOCK" display -t "$SESS" -p '#{pane_height}')"
# Absolute path (trap 11: -e PATH= is silently ignored).
tmux -L "$SOCK" send-keys -t "$SESS" \
  "FIGARO_RUNTIME_DIR=$FIGARO_RUNTIME_DIR FIGARO_STATE_DIR=$FIGARO_STATE_DIR $BIN listen $ARIA" Enter

# Poll until a TOOL IS ACTUALLY RUNNING.
#
# The trigger oracle is the IR, NOT the screen: the footer carries the mantra,
# the mantra is the prompt, and the prompt contains the word "bash": so a
# screen grep reported a tool in flight while the model was still thinking, and
# the run proved nothing. (tmux-testing trap 2: only a rendered body line is
# sound, and this is not one.) The screen stays the instrument for the CLAIM
# (what the status row paints); the IR is the instrument for the TRIGGER.
RUNNING=no
for i in $(seq 1 90); do
  sleep 1
  if "$BIN" show --id "$ARIA" -j 2>/dev/null | python3 -c "
import json,sys
try: doc=json.load(sys.stdin)
except Exception: sys.exit(1)
parts=doc.get('parts',[]) if isinstance(doc,dict) else doc
for p in parts:
    for n in (p.get('nodes') or []):
        if n.get('type')=='tool' and n.get('status')=='running': sys.exit(0)
sys.exit(1)
"; then RUNNING=yes; break; fi
done
echo "tool RUNNING in the IR when the key was pressed: $RUNNING"
"$BIN" list -j | python3 -c "
import json,sys
for a in json.load(sys.stdin):
    if a.get('id')=='$ARIA': print('aria state at keypress:', a.get('state'))
"

tmux -L "$SOCK" capture-pane -p -t "$SESS" > "$ROOT/before.txt"
echo "--- BEFORE (tail) ---"; tail -6 "$ROOT/before.txt"

# THE GESTURE.
tmux -L "$SOCK" send-keys -t "$SESS" H
sleep 3
tmux -L "$SOCK" capture-pane -p -t "$SESS" > "$ROOT/after.txt"
echo "--- AFTER (tail) ---"; tail -6 "$ROOT/after.txt"

echo "--- pane still running the pager? ---"
tmux -L "$SOCK" list-panes -t "$SESS" -F 'cmd=#{pane_current_command} dead=#{pane_dead}'

echo "--- aria state ---"
"$BIN" list -j | python3 -c "
import json,sys
for a in json.load(sys.stdin):
    if a.get('id')=='$ARIA': print(a.get('state'), a.get('message_count'))
"
echo "--- did the turn keep growing after the hangup? ---"
sleep 4
tmux -L "$SOCK" capture-pane -p -t "$SESS" > "$ROOT/after2.txt"
if diff -q "$ROOT/after.txt" "$ROOT/after2.txt" >/dev/null; then echo "STABLE (turn stopped)"; else echo "STILL MOVING"; fi
cp "$ROOT/after.txt" "$ROOT/final.txt"

# THE LEGALITY CLAIM, against a REAL provider. A turn truncated mid-flight
# leaves a partial assistant message plus synthetic tool_results in the IR; if
# that state were illegal the next request would come back 400 and the reply
# would never arrive. So: ask for something unmistakable and watch for it.
echo "--- can the aria receive a new message at that exact point? ---"
"$BIN" send -f --id "$ARIA" -- 'Reply with exactly: PLUMBAGO' >/dev/null 2>&1
for i in $(seq 1 45); do
  sleep 1
  tmux -L "$SOCK" capture-pane -p -S - -t "$SESS" > "$ROOT/after3.txt"
  if grep -qE '^[[:space:]]*PLUMBAGO[[:space:]]*$' "$ROOT/after3.txt"; then break; fi
done
if grep -qE '^[[:space:]]*PLUMBAGO[[:space:]]*$' "$ROOT/after3.txt"; then
  echo "ACCEPTED: the next turn ran and answered on the same aria"
else
  echo "REFUSED or LOST: no answer to the post-hangup prompt"
  tail -12 "$ROOT/after3.txt"
fi
echo "--- final status row ---"
tmux -L "$SOCK" capture-pane -p -t "$SESS" | grep -E 'ctx ~' | tail -1
echo "--- IR legality: every tool_use answered, tail is not a dangling assistant ---"
"$BIN" show --id "$ARIA" -j > "$ROOT/ir.json" 2>/dev/null && python3 - "$ROOT/ir.json" <<'PYEOF'
import json,sys
doc=json.load(open(sys.argv[1]))
parts=doc.get("parts",doc if isinstance(doc,list) else [])
print("turns:",len(parts))
for p in parts:
    nodes=p.get("nodes") or []
    kinds=[n.get("type") for n in nodes]
    print(" turn",p.get("turn"),"sealed",p.get("sealed"),kinds[:6])
PYEOF
