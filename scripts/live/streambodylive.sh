# Does a real endpoint accept a request body with NO CONTENT-LENGTH, and are
# the bytes the same as the buffered framing puts on the wire?
#
# Part III of plans/delta-seam.md called the first question "the risk that
# decides feasibility" and said it is a short experiment against each
# endpoint, not an argument. This is that experiment, plus the oracle the unit
# test cannot reach: the SAME send under both framings, with figaro's own wire
# dump on, diffed byte for byte.
#
# It costs two real API calls.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/layered
ROOT=$(mktemp -d /var/tmp/figstream.XXXX); BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt" "$ROOT/wire-off" "$ROOT/wire-on"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT

PROMPT="reply with exactly: one"

ARIAS=""
send_under() { # $1 = wire dir, $2 = streaming on/off
  ARIA=$("$BIN" new 2>&1 | grep -oE "[0-9a-f]{8}" | head -1)
  ARIAS="$ARIAS $ARIA"
  "$BIN" set --id "$ARIA" system.environment.figaro_wire_dir "$1" >/dev/null 2>&1
  "$BIN" set --id "$ARIA" system.stream_request_body "$2" >/dev/null 2>&1
  echo "    aria $ARIA  stream_request_body=$2"
  OUT=$("$BIN" send --id "$ARIA" -- "$PROMPT" 2>&1)
  case "$OUT" in
    *"completed ✓"*) echo "    THE ENDPOINT ACCEPTED IT: $(echo "$OUT" | tr -d '\n' | tail -c 60)" ;;
    *) echo "    FAILED: $(echo "$OUT" | tr -d '\n' | tail -c 200)" ;;
  esac
}

echo "--- BUFFERED (the framing that ships today)"
send_under "$ROOT/wire-off" false
echo
echo "--- STREAMED (no Content-Length)"
send_under "$ROOT/wire-on" true

req_of() { find "$1" -name '*.req.http' | sort | head -1; }
OFF=$(req_of "$ROOT/wire-off"); ON=$(req_of "$ROOT/wire-on")
echo
echo "--- THE WIRE"
[ -z "$OFF" ] || [ -z "$ON" ] && { echo "FAIL: no wire dump (off=$OFF on=$ON)"; echo "root: $ROOT"; exit 1; }
echo "    buffered: $(wc -c <"$OFF") bytes    streamed: $(wc -c <"$ON") bytes"

# The bodies, not the headers: everything after the blank line. Two different
# arias carry their own ids in their own reminders, so those are normalized --
# EVERYTHING ELSE MUST MATCH. (Whether a Content-Length rides the wire is a
# unit test, TestTheFramingsDifferOnlyInTheirLength: net/http sets that field
# at write time and it is not in the dumped header block.)
body_of() { awk 'BEGIN{b=0} b{print} /^\r?$/{b=1}' "$1" | sed -E "s/$(echo $ARIAS | cut -d" " -f1)|$(echo $ARIAS | cut -d" " -f2)/ARIA/g"; }
if diff <(body_of "$OFF") <(body_of "$ON") >"$ROOT/body.diff"; then
  echo "PASS: the two framings put the SAME BYTES on the wire."
else
  echo "DIFFER: $(wc -l <"$ROOT/body.diff") lines -- see $ROOT/body.diff"
  head -20 "$ROOT/body.diff"
fi

echo
echo "root: $ROOT (delete it)"
