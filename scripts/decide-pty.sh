#!/usr/bin/env bash
# decide-pty.sh: drive `figaro decide` end to end in a REAL pty: the composer's
# ?? line, the options line, :decide, the decision pit, the CLI verb and the
# system.model refusal, against a fake LLM (devpane's prose gateway) and the
# fake System One endpoint, inside the dev shell.
#
#   scripts/decide-pty.sh            # asserts and reports
#   KEEP=1 scripts/decide-pty.sh     # leave the unit up for a look
#
# Read skills/figaro/contributing/isolated-pane.md first.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1
source "$ROOT/scripts/devpane.sh"

FAIL=0
ok() { echo "  ok   $1"; }
bad() { echo "  FAIL $1"; FAIL=1; }
has() { if grep -qF -- "$2" "$3"; then ok "$1"; else bad "$1 (no '$2')"; fi; }
hasnt() { if grep -qF -- "$2" "$3"; then bad "$1 ('$2' present)"; else ok "$1"; fi; }

dp_init decide || exit 1

S1_PORT="$(dp_freeport)"
S1_LOG="$DP_DIR/systemone.jsonl"
: > "$S1_LOG"
python3 "$ROOT/scripts/fake-systemone.py" "$S1_PORT" "$S1_LOG" &
S1_PID=$!
trap 'kill $S1_PID 2>/dev/null; [ -n "${KEEP:-}" ] || dp_down' EXIT
cat > "$DP_DIR/config/providers/fake1.toml" <<EOF
preset   = "gateway"
base_url = "http://127.0.0.1:$S1_PORT/v1"
EOF
printf 'decide = { provider = "fake1", model = "jev-latest" }\n' >> "$DP_DIR/config/outfits/pane.toml"
echo "decide-pty: systemone :$S1_PORT (pid $S1_PID)"

turns() { wc -l < "$DP_DIR/requests.jsonl" 2>/dev/null | tr -d ' '; }
decisions() { wc -l < "$S1_LOG" | tr -d ' '; }

ARIA="$(dp_new)" || exit 1
echo "decide-pty: aria $ARIA"
dp_reply 1 <<'EOF'
The retry loop calls charge() again on a 500 and the handler has no idempotency key, so a retried request charges twice.

The fix is a key per request, stored with the charge, so a retry finds the first attempt and returns its result.

A queue would also work, but it moves the failure somewhere harder to see.

Doing nothing leaves the double charge in place for every 500 the processor returns.
EOF
dp_fig send -f --id "$ARIA" -- "why do we double-charge?" >/dev/null
dp_wait_idle "$ARIA" || exit 1
dp_reply default <<'EOF'
Noted.

Anything else before we pick one?
EOF
for q in "what does the processor return on a timeout?" "and on a 409?"; do
  dp_fig send -f --id "$ARIA" -- "$q" >/dev/null
  dp_wait_idle "$ARIA" || exit 1
done
T0="$(turns)"

echo "-- the system.model guard:"
if dp_fig set --id "$ARIA" system.model jev-latest > "$DP_DIR/set.out" 2>&1; then
  bad "system.model accepted a decision model"
else
  has "system.model refuses jev and names the verb" "figaro decide" "$DP_DIR/set.out"
fi

echo "-- the CLI verb, bound by --id:"
dp_fig decide --id "$ARIA" -C="idempotency-key","queue the charge",nothing -- which fix? > "$DP_DIR/cli.out" 2>&1
has "the winner is marked" "idempotency-key" "$DP_DIR/cli.out"
has "the marker is on it" "←" "$DP_DIR/cli.out"
has "a quoted option kept its space" "queue the charge" "$DP_DIR/cli.out"
has "the footer counts the window" "messages" "$DP_DIR/cli.out"

echo "-- the CLI verb, stateless under -A:"
dp_fig -A decide --context "a deploy log with no errors" -- did the deploy fail? > "$DP_DIR/stateless.out" 2>&1
has "a bare question is a yes/no" "yes" "$DP_DIR/stateless.out"

dp_pane 100 26 decide || exit 1
dp_run figaro listen "$ARIA" || exit 1
dp_stable

echo "-- composer: a bare ?? opens the options line:"
D0="$(decisions)"
dp_key '>'
dp_type 'which fix?'
dp_key Enter
dp_type '??'
dp_key C-s
dp_stable
dp_cap > "$DP_DIR/cap-options.txt"
has "the options line names the question" '?? which fix?' "$DP_DIR/cap-options.txt"
has "it says how to separate options" 'comma separated' "$DP_DIR/cap-options.txt"
[ "$(decisions)" = "$D0" ] && ok "nothing went out yet" || bad "a decision went out before the options"

dp_type 'idempotency-key, queue, nothing'
dp_key C-s
sleep 1
dp_stable
dp_cap > "$DP_DIR/cap-pit.txt"
echo "-- the decision pit:"
has "the winner is shown" 'idempotency-key' "$DP_DIR/cap-pit.txt"
has "it carries the marker" '←' "$DP_DIR/cap-pit.txt"
has "every option is scored" '0.14' "$DP_DIR/cap-pit.txt"
has "confidence is reported" 'confidence 0.81' "$DP_DIR/cap-pit.txt"
has "the bar says what was decided" 'decided: idempotency-key' "$DP_DIR/cap-pit.txt"

