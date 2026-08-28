#!/usr/bin/env python3
"""A fake OpenAI-compatible gateway that makes TOOL CALLS.

fake-gateway.py answers every request with the same two words, which is all
prompt-caching needed. Forking at a node needs a turn with SEVERAL nodes in it,
and the only way an assistant turn grows nodes is by saying something and then
calling a tool -- two content blocks in one message, which is exactly the shape
a node coordinate has to retreat past.

So this one answers from a SCRIPT, one entry per request:

    text        one prose block, then stop
    say+call    one prose block AND a tool call in the same message
    call        a bare tool call

The tool is always `bash`, always `echo`, and always harmless: what is under
test is the shape of the log, not the tool.
"""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

RECORD = sys.argv[2] if len(sys.argv) > 2 else "/tmp/fakegw-tools-requests.jsonl"

# A TURN THAT TAKES ITS TIME, on demand. Some behaviour only exists while a turn
# is IN FLIGHT -- a prompt sent then is queued rather than run, which is the only
# way to put rows in the queue pit -- and a gateway that answers in a millisecond
# cannot produce it. The delay is read per request from a file beside the record,
# so a script can turn it on for one turn and off again without a restart.
DELAY_FILE = RECORD + ".delay"


def delay_seconds():
    try:
        with open(DELAY_FILE) as fh:
            return float(fh.read().strip() or 0)
    except (OSError, ValueError):
        return 0.0

# One entry per request the daemon makes, in order. The last entry repeats.
SCRIPT = [
    ("say+call", "First I look around.", "echo one"),
    ("say+call", "Then I look again.", "echo two"),
    ("call", None, "echo three"),
    ("text", "All done: DONE.", None),
]

USAGE = {
    "prompt_tokens": 100,
    "completion_tokens": 5,
    "total_tokens": 105,
    "prompt_tokens_details": {"cached_tokens": 0},
}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.path.endswith("/models"):
            body = json.dumps(
                {"data": [{"id": "auto", "name": "auto", "context_length": 200000}]}
            ).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(404)
        self.end_headers()

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(length)
        try:
            parsed = json.loads(raw)
        except json.JSONDecodeError:
            parsed = {"unparseable": raw.decode("utf-8", "replace")}
        with open(RECORD, "a") as fh:
            fh.write(json.dumps({"path": self.path, "body": parsed}) + "\n")
        if (secs := delay_seconds()) > 0:
            time.sleep(secs)
        n = sum(1 for _ in open(RECORD))
        kind, text, cmd = SCRIPT[min(n, len(SCRIPT)) - 1]

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for frame in self.frames(n, kind, text, cmd):
            self.wfile.write(b"data: " + json.dumps(frame).encode() + b"\n\n")
            self.wfile.flush()
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def frames(self, n, kind, text, cmd):
        out = []
        if text:
            out.append({"choices": [{"delta": {"content": text}}]})
        if cmd:
            out.append({"choices": [{"delta": {"tool_calls": [{
                "index": 0,
                "id": f"call_{n}",
                "type": "function",
                "function": {"name": "bash", "arguments": ""},
            }]}}]})
            args = json.dumps({"command": cmd})
            out.append({"choices": [{"delta": {"tool_calls": [{
                "index": 0,
                "function": {"arguments": args},
            }]}}]})
            out.append({"choices": [{"delta": {}, "finish_reason": "tool_calls"}],
                        "usage": USAGE})
            return out
        out.append({"choices": [{"delta": {}, "finish_reason": "stop"}], "usage": USAGE})
        return out


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8898
    # THREADED, because a delayed turn must not hold up the model list or the
    # next request: a single-threaded server makes "slow" mean "stopped".
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
