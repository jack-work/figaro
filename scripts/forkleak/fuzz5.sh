#!/usr/bin/env bash
# Fuzz trial, fifth shape: TOOL-HEAVY TURNS. Every turn spans several logical
# times (prose, a tool call, its result, more prose), so a turn id and an LT are
# far apart -- the arithmetic a one-node fixture cannot tell apart.
#
# The reader forks from the pager, the branch answers, and the reader goes back.
# Usage: fuzz5.sh <seed>
#   nparent 1..3
#   at      head | :1 | :2 | :2.2   (a NODE coordinate, mid-turn)
#   bturns  1..2  turns the branch takes before the reader returns
#   back    0 ^O | 1 :listen | 2 :attend
#   shelf   0|1   three decoy arias in between (evicts the parked parent)
#   post    0|1   the parent takes a turn while the reader is away
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SEED=${1:-1}
BOXN=$ROOT/j$SEED
PORT=$((9800 + SEED))
W=${W:-100} H=${H:-58}
SOCK=$BOXN/tmux.sock
CHILD="CHILDQ$SEED"
export GW=$HERE/../fake-gateway-tools.py

read -r K_NP K_AT K_BT K_BACK K_SHELF K_POST <<<"$(python3 -c "import random;random.seed($SEED+123000);print(' '.join(str(random.randrange(n)) for n in [3,4,2,3,2,2]))")"
NPARENT=$((K_NP + 1)); BTURNS=$((K_BT + 1))
case $K_AT in 0) AT="head";; 1) AT=":1";; 2) AT=":2";; 3) AT=":2.2";; esac
[[ "$AT" == ":2" && $NPARENT -lt 2 ]] && AT=":1"
[[ "$AT" == ":2.2" && $NPARENT -lt 2 ]] && AT=":1.2"
echo "seed=$SEED nparent=$NPARENT at=$AT bturns=$BTURNS back=$K_BACK shelf=$K_SHELF post=$K_POST"

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
  for i in $(seq $((secs*2))); do
    sleep 0.5; cur=$(vis)
    if [[ "$cur" == "$last" ]]; then stable=$((stable+1)); (( stable>=3 )) && return 0
    else stable=0; fi
    last=$cur
  done
  return 1
}
wait_has() { local pat=$1 secs=${2:-40} i; for i in $(seq $((secs*2))); do cap | grep -q "$pat" && return 0; sleep 0.5; done; return 1; }
cmdline() { tmx send-keys -t fk:0 -l ":"; sleep 0.3; tmx send-keys -t fk:0 -l "$1"; sleep 0.3; tmx send-keys -t fk:0 Enter; }

P=$(aria_new) || exit 3
for i in $(seq "$NPARENT"); do
  "$BIN" send --id "$P" -f -- "PARENTQ$i look around" >/dev/null 2>&1
  wait_idle "$P" 90 || { echo "SETUP-FAIL parent turn $i"; exit 3; }
done
# LTs per turn: the whole point of this fixture. Report it, so a clean run says
# whether it even had the shape it exists to produce.
LTS=$("$BIN" show --id "$P" -a -j 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin)
n=[len(p.get("nodes",[])) for p in d.get("parts",[])]
print(sum(n), len(n))' 2>/dev/null)
echo "   parent nodes/turns: $LTS"

DECOYS=()
if (( K_SHELF )); then
  for i in 1 2 3; do
    D=$(aria_new) || exit 3
    "$BIN" send --id "$D" -f -- "DECOYQ$i look around" >/dev/null 2>&1
    wait_idle "$D" 90 || true
    DECOYS+=("$D")
  done
fi

tmx new-session -d -s fk -x "$W" -y "$((H+1))" bash --norc
tmx set -g status off
tmx send-keys -t fk:0 -l "export FIGARO_CONFIG_DIR=$BOX/cfg FIGARO_STATE_DIR=$BOX/state FIGARO_RUNTIME_DIR=$BOX/rt FIGARO_CACHE_DIR=$BOX/cache FIGARO_NO_BIND=1"
tmx send-keys -t fk:0 Enter
tmx send-keys -t fk:0 -l "$BIN listen $P"
tmx send-keys -t fk:0 Enter
wait_has "PARENTQ$NPARENT" 40 || { echo "SETUP-FAIL pager cold"; cap|tail -10; exit 3; }
wait_stable 10

if [[ "$AT" == "head" ]]; then cmdline "fork -- $CHILD look around"
else cmdline "fork $P$AT -- $CHILD look around"; fi
wait_has "$CHILD" 40 || { echo "SETUP-FAIL fork never showed"; cap|tail -15; exit 3; }
B=$(cap | grep -oE "prompting [0-9a-f]{8}" | tail -1 | awk '{print $2}')
[[ -n "$B" ]] && wait_idle "$B" 90
wait_stable 12
for i in $(seq 2 "$BTURNS"); do
  [[ -z "$B" ]] && break
  "$BIN" send --id "$B" -f -- "${CHILD}x$i look around" >/dev/null 2>&1
  wait_idle "$B" 90 || true
  wait_stable 10
done
if (( K_POST )); then
  "$BIN" send --id "$P" -f -- "PARENTPOSTQ look around" >/dev/null 2>&1
  wait_idle "$P" 90 || true
fi
if (( K_SHELF )); then
  for D in "${DECOYS[@]}"; do cmdline "attend $D"; wait_stable 8; done
fi

HOPS=1; (( K_SHELF )) && HOPS=4
case $K_BACK in
  0) for _ in $(seq $HOPS); do tmx send-keys -t fk:0 C-o; sleep 1.5; done ;;
  1) cmdline "listen $P" ;;
  2) cmdline "attend $P" ;;
esac
wait_stable 15
vis > "$BOXN/vis.txt"; cap > "$BOXN/cap.txt"
"$BIN" show --id "$P" -a > "$BOXN/truth.txt" 2>/dev/null

showing=$(grep -oE "· [0-9a-f]{8} ·" "$BOXN/vis.txt" | head -1 | tr -d '· ')
leak=$(grep -c "$CHILD" "$BOXN/vis.txt")
leak_log=$(grep -c "$CHILD" "$BOXN/truth.txt")
echo "seed=$SEED parent=$P branch=${B:-?} showing=${showing:-?} vis=$leak log=$leak_log"
if [[ -n "$showing" && "$showing" != "$P" ]]; then echo "seed=$SEED HOP-FAIL"; exit 4; fi
(( leak_log > 0 )) && { echo "seed=$SEED STORE-LEAK"; exit 2; }
if (( leak > 0 )); then echo "seed=$SEED LEAK"; cat "$BOXN/vis.txt"; exit 1; fi
exit 0
