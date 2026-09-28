#!/usr/bin/env bash
# Fuzz trial, fourth shape: RECLAMATION. The parent goes dormant (the daemon
# reclaims its agent) and/or its composed UI IR is evicted, so the next read
# RECOMPOSES the parent's turns from the log through the shared, lineage-aware
# composed cache -- the one thing in the daemon a fork's child and its parent
# both touch.
#
# Usage: fuzz4.sh <seed>
#   nparent 1..3   turns before the fork
#   at      head|:1|:2
#   branchturns 1..2  turns the branch takes (its records are what could leak)
#   dormant 0|1    wait past dormant_after so the parent's agent is reclaimed
#   norm    0|1    `figaro normalize` (deferred topology work) before reading
#   post    0|1    the parent takes one more turn after the fork
#   pager   0|1    read back through the pager in a pty as well as `show`
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SEED=${1:-1}
BOXN=$ROOT/i$SEED
PORT=$((9600 + SEED))
W=${W:-100} H=${H:-58}
SOCK=$BOXN/tmux.sock
CHILD="CHILDQ$SEED"

read -r K_NP K_AT K_BT K_DORM K_NORM K_POST K_PAGER <<<"$(python3 -c "import random;random.seed($SEED+77000);print(' '.join(str(random.randrange(n)) for n in [3,3,2,2,2,2,2]))")"
NPARENT=$((K_NP + 1)); BTURNS=$((K_BT + 1))
case $K_AT in 0) AT="head";; 1) AT=":1";; 2) AT=":2";; esac
[[ "$AT" == ":2" && $NPARENT -lt 2 ]] && AT=":1"
echo "seed=$SEED nparent=$NPARENT at=$AT bturns=$BTURNS dormant=$K_DORM norm=$K_NORM post=$K_POST pager=$K_PAGER"

box_up "$BOXN"
cat > "$BOX/cfg/config.toml" <<'EOF'
default_outfit = "probe"
interactive = true

[memory]
# The smallest dormancy the config allows, swept often: the parent's agent is
# reclaimed while we are not looking, and the next read must rebuild it.
dormant_after_minutes = 1
sweep_interval_seconds = 5
# One mebibyte of composed UI IR across every aria: a fat reply evicts, and
# the read that lands in the evicted range recomposes it.
ui_window_mb = 1
EOF
export FORKLEAK_BIG=60          # ~60KB per reply
gw_up || exit 3

tmx() { tmux -S "$SOCK" "$@" >/dev/null 2>&1; }
tmxo() { tmux -S "$SOCK" "$@" 2>/dev/null; }
cleanup() { tmx kill-server; "$BIN" stop >/dev/null 2>&1; gw_down; }
trap cleanup EXIT

P=$(aria_new) || exit 3
for i in $(seq "$NPARENT"); do
  "$BIN" send --id "$P" -f -- "PARENTQ$i answer at length" >/dev/null 2>&1
  wait_idle "$P" 90 || { echo "SETUP-FAIL parent turn $i"; exit 3; }
done

if [[ "$AT" == "head" ]]; then "$BIN" fork --id "$P" --stay -f -j -- "$CHILD branch question" >"$BOXN/fork.json" 2>"$BOXN/fork.err"
else "$BIN" fork "$P$AT" --stay -f -j -- "$CHILD branch question" >"$BOXN/fork.json" 2>"$BOXN/fork.err"; fi
B=$(python3 -c 'import json;print(json.load(open("'"$BOXN"'/fork.json")).get("alternative",""))' 2>/dev/null)
[[ -z "$B" ]] && { echo "SETUP-FAIL no branch: $(cat "$BOXN/fork.err")"; exit 3; }
wait_idle "$B" 90 || echo "NOTE branch never idled"
for i in $(seq 2 "$BTURNS"); do
  "$BIN" send --id "$B" -f -- "CHILDQ${SEED}x$i answer at length" >/dev/null 2>&1
  wait_idle "$B" 90 || true
done
if (( K_POST )); then
  "$BIN" send --id "$P" -f -- "PARENTPOSTQ answer at length" >/dev/null 2>&1
  wait_idle "$P" 90 || true
fi

(( K_NORM )) && "$BIN" normalize >/dev/null 2>&1
if (( K_DORM )); then
  # dormant_after is one minute, swept every five seconds.
  sleep 70
  "$BIN" doctor mem > "$BOXN/mem.txt" 2>&1
fi

# READ THE PARENT. show walks composed pages through the daemon, which is the
# same door the pager reads: a recompose lands here.
"$BIN" show --id "$P" -a > "$BOXN/truth.txt" 2>/dev/null
leak_show=$(grep -c "CHILDQ$SEED" "$BOXN/truth.txt")
# and the branch, to prove the fixture ran at all
"$BIN" show --id "$B" -a > "$BOXN/branch.txt" 2>/dev/null
has_child=$(grep -c "CHILDQ$SEED" "$BOXN/branch.txt")

leak_pager=0
if (( K_PAGER )); then
  tmx new-session -d -s fk -x "$W" -y "$((H+1))" bash --norc
  tmx set -g status off
  tmx send-keys -t fk:0 -l "export FIGARO_CONFIG_DIR=$BOX/cfg FIGARO_STATE_DIR=$BOX/state FIGARO_RUNTIME_DIR=$BOX/rt FIGARO_CACHE_DIR=$BOX/cache FIGARO_NO_BIND=1"
  tmx send-keys -t fk:0 Enter
  tmx send-keys -t fk:0 -l "$BIN listen $P"
  tmx send-keys -t fk:0 Enter
  sleep 8
  tmxo capture-pane -p -S - -t fk:0 > "$BOXN/pager.txt"
  leak_pager=$(grep -c "CHILDQ$SEED" "$BOXN/pager.txt")
fi

echo "seed=$SEED parent=$P branch=$B show=$leak_show pager=$leak_pager (branch has its own token: $has_child)"
(( has_child == 0 )) && { echo "seed=$SEED SETUP-FAIL the branch never recorded its question"; exit 3; }
if (( leak_show > 0 || leak_pager > 0 )); then
  echo "seed=$SEED LEAK"
  grep -n "CHILDQ$SEED" "$BOXN/truth.txt" "$BOXN/pager.txt" 2>/dev/null | head -20
  exit 1
fi
exit 0
