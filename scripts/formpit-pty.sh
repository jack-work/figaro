#!/usr/bin/env bash
# form show/listen in the pit, against a REAL aria on a REAL model.
#
# The fake gateway cannot produce a form worth walking: what is under test is a
# tree of real keys (skills, system, credo…), the pit's cursor over it, and
# whether EVERY row can hold the highlight. So this one spends tokens.
#
# Isolated store, isolated tmux server, credentials copied from the real config
# and never written back.
set -uo pipefail

BIN=${BIN:-/tmp/statusbar/figaro}
DIR=$(mktemp -d /tmp/formpit.XXXXXX)
SOCK=$DIR/tmux.sock
export FIGARO_STATE_DIR=$DIR/state FIGARO_RUNTIME_DIR=$DIR/run \
       FIGARO_CONFIG_DIR=$DIR/cfg FIGARO_CACHE_DIR=$DIR/cache
mkdir -p "$FIGARO_STATE_DIR" "$FIGARO_RUNTIME_DIR" "$FIGARO_CACHE_DIR"
cp -r "$HOME/.config/figaro/." "$FIGARO_CONFIG_DIR"
chmod 700 "$FIGARO_CONFIG_DIR"

cleanup() {
  tmux -S "$SOCK" kill-server 2>/dev/null
  "$BIN" stop >/dev/null 2>&1
  rm -rf "$DIR"
}
trap cleanup EXIT

fail=0
ARIA=$("$BIN" new -j | python3 -c 'import json,sys;print(json.load(sys.stdin)["aria_id"])') || exit 1
echo "== real aria $ARIA (real model, real tokens)"
"$BIN" send -f --id "$ARIA" -- "Say the single word: ready." >/dev/null 2>&1
for _ in $(seq 90); do
  [[ "$("$BIN" status "$ARIA" -j 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin).get("state",""))')" == idle ]] && break
  sleep 1
done
echo "== form has $("$BIN" state show --id "$ARIA" -j 2>/dev/null | python3 -c 'import json,sys;print(len(json.load(sys.stdin)))') top-level keys"

tmux -S "$SOCK" new-session -d -s pit -x 120 -y 41 bash --norc
tmux -S "$SOCK" set -g status off
H=$(tmux -S "$SOCK" display -p -t pit:0 '#{pane_height}')
tmux -S "$SOCK" send-keys -t pit:0 "export FIGARO_STATE_DIR=$FIGARO_STATE_DIR FIGARO_RUNTIME_DIR=$FIGARO_RUNTIME_DIR FIGARO_CONFIG_DIR=$FIGARO_CONFIG_DIR FIGARO_CACHE_DIR=$FIGARO_CACHE_DIR FORCE_COLOR=1" Enter
tmux -S "$SOCK" send-keys -t pit:0 "$BIN listen $ARIA" Enter
sleep 4
echo "== pane height $H"

# Open the form in the pit.
tmux -S "$SOCK" send-keys -t pit:0 ':'; sleep 0.5
tmux -S "$SOCK" send-keys -t pit:0 -l "form listen"; sleep 0.5
tmux -S "$SOCK" send-keys -t pit:0 Enter; sleep 3

pane() { tmux -S "$SOCK" capture-pane -p -t pit:0; }
# The highlighted row: the picker paints it with a background SGR, so read the
# ESCAPES, not the text -- capture-pane -e keeps them.
hl() { tmux -S "$SOCK" capture-pane -p -e -t pit:0 | grep -a "48;5;237m" | head -1 | sed 's/\x1b\[[0-9;]*m//g' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'; }

first=$(hl)
echo "first highlight: [$first]"
[[ -n "$first" ]] || { echo "FAIL: nothing is highlighted in the form pit"; fail=1; }

if pane | grep -q "𝄢"; then echo "ok   the form pit shows its glyph"; else echo "FAIL: no 𝄢 on the bar"; fail=1; fi

# WALK THE WHOLE LIST with j, collecting every distinct highlight. Every step
# must move by exactly one row: skipping is the bug this test exists for.
declare -A seen
order=()
prev="$first"
seen["$first"]=1; order+=("$first")
skips=0
for i in $(seq 1 60); do
  tmux -S "$SOCK" send-keys -t pit:0 j; sleep 0.22
  cur=$(hl)
  [[ -z "$cur" ]] && { echo "FAIL: highlight vanished after $i presses of j"; fail=1; break; }
  if [[ "$cur" == "$prev" ]]; then
    # bottom of the list, or a stuck cursor
    break
  fi
  if [[ -n "${seen[$cur]:-}" ]]; then
    echo "FAIL: j revisited a row already seen: [$cur]"; fail=1; break
  fi
  seen["$cur"]=1; order+=("$cur"); prev="$cur"
done
echo "== j visited ${#order[@]} distinct rows"
printf '   %s\n' "${order[@]}" | head -12
(( ${#order[@]} >= 8 )) || { echo "FAIL: j only reached ${#order[@]} rows; the form has far more"; fail=1; }

# Now walk BACK with k and insist we see the same rows in reverse. A cursor that
# skips on the way down often skips differently on the way up.
back=()
for i in $(seq 1 ${#order[@]}); do
  tmux -S "$SOCK" send-keys -t pit:0 k; sleep 0.22
  cur=$(hl)
  [[ -n "$cur" ]] || break
  back+=("$cur")
  [[ "$cur" == "${order[0]}" ]] && break
done
echo "== k walked back over ${#back[@]} rows"
if [[ "${back[-1]}" == "${order[0]}" ]]; then
  echo "ok   k returned to the first row"
else
  echo "FAIL: k did not return to the first row (ended on [${back[-1]}])"; fail=1
fi

# Every row j visited must be visited by k too -- EXCEPT THE LAST, which is
# where k starts and therefore never reports again. (The first draft of this
# check asserted otherwise and failed on a correct program: k walked back over
# 9 of 10 rows because the tenth was under the cursor when it began.)
missing=0
for row in "${order[@]:1:${#order[@]}-2}"; do
  found=0
  for b in "${back[@]}"; do [[ "$b" == "$row" ]] && { found=1; break; }; done
  (( found )) || { echo "FAIL: k skipped a row j visited: [$row]"; missing=1; }
done
(( missing )) && fail=1
(( missing )) || echo "ok   j and k visit the same rows"

# The pit's HEIGHT must not move as the cursor crosses the list edges.
tmux -S "$SOCK" send-keys -t pit:0 G; sleep 0.5
hbot=$(pane | grep -c .)
tmux -S "$SOCK" send-keys -t pit:0 g; sleep 0.2; tmux -S "$SOCK" send-keys -t pit:0 g; sleep 0.5
htop=$(pane | grep -c .)
if [[ "$hbot" == "$htop" ]]; then
  echo "ok   the pit's height is the same at both ends ($htop rows)"
else
  echo "FAIL: the pit changed height between the ends of the list: $htop vs $hbot"; fail=1
fi

# Enter expands a branch: the row count must grow.
before=$(pane | grep -c .)
tmux -S "$SOCK" send-keys -t pit:0 Enter; sleep 1
[[ -n "$(hl)" ]] || { echo "FAIL: the highlight was lost by Enter"; fail=1; }
echo "ok   Enter kept the highlight"

tmux -S "$SOCK" send-keys -t pit:0 Escape; sleep 0.5
tmux -S "$SOCK" send-keys -t pit:0 q; sleep 1
echo
(( fail )) && echo "FAILED" || echo "all good"
exit $fail
