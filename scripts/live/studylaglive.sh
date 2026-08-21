# Does a studied form's change reach the model on the NEXT send?
#
# Gluck, 2026-08-20, from a dev shell: "I set form values, cast it to a role,
# then set a role-purpose on the form. figaro did not see the role purpose."
#
# scripts/live/restartlive.sh already carries this defect as EXPECTED -- "the
# first one after a restart is known to be one behind" -- but it only ever
# observed it ACROSS A RESTART. This asks the question with no restart at all,
# which is Gluck's case.
#
# The provider is a LOCAL SINK, so the run is free and repeatable, and the
# assertion is on the REQUEST BODY: the only thing that decides whether the
# model saw it.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/layered
ROOT=$(mktemp -d /var/tmp/figstudylag.XXXX); BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt" "$ROOT/wire"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true

cat >"$ROOT/sink.go" <<'GO'
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	dir := os.Args[2]
	n := 0
	http.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		n++
		b, _ := io.ReadAll(r.Body)
		_ = os.WriteFile(fmt.Sprintf("%s/req-%02d.json", dir, n), b, 0o644)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for _, ev := range []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "%s\n\n", ev)
			w.(http.Flusher).Flush()
		}
	})
	panic(http.ListenAndServe(os.Args[1], nil))
}
GO
# A FREE PORT, CHOSEN NOT ASSUMED, and the sink must PROVE it started. A stale
# sink from an earlier run held a fixed port, the new one panicked with
# "address already in use", the sends went to the OLD process, and this script
# printed a confident FAIL about figaro three times running.
PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
go run "$ROOT/sink.go" "127.0.0.1:$PORT" "$ROOT/wire" >"$ROOT/sink.out" 2>&1 &
SINK=$!
trap 'kill $SINK 2>/dev/null' EXIT
for _ in $(seq 1 40); do
  curl -s -o /dev/null -m 1 "http://127.0.0.1:$PORT/v1/messages" -d '{}' && break
  sleep 0.5
done
if ! kill -0 $SINK 2>/dev/null || grep -qE "in use|panic" "$ROOT/sink.out" 2>/dev/null; then
  echo "FAIL: the sink did not start, so nothing below would be evidence:"
  head -3 "$ROOT/sink.out"; exit 1
fi
rm -f "$ROOT"/wire/req-*.json   # the health check is not a turn

export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
export ANTHROPIC_BASE_URL="http://127.0.0.1:$PORT/v1" ANTHROPIC_API_KEY="sink"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT

ARIA=$("$BIN" new 2>&1 | grep -oE "[0-9a-f]{8}" | head -1)
"$BIN" state set --id "$ARIA" system.use_official_sdk false >/dev/null 2>&1
FORM=$("$BIN" form new -S name=therole 2>&1 | grep -oE "@[0-9a-f]+" | head -1)
echo "aria $ARIA, role form $FORM"

echo
echo "--- GLUCK'S SEQUENCE: cast the aria into the role, THEN set role-purpose"
CAST_OUT=$("$BIN" cast "$ARIA" "$FORM" 2>&1); echo "    cast: $(echo "$CAST_OUT" | head -1)"
case "$CAST_OUT" in *error*|*unknown*) echo "FAIL: the cast did not happen, so nothing below is evidence"; echo "root: $ROOT"; exit 1;; esac
"$BIN" state set --id "$FORM" role-purpose "carry the razor" 2>&1 | tail -1 | cut -c1-90

echo
# ASSERT THE SEND. Discarding it is how onappendlive printed PASS for a whole
# campaign while its send failed, and I wrote that flaw again in this file.
# REQ is the request THIS send produced. Indexes are not used: the sink's
# counter does not reset and a health check already consumed one, which had
# these assertions reading the wrong file.
REQ=""
send_or_die() {
  OUT=$("$BIN" send --id "$ARIA" -- "$1" 2>&1)
  case "$OUT" in
    *"completed ✓"*) : ;;
    *) echo "FAIL: send ($1) did not complete: $(echo "$OUT" | tr -d '\n' | tail -c 200)"
       echo "root: $ROOT"; exit 1 ;;
  esac
  REQ=$(ls -t "$ROOT"/wire/req-*.json 2>/dev/null | head -1)
  if [ -z "$REQ" ]; then echo "FAIL: send ($1) produced no request"; echo "root: $ROOT"; exit 1; fi
  echo "    send ($1): completed -> $(basename "$REQ")"
}
carries() { n=$(grep -c "$2" "$1" 2>/dev/null); echo "${n:-0}"; }
echo "--- send #1, immediately after the patch"
send_or_die first;  A1=$(carries "$REQ" role-purpose)
echo "--- send #2, one turn later"
send_or_die second; A2=$(carries "$REQ" role-purpose)

echo
echo "    turn 1 after the patch: role-purpose x$A1"
echo "    turn 2 after the patch: role-purpose x$A2"
if [ "$A1" -gt 0 ]; then
  echo "ARM A PASS: with no restart, the patch reaches the model on the NEXT send."
elif [ "$A2" -gt 0 ]; then
  echo "ARM A LAG: one turn behind even with no restart."
else
  echo "ARM A FAIL: the change never arrived."
fi

# ---------------------------------------------------------------- ARM B
# The same question ACROSS A DAEMON RESTART, which is what
# scripts/live/restartlive.sh calls "the known lag" and carries as expected.
echo
echo "=== ARM B: the same patch, but the daemon restarts in between"
"$BIN" state set --id "$FORM" second-purpose "shave the town" >/dev/null 2>&1
"$BIN" stop >/dev/null 2>&1; sleep 2
send_or_die "after restart";  B1=$(carries "$REQ" second-purpose)
send_or_die "one turn later"; B2=$(carries "$REQ" second-purpose)
echo
echo "    turn 1 after the restart: second-purpose x$B1"
echo "    turn 2 after the restart: second-purpose x$B2"
if [ "$B1" -gt 0 ]; then
  echo "ARM B PASS: the restart costs nothing; the first turn carries it."
elif [ "$B2" -gt 0 ]; then
  echo "ARM B REPRODUCES THE KNOWN LAG: the first turn after a restart is ONE BEHIND."
  echo "      A user who sends once and reads the reply sees the change as LOST."
else
  echo "ARM B FAIL: the change never arrived at all."
fi

# ---------------------------------------------------------------- ARM C
# restartlive.sh's EXACT order, which is not arm B's: the daemon is stopped
# FIRST and the patch is then applied to a DEAD daemon, so the patch command
# is what restarts it. That is the arrangement its comment calls "the known
# lag", and the difference from arm B is which side of the restart the patch
# falls on.
echo
echo "=== ARM C: stop FIRST, then patch (the patch restarts the daemon)"
"$BIN" stop >/dev/null 2>&1; sleep 2
"$BIN" state set --id "$FORM" third-purpose "sharpen the blade" >/dev/null 2>&1
sleep 3
send_or_die "after patch-on-dead-daemon"; C1=$(carries "$REQ" third-purpose)
send_or_die "one turn later again";       C2=$(carries "$REQ" third-purpose)
echo
echo "    turn 1: third-purpose x$C1"
echo "    turn 2: third-purpose x$C2"
if [ "$C1" -gt 0 ]; then
  echo "ARM C PASS: even patched against a dead daemon, the first turn carries it."
elif [ "$C2" -gt 0 ]; then
  echo "ARM C REPRODUCES THE KNOWN LAG: first turn one behind."
else
  echo "ARM C FAIL: the change never arrived."
fi
echo
echo "root: $ROOT (delete it)"
