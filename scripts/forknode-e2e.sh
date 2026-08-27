#!/usr/bin/env bash
# Forking at a NODE, end to end: no credentials, no network, no tokens.
#
# `<id>:<turn>.<node>` is a coordinate the PAGER prints and the fork wire does
# not speak: a node is a content block, a fork cuts whole messages. This script
# builds a real aria whose turn has both kinds of node -- ones that begin a
# message and ones that ride inside one behind a paragraph -- forks at each,
# and reads back what the branch actually kept.
#
# It asserts PROPERTIES, not a copy of the implementation's arithmetic:
#
#   1. the node you named is gone from the branch
#   2. nothing is cut inside a tool call: no branch ends between a call and
#      its result, which is the conversation Anthropic refuses to continue
#   3. a node that BEGINS a message is exact: the branch ends one LT below it
#   4. a node that does not says so on stderr, and lands earlier
#   5. `:<turn>.0` keeps the turn's question and drops the answer
#   6. `:<turn>.-1` is the turn: the question goes too
#
# Touches nothing outside /tmp/figaro-forknode. Your daemon is never used.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BOX=/tmp/figaro-forknode
BIN=$BOX/figaro
PORT=${PORT:-8918}

rm -rf "$BOX"; mkdir -p "$BOX"/{cfg/outfits,cfg/providers,state,rt,cache}
cleanup() {
  "$BIN" stop >/dev/null 2>&1 || true
  kill "${GW_PID:-0}" 2>/dev/null || true
}
trap cleanup EXIT

echo "== building from $(git -C "$ROOT" rev-parse --short HEAD)"
( cd "$ROOT" && go build -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse HEAD)" -o "$BIN" ./cmd/figaro ) || exit 1

echo "== starting the scripted gateway on :$PORT"
python3 "$ROOT/scripts/fake-gateway-tools.py" "$PORT" "$BOX/requests.jsonl" & GW_PID=$!
sleep 1

cat > "$BOX/cfg/providers/gateway.toml" <<EOF
base_url = "http://127.0.0.1:$PORT/v1"
EOF
printf 'You are a test agent.\n' > "$BOX/cfg/credo.md"
cat > "$BOX/cfg/outfits/forknode.toml" <<'EOF'
duke-title = "forker"

[system]
provider   = "gateway"
model      = "auto"
max_tokens = 64
credo      = { fileName = "credo.md" }
EOF
cat > "$BOX/cfg/config.toml" <<'EOF'
default_outfit = "forknode"
interactive = false
EOF

export FIGARO_CONFIG_DIR=$BOX/cfg FIGARO_STATE_DIR=$BOX/state \
       FIGARO_RUNTIME_DIR=$BOX/rt FIGARO_CACHE_DIR=$BOX/cache FIGARO_NO_BIND=1

ARIA=$("$BIN" new -j | python3 -c 'import json,sys; print(json.load(sys.stdin)["aria_id"])') || exit 1
echo "== aria $ARIA: one turn, three tool calls, prose in front of two of them"
"$BIN" send -f --id "$ARIA" -- "look around" >/dev/null 2>&1
for _ in $(seq 60); do
  st=$("$BIN" status "$ARIA" -j 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin).get("state",""))' 2>/dev/null)
  [[ "$st" == "idle" ]] && break
  sleep 0.5
done

# table <aria> <turn> -> "idx type first last" per node, from the daemon's own
# composed nodes: the numbering the pager shows and the coordinate names.
table() {
  "$BIN" show --id "$1" --from "$2" --to "$2" -j 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin)
for p in d["parts"]:
    if p["turn"] != '"$2"': continue
    for i,n in enumerate(p.get("nodes",[])):
        lts=n.get("lts") or [0]
        print(int(p.get("from",0))+i, n["type"], lts[0], lts[-1])
'
}

# turn_ids <aria> -> the turn ids the aria holds ("" for an empty branch,
# whose `show` prints prose rather than JSON because there is nothing to show).
turn_ids() {
  "$BIN" show --id "$1" -a -j 2>/dev/null | python3 -c '
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(0)
print(" ".join(str(p["turn"]) for p in d["parts"]))
' 2>/dev/null
}

# THE BRANCH IS MEASURED IN NODES, not in LTs, and the first draft of this
# script measured LTs and was wrong: a fork seeds the child with a message of
# its own (the branch learns its new id), so the highest LT in a branch is the
# branch's own bookkeeping, not the last thing it inherited. Nodes come only
# from the conversation.

