#!/usr/bin/env python3
"""A fake System One (decisions) endpoint.

Answers POST /v1/systemone the way api.typesafe.ai and
api.experientiallabs.ai do, and refuses a body carrying any member beyond
model/state/questions the way they do, because that refusal is the one error
a broker in front of this wire can cause. Records every request so a test can
assert on the state figaro built.

    python3 scripts/fake-systemone.py <port> [record.jsonl]
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

RECORD = sys.argv[2] if len(sys.argv) > 2 else "/tmp/fake-systemone.jsonl"
ALLOWED = {"model", "state", "questions"}

CATALOG = {"models": [{"name": "jev-latest", "description": "decisions", "release_date": "2026-09-15"}]}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def _json(self, status, body):
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.path.endswith("/models"):
            self._json(200, CATALOG)
            return
        self.send_response(404)
        self.end_headers()

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(length)
        try:
            req = json.loads(raw)
        except json.JSONDecodeError:
            req = {"unparseable": raw.decode("utf-8", "replace")}
        with open(RECORD, "a") as fh:
            fh.write(json.dumps({"path": self.path, "body": req}) + "\n")

        if not self.path.endswith("/systemone"):
            self.send_response(404)
            self.end_headers()
            return

        extra = set(req) - ALLOWED
        if extra:
            self._json(400, {"error": {
                "message": "Invalid decision request. Send only model, state, and bounded typed "
                           "questions; chat, streaming, tools, and generation controls are not supported.",
                "type": "invalid_request_error", "param": "body", "code": "invalid_parameter"}})
            return

        questions = req.get("questions") or {}
        key = next(iter(questions), "decision")
        q = questions.get(key) or {}
        answers = {}
        if q.get("type") == "choice":
            options = list((q.get("criteria") or {}).keys())
            # The first option wins, with a spread over the rest: enough shape
            # to prove the renderer sorts, highlights and reports confidence.
            spread = {}
            for i, opt in enumerate(options):
                spread[opt] = 0.72 if i == 0 else round(0.28 / max(len(options) - 1, 1), 2)
            answers[key] = {"type": "choice", "choice": options[0] if options else "",
                            "confidence": 0.81, "probabilities": spread}
        else:
            answers[key] = {"type": "noul", "noul": 0.93}

        state = req.get("state")
        charged = len(json.dumps(state)) // 4 if state is not None else 0
        self._json(200, {
            "id": "decision_fake", "model": req.get("model", "jev-latest"),
            "answers": answers,
            "usage": {"input_tokens": charged, "output_tokens": 20, "cost": 0.0, "is_byok": False},
            "provider": "fake",
        })


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
