#!/usr/bin/env bash
# succeed-e2e.sh: figaro succeed and demote against a COPY of the real store,
# in the .#snapshot dev shell. Never the live daemon.
#
#   scripts/succeed-e2e.sh            fake provider; reuse the unit's snapshot
#   FRESH=1 scripts/succeed-e2e.sh    take a new copy first
#   REAL=1 scripts/succeed-e2e.sh     real config, real credentials, real
#                                     models: every successor must answer
#   ONLY=pager scripts/succeed-e2e.sh run the scenarios whose name matches
#
# Exits non-zero when any check fails. The unit dir is kept (DP_KEEP) so the
# copy is taken once; rm -rf /var/tmp/devpane-succeed* to discard it.
set -u
cd "$(git rev-parse --show-toplevel)"
source scripts/devpane.sh
export DP_SHELL=.#snapshot DP_KEEP=1
UNIT=succeed
if [ -n "${REAL:-}" ]; then
  UNIT=succeed-real
  dp_env() {
    printf '%s\n' \
      "FIGARO_DEV_ROOT=$DP_DIR/dev" \
      "FIGARO_STATE_DIR=$DP_DIR/state" \
      "FIGARO_RUNTIME_DIR=$DP_DIR/run" \
      "FIGARO_CACHE_DIR=$DP_DIR/cache"
  }
fi
UNIT_DIR=/var/tmp/devpane-$UNIT
[ -n "${FRESH:-}" ] && rm -f "$UNIT_DIR/state/.snapshot-taken"
dp_init "$UNIT" >/dev/null || exit 1
dp_reply default <<< "acknowledged"

# A real model is asked for a word it can only produce by answering, and told
# to stay off its tools: the successors are copies of working agents.
SAY="Reply with the word DETAES spelled backwards and nothing else. Do not use any tools."
BUSY="Count from 1 to 300, one number per line. Do not use any tools."

FAILS=0
pass() { echo "  ok   $*"; }
fail() { echo "  FAIL $*"; FAILS=$((FAILS + 1)); }
check() { local what="$1"; shift; if "$@"; then pass "$what"; else fail "$what"; fi; }
refuses() { ! dp_fig "$@" >/dev/null 2>&1; }
gone() { ! exists "$1"; }
marked() { [[ "$(dp_fig state "$1" -j | jq -r .mantra)" == SUCCEEDED:* ]]; }
in_context() { dp_fig show "$1" -n 1 2>/dev/null | sed 's/\x1b\[[0-9;]*m//g' | grep -qF "$2"; }
takes() { dp_fig send -f --id "$1" -- "$2" >/dev/null 2>&1; }
reads() { dp_fig show "$1" -n 2 >/dev/null 2>&1; }
answered() { [ -z "${REAL:-}" ] || { dp_wait_idle "$1" >/dev/null && in_context "$1" SEATED; }; }

ROWS=$UNIT_DIR/rows.json
rows() { dp_fig ls -j > "$ROWS"; }
row() { jq -c --arg id "$1" '.[] | select(.id == $id)' "$ROWS"; }
field() { jq -r --arg id "$1" --arg f "$2" '.[] | select(.id == $id) | .[$f] // ""' "$ROWS"; }
kids() { jq -r --arg id "$1" '.[] | select(.present == $id and (.kind // "conversation") == "conversation") | .id' "$ROWS" | sort; }
exists() { [ -n "$(row "$1")" ]; }
role_by_name() { jq -r --arg n "$1" '[.[] | select(.name == $n and (.target_aria // "") != "")][0].id // ""' "$ROWS"; }
target() { field "$1" target_aria; }

# The fork inherits system.provider; on this copy every predecessor answers to
# the fake gateway, so no turn can reach a real provider.
fake_provider() {
  [ -n "${REAL:-}" ] && return 0
  dp_fig set --id "$1" system.provider gateway >/dev/null 2>&1
  dp_fig set --id "$1" system.model auto >/dev/null 2>&1
}

succeed_json() { dp_fig succeed -j "$@" 2>$UNIT_DIR/succeed.err; }

