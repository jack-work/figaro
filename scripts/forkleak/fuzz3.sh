#!/usr/bin/env bash
# Fuzz trial, third shape: the INLINE surface (incipit), not the pager.
# A shell sends to the parent and watches the reply print inline. A fork is
# made (from another shell, or by this one) around that turn. Does the branch's
# inquiry show up at the end of the parent's reply?
#
# Usage: fuzz3.sh <seed>
#   nparent 1..3   turns before
#   at      head|:1|:2
#   when    0 fork before the parent's last send | 1 fork MID-TURN | 2 fork after idle
#   second  0|1    the parent takes one more inline turn at the end
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SEED=${1:-1}
BOXN=$ROOT/h$SEED
PORT=$((9400 + SEED))
W=${W:-100} H=${H:-58}
SOCK=$BOXN/tmux.sock
CHILD="CHILDQ$SEED"

read -r K_NP K_AT K_WHEN K_SECOND <<<"$(python3 -c "import random;random.seed($SEED+31000);print(' '.join(str(random.randrange(n)) for n in [3,3,3,2]))")"
NPARENT=$((K_NP + 1))
case $K_AT in 0) AT="head";; 1) AT=":1";; 2) AT=":2";; esac
[[ "$AT" == ":2" && $NPARENT -lt 2 ]] && AT=":1"
echo "seed=$SEED nparent=$NPARENT at=$AT when=$K_WHEN second=$K_SECOND"

box_up "$BOXN"
cat > "$BOX/cfg/config.toml" <<'EOF'
default_outfit = "probe"
interactive = true
EOF
export FORKLEAK_SLOW=0.5
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
forkit() {
  if [[ "$AT" == "head" ]]; then "$BIN" fork --id "$P" --stay -f -- "$CHILD branch question" >>"$BOXN/fork.log" 2>&1
  else "$BIN" fork "$P$AT" --stay -f -- "$CHILD branch question" >>"$BOXN/fork.log" 2>&1; fi
}

P=$(aria_new) || exit 3
for i in $(seq "$NPARENT"); do
  "$BIN" send --id "$P" -f -- "PARENTQ$i answer briefly" >/dev/null 2>&1
  wait_idle "$P" 60 || { echo "SETUP-FAIL parent turn $i"; exit 3; }
done

tmx new-session -d -s fk -x "$W" -y "$((H+1))" bash --norc
tmx set -g status off
tmx send-keys -t fk:0 -l "export FIGARO_CONFIG_DIR=$BOX/cfg FIGARO_STATE_DIR=$BOX/state FIGARO_RUNTIME_DIR=$BOX/rt FIGARO_CACHE_DIR=$BOX/cache FIGARO_NO_BIND=1; export PS1='\$ '"
tmx send-keys -t fk:0 Enter
sleep 1

(( K_WHEN == 0 )) && forkit

# the inline turn the reader is watching
tmx send-keys -t fk:0 -l "$BIN send --id $P -- 'PARENTLIVEQ answer briefly'"
tmx send-keys -t fk:0 Enter
if (( K_WHEN == 1 )); then
  sleep 2.5
  forkit
fi
wait_idle "$P" 90 || echo "NOTE parent never idled"
wait_stable 12
(( K_WHEN == 2 )) && { forkit; sleep 4; }

if (( K_SECOND )); then
  tmx send-keys -t fk:0 -l "$BIN send --id $P -- 'PARENTPOSTQ answer briefly'"
  tmx send-keys -t fk:0 Enter
  wait_idle "$P" 90 || true
  wait_stable 12
fi

vis > "$BOXN/vis.txt"; cap > "$BOXN/cap.txt"
"$BIN" show --id "$P" -a > "$BOXN/truth.txt" 2>/dev/null
leak_vis=$(grep -c "$CHILD" "$BOXN/cap.txt")
leak_truth=$(grep -c "$CHILD" "$BOXN/truth.txt")
echo "seed=$SEED parent=$P inline-leak=$leak_vis log=$leak_truth"
if (( leak_truth > 0 )); then echo "seed=$SEED STORE-LEAK"; exit 2; fi
if (( leak_vis > 0 )); then echo "seed=$SEED LEAK"; cat "$BOXN/cap.txt"; exit 1; fi
exit 0
