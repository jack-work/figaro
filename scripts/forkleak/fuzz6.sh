#!/usr/bin/env bash
# Sixth shape, aimed at what the REAL store shows: a fork made while the parent
# is MID-TOOL, with a message already queued on the parent. On disk, aria
# 8fc9ebed carries `tool call closed without a result (interrupt, fork, or
# fault)` and then three questions nobody ever answered, while its branch holds
# the same question WITH an answer.
#
# Usage: fuzz6.sh <seed>   (knobs: queue before the fork, fork with/without a
# prompt, --stay or not)
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SEED=${1:-1}
BOXN=$ROOT/k$SEED
PORT=$((9900 + SEED))
SOCK=$BOXN/tmux.sock
W=100 H=58
CHILD="CHILDQ$SEED"

read -r K_QUEUE K_PROMPT K_STAY <<<"$(python3 -c "import random;random.seed($SEED+555);print(' '.join(str(random.randrange(n)) for n in [2,2,2]))")"
echo "seed=$SEED queue=$K_QUEUE prompt=$K_PROMPT stay=$K_STAY"

box_up "$BOXN"
cat > "$BOX/cfg/config.toml" <<'EOF'
default_outfit = "probe"
interactive = false
EOF
export GW=$HERE/../fake-gateway-tools.py
gw_up || exit 3
cleanup() { "$BIN" stop >/dev/null 2>&1; gw_down; }
trap cleanup EXIT

P=$(aria_new) || exit 3
"$BIN" send --id "$P" -f -- "PARENTQ1 look around" >/dev/null 2>&1
wait_idle "$P" 90 || { echo "SETUP-FAIL"; exit 3; }

# A turn in flight, slowly: the tools gateway reads its delay from a file, so
# the next turn takes its time and can be forked mid-tool.
echo 6 > "$BOX/requests.jsonl.delay"
"$BIN" send --id "$P" -f -- "PARENTQ2 look around" >/dev/null 2>&1
sleep 4

# A message typed at a busy aria: queued, not answered.
(( K_QUEUE )) && "$BIN" send --id "$P" -f -- "QUEUEDQ$SEED answer this later" >/dev/null 2>&1
sleep 1

STAYF=""; (( K_STAY )) && STAYF="--stay"
if (( K_PROMPT )); then
  "$BIN" fork --id "$P" $STAYF -f -j -- "$CHILD look around" >"$BOXN/fork.json" 2>"$BOXN/fork.err"
else
  "$BIN" fork --id "$P" $STAYF -j >"$BOXN/fork.json" 2>"$BOXN/fork.err"
fi
cat "$BOXN/fork.err" | head -3
B=$(python3 -c 'import json;print(json.load(open("'"$BOXN"'/fork.json")).get("alternative",""))' 2>/dev/null)
echo "branch=$B"
echo 0 > "$BOX/requests.jsonl.delay"
wait_idle "$P" 120 || echo "NOTE parent never idled"
[[ -n "$B" ]] && { wait_idle "$B" 120 || echo "NOTE branch never idled"; }
sleep 2

"$BIN" show --id "$P" -a > "$BOXN/parent.txt" 2>/dev/null
[[ -n "$B" ]] && "$BIN" show --id "$B" -a > "$BOXN/branch.txt" 2>/dev/null
# Queue state, for the record.
"$BIN" queue ls --id "$P" > "$BOXN/queue.txt" 2>&1

echo "--- parent tail ---"; tail -25 "$BOXN/parent.txt"
echo "--- leak checks ---"
echo "branch inquiry on the parent: $(grep -c "$CHILD" "$BOXN/parent.txt")"
echo "queued inquiry on the parent: $(grep -c "QUEUEDQ$SEED" "$BOXN/parent.txt")"
echo "queued inquiry on the branch: $(grep -c "QUEUEDQ$SEED" "${BOXN}/branch.txt" 2>/dev/null || echo 0)"
echo "parent queue: $(cat "$BOXN/queue.txt" | head -3)"
