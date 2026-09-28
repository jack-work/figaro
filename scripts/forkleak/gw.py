#!/usr/bin/env python3
"""A scripted OpenAI-compatible gateway for the fork-leak hunt.

Every reply quotes the prompt it answers, so a rendered transcript says which
aria's question produced which answer. Records every request body.
"""
import json
import os
import re
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

RECORD = sys.argv[2] if len(sys.argv) > 2 else "/tmp/forkleak-requests.jsonl"
LOCK = threading.Lock()


def last_user_text(body):
    msgs = body.get("messages") or []
    for m in reversed(msgs):
        if m.get("role") != "user":
            continue
        c = m.get("content")
        if isinstance(c, str):
            return c
        if isinstance(c, list):
            for part in c:
                if isinstance(part, dict) and part.get("type") == "text":
                    return part.get("text", "")
    return ""



def read_body(handler):
    """Read the request body, chunked or not.

    figaro streams its request with Transfer-Encoding: chunked, so a handler
    that trusts Content-Length reads ZERO BYTES and every request looks empty.
    That is how this gateway answered NOTAG to everything for an hour.
    """
    length = handler.headers.get("Content-Length")
    if length:
        return handler.rfile.read(int(length))
    if (handler.headers.get("Transfer-Encoding") or "").lower() != "chunked":
        return b""
    out = b""
    while True:
        line = handler.rfile.readline().strip()
        if not line:
            break
        size = int(line.split(b";")[0], 16)
        if size == 0:
            handler.rfile.readline()
            break
        out += handler.rfile.read(size)
        handler.rfile.readline()
    return out


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
        raw = read_body(self)
        try:
            body = json.loads(raw)
        except json.JSONDecodeError:
            body = {}
        with LOCK:
            with open(RECORD, "a") as fh:
                fh.write(json.dumps({"path": self.path, "body": body}) + "\n")

        q = last_user_text(body) or json.dumps(body)
        # The LAST token in the request: the question being answered now. The
        # prompt does not always arrive as the last user message (the board
        # rides along), so the whole body is the honest place to look.
        toks = re.findall(r"\b((?:PARENT|CHILD|DECOY|CHURN)Q[0-9A-Za-z]*)\b", json.dumps(body))
        tag = toks[-1] if toks else "NOTAG"
        text = f"ANSWER[{tag}] ecco fatto."

        # SLOW mode: a long streaming reply, so a fork can be made while the
        # parent is still mid-turn. That window is where a race can live.
        slow = float(os.environ.get("FORKLEAK_SLOW", "0"))
        # BIG mode: a fat reply, so a small UI cache budget actually evicts.
        big = int(os.environ.get("FORKLEAK_BIG", "0"))
        usage = {"prompt_tokens": 100, "completion_tokens": 5, "total_tokens": 105,
                 "prompt_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 0}}
        frames = [{"choices": [{"delta": {"content": text[:8]}}]},
                  {"choices": [{"delta": {"content": text[8:]}}]}]
        if big:
            filler = ("lorem ipsum dolor sit amet consectetur adipiscing elit sed do "
                      "eiusmod tempor incididunt ut labore et dolore magna aliqua. ")
            chunk = (filler * ((1024 // len(filler)) + 1))[:1024]
            for i in range(big):
                frames.append({"choices": [{"delta": {"content": f"\n\nP{i}: " + chunk}}]})
        if slow:
            for i in range(12):
                frames.append({"choices": [{"delta": {"content": f" part{i}."}}]})
        frames.append({"choices": [{"delta": {}, "finish_reason": "stop"}], "usage": usage})
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        for frame in frames:
            if slow:
                time.sleep(slow)
            payload = b"data: " + json.dumps(frame).encode() + b"\n\n"
            self.wfile.write(b"%x\r\n" % len(payload) + payload + b"\r\n")
            self.wfile.flush()
        payload = b"data: [DONE]\n\n"
        self.wfile.write(b"%x\r\n" % len(payload) + payload + b"\r\n")
        self.wfile.write(b"0\r\n\r\n")
        self.wfile.flush()


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8931
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
