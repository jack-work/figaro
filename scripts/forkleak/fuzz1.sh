#!/usr/bin/env bash
# One fuzz trial of the fork-hop leak. Usage: fuzz1.sh <seed>
#
# Knobs derived from the seed (printed, so a failure names its own repro):
#   nparent   1..3   turns the parent takes before the fork
#   at        head | :1 | :2        where the fork cuts
#   post      0|1    the parent takes another turn while the reader is away
#   settle    0|1    wait for the branch to go idle before hopping back
#   back      0|1|2  ^O | :listen <parent> | :attend <parent>
#   shelf     0|1    hop through 3 other arias first (evict the parked parent)
#   churn     0|1    fork something else while away (moves the lineage epoch)
#
# Asserts: the branch's inquiry token must not appear in the PARENT's view.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SEED=${1:-1}
BOXN=$ROOT/f$SEED
PORT=$((9000 + SEED))
W=${W:-100} H=${H:-58}
SOCK=$BOXN/tmux.sock
CHILD="CHILDQ$SEED"

r() { python3 -c "import random;random.seed($SEED);print(' '.join(str(random.randrange(n)) for n in [3,3,2,2,3,2,2]))"; }
read -r K_NP K_AT K_POST K_SETTLE K_BACK K_SHELF K_CHURN <<<"$(r)"
NPARENT=$((K_NP + 1))
case $K_AT in 0) AT="head";; 1) AT=":1";; 2) AT=":2";; esac
[[ "$AT" == ":2" && $NPARENT -lt 2 ]] && AT=":1"
echo "seed=$SEED nparent=$NPARENT at=$AT post=$K_POST settle=$K_SETTLE back=$K_BACK shelf=$K_SHELF churn=$K_CHURN"

box_up "$BOXN"
cat > "$BOX/cfg/config.toml" <<'EOF'
default_outfit = "probe"
interactive = true
EOF
gw_up || exit 3

tmx() { tmux -S "$SOCK" "$@" >/dev/null 2>&1; }
tmxo() { tmux -S "$SOCK" "$@" 2>/dev/null; }
cap() { tmxo capture-pane -p -S - -t fk:0; }
vis() { tmxo capture-pane -p -t fk:0; }
cleanup() { tmx kill-server; "$BIN" stop >/dev/null 2>&1; gw_down; }
trap cleanup EXIT

wait_stable() {
  local secs=${1:-15} last="" stable=0 cur i
  for i in $(seq $((secs * 2))); do
    sleep 0.5; cur=$(vis)
    if [[ "$cur" == "$last" ]]; then stable=$((stable+1)); (( stable >= 3 )) && return 0
    else stable=0; fi
    last=$cur
  done
  return 1
}
wait_has() {
  local pat=$1 secs=${2:-30} i
  for i in $(seq $((secs*2))); do cap | grep -q "$pat" && return 0; sleep 0.5; done
  return 1
}
box() { # run a figaro command against this box from outside the pane
  "$BIN" "$@"
}
cmdline() { # type a pager command line
  tmx send-keys -t fk:0 -l ":"; sleep 0.3
  tmx send-keys -t fk:0 -l "$1"; sleep 0.3
  tmx send-keys -t fk:0 Enter
}

P=$(aria_new) || exit 3
for i in $(seq "$NPARENT"); do
  box send --id "$P" -f -- "PARENTQ$i answer briefly" >/dev/null 2>&1
  wait_idle "$P" || { echo "SETUP-FAIL parent turn $i"; exit 3; }
done

# decoys for shelf pressure
DECOYS=()
if (( K_SHELF )); then
  for i in 1 2 3; do
    D=$(aria_new) || exit 3
    box send --id "$D" -f -- "DECOYQ$i answer briefly" >/dev/null 2>&1
    wait_idle "$D" || true
    DECOYS+=("$D")
  done
fi

tmx new-session -d -s fk -x "$W" -y "$((H+1))" bash --norc
tmx set -g status off
tmx send-keys -t fk:0 -l "export FIGARO_CONFIG_DIR=$BOX/cfg FIGARO_STATE_DIR=$BOX/state FIGARO_RUNTIME_DIR=$BOX/rt FIGARO_CACHE_DIR=$BOX/cache FIGARO_NO_BIND=1"
tmx send-keys -t fk:0 Enter
tmx send-keys -t fk:0 -l "$BIN listen $P"
tmx send-keys -t fk:0 Enter
wait_has "PARENTQ$NPARENT" 25 || { echo "SETUP-FAIL pager cold"; cap|tail -10; exit 3; }
wait_stable 10

# the fork, from inside the pager, with a prompt
if [[ "$AT" == "head" ]]; then cmdline "fork -- $CHILD branch question"
else cmdline "fork $P$AT -- $CHILD branch question"; fi
wait_has "$CHILD" 30 || { echo "SETUP-FAIL fork never showed"; cap|tail -15; exit 3; }
B=$(box list -g -a -j 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin)
rows=d if isinstance(d,list) else d.get("arias") or d.get("rows") or []
print("\n".join(r.get("id","") for r in rows))' 2>/dev/null | grep -v "^$P$" | grep -v "^$" | head -1)
(( K_SETTLE )) && { [[ -n "$B" ]] && wait_idle "$B" 30; wait_stable 12; } || sleep 1

# the parent moves on while the reader is away
if (( K_POST )); then
  box send --id "$P" -f -- "PARENTPOST answer briefly" >/dev/null 2>&1
  wait_idle "$P" 30 || true
fi
# topology churn: a fork elsewhere moves the lineage epoch
if (( K_CHURN )); then
  C=$(aria_new); box send --id "$C" -f -- "CHURNQ answer briefly" >/dev/null 2>&1
  wait_idle "$C" 20 || true
  box fork --id "$C" --stay -f >/dev/null 2>&1
fi
# shelf pressure: three other arias in between
if (( K_SHELF )); then
  for D in "${DECOYS[@]}"; do cmdline "listen $D"; wait_stable 8; done
fi

# back to the parent. ^O steps ONE entry, so with decoys in between it takes
# as many presses as hops away from the parent.
HOPS=1
(( K_SHELF )) && HOPS=$((1 + 3))
case $K_BACK in
  0) for _ in $(seq $HOPS); do tmx send-keys -t fk:0 C-o; sleep 1.5; done ;;
  1) cmdline "listen $P" ;;
  2) cmdline "attend $P" ;;
esac
wait_stable 15
vis > "$BOXN/vis-parent.txt"; cap > "$BOXN/cap-parent.txt"

# Is the pager actually showing the parent? A view of the branch is not a leak,
# it is a failed hop, and must not be reported as one.
showing=$(grep -oE "· [0-9a-f]{8} ·" "$BOXN/vis-parent.txt" | head -1 | tr -d '· ')
leak=$(grep -c "$CHILD" "$BOXN/vis-parent.txt")
echo "seed=$SEED parent=$P branch=${B:-?} showing=${showing:-?} leak=$leak"
if [[ -n "$showing" && "$showing" != "$P" ]]; then
  echo "seed=$SEED HOP-FAIL (showing ${showing:-?}, wanted $P)"
  exit 4
fi
if (( leak > 0 )); then
  echo "seed=$SEED LEAK"
  cat "$BOXN/vis-parent.txt"
  exit 1
fi
exit 0
