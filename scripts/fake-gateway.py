#!/usr/bin/env python3
"""A fake OpenAI-compatible gateway.

Records every request body it receives and answers with a canned SSE stream
carrying a usage block shaped like a real gateway's — including
prompt_tokens_details, which is the field figaro has to fold into its cache
buckets. No credentials, no network, so the whole path (provider -> HTTP ->
SSE -> IR -> status) can be exercised honestly.
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

RECORD = sys.argv[2] if len(sys.argv) > 2 else "/tmp/fakegw-requests.jsonl"


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.path.endswith("/models"):
            body = json.dumps({"data": [{"id": "auto", "name": "auto", "context_length": 200000}]}).encode()
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
            fh.write(json.dumps({
                "path": self.path,
                "headers": {k.lower(): v for k, v in self.headers.items()
                            if k.lower() in ("x-session-id", "authorization", "content-type")},
                "body": parsed,
            }) + "\n")

        # Second and later turns report a cache read, the way a warm gateway
        # does; the first writes the cache.
        turn = sum(1 for _ in open(RECORD))
        if turn == 1:
            details = {"cached_tokens": 0, "cache_write_tokens": 4096}
        else:
            details = {"cached_tokens": 4096, "cache_write_tokens": 0}
        usage = {
            "prompt_tokens": 4196,
            "completion_tokens": 5,
            "total_tokens": 4201,
            "prompt_tokens_details": details,
        }

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        frames = [
            {"choices": [{"delta": {"content": "Ecco "}}]},
            {"choices": [{"delta": {"content": "fatto."}}]},
            {"choices": [{"delta": {}, "finish_reason": "stop"}], "usage": usage},
        ]
        for frame in frames:
            self.wfile.write(b"data: " + json.dumps(frame).encode() + b"\n\n")
            self.wfile.flush()
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8899
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()
