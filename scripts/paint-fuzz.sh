#!/usr/bin/env bash
# Randomized resize/gesture fuzz over tmux against a REAL binary in a REAL pty.
#
# Oracles, checked after every step:
#   1. SMEAR  — status-line text ("· ctx"/"ctx ~") may appear on at most ONE
#               row (the footer). More than one is the bug this branch fixes.
#   2. WIDTH  — no row may exceed the pane width (clipToWidth/ellipsis invariant).
#   3. ALIVE  — the pager chrome must still be there; a fuzz that quietly kills
#               the process would otherwise report CLEAN forever (trap 12).
#
# Deliberately mixes width-only, height-only and both-axis changes, extremes
# (tiny panes below the pager's own chrome), and rapid bursts with no settle.
set -uo pipefail
FIG=${FIG:?set FIG=/abs/path/to/figaro}
ARIA=${ARIA:?set ARIA=<scratch aria id>}
STEPS=${STEPS:-60}
SEED=${SEED:-7}
SOCK=/var/tmp/rb/tmux.sock; T="tmux -S $SOCK"; SESS=fuzz3
OUT=${OUT:-/var/tmp/rb/fuzz3}; mkdir -p -m 700 "$OUT"; rm -f "$OUT"/*.txt
ENVV="FIGARO_RUNTIME_DIR=/var/tmp/rb/run FIGARO_STATE_DIR=/var/tmp/rb/state FIGARO_CACHE_DIR=/var/tmp/rb/cache"

INJECT=${INJECT:-0}
RANDOM=$SEED
$T kill-session -t $SESS 2>/dev/null
$T new-session -d -s $SESS -x 100 -y 31 -c /var/tmp/rb "env $ENVV bash --norc"
sleep 0.4
case "${MODE:-listen}" in
  listen) # zero tokens, pager up indefinitely
    $T send-keys -t $SESS "$FIG listen $ARIA" Enter; sleep 3; $T send-keys -t $SESS C-t ;;
  live)   # a real streaming turn, so the spinner animates while we resize
    $T send-keys -t $SESS "$FIG send --id $ARIA -l -- 'Print a numbered list from 1 to 500, one per line, each with a different English word.'" Enter ;;
esac
for i in $(seq 1 60); do sleep 0.5; $T capture-pane -pt $SESS | grep -qE 'ctx ~?[0-9]' && break; done
echo "pager up (poll $i); fuzzing $STEPS steps, seed $SEED"

fail=0; checked=0
# NO C-d: if the pager ever exits, C-d reaches the shell as EOF and kills
# the session — which is how the first run ended, and it was the harness, not
# figaro. Every other motion is safe to land in bash.
gestures=(k j C-u g G C-o Home End)
for step in $(seq 1 "$STEPS"); do
  case $((RANDOM % 4)) in
    0) w=$((40 + RANDOM % 120)); h=$(( $($T display -t $SESS -p '#{pane_height}') ));;   # width only
    1) w=$( $T display -t $SESS -p '#{pane_width}' ); h=$((6 + RANDOM % 50));;            # height only
    2) w=$((40 + RANDOM % 120)); h=$((6 + RANDOM % 50));;                                 # both
    3) w=$((20 + RANDOM % 15)); h=$((3 + RANDOM % 6));;                                   # extremes
  esac
  $T resize-window -t $SESS -x "$w" -y "$((h+1))" 2>/dev/null
  # Every fourth step, a write that BYPASSES THE FRAME BUFFER — the shape of
  # stream.go's error/interrupt prints, injected from outside so the fuzz
  # discriminates the arms instead of merely guarding an invariant. Without
  # the barrier this is what smears a frozen status row across the transcript.
  if (( INJECT )) && (( step % 4 == 0 )); then
    printf '\nSTRAY-WRITE' > "$($T display -t $SESS -p '#{pane_tty}')" 2>/dev/null
  fi
  # half the steps also fire a gesture, some with no settle at all
  if (( RANDOM % 2 )); then
    $T send-keys -t $SESS "${gestures[$((RANDOM % ${#gestures[@]}))]}" 2>/dev/null
  fi
  sleep 0.$((1 + RANDOM % 4))

  pw=$($T display -t $SESS -p '#{pane_width}')
  ph=$($T display -t $SESS -p '#{pane_height}')
  cap="$OUT/step-$(printf %03d "$step").txt"
  $T capture-pane -pt $SESS > "$cap"
  # A pane below the pager's own chrome (renderFrame bails under 4 rows, the
  # auto-pager floors at 10) cannot be measured: what is on screen is tmux's
  # truncation of an older frame, not a frame figaro chose. Drive it anyway —
  # the point is that the NEXT workable size must be clean — but do not score
  # it, and never call it a failure. Scoring the unmeasurable is how a fuzz
  # invents both false alarms and false calm.
  if [ "${ph:-0}" -lt 10 ]; then continue; fi
  checked=$((checked+1))

  n=$(grep -cE 'ctx ~?[0-9]+(\.[0-9]+)?[km]?/' "$cap")
  if [ "$n" -gt 1 ]; then
    echo "  step $step (${pw}x${h}): SMEAR — $n status rows -> $cap"; fail=$((fail+1)); continue
  fi
  wide=$(awk -v w="$pw" '{ n=0; for(i=1;i<=length($0);i++){ c=substr($0,i,1); n++ } if (n>w) print NR": "n }' "$cap" | head -3)
  if [ -n "$wide" ]; then
    echo "  step $step (${pw}x${h}): OVERWIDE rows (pane $pw): $wide -> $cap"; fail=$((fail+1)); continue
  fi
  if ! pgrep -f "$FIG" >/dev/null; then
    echo "  step $step: FIGARO IS GONE (process exited) -> $cap"; fail=$((fail+1)); break
  fi
  if ! grep -qE 'aria [0-9a-f]{8}|ctx |help' "$cap"; then
    # The pager is gone. In live mode that is usually just the turn ending, and
    # measuring a dead pager is how a fuzz reports CLEAN forever (trap 12):
    # STOP rather than keep "checking" nothing.
    echo "  step $step: pager gone (turn over?) — stopping after $checked checked steps"
    checked=$((checked-1)); break
  fi
done

echo "--- fuzz: $fail failures over $checked checked steps (seed $SEED)"
$T capture-pane -pt $SESS > "$OUT/final.txt"
$T send-keys -t $SESS C-c 2>/dev/null; sleep 1
$T kill-session -t $SESS 2>/dev/null
exit $((fail > 0))
