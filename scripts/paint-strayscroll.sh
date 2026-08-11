#!/usr/bin/env bash
# PROVE the mechanism: while the pager is up with a live turn, a single write
# that bypasses the frame buffer lands at the cursor (parked on the status row)
# and SCROLLS the alt grid. The painter's base (t.prev) still describes the
# unscrolled screen, so it never repaints the shifted rows, and each later
# spinner tick writes a fresh status TAIL one row below the stale one.
set -uo pipefail
FIG=${FIG:?set FIG=/abs/path/to/figaro}
ID=${ARIA:?set ARIA=<aria id> (a scratch aria, never a real one)}
SOCK=/var/tmp/rb/tmux.sock; T="tmux -S $SOCK"; SESS=inj
OUT=/var/tmp/rb/inject; mkdir -p -m 700 "$OUT"; rm -f "$OUT"/*.txt
ENVV="FIGARO_RUNTIME_DIR=/var/tmp/rb/run FIGARO_STATE_DIR=/var/tmp/rb/state FIGARO_CACHE_DIR=/var/tmp/rb/cache"

$T kill-session -t $SESS 2>/dev/null
$T new-session -d -s $SESS -x 100 -y 31 -c /var/tmp/rb "env $ENVV bash --norc"
sleep 0.5
TTY=$($T display -t $SESS -p '#{pane_tty}')
echo "pane tty: $TTY"
$T send-keys -t $SESS "$FIG send --id $ID -l -- 'Print a numbered list from 1 to 400, one per line, each with a different English word.'" Enter

# wait for a live, animating pager
for i in $(seq 1 60); do sleep 0.5; $T capture-pane -pt $SESS | grep -qE '[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]' && break; done
echo "spinner live after $i polls"
$T capture-pane -pt $SESS > "$OUT/00-before.txt"
echo "status rows before: $(grep -cE 'ctx ~?[0-9]' "$OUT/00-before.txt")"

# THE INJECTION: one newline + text, exactly the shape of stream.go's
# `fmt.Fprintln(os.Stderr, "\n"+reason)` while the pager is up.
printf '\nSTRAY-WRITE' > "$TTY"
sleep 3
$T capture-pane -pt $SESS > "$OUT/01-after-inject.txt"
echo "status rows after:  $(grep -cE 'ctx ~?[0-9]' "$OUT/01-after-inject.txt")"
sleep 4
$T capture-pane -pt $SESS > "$OUT/02-later.txt"
echo "status rows +4s:    $(grep -cE 'ctx ~?[0-9]' "$OUT/02-later.txt")"

# does a resize heal it, as the user reports?
$T resize-window -t $SESS -x 96 -y 31; sleep 2
$T capture-pane -pt $SESS > "$OUT/03-after-resize.txt"
echo "status rows after resize: $(grep -cE 'ctx ~?[0-9]' "$OUT/03-after-resize.txt")"
$T send-keys -t $SESS C-c; sleep 1
$T kill-session -t $SESS 2>/dev/null; $T kill-server 2>/dev/null
