#!/usr/bin/env bash
# AD HOC REGRESSION for the api-coherence branch, run against a REAL daemon on
# an isolated store, in the nix devshell. It exercises the seams this branch
# cut, and it asserts rather than prints:
#
#   1. the collapsed READS -- one name, both doors, live AND dormant
#   2. the SDK's two clients, through the CLI verbs that use them
#   3. attendance, per verb (attendlive.sh covers this in full)
#   4. a real turn, so the whole stack is proven end to end
#
# The daemon here is the one BUILT FROM THIS TREE, not the installed one:
# a stale derivation answering on a familiar socket is the classic way to
# prove nothing (see skills/figaro, and the parent aria's restart storm).
set -uo pipefail
cd "$(dirname "$0")/../.."
ROOT=$(mktemp -d /var/tmp/figregress.XXXX); BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT

fails=0
ok()   { echo "  PASS  $1"; }
bad()  { echo "  FAIL  $1"; fails=$((fails+1)); }
has()  { if echo "$2" | grep -q "$3"; then ok "$1"; else bad "$1: got [$(echo "$2"|tr -d '\n'|tail -c 160)]"; fi; }

echo "== the daemon is THIS tree's build"
"$BIN" --bind ls >/dev/null 2>&1
V=$("$BIN" --version 2>&1 | head -1)
echo "     $V"

echo "== a turn (the whole stack, live)"
A=$("$BIN" --bind new -j 2>/dev/null | python3 -c 'import sys,json;print(json.load(sys.stdin)["aria_id"])')
OUT=$("$BIN" --bind send --id "$A" -- 'reply with exactly: PERUKE' 2>&1)
has "send completes" "$OUT" "completed"
has "the model answered" "$OUT" "PERUKE"

echo "== reads while the aria is LIVE (routed to the agent)"
has "show"     "$("$BIN" --bind show --id "$A" 2>&1)"        "PERUKE"
has "state"    "$("$BIN" --bind state --id "$A" -j 2>&1)"    "system"
has "status"   "$("$BIN" --bind status --id "$A" 2>&1)"      "$A"

echo "== the SAME reads while DORMANT (routed to the store, nothing woken)"
# STOP the daemon, do not kill the aria: kill is a DELETION (topology_handlers
# says so and the store agrees -- an early draft of this script asserted
# against a trunk it had just removed and blamed the router). A restarted
# daemon has the aria on disk and no agent for it, which is the case the
# collapsed read surface exists to serve.
"$BIN" stop >/dev/null 2>&1
sleep 1
has "show (dormant)"   "$("$BIN" --bind show --id "$A" 2>&1)"     "PERUKE"
has "state (dormant)"  "$("$BIN" --bind state --id "$A" -j 2>&1)" "system"
has "status (dormant)" "$("$BIN" --bind status --id "$A" 2>&1)"   "$A"
LIVE=$("$BIN" --bind ls 2>&1 | grep -c "$A" || true)
[ "$LIVE" -ge 1 ] && ok "the aria still lists after the read" || bad "the aria vanished from ls"

echo "== forms, study, cast (the SDK's other door)"
F=$("$BIN" --bind form new -S name=regress 2>&1 | grep -oE "@[0-9a-f]+" | head -1)
has "form new mints" "$F" "@"
"$BIN" --bind set --id "$F" note hello >/dev/null 2>&1
has "form state round-trips" "$("$BIN" --bind state --id "$F" -j 2>&1)" "hello"
"$BIN" --bind attend "$A" >/dev/null 2>&1
"$BIN" --bind study "$A" "$F" >/dev/null 2>&1
has "study lands on the board" "$("$BIN" --bind state --id "$A" -j 2>&1)" "studies"

echo "== fork (topology through the collapsed surface)"
B=$("$BIN" --bind fork "$A" 2>&1 | grep -oE "\b[0-9a-f]{8}\b" | tail -1)
has "fork mints a branch" "$B" "[0-9a-f]"
has "the branch carries the history" "$("$BIN" --bind show --id "$B" 2>&1)" "PERUKE"

"$BIN" stop >/dev/null 2>&1
rm -rf "$ROOT"
echo
if [ "$fails" -eq 0 ]; then echo "PASS: the api-coherence branch serves a real session"; else echo "FAIL: $fails check(s)"; exit 1; fi
