#!/usr/bin/env bash
# The form in the pit, against a REAL aria on a REAL model.
#
# The fake gateway cannot produce a form worth walking: what is under test is a
# tree of real keys (skills, system, credo…), the pit's cursor over it, and
# whether EVERY row can hold the highlight. So this one spends tokens.
#
# BOTH SPELLINGS ARE WALKED. `form show` and `form listen` are the same pit now
# -- `show` used to be captured as text and pasted into an output pit whose rows
# are lines, so its motions moved among the lines that happened to carry an
# aria-shaped id and skipped whole properties. A test that walks only `listen`
# is a test that would have shipped that.
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

pane() { tmux -S "$SOCK" capture-pane -p -t pit:0; }
# The highlighted row: the picker paints it with a background SGR, so read the
# ESCAPES, not the text -- capture-pane -e keeps them.
hl() { tmux -S "$SOCK" capture-pane -p -e -t pit:0 | grep -a "48;5;237m" | head -1 | sed 's/\x1b\[[0-9;]*m//g' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'; }
key() { tmux -S "$SOCK" send-keys -t pit:0 "$@"; }

open_pit() {   # open_pit "<command line>"
  key ':'; sleep 0.5
  tmux -S "$SOCK" send-keys -t pit:0 -l "$1"; sleep 0.5
  key Enter; sleep 3
}

walk() {  # walk "<command line>"
  local verb=$1
  echo
  echo "===== :$verb"
  open_pit "$verb"

  if pane | grep -q "𝄢"; then echo "ok   the form pit shows its glyph"; else echo "FAIL: no 𝄢 on the bar"; fail=1; fi

  local first
  first=$(hl)
  echo "first highlight: [$first]"
  [[ -n "$first" ]] || { echo "FAIL: nothing is highlighted in the form pit"; fail=1; }

  # WALK THE WHOLE LIST with j, collecting every distinct highlight. Every step
  # must move by exactly one row: skipping is the bug this test exists for.
  declare -A seen=()
  local order=() prev=$first cur
  seen["$first"]=1; order+=("$first")
  local i
  for i in $(seq 1 60); do
    key j; sleep 0.22
    cur=$(hl)
    [[ -z "$cur" ]] && { echo "FAIL: highlight vanished after $i presses of j"; fail=1; break; }
    if [[ "$cur" == "$prev" ]]; then break; fi   # the bottom, or a stuck cursor
    if [[ -n "${seen[$cur]:-}" ]]; then
      echo "FAIL: j revisited a row already seen: [$cur]"; fail=1; break
    fi
    seen["$cur"]=1; order+=("$cur"); prev="$cur"
  done
  echo "== j visited ${#order[@]} distinct rows"
  printf '   %s\n' "${order[@]}" | head -12
  (( ${#order[@]} >= 8 )) || { echo "FAIL: j only reached ${#order[@]} rows; the form has far more"; fail=1; }

  # Now walk BACK with k and insist we see the same rows in reverse. A cursor
  # that skips on the way down often skips differently on the way up.
  local back=()
  for i in $(seq 1 ${#order[@]}); do
    key k; sleep 0.22
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
  local missing=0 row b found
  for row in "${order[@]:1:${#order[@]}-2}"; do
    found=0
    for b in "${back[@]}"; do [[ "$b" == "$row" ]] && { found=1; break; }; done
    (( found )) || { echo "FAIL: k skipped a row j visited: [$row]"; missing=1; }
  done
  (( missing )) && fail=1
  (( missing )) || echo "ok   j and k visit the same rows"

  # ^N CHOOSES, and in a list where every row is selectable it must reach a
  # different row each time, exactly as j does.
  local picks=() n
  for n in 1 2 3; do
    key C-n; sleep 0.3
    picks+=("$(hl)")
  done
  if [[ "${picks[0]}" != "${picks[1]}" && "${picks[1]}" != "${picks[2]}" ]]; then
    echo "ok   ^N moves the selection one row at a time"
  else
    echo "FAIL: ^N stuck: [${picks[0]}] [${picks[1]}] [${picks[2]}]"; fail=1
  fi

  # The pit's HEIGHT must not move as the cursor crosses the list edges.
  key G; sleep 0.5
  local hbot htop
  hbot=$(pane | grep -c .)
  key g; sleep 0.2; key g; sleep 0.5
  htop=$(pane | grep -c .)
  if [[ "$hbot" == "$htop" ]]; then
    echo "ok   the pit's height is the same at both ends ($htop rows)"
  else
    echo "FAIL: the pit changed height between the ends of the list: $htop vs $hbot"; fail=1
  fi

  # ENTER EXPANDS A BRANCH. It is only reachable if the pit's selection is
  # visible to the view's own verbs -- and it must not repaint from inside the
  # key handler, which is the render lock taken twice and a pager that stops.
  local before row2
  for i in $(seq 1 14); do
    row=$(hl)
    [[ "$row" =~ \([0-9]+\)$ ]] && break
    key C-n; sleep 0.3
  done
  if [[ "$row" =~ \([0-9]+\)$ ]]; then
    before=$(pane)
    key Enter; sleep 1.5
    if [[ "$(pane)" == "$before" ]]; then
      echo "FAIL: Enter on branch [$row] changed nothing"; fail=1
    else
      echo "ok   Enter expanded [$row]"
    fi
    row2=$(hl)
    if [[ "$row2" == "$row" ]]; then
      echo "ok   the cursor stayed on the branch it opened"
    else
      echo "FAIL: Enter moved the selection ([$row] -> [$row2])"; fail=1
    fi
  else
    echo "FAIL: no branch row was reachable with ^N"; fail=1
  fi

  # AND THE PAGER IS STILL ALIVE: every check after a freeze reads a frozen
  # screen and passes for the wrong reason.
  key Escape; sleep 0.6
  key '?'; sleep 1
  if pane | grep -q "close help\|exit; keeps the turn running"; then
    echo "ok   the pager still takes keys after Enter in the form pit"
  else
    echo "FAIL: the pager is frozen after Enter in the form pit"; fail=1
  fi
  key Escape; sleep 0.5
}

walk "form listen"
walk "form show"
walk "state show"

key q; sleep 1
echo
(( fail )) && echo "FAILED" || echo "all good"
exit $fail