echo "-- the pit has the focus, and every option is a row:"
washed() { dp_raw | grep -F '48;5;240' | sed 's/\x1b\[[0-9;]*[A-Za-z]//g'; }
washed > "$DP_DIR/wash-0.txt"
has "the cursor starts on the preferred option" 'idempotency-key' "$DP_DIR/wash-0.txt"
dp_key j; dp_stable; washed > "$DP_DIR/wash-j.txt"
has "j moves to the next option" 'queue' "$DP_DIR/wash-j.txt"
dp_key y; dp_stable; dp_cap > "$DP_DIR/cap-yank.txt"
has "y copies the option under the cursor" 'yanked queue' "$DP_DIR/cap-yank.txt"
dp_key T; dp_stable; P0="$(dp_cap | grep -o '[0-9]*–[0-9]*/[0-9]*' | tail -1)"
dp_key k; dp_stable; P1="$(dp_cap | grep -o '[0-9]*–[0-9]*/[0-9]*' | tail -1)"; washed > "$DP_DIR/wash-Tk.txt"
has "after T, k leaves the pit alone" 'queue' "$DP_DIR/wash-Tk.txt"
[ "$P0" != "$P1" ] && ok "after T, k scrolls the conversation ($P0 -> $P1)" || bad "after T, k did not scroll the conversation ($P0)"
dp_key T; dp_key k; dp_stable; washed > "$DP_DIR/wash-TTk.txt"
has "T again gives the pit the keys back" 'idempotency-key' "$DP_DIR/wash-TTk.txt"

echo "-- D reopens and closes it:"
dp_key Escape
dp_stable
dp_cap > "$DP_DIR/cap-closed.txt"
hasnt "Esc closed the pit" '?? which fix?' "$DP_DIR/cap-closed.txt"
dp_key D
dp_stable
dp_cap > "$DP_DIR/cap-D.txt"
has "D brings the last decision back" '?? which fix?' "$DP_DIR/cap-D.txt"
washed > "$DP_DIR/wash-D.txt"
has "with the focus on the preferred option" 'idempotency-key' "$DP_DIR/wash-D.txt"
dp_key D
dp_stable
dp_cap > "$DP_DIR/cap-DD.txt"
hasnt "D again closes it" '?? which fix?' "$DP_DIR/cap-DD.txt"

echo "-- :decide replaces the decision:"
dp_stable
dp_key ':'
dp_type 'decide -C=yes,no -- is a retried charge billed twice?'
dp_key Enter
sleep 1
dp_stable
dp_cap > "$DP_DIR/cap-second.txt"
has "the new question is shown" 'billed twice' "$DP_DIR/cap-second.txt"
hasnt "the old decision is gone" 'idempotency-key' "$DP_DIR/cap-second.txt"
has "yes/no reads as yes" 'yes' "$DP_DIR/cap-second.txt"

echo "-- a ?? draft is not a turn:"
[ "$(turns)" = "$T0" ] && ok "the LLM saw no new request ($T0)" || bad "the LLM was sent $(( $(turns) - T0 )) request(s)"
dp_fig show "$ARIA" > "$DP_DIR/show.txt" 2>&1
hasnt "the transcript holds no decision" 'which fix?' "$DP_DIR/show.txt"

echo "-- what the endpoint was sent:"
python3 - "$S1_LOG" <<'PY'
import json, sys
rows = [json.loads(l)["body"] for l in open(sys.argv[1]) if l.strip()]
fails = 0
def check(ok, what):
    global fails
    print(("  ok   " if ok else "  FAIL ") + what)
    fails += 0 if ok else 1
check(len(rows) >= 4, f"{len(rows)} decisions recorded")
check(all(set(b) <= {"model", "state", "questions"} for b in rows), "every body is closed")
pane = [b for b in rows if (b["questions"]["decision"].get("instructions") == "which fix?" and len(b["questions"]["decision"].get("criteria") or {}) == 3)]
check(len(pane) >= 1, "the composer's options became three criteria")
st = pane[-1]["state"] if pane else {}
convo = st.get("conversation", []) if isinstance(st, dict) else []
check(any("double-charge" in c for c in convo), "the aria's conversation reached the state")
check(not any("which fix?" in c for c in convo), "the question did not leak into the conversation")
stateless = [b for b in rows if "deploy" in json.dumps(b)]
check(bool(stateless) and isinstance(stateless[-1]["state"], dict) and "conversation" not in stateless[-1]["state"], "-A sent no conversation")
sys.exit(1 if fails else 0)
PY
[ $? = 0 ] || FAIL=1

echo
if [ "$FAIL" = 0 ]; then echo "decide-pty: PASS"; else echo "decide-pty: FAIL"; fi
[ -n "${KEEP:-}" ] && echo "decide-pty: unit kept at $DP_DIR (tmux -S $DP_SOCK attach -t $DP_SESS)"
exit "$FAIL"
