#!/usr/bin/env bash
# NOTE: tmux is shared with other arias — private socket only (-L fig-2747),
# and never kill-server without -L.
# The master's three-part oracle, repeated across geometry x entry x exit.
#
#   (a) SEEDED  — pre-existing lines still in scrollback after figaro exits
#   (b) CONTENT — the aria's last chunk reached scrollback
#   (c) PROMPT  — the shell prompt is on a fresh line BELOW the content
#
# "Occasionally" is the hard part, so every cell runs N times and the failures
# are counted, not the passes.
#
# tmux is SHARED: private socket only, and never kill-server without -L.
set -uo pipefail
FIG=${FIG:-/var/tmp/xs/figaro-fixed}
ARIA=${ARIA:-06c22c16}
T="tmux -L fig-2747"
OUT=${OUT:-/var/tmp/xs/matrix}; mkdir -p -m 700 "$OUT"; rm -f "$OUT"/*.txt
N=${N:-3}
ENVV="FIGARO_RUNTIME_DIR=/var/tmp/xs/run FIGARO_STATE_DIR=/var/tmp/xs/state FIGARO_CACHE_DIR=/var/tmp/xs/cache FIGARO_NO_SELF_SPAWN=1"

pass=0; fail=0
one() { # cols rows mode exit run
  local cols=$1 rows=$2 mode=$3 xp=$4 run=$5
  local S="m-$$-$run" tag="${cols}x${rows}-${mode}-${xp}-r${run}"
  $T kill-session -t "$S" 2>/dev/null
  $T new-session -d -s "$S" -x "$cols" -y "$((rows+1))" -c /var/tmp/xs "env $ENVV bash --norc"
  sleep 0.4
  # Seed: enough lines to fill the pane and spill into scrollback, each unique.
  $T send-keys -t "$S" "for i in \$(seq 1 60); do printf 'SEED-%03d\n' \$i; done" Enter
  sleep 0.8
  local before; before=$($T capture-pane -pt "$S" -S - | grep -c '^SEED-')

  case "$mode" in
    pager) $T send-keys -t "$S" "$FIG listen $ARIA" Enter; sleep 3; $T send-keys -t "$S" C-t; sleep 2 ;;
    plain) $T send-keys -t "$S" "$FIG listen $ARIA" Enter; sleep 3 ;;
    fast)  # open and close inside one frame interval: the deferred-frame case
           $T send-keys -t "$S" "$FIG listen $ARIA" Enter; sleep 3; $T send-keys -t "$S" C-t ;;
  esac
  case "$xp" in
    q)   $T send-keys -t "$S" q ;;
    C-d) $T send-keys -t "$S" C-d ;;
    C-c) $T send-keys -t "$S" C-c ;;
  esac
  sleep 2.5

  local full after content prompt bad=""
  full="$OUT/$tag.txt"
  $T capture-pane -pt "$S" -S - > "$full"
  after=$($T capture-pane -pt "$S" -S - | grep -c '^SEED-')
  content=$(grep -cE '^\s*(│|✓|>|<) |aria [0-9a-f]{8}' "$full")
  # the prompt must be the LAST non-empty line, i.e. nothing painted over it
  prompt=$(grep -n 'bash-5' "$full" | tail -1 | cut -d: -f1)
  local last; last=$(grep -n . "$full" | tail -1 | cut -d: -f1)

  [ "$after" -lt "$before" ] && bad="$bad SEEDS($before->$after)"
  [ "${content:-0}" -eq 0 ] && bad="$bad NO-CONTENT"
  [ "${prompt:-0}" -ne "${last:-0}" ] && bad="$bad PROMPT-NOT-LAST(p=$prompt last=$last)"

  if [ -n "$bad" ]; then
    printf '  FAIL %-34s%s\n' "$tag" "$bad"; fail=$((fail+1))
  else
    pass=$((pass+1))
  fi
  $T kill-session -t "$S" 2>/dev/null
}

echo "binary: $FIG  ($(md5sum "$FIG" | cut -c1-12))   runs per cell: $N"
for geom in "100 30" "100 12" "100 5" "60 40" "200 24"; do
  set -- $geom
  for mode in pager plain fast; do
    for xp in q C-d C-c; do
      for r in $(seq 1 "$N"); do one "$1" "$2" "$mode" "$xp" "$r"; done
    done
  done
done
echo "--- $pass passed, $fail failed"
