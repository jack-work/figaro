#!/usr/bin/env bash
# THE EXPERIMENT GLUCK'S OWN NOTE POINTS AT: fork an aria DURING TOOL USE.
#
# Fourteen conversations in the real store end in questions that were committed
# and never answered; several of those carry the door's marker, "tool call
# closed without a result (interrupt, fork, or fault)", and one of them ends
# with Gluck writing "I find that I cannot fork during tool use".
#
# So: put the parent inside a 25-second tool call, fork it there, and then ask
# the parent a question. Does the parent answer?
#
# Usage: toolfork.sh <seed> [prompted|bare] [stay|move]
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

SEED=${1:-1}
MODE=${2:-prompted}
STAYMODE=${3:-stay}
BOXN=$ROOT/t$SEED
PORT=$((9950 + SEED))
export GW=$HERE/gw-slowtool.py
export FORKLEAK_TOOL_SLEEP=25

box_up "$BOXN"
cat > "$BOX/cfg/config.toml" <<'EOF'
default_outfit = "probe"
interactive = false
EOF
gw_up || exit 3
cleanup() { "$BIN" stop >/dev/null 2>&1; gw_down; }
trap cleanup EXIT

state() { "$BIN" status "$1" -j 2>/dev/null | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d.get("state",""))' 2>/dev/null; }

P=$(aria_new) || exit 3
echo "parent=$P mode=$MODE $STAYMODE"

# Turn one: the parent enters a 25s tool call.
"$BIN" send --id "$P" -f -- "PARENTQ1 run the long command" >/dev/null 2>&1
for _ in $(seq 40); do
  pgrep -f "sleep 25" >/dev/null && break
  sleep 0.5
done
pgrep -af "sleep 25" | head -2
echo "state before the fork: $(state "$P")"

# THE FORK, taken while the tool is outstanding.
STAYF=""; [[ "$STAYMODE" == stay ]] && STAYF="--stay"
if [[ "$MODE" == prompted ]]; then
  timeout 60 "$BIN" fork --id "$P" $STAYF -f -j -- "CHILDQ$SEED run the long command" >"$BOXN/fork.json" 2>"$BOXN/fork.err"
else
  timeout 60 "$BIN" fork --id "$P" $STAYF -j >"$BOXN/fork.json" 2>"$BOXN/fork.err"
fi
echo "fork exit=$? err=$(head -2 "$BOXN/fork.err" | tr '\n' ' ')"
B=$(python3 -c 'import json;print(json.load(open("'"$BOXN"'/fork.json")).get("alternative",""))' 2>/dev/null)
echo "branch=$B"

# Let the parent's tool finish on its own, then ask it something.
for _ in $(seq 90); do
  [[ "$(state "$P")" == idle ]] && break
  sleep 1
done
echo "state after the fork: $(state "$P")"

"$BIN" send --id "$P" -f -- "PARENTQ2 did you survive" >/dev/null 2>&1
answered=no
for _ in $(seq 60); do
  if "$BIN" show --id "$P" -a 2>/dev/null | grep -q "ANSWER\[PARENTQ2\]"; then answered=yes; break; fi
  sleep 1
done

"$BIN" show --id "$P" -a > "$BOXN/parent.txt" 2>/dev/null
[[ -n "$B" ]] && "$BIN" show --id "$B" -a > "$BOXN/branch.txt" 2>/dev/null

echo "=== parent tail ==="; tail -22 "$BOXN/parent.txt"
echo "=== verdict ==="
echo "parent answered the follow-up: $answered"
echo "parent state: $(state "$P")"
echo "tool-closed marker on the parent: $(grep -c "closed without a result" "$BOXN/parent.txt")"
echo "branch inquiry on the parent: $(grep -c "CHILDQ$SEED" "$BOXN/parent.txt")"
[[ "$answered" == yes ]] && exit 0 || exit 1
