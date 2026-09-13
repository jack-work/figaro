#!/usr/bin/env python3
"""A fake OpenAI-compatible gateway that streams SLOWLY, on purpose.

fake-gateway-tools.py answers in one burst, which is right for asserting the
shape of a log and wrong for measuring paint: a turn that is over in a
millisecond has no thinking window to sample, so every frame interval lands in
the same bucket.

This one takes its time. Per request it reads a JSON config beside the record
file (<record>.cfg):

    ttft    seconds before the first token
    chunks  how many content deltas
    gap     seconds between them

so a script can make a turn last two seconds with forty paints in it, then make
the next one last twenty, without restarting anything.
"""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from http_fixture import request_body

RECORD = sys.argv[2] if len(sys.argv) > 2 else "/tmp/jitter-gw.jsonl"
CFG = RECORD + ".cfg"

WORDS = ("largo al factotum della citta pronto prontissimo son come il fulmine "
         "sono il barbiere di qualita ah bravo figaro bravo bravissimo "
         "fortunatissimo per verita ").split()


def config():
    cfg = {"ttft": 0.2, "chunks": 30, "gap": 0.05}
    try:
        with open(CFG) as fh:
            cfg.update(json.load(fh))
    except (OSError, ValueError):
        pass
    return cfg


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.path.endswith("/models"):
            self.json({"data": [{"id": "auto", "name": "auto",
                                 "context_length": 200000}]})
            return
        self.send_response(404)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def json(self, body):
        raw = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_POST(self):
        raw = request_body(self)
        try:
            parsed = json.loads(raw)
        except json.JSONDecodeError:
            parsed = {"unparseable": raw.decode("utf-8", "replace")}
        with open(RECORD, "a") as fh:
            fh.write(json.dumps({"path": self.path, "body": parsed}) + "\n")
        cfg = config()

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        time.sleep(float(cfg["ttft"]))
        n = int(cfg["chunks"])
        for i in range(n):
            word = WORDS[i % len(WORDS)] + " "
            self.chunk({"choices": [{"delta": {"content": word}}]})
            time.sleep(float(cfg["gap"]))
        self.chunk({"choices": [{"delta": {}, "finish_reason": "stop"}],
                    "usage": {"prompt_tokens": 100, "completion_tokens": n,
                              "total_tokens": 100 + n,
                              "prompt_tokens_details": {"cached_tokens": 0}}})
        self.raw(b"data: [DONE]\n\n")
        self.raw(b"")

    def chunk(self, frame):
        self.raw(b"data: " + json.dumps(frame).encode() + b"\n\n")

    def raw(self, payload):
        self.wfile.write(b"%x\r\n" % len(payload) + payload + b"\r\n")
        self.wfile.flush()


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8899
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