scenario_fork_and_retire() {
  local name="$1"
  echo "== $name: fork at turn 2, retire with a ttl"
  rows
  local role old was_present before
  role="$(role_by_name "$name")"
  old="$(target "$role")"
  [ -n "$role" ] && [ -n "$old" ] || { fail "$name: no such role in the snapshot"; return; }
  was_present="$(field "$old" present)"
  before="$(kids "$old")"
  echo "   role $role -> $old, $(echo "$before" | grep -c .) drawn children"
  fake_provider "$old"

  local rep new
  rep="$(succeed_json "$role" --title "$name II" --ttl 30m -- "$SAY")"
  echo "   $(echo "$rep" | jq -c '{successor, demoted_under, prompted, steered, cast, dropped, abdicated, warnings}')"
  new="$(echo "$rep" | jq -r .successor)"
  dp_wait_idle "$new" >/dev/null
  rows
  check "role points at the successor" [ "$(target "$role")" = "$new" ]
  check "successor's mantra is the title" [ "$(field "$new" mantra)" = "$name II" ]
  check "successor drawn where the predecessor was" [ "$(field "$new" present)" = "$was_present" ]
  check "predecessor drawn under the successor" [ "$(field "$old" present)" = "$new" ]
  check "predecessor is a leaf" [ -z "$(kids "$old")" ]
  check "predecessor mantra marked" marked "$old"
  check "predecessor retired" [ "$(field "$old" retired_at)" != "" ]
  check "predecessor carries a ttl" [ "$(field "$old" ttl_ms)" != "" ]
  local k missing=0 moved=0
  for k in $before; do
    exists "$k" || missing=$((missing + 1))
    [ "$(field "$k" present)" = "$old" ] && moved=$((moved + 1))
  done
  check "every former child survives" [ "$missing" = 0 ]
  check "no former child is drawn under the predecessor" [ "$moved" = 0 ]
  check "the prompt is in the successor's context" in_context "$new" DETAES
  check "the successor answered" answered "$new"
  check "the retired predecessor refuses a prompt" refuses send -f --id "$old" -- hello
}

scenario_kill() {
  local name="$1"
  echo "== $name: --ttl 0 kills the predecessor, its branches survive"
  rows
  local role old before
  role="$(role_by_name "$name")"
  old="$(target "$role")"
  before="$(kids "$old")"
  echo "   role $role -> $old, $(echo "$before" | grep -c .) drawn children"
  fake_provider "$old"
  local rep new
  rep="$(succeed_json "$role" --ttl 0 -- "$SAY")"
  echo "   $(echo "$rep" | jq -c '{successor, killed, failed_at, error, warnings}')"
  new="$(echo "$rep" | jq -r .successor)"
  rows
  check "predecessor is gone" gone "$old"
  check "role points at the successor" [ "$(target "$role")" = "$new" ]
  local k missing=0
  for k in $before; do exists "$k" || missing=$((missing + 1)); done
  check "every former child survives the kill" [ "$missing" = 0 ]
  check "the successor still reads its history" reads "$new"
  check "the successor answered" answered "$new"
}

scenario_linger() {
  local role="$1"
  echo "== $role: --abdication linger at --turn 5"
  rows
  local old; old="$(target "$role")"
  fake_provider "$old"
  local rep new
  rep="$(succeed_json "$role" --turn 5 --abdication linger -- "$SAY")"
  echo "   $(echo "$rep" | jq -c '{successor, abdication, failed_at, error}')"
  new="$(echo "$rep" | jq -r .successor)"
  rows
  check "role points at the successor" [ "$(target "$role")" = "$new" ]
  check "lingering predecessor not retired" [ -z "$(field "$old" retired_at)" ]
  check "the successor answered" answered "$new"
  check "lingering predecessor still takes a prompt" takes "$old" "still there"
}

scenario_named_successor_mid_turn() {
  local role="$1"
  echo "== $role: --successor that is mid-turn"
  rows
  local old; old="$(target "$role")"
  local heir; heir="$(dp_new)"
  dp_delay 4
  dp_fig send -f --id "$heir" -- "$BUSY" >/dev/null 2>&1
  sleep 1
  local rep
  rep="$(succeed_json "$role" --successor "$heir" --title heir -- "$SAY")"
  dp_delay 0
  echo "   $(echo "$rep" | jq -c '{successor, forked, steered, cast, failed_at, error}')"
  dp_wait_idle "$heir" >/dev/null
  rows
  check "no fork was made" [ "$(echo "$rep" | jq -r '.forked // false')" = false ]
  check "the busy successor was steered" [ "$(echo "$rep" | jq -r '.steered // false')" = true ]
  check "role points at the named successor" [ "$(target "$role")" = "$heir" ]
  check "named successor took the title" [ "$(field "$heir" mantra)" = heir ]
  check "the steered successor answered" answered "$heir"
}

