#!/usr/bin/env bash
# NOTE: tmux is shared with other arias — private socket only (-L fig-2747),
# and never kill-server without -L.
# Decisive and tmux-independent: record what figaro WRITES (script -f) and ask
# whether the alt screen was ever entered before the exit sequence erases.
set -uo pipefail
FIG=${FIG:-/var/tmp/xs/figaro}; ARIA=${ARIA:-06c22c16}
T="tmux -L fig-2747"; S="xs-bytes-$$"
ENVV="FIGARO_RUNTIME_DIR=/var/tmp/xs/run FIGARO_STATE_DIR=/var/tmp/xs/state FIGARO_CACHE_DIR=/var/tmp/xs/cache FIGARO_NO_SELF_SPAWN=1"
ROWS=${ROWS:-3}; RAW=/var/tmp/xs/raw-$ROWS.bin
rm -f "$RAW"
$T kill-session -t $S 2>/dev/null
$T new-session -d -s $S -x 100 -y "$((ROWS+1))" -c /var/tmp/xs "env $ENVV bash --norc"
sleep 0.4
$T send-keys -t $S "script -q -f $RAW -c '$FIG listen $ARIA'" Enter
sleep 3
$T send-keys -t $S C-t   # ask for the pager
sleep 2
$T send-keys -t $S q     # leave it
sleep 2
$T kill-session -t $S 2>/dev/null
enter=$(grep -c $'\x1b\[?1049h' "$RAW" 2>/dev/null || true)
# count occurrences properly (grep -c counts LINES); use grep -o
enter=$(grep -ao $'\x1b\[?1049h' "$RAW" | wc -l)
leave=$(grep -ao $'\x1b\[?1049l' "$RAW" | wc -l)
erase=$(grep -ao $'\x1b\[2J'     "$RAW" | wc -l)
printf 'pane rows=%s   ?1049h(enter)=%s   ?1049l(leave)=%s   2J(erase)=%s\n' "$ROWS" "$enter" "$leave" "$erase"
if [ "$enter" -eq 0 ] && [ "$erase" -gt 0 ]; then
  echo "  >>> UNPAIRED ERASE: 2J written to the PRIMARY screen; the alt screen was never entered."
fi
