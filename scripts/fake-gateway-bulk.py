#!/usr/bin/env python3
"""An OpenAI-compatible gateway that answers with BULK: a paragraph of the
size you ask for, streamed in chunks.

fake-gateway.py answers "Ecco fatto." That is right for a protocol test and
useless for a measurement about bytes: an aria whose every turn is twelve
characters has no prefix worth retaining. Here the reply size is the point.

    fake-gateway-bulk.py <port> <record.jsonl> [reply_bytes]

No credentials, no network beyond loopback.
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer, ThreadingHTTPServer

from http_fixture import request_body

RECORD = sys.argv[2] if len(sys.argv) > 2 else "/tmp/fakegw-bulk.jsonl"
SIZE = int(sys.argv[3]) if len(sys.argv) > 3 else 3000

WORDS = ("largo al factotum della citta la ran la lera la ran la la "
         "presto a bottega che l alba e gia ah che bel vivere che bel piacere "
         "per un barbiere di qualita ").split()


def paragraph(n):
    out = []
    total = 0
    i = 0
    while total < n:
        w = WORDS[i % len(WORDS)]
        out.append(w)
        total += len(w) + 1
        i += 1
        if i % 14 == 0:
            out.append("\n")
    return " ".join(out)


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

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
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_POST(self):
        raw = request_body(self)
        try:
            parsed = json.loads(raw)
        except json.JSONDecodeError:
            parsed = {"unparseable": raw.decode("utf-8", "replace")}
        with open(RECORD, "a") as fh:
            fh.write(json.dumps({"path": self.path, "n": len(parsed.get("messages", []))}) + "\n")

        text = paragraph(SIZE)
        usage = {
            "prompt_tokens": 4196,
            "completion_tokens": SIZE // 4,
            "total_tokens": 4196 + SIZE // 4,
            "prompt_tokens_details": {"cached_tokens": 4096, "cache_write_tokens": 0},
        }
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        step = 400
        for i in range(0, len(text), step):
            frame = {"choices": [{"delta": {"content": text[i:i + step]}}]}
            self.wfile.write(b"data: " + json.dumps(frame).encode() + b"\n\n")
        self.wfile.write(b"data: " + json.dumps(
            {"choices": [{"delta": {}, "finish_reason": "stop"}], "usage": usage}).encode() + b"\n\n")
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8899
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
