#!/usr/bin/env bash
# Fuzz trial, second shape: the pager NEVER LEAVES the parent. Somebody else
# forks the parent and prompts the branch. The question is whether the branch's
# inquiry is painted into the parent's transcript.
#
# Usage: fuzz2.sh <seed>
# Knobs from the seed:
#   nparent  1..3  turns before the fork
#   at       head | :1 | :2
#   when     0 the parent is idle when the fork happens
#            1 the parent is MID-TURN (slow gateway) when the fork happens
#   who      0 another shell forks with --stay (the pager stays put)
#            1 the pager forks and comes straight back with ^O
#   post     0|1  the parent takes one more turn afterwards
#   tail     0|1  the reader presses G (follow the tail) at the end
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SEED=${1:-1}
BOXN=$ROOT/g$SEED
PORT=$((9200 + SEED))
W=${W:-100} H=${H:-58}
SOCK=$BOXN/tmux.sock
CHILD="CHILDQ$SEED"

read -r K_NP K_AT K_WHEN K_WHO K_POST K_TAIL <<<"$(python3 -c "import random;random.seed($SEED+7000);print(' '.join(str(random.randrange(n)) for n in [3,3,2,2,2,2]))")"
NPARENT=$((K_NP + 1))
case $K_AT in 0) AT="head";; 1) AT=":1";; 2) AT=":2";; esac
[[ "$AT" == ":2" && $NPARENT -lt 2 ]] && AT=":1"
echo "seed=$SEED nparent=$NPARENT at=$AT when=$K_WHEN who=$K_WHO post=$K_POST tail=$K_TAIL"

box_up "$BOXN"
cat > "$BOX/cfg/config.toml" <<'EOF'
default_outfit = "probe"
interactive = true
EOF
export FORKLEAK_SLOW=0.6
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
wait_has() { local pat=$1 secs=${2:-30} i; for i in $(seq $((secs*2))); do cap | grep -q "$pat" && return 0; sleep 0.5; done; return 1; }
cmdline() { tmx send-keys -t fk:0 -l ":"; sleep 0.3; tmx send-keys -t fk:0 -l "$1"; sleep 0.3; tmx send-keys -t fk:0 Enter; }

P=$(aria_new) || exit 3
for i in $(seq "$NPARENT"); do
  "$BIN" send --id "$P" -f -- "PARENTQ$i answer briefly" >/dev/null 2>&1
  wait_idle "$P" 60 || { echo "SETUP-FAIL parent turn $i"; exit 3; }
done

tmx new-session -d -s fk -x "$W" -y "$((H+1))" bash --norc
tmx set -g status off
tmx send-keys -t fk:0 -l "export FIGARO_CONFIG_DIR=$BOX/cfg FIGARO_STATE_DIR=$BOX/state FIGARO_RUNTIME_DIR=$BOX/rt FIGARO_CACHE_DIR=$BOX/cache FIGARO_NO_BIND=1"
tmx send-keys -t fk:0 Enter
tmx send-keys -t fk:0 -l "$BIN listen $P"
tmx send-keys -t fk:0 Enter
wait_has "PARENTQ$NPARENT" 40 || { echo "SETUP-FAIL pager cold"; cap|tail -10; exit 3; }
wait_stable 10

# If the fork is to happen mid-turn, put the parent in one first.
if (( K_WHEN )); then
  "$BIN" send --id "$P" -f -- "PARENTQ$((NPARENT+1)) answer briefly" >/dev/null 2>&1
  sleep 2   # inside the streamed reply, not before it
fi

if (( K_WHO )); then
  if [[ "$AT" == "head" ]]; then cmdline "fork -- $CHILD branch question"
  else cmdline "fork $P$AT -- $CHILD branch question"; fi
  sleep 3
  tmx send-keys -t fk:0 C-o     # straight back to the parent
else
  if [[ "$AT" == "head" ]]; then "$BIN" fork --id "$P" --stay -f -- "$CHILD branch question" >"$BOXN/fork.json" 2>&1
  else "$BIN" fork "$P$AT" --stay -f -- "$CHILD branch question" >"$BOXN/fork.json" 2>&1; fi
fi

wait_idle "$P" 90 || echo "NOTE parent never idled"
sleep 2
if (( K_POST )); then
  "$BIN" send --id "$P" -f -- "PARENTPOSTQ answer briefly" >/dev/null 2>&1
  wait_idle "$P" 90 || true
fi
(( K_TAIL )) && { tmx send-keys -t fk:0 -l "G"; sleep 1; }
wait_stable 20

vis > "$BOXN/vis.txt"; cap > "$BOXN/cap.txt"
"$BIN" show --id "$P" -a > "$BOXN/truth.txt" 2>/dev/null

showing=$(grep -oE "· [0-9a-f]{8} ·" "$BOXN/vis.txt" | head -1 | tr -d '· ')
leak_vis=$(grep -c "$CHILD" "$BOXN/vis.txt")
leak_all=$(grep -c "$CHILD" "$BOXN/cap.txt")
leak_truth=$(grep -c "$CHILD" "$BOXN/truth.txt")
echo "seed=$SEED parent=$P showing=${showing:-?} vis=$leak_vis scrollback=$leak_all log=$leak_truth"
if [[ -n "$showing" && "$showing" != "$P" ]]; then echo "seed=$SEED HOP-FAIL"; exit 4; fi
if (( leak_truth > 0 )); then echo "seed=$SEED STORE-LEAK (the log itself has it)"; exit 2; fi
if (( leak_vis > 0 || leak_all > 0 )); then
  echo "seed=$SEED LEAK"
  cat "$BOXN/vis.txt"
  exit 1
fi
exit 0
