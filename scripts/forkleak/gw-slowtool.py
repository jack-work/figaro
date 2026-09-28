#!/usr/bin/env python3
"""A gateway whose first move is a LONG tool call.

The point is a turn that sits inside a tool for many seconds, so a fork can be
taken while the call is outstanding: the state Gluck's own note names ("I find
that I cannot fork during tool use") and the state fourteen arias in his store
end in.
"""
import json
import os
import re
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

RECORD = sys.argv[2] if len(sys.argv) > 2 else "/tmp/slowtool-requests.jsonl"
LOCK = threading.Lock()
SLEEP = os.environ.get("FORKLEAK_TOOL_SLEEP", "25")


def has_tool_result(body):
    for m in body.get("messages") or []:
        c = m.get("content")
        if isinstance(c, list):
            for part in c:
                if isinstance(part, dict) and part.get("type") == "tool_result":
                    return True
        if m.get("role") == "tool":
            return True
    return False


def tag(body):
    toks = re.findall(r"\b((?:PARENT|CHILD|DECOY|QUEUED)Q[0-9A-Za-z]*)\b", json.dumps(body))
    return toks[-1] if toks else "NOTAG"



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
                fh.write(json.dumps({"path": self.path, "tool_result": has_tool_result(body),
                                     "roles": [m.get("role") for m in (body.get("messages") or [])][-4:],
                                     "last": json.dumps((body.get("messages") or [{}])[-1])[:400]}) + "\n")

        usage = {"prompt_tokens": 100, "completion_tokens": 5, "total_tokens": 105,
                 "prompt_tokens_details": {"cached_tokens": 0}}
        t = tag(body)
        if has_tool_result(body):
            frames = [
                {"choices": [{"delta": {"content": f"ANSWER[{t}] the tool came back."}}]},
                {"choices": [{"delta": {}, "finish_reason": "stop"}], "usage": usage},
            ]
        else:
            call = {
                "index": 0,
                "id": "call_slow_1",
                "type": "function",
                "function": {"name": "bash", "arguments": json.dumps(
                    {"command": f"sleep {SLEEP}; echo slowtool-done", "yieldMs": 120000})},
            }
            frames = [
                {"choices": [{"delta": {"content": f"WORKING[{t}] running a long command."}}]},
                {"choices": [{"delta": {"tool_calls": [call]}}]},
                {"choices": [{"delta": {}, "finish_reason": "tool_calls"}], "usage": usage},
            ]

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        for frame in frames:
            payload = b"data: " + json.dumps(frame).encode() + b"\n\n"
            self.wfile.write(b"%x\r\n" % len(payload) + payload + b"\r\n")
            self.wfile.flush()
        payload = b"data: [DONE]\n\n"
        self.wfile.write(b"%x\r\n" % len(payload) + payload + b"\r\n")
        self.wfile.write(b"0\r\n\r\n")
        self.wfile.flush()


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 9950
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
