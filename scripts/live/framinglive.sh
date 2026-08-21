# Does system.stream_request_body ON AN ARIA'S BOARD reach the wire?
#
# The unit tests prove the two framings and the byte-identity; this proves the
# SETTING travels -- board -> snapshot -> provider -> transport -- which no
# unit test can, because the wiring lives in the daemon.
#
# THE SINK IS LOCAL AND HTTP/1.1, which is what makes the framing VISIBLE: an
# unknown-length body is Transfer-Encoding: chunked there, where over HTTP/2
# (what the real endpoints negotiate) it is simply an absent length. No API
# call, no cost, and the same aria is used for both arms so nothing but the
# setting differs.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/layered
ROOT=$(mktemp -d /var/tmp/figframing.XXXX); BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true

cat >"$ROOT/sink.go" <<'GO'
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

// The sink answers a minimal well-formed Anthropic SSE stream and records how
// the request framed its body.
func main() {
	out, err := os.Create(os.Args[2])
	if err != nil {
		panic(err)
	}
	http.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		te := "none"
		if len(r.TransferEncoding) > 0 {
			te = r.TransferEncoding[0]
		}
		fmt.Fprintf(out, "proto=%s transfer-encoding=%s content-length=%d body-bytes=%d\n",
			r.Proto, te, r.ContentLength, n)
		out.Sync()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for _, ev := range []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"one"}}`,
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
# Redirected: a background child holding the script's stdout keeps the pipe
# open after the script exits, and a caller reading through `tail` hangs.
go run "$ROOT/sink.go" 127.0.0.1:8791 "$ROOT/framing.log" >"$ROOT/sink.out" 2>&1 &
SINK=$!
trap 'kill $SINK 2>/dev/null; pkill -f "$ROOT/figaro" 2>/dev/null' EXIT
sleep 3

export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
export ANTHROPIC_BASE_URL="http://127.0.0.1:8791/v1" ANTHROPIC_API_KEY="sink"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT

ARIA=$("$BIN" new 2>&1 | grep -oE "[0-9a-f]{8}" | head -1)
echo "aria $ARIA, one board, two settings"

for VALUE in true false true; do
  "$BIN" set --id "$ARIA" system.stream_request_body "$VALUE" >/dev/null 2>&1
  OUT=$("$BIN" send --id "$ARIA" -- "reply with exactly: one" 2>&1)
  case "$OUT" in
    *"completed ✓"*) STATUS="ok" ;;
    *) STATUS="FAILED: $(echo "$OUT" | tr -d '\n' | tail -c 120)" ;;
  esac
  echo "  stream_request_body=$VALUE  send=$STATUS  wire: $(tail -1 "$ROOT/framing.log")"
done

echo
echo "--- THE VERDICT"
CHUNKED=$(grep -c 'transfer-encoding=chunked' "$ROOT/framing.log")
LENGTHED=$(grep -c 'transfer-encoding=none' "$ROOT/framing.log")
if [ "$CHUNKED" = "2" ] && [ "$LENGTHED" = "1" ]; then
  echo "PASS: the board key decides the framing, both ways, on one aria."
else
  echo "FAIL: $CHUNKED chunked and $LENGTHED length-declared requests, want 2 and 1"
  cat "$ROOT/framing.log"
fi
echo
echo "root: $ROOT (delete it)"
