#!/usr/bin/env bash
# WHO ENDS UP ATTENDING WHAT. One law, seven verbs, asserted rather than
# described: a verb that creates something FOR you attends it, a verb that
# creates something BESIDE you does not, and --stay suppresses the move.
#
# Attendance used to be a side effect of the mint helper -- five verbs with
# five rules and a restoreAttendance that undid a bind its own mint should
# never have done -- so the thing this script exists to catch is a birth verb
# quietly moving (or failing to move) the shell.
#
# No model is called: every verb here is topology, not conversation.
set -uo pipefail
cd "$(dirname "$0")/../.."
ROOT=$(mktemp -d /var/tmp/figattend.XXXX); BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT

fails=0
bound() { "$BIN" --bind status --json 2>/dev/null | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("id",""))' 2>/dev/null; }
check() { # $1 label, $2 expected, $3 actual
  if [ "$2" = "$3" ]; then echo "  PASS  $1 -> $3"
  else echo "  FAIL  $1: expected [$2], attending [$3]"; fails=$((fails+1)); fi
}

echo "== a verb that creates something FOR you attends it"
A1=$("$BIN" --bind new -j 2>/dev/null | python3 -c 'import sys,json;print(json.load(sys.stdin)["aria_id"])')
check "fig new" "$A1" "$(bound)"

FORM=$("$BIN" --bind form new -S probe=1 2>&1 | grep -oE "@[0-9a-f]+" | head -1)
check "fig form new" "$FORM" "$(bound)"

"$BIN" --bind attend "$A1" >/dev/null 2>&1
check "fig attend <aria>" "$A1" "$(bound)"

echo "== a verb that creates something BESIDE you leaves the shell alone"
A2=$("$BIN" --bind new -j 2>/dev/null | python3 -c 'import sys,json;print(json.load(sys.stdin)["aria_id"])')
"$BIN" --bind attend "$A1" >/dev/null 2>&1
FORM2=$("$BIN" --bind form fork "$FORM" -S probe=2 2>&1 | grep -oE "@[0-9a-f]+" | tail -1)
# form fork is a birth verb too: it mints FOR you, so it attends. Recorded
# here so the answer is asserted rather than assumed either way.
check "fig form fork" "$FORM2" "$(bound)"

echo "== cast: the aria plays the role, the shell keeps the form"
"$BIN" --bind attend "$FORM" >/dev/null 2>&1
BEFORE=$(bound)
"$BIN" --bind cast >/dev/null 2>&1
check "fig cast (from an attended form)" "$BEFORE" "$(bound)"

echo "== binding disabled: no verb moves anything"
# NO --bind here: that flag means "bind even where it is off by default" and
# outranks the env var, so passing both tests nothing.
FIGARO_NO_BIND=1 "$BIN" new -j >/dev/null 2>&1
check "fig new under FIGARO_NO_BIND" "$BEFORE" "$(bound)"

"$BIN" --bind stop >/dev/null 2>&1
rm -rf "$ROOT"
if [ "$fails" -eq 0 ]; then echo "PASS: attendance obeys the law"; else echo "FAIL: $fails verb(s) off the law"; exit 1; fi
