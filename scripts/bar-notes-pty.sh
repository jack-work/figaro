#!/usr/bin/env bash
# bar-notes-pty.sh: a one-line note rides the status bar beside "sent" instead
# of opening a row of its own. Real dev-shell binary, isolated pane, fake LLM.
#
#   scripts/bar-notes-pty.sh            # asserts and reports
#   KEEP=1 scripts/bar-notes-pty.sh     # leave the unit up for a look
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1
source "$ROOT/scripts/devpane.sh"

FAIL=0
ok() { echo "  ok   $1"; }
bad() { echo "  FAIL $1"; FAIL=1; }

dp_init barnotes || exit 1
trap '[ -n "${KEEP:-}" ] || dp_down' EXIT

ARIA="$(dp_new)" || exit 1
dp_reply default <<'EOF'
Noted.
EOF
for q in "first question" "second question"; do
  dp_fig send -f --id "$ARIA" -- "$q" >/dev/null
  dp_wait_idle "$ARIA" || exit 1
done

dp_pane 110 24 barnotes || exit 1
dp_run figaro listen "$ARIA" || exit 1
dp_stable

bar() { dp_cap | grep -v '^\s*$' | tail -1; }
above() { dp_cap | grep -v '^\s*$' | tail -2 | head -1; }

echo "-- a long one-line note: :fork --stay -- go"
dp_key ':'
dp_type 'fork --stay -- go'
dp_key Enter
for i in $(seq 40); do sleep 0.25; bar | grep -q 'forked' && break; done
dp_stable
B1="$(bar)"; A1="$(above)"
echo "       bar:   $B1"
echo "       above: $A1"
case "$B1" in *forked*) ok "the fork note is on the bar";; *) bad "the fork note is not on the bar";; esac
case "$A1" in *forked*) bad "the fork note also took a row above the bar";; *) ok "no row of its own";; esac

echo "-- then sent, from the composer"
dp_key '>'
dp_type 'hello there'
dp_key C-s
for i in $(seq 40); do sleep 0.25; bar | grep -q '^sent' && break; done
dp_stable
B2="$(bar)"
echo "       bar:   $B2"
case "$B2" in "sent · forked"*) ok "sent leads, the fork note follows it";; *) bad "expected 'sent · forked…' at the start of the bar";; esac
W=$(printf '%s' "$B2" | python3 -c 'import sys,unicodedata; s=sys.stdin.read(); print(sum(2 if unicodedata.east_asian_width(c) in "WF" else 1 for c in s))')
[ "$W" -le 110 ] && ok "the bar fits its 110 columns ($W)" || bad "the bar is $W columns in a 110-column pane"

echo "-- both are in the notifications history"
dp_key ' '
dp_key n
dp_stable
dp_cap > "$DP_DIR/cap-notes.txt"
grep -q "forked" "$DP_DIR/cap-notes.txt" && ok "the fork note is in the history" || bad "the fork note is missing from the history"
grep -q "sent" "$DP_DIR/cap-notes.txt" && ok "sent is in the history" || bad "sent is missing from the history"

echo
if [ "$FAIL" = 0 ]; then echo "bar-notes-pty: PASS"; else echo "bar-notes-pty: FAIL"; fi
[ -n "${KEEP:-}" ] && echo "bar-notes-pty: unit kept at $DP_DIR (tmux -S $DP_SOCK attach -t $DP_SESS)"
exit "$FAIL"
