# Does the fig IR write path write translations WHEN AN ENTRY LANDS, in a real
# daemon, against a real provider?
#
# Everything about that path is unit-tested with a fake encoder, and a green
# unit suite is not evidence about the product: the wiring lives in
# internal/cli, the encoders come from the registry, and the accessors are
# built per aria. None of that is exercised by a test.
#
# THE DISCRIMINATOR, because counting cannot tell two writers apart: an entry
# that lands with NO SEND. `fig study` appends a fig IR record and speaks to no
# provider, so a translation naming that LT can only have been written as the
# entry landed -- no catch-up can have run, because nothing sent.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/layered
ROOT=$(mktemp -d /var/tmp/figonappend.XXXX); BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT

ARIA=$("$BIN" new 2>&1 | grep -oE "[0-9a-f]{8}" | head -1)
echo "aria $ARIA"

# THE NODE, NOT THE STORE. Every aria and every form has its own directory
# under ir/ and translations-v2/<provider>/; summing across them measures
# other arias, which is the first way this probe was wrong.
node_dir() { find "$ROOT/state/arias/ir" -maxdepth 1 -mindepth 1 -type d | while read -r d; do
    if grep -lq "$1" "$d"/*.jsonl 2>/dev/null; then echo "$d"; return; fi
  done; }

ir_lts()    { cat "$1"/*.jsonl 2>/dev/null | grep -oE '"_idx":[0-9]+' | cut -d: -f2 | tr '\n' ' '; }
trans_mains() { cat "$ROOT/state/arias/translations-v2/anthropic/$(basename "$1")"/*.jsonl 2>/dev/null |
                grep -oE '"m":[0-9]+' | cut -d: -f2 | tr '\n' ' '; }

echo "--- one send, so the aria has a translator channel at all"
# ASSERT THE SEND, DO NOT DISCARD IT. This step ran into /dev/null for a whole
# campaign, and on 2026-08-20 it had been FAILING with "empty context" while
# this script printed PASS: the study it asserts on does not need a provider.
# A live script that throws away the outcome of a real verb is a unit test with
# a daemon attached.
SEND_OUT=$("$BIN" send --id "$ARIA" -- "reply with exactly: one" 2>&1)
case "$SEND_OUT" in
  *"completed ✓"*) echo "    send: completed" ;;
  *) echo "FAIL: the send did not complete: $(echo "$SEND_OUT" | tr -d '\n' | tail -c 200)"
     echo "root: $ROOT (delete it)"; exit 1 ;;
esac
NODE=$(node_dir "reply with exactly: one")
[ -z "$NODE" ] && { echo "FAIL: could not find the aria's node dir"; exit 1; }
echo "    node $(basename "$NODE")"
echo "    fig IR LTs:            $(ir_lts "$NODE")"
echo "    translations name LTs: $(trans_mains "$NODE")"

echo
echo "--- THE PROBE: study a form. One fig IR entry lands. NOTHING SENDS."
FORM=$("$BIN" form new --set probe=1 2>&1 | grep -oE "@[0-9a-f]+" | head -1)
echo "    form $FORM"
"$BIN" study "$ARIA" "$FORM" 2>&1 | head -2
sleep 1
IR_AFTER=$(ir_lts "$NODE"); TR_AFTER=$(trans_mains "$NODE")
echo "    fig IR LTs:            $IR_AFTER"
echo "    translations name LTs: $TR_AFTER"

LAST_IR=$(echo "$IR_AFTER" | tr ' ' '\n' | grep . | tail -1)
echo
if echo "$TR_AFTER" | tr ' ' '\n' | grep -qx "$LAST_IR"; then
  echo "PASS: fig IR entry $LAST_IR landed with no send AND has a translation."
  echo "      Only the write path can have written it."
else
  echo "FAIL: fig IR entry $LAST_IR has no translation, so translations still"
  echo "      wait for a send."
fi
echo
echo "root: $ROOT (delete it)"