scenario_refusals() {
  echo "== refusals"
  rows
  local dangling="" live tgt r
  for r in $(jq -r '.[] | select((.target_aria // "") != "") | .id' "$ROWS"); do
    exists "$(target "$r")" || { dangling="$r"; break; }
  done
  live="$(role_by_name "barone-lead")"
  tgt="$(target "$live")"
  check "a role whose target is gone" refuses succeed "$dangling" -- go
  check "the current target as its own successor" refuses succeed "$live" --successor "$tgt" -- go
  check "an aria where the role goes" refuses succeed "$tgt" -- go
  check "a successor that does not exist" refuses succeed "$live" --successor deadbeefdeadbeef -- go
  rows
  check "a refusal changed nothing" [ "$(target "$live")" = "$tgt" ]
}

# Two roles share this predecessor; the one not succeeded keeps pointing at it
# and holds it from the reap, which the report must say.
scenario_shared_predecessor() {
  local name="$1"
  echo "== $name: a predecessor another role also targets, forked at --turn 1"
  rows
  local role old
  role="$(role_by_name "$name")"
  old="$(target "$role")"
  fake_provider "$old"
  local rep new
  rep="$(succeed_json "$role" --turn 1 -- "$SAY")"
  echo "   $(echo "$rep" | jq -c '{successor, demoted_under, warnings, failed_at, error}')"
  new="$(echo "$rep" | jq -r .successor)"
  rows
  check "role points at the successor" [ "$(target "$role")" = "$new" ]
  check "the other role is named" warned "$rep" "also targets"
  check "the predecessor is a leaf" [ -z "$(kids "$old")" ]
  check "the successor answered" answered "$new"
}

warned() { echo "$1" | jq -e --arg w "$2" '.warnings // [] | any(contains($w))' >/dev/null; }
handle() { field "$1" handle; }
on_screen() { dp_cap | grep -qF "$1"; }
wait_screen() { local i; for i in $(seq 60); do on_screen "$1" && return 0; sleep 0.5; done; return 1; }

# The pager follows the successor when the predecessor is on screen; --stay
# keeps it. Typed one byte per read into the ':' line of a real pager.
scenario_pager() {
  local name="$1" stay="$2" tag="$3"
  echo "== $name: :succeed in the pager${stay:+ with $stay}"
  rows
  local role old
  role="$(role_by_name "$name")"
  old="$(target "$role")"
  fake_provider "$old"
  local was; was="$(handle "$old")"
  dp_pane 120 40 "$tag" >/dev/null
  dp_run "figaro listen $old"
  wait_screen "$was" || { fail "$name: the pager never showed $was"; return; }
  dp_stable
  dp_type ":succeed $role $stay --title pager-heir -- $SAY"
  dp_key Enter
  local i new=""
  for i in $(seq 60); do
    rows
    new="$(target "$role")"
    [ "$new" != "$old" ] && break
    sleep 0.5
  done
  check "the role moved" [ "$new" != "$old" ]
  local heir; heir="$(handle "$new")"
  if [ -z "$stay" ]; then
    check "the pager shows the successor $heir" wait_screen "$heir"
  else
    sleep 2
    dp_stable
    check "--stay keeps the predecessor on screen" on_screen "$was"
  fi
  dp_type ":no"; dp_key Enter
  check "the succession is in the pager's notifications" wait_screen "succeeds"
  check "the successor answered" answered "$new"
  dp_cap | grep -F "succeeds" | head -1 | sed 's/^ */   | /'
  dp_key q
}

run() { [ -z "${ONLY:-}" ] || [[ "$1" == *"$ONLY"* ]] && "$@"; }
run scenario_refusals
run scenario_fork_and_retire "Schoolmaster"
run scenario_kill "King of Spain"
run scenario_linger "$(rows; jq -r '[.[] | select(.name == "paperd-jack-cs7641-notes" and (.target_aria // "") != "")] | map(.id) | .[]' "$ROWS" | while read -r r; do t="$(target "$r")"; [ "$(field "$t" kind)" = conversation ] && [ "$(field "$t" message_count)" -gt 20 ] && { echo "$r"; break; }; done)"
run scenario_named_successor_mid_turn "$(rows; role_by_name "barone-lead")"
run scenario_shared_predecessor "il-maestro"
run scenario_pager "capture-stack keeper" "" pager
run scenario_pager "form-work-lead" "--stay" stay
echo
if [ "$FAILS" = 0 ]; then echo "succeed-e2e: all checks passed"; else echo "succeed-e2e: $FAILS check(s) failed"; fi
dp_down >/dev/null
exit "$FAILS"