mapfile -t TABLE < <(table "$ARIA" 1)
if (( ${#TABLE[@]} < 4 )); then
  echo "FAIL: the fixture turn has ${#TABLE[@]} nodes; this proves nothing"
  printf '   %s\n' "${TABLE[@]}"
  exit 1
fi
echo "== turn 1's nodes (idx type firstLT lastLT):"
printf '   %s\n' "${TABLE[@]}"

fail=0
say_ok()   { echo "ok   $*"; }
say_fail() { echo "FAIL $*"; fail=1; }

# fork_at <coord> -> sets ALT and NOTE
fork_at() {
  NOTE=$("$BIN" fork "$ARIA:$1" -j 2>"$BOX/err.txt" >"$BOX/fork.json"; cat "$BOX/err.txt")
  ALT=$(python3 -c 'import json;print(json.load(open("'"$BOX"'/fork.json"))["alternative"])' 2>/dev/null)
  [[ -n "$ALT" ]]
}

echo
for row in "${TABLE[@]}"; do
  read -r idx typ first last <<<"$row"
  prev_first=""
  if (( idx > 0 )); then read -r _ _ prev_first _ <<<"${TABLE[idx-1]}"; fi
  if ! fork_at "1.$idx"; then
    say_fail "node $idx: fork failed: $NOTE"
    continue
  fi
  mapfile -t KEPT < <(table "$ALT" 1)
  kept=${#KEPT[@]}

  # 1. what the branch kept is a PREFIX of what the parent had, and the node
  #    named is not in it.
  ok=1
  for (( k = 0; k < kept; k++ )); do
    [[ "${KEPT[k]}" == "${TABLE[k]}" ]] || { say_fail "node $idx: branch node $k is '${KEPT[k]}', parent has '${TABLE[k]}'"; ok=0; }
  done
  (( kept <= idx )) || { say_fail "node $idx: the branch kept $kept nodes, so the node named is still in it"; ok=0; }

  # 2. no tool call is left without its result. A tool node carries both
  #    coordinates, so a retained tool with one LT is a stranded call -- the
  #    conversation Anthropic refuses to continue.
  for keptrow in "${KEPT[@]}"; do
    read -r kidx ktyp kfirst klast <<<"$keptrow"
    if [[ "$ktyp" == "tool" && "$kfirst" == "$klast" ]]; then
      say_fail "node $idx: the branch keeps tool node $kidx with no result (LT $kfirst)"; ok=0
    fi
  done

  # 3/4. exact when the node opens a message, reported when it does not.
  if [[ -z "$prev_first" || "$prev_first" != "$first" ]]; then
    if (( kept == idx )); then
      (( ok )) && say_ok "node $idx ($typ) is exact: the branch keeps nodes 0..$((idx-1))"
    else
      say_fail "node $idx ($typ) opens a message but the branch kept $kept nodes, want $idx"
    fi
    [[ -n "$NOTE" ]] && say_fail "node $idx: an exact fork should say nothing extra, said: $NOTE"
  else
    if [[ "$NOTE" != *"cannot be cut at"* && "$NOTE" != *"whole turn"* ]]; then
      say_fail "node $idx shares a message with node $((idx-1)) and the fork did not say so (said: ${NOTE:-nothing})"
    elif (( ok )); then
      say_ok "node $idx ($typ) rides inside a message, kept $kept: $NOTE"
    fi
  fi
  "$BIN" kill "$ALT" -y >/dev/null 2>&1
done

echo
# 5. node 0 keeps the question and nothing else.
if fork_at "1.0"; then
  kept=$(table "$ALT" 1 | wc -l)
  if [[ " $(turn_ids "$ALT") " == *" 1 "* && "$kept" == "0" ]]; then
    say_ok ":1.0 keeps turn 1's question and none of its answer"
  else
    say_fail ":1.0 left turns '$(turn_ids "$ALT")' with $kept nodes; want turn 1 with none"
  fi
  "$BIN" kill "$ALT" -y >/dev/null 2>&1
else
  say_fail ":1.0 did not fork: $NOTE"
fi

# 6. node -1 is the turn itself.
if fork_at "1.-1"; then
  if [[ " $(turn_ids "$ALT") " == *" 1 "* ]]; then
    say_fail ":1.-1 kept turn 1; the question is what it addresses"
  else
    say_ok ":1.-1 takes the question too (no turn 1 in the branch)"
  fi
  "$BIN" kill "$ALT" -y >/dev/null 2>&1
else
  say_fail ":1.-1 did not fork: $NOTE"
fi

# 7. a node nobody has is refused, and says what exists.
if fork_at "1.999"; then
  say_fail ":1.999 forked at a node that does not exist"
  "$BIN" kill "$ALT" -y >/dev/null 2>&1
elif [[ "$NOTE" == *"no node 999"* ]]; then
  say_ok ":1.999 is refused: $NOTE"
else
  say_fail ":1.999 was refused without saying what exists: $NOTE"
fi

# 8. THE BRANCH IS PROMPTABLE. `send <id>:<turn>.<node>` forks and sends in
#    one gesture, and the point of never stranding a tool call is that the
#    branch can then take a turn at all.
# NOT --stay: that parks the branch and sends to the ORIGINAL trunk, which
# would prove only that the original still works. The branch is the thing whose
# history this feature cut, so the branch is what has to take a turn.
if "$BIN" send "$ARIA:1.3" -f -- "carry on from here" >/dev/null 2>"$BOX/senderr.txt"; then
  # send names the branch it made on stderr: "forked X at ... -> Y"
  BR=$(grep -oE '\-> (attending )?[0-9a-f]{8}' "$BOX/senderr.txt" | grep -oE '[0-9a-f]{8}' | head -1)
  for _ in $(seq 30); do
    "$BIN" show --id "$BR" -a -j 2>/dev/null | grep -q "carry on from here" && break
    sleep 0.5
  done
  if [[ -n "$BR" ]] && "$BIN" show --id "$BR" -a -j 2>/dev/null | grep -q "carry on from here"; then
    kept=$(table "$BR" 1 | wc -l)
    say_ok "send :1.3 forked to $BR (kept $kept nodes of turn 1) and it took the prompt"
  else
    say_fail "send :1.3 forked to '${BR:-?}' but the branch never got the prompt"
  fi
else
  say_fail "send :1.3 failed: $(cat "$BOX/senderr.txt")"
fi

echo
if (( fail )); then echo "FAILED"; else echo "all good"; fi
exit $fail
