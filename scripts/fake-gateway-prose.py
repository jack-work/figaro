#!/usr/bin/env python3
"""A fake OpenAI-compatible gateway whose PROSE is chosen by the caller.

fake-gateway.py answers two words; fake-gateway-tools.py answers from a fixed
script. Neither can produce a long, known paragraph, which is what anything
about quoting, wrapping or truncation needs: the passage has to be longer than
the budget that clips it, and it has to be a string the oracle already knows.

    fake-gateway-prose.py <port> <record.jsonl> <replydir>

For request N it streams <replydir>/N.md, falling back to <replydir>/default.md.
The text is chunked so the SSE path is exercised rather than bypassed.
"""
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 8897
RECORD = sys.argv[2] if len(sys.argv) > 2 else "/tmp/fakegw-prose-requests.jsonl"
REPLIES = sys.argv[3] if len(sys.argv) > 3 else "/tmp/fakegw-prose-replies"

USAGE = {
    "prompt_tokens": 100,
    "completion_tokens": 5,
    "total_tokens": 105,
    "prompt_tokens_details": {"cached_tokens": 0},
}
CHUNK = 48


def reply_text(n):
    for name in (f"{n}.md", "default.md"):
        path = os.path.join(REPLIES, name)
        if os.path.exists(path):
            with open(path) as fh:
                return fh.read().rstrip("\n")
    return "Ecco fatto."


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
        n = sum(1 for _ in open(RECORD))
        text = reply_text(n)

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for i in range(0, len(text), CHUNK):
            self.frame({"choices": [{"delta": {"content": text[i:i + CHUNK]}}]})
        self.frame({"choices": [{"delta": {}, "finish_reason": "stop"}], "usage": USAGE})
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def frame(self, obj):
        self.wfile.write(b"data: " + json.dumps(obj).encode() + b"\n\n")
        self.wfile.flush()


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
