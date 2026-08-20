# Does a studied form's change reach the model when it is set WHILE A TOOL IS
# IN FLIGHT?
#
# Gluck, 2026-08-20: "I think it happens when a tool is in flight? ... a change
# fired during my tool-call window and was dropped rather than queued for the
# next boundary."
#
# THE MECHANISM IT TESTS: store.observedCursors reads each studied form's tail
# AT APPEND TIME, so any fig IR entry appended after the patch should carry the
# new version -- including the TOOL RESULT entry, which is a RoleInput and
# therefore passes provider.carriesStudy. If that is true the change rides the
# tool result and arrives in the same turn. If it is false it is dropped.
#
# Free and repeatable: a local sink plays the provider and hands out one
# tool_use, so the tool-call window is ours to patch inside of.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/layered
ROOT=$(mktemp -d /var/tmp/figtoolstudy.XXXX); BIN=$ROOT/figaro
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
	"sync/atomic"
)

// Turn 1 hands out a tool_use that sleeps; turn 2+ ends the turn. The sleep is
// the window the test patches inside of.
func main() {
	dir := os.Args[2]
	var n atomic.Int32
	ev := func(w http.ResponseWriter, s string) {
		fmt.Fprintf(w, "%s\n\n", s)
		w.(http.Flusher).Flush()
	}
	http.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		i := n.Add(1)
		b, _ := io.ReadAll(r.Body)
		_ = os.WriteFile(fmt.Sprintf("%s/req-%02d.json", dir, i), b, 0o644)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		ev(w, `event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`)
		if i == 2 { // turn 1 of the aria (turn 1 of the sink is the health check)
			ev(w, `event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tc_sleep","name":"bash","input":{}}}`)
			ev(w, `event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"sleep 12\"}"}}`)
			ev(w, `event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`)
			ev(w, `event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`)
		} else {
			ev(w, `event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
			ev(w, `event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
			ev(w, `event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`)
			ev(w, `event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)
		}
		ev(w, `event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
	})
	panic(http.ListenAndServe(os.Args[1], nil))
}
GO

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

export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
export ANTHROPIC_BASE_URL="http://127.0.0.1:$PORT/v1" ANTHROPIC_API_KEY="sink"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT

ARIA=$("$BIN" new 2>&1 | grep -oE "[0-9a-f]{8}" | head -1)
"$BIN" state set --id "$ARIA" system.use_official_sdk false >/dev/null 2>&1
FORM=$("$BIN" form new -S name=therole 2>&1 | grep -oE "@[0-9a-f]+" | head -1)
"$BIN" cast "$ARIA" "$FORM" >/dev/null 2>&1
echo "aria $ARIA studies role $FORM"

echo
echo "--- send: the sink answers with a tool_use that sleeps 12s"
"$BIN" send --id "$ARIA" -- "run the tool" >"$ROOT/send.out" 2>&1 &
SENDPID=$!

echo "--- waiting for the tool to actually be running before patching"
for _ in $(seq 1 40); do
  pgrep -f "sleep 12" >/dev/null 2>&1 && break
  sleep 0.5
done
if ! pgrep -f "sleep 12" >/dev/null 2>&1; then
  echo "FAIL: the tool never ran, so there was no in-flight window to patch inside."
  echo "      send output: $(tr -d '\n' < "$ROOT/send.out" | tail -c 200)"
  echo "root: $ROOT"; exit 1
fi
echo "    tool is in flight -- PATCHING THE STUDIED FORM NOW"
"$BIN" state set --id "$FORM" midturn-key "set while a tool was running" 2>&1 | tail -1 | cut -c1-80

wait $SENDPID
echo "    send finished: $(tr -d '\n' < "$ROOT/send.out" | tail -c 60)"

echo
echo "--- WHICH REQUEST CARRIED IT?"
for f in "$ROOT"/wire/req-*.json; do
  n=$(grep -c "midturn-key" "$f" 2>/dev/null); n=${n:-0}
  echo "    $(basename "$f"): midturn-key x$n"
done

# req-02 is the aria's first turn (req-01 is the health check); req-03 is the
# continuation that carries the tool_result.
AFTERTOOL=$(grep -c "midturn-key" "$ROOT/wire/req-03.json" 2>/dev/null); AFTERTOOL=${AFTERTOOL:-0}
echo
if [ "$AFTERTOOL" -gt 0 ]; then
  echo "PASS: the change set MID-TOOL rode the tool_result into the same turn."
else
  echo "REPRODUCED: a change set while a tool was in flight did NOT reach the model"
  echo "            on the continuation that carried the tool result."
  echo "--- does it arrive on a LATER send at all?"
  "$BIN" send --id "$ARIA" -- "again" >/dev/null 2>&1
  LAST=$(ls -t "$ROOT"/wire/req-*.json | head -1)
  n=$(grep -c "midturn-key" "$LAST" 2>/dev/null); n=${n:-0}
  if [ "$n" -gt 0 ]; then
    echo "    yes, on the NEXT send ($(basename "$LAST")): late, not lost."
  else
    echo "    NO: it is LOST, not late."
  fi
fi
echo
echo "root: $ROOT (delete it)"
