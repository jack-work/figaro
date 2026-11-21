#!/usr/bin/env python3
import json
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1])
SCRIPT = sys.argv[2]
RECORD = sys.argv[3]

lock = threading.Lock()
fails = {}


def load():
    with open(SCRIPT) as fh:
        return json.load(fh)


def texts(message):
    content = message.get("content")
    if isinstance(content, str):
        return [content]
    return [b.get("text", "") for b in content if b.get("type") == "text"]


def pick(script, messages):
    for i in range(len(messages) - 1, -1, -1):
        m = messages[i]
        if m.get("role") != "user":
            continue
        joined = "\n".join(texts(m))
        for rule in script["rules"]:
            if rule["when"] in joined:
                after = sum(1 for later in messages[i + 1:] if later.get("role") == "assistant")
                return rule, after
    return None, 0


def sse(event, data):
    return ("event: %s\ndata: %s\n\n" % (event, json.dumps(data))).encode()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def reply(self, code, body, ctype="application/json"):
        raw = body if isinstance(body, bytes) else json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def read_body(self):
        if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
            parts = []
            while True:
                size = int(self.rfile.readline().strip() or b"0", 16)
                if size == 0:
                    self.rfile.readline()
                    return b"".join(parts)
                parts.append(self.rfile.read(size))
                self.rfile.readline()
        return self.rfile.read(int(self.headers.get("Content-Length", 0)))

    def do_GET(self):
        self.reply(404, {"type": "error", "error": {"type": "not_found_error", "message": "no"}})

    def do_POST(self):
        body = json.loads(self.read_body() or b"{}")
        script = load()
        messages = body.get("messages", [])
        rule, n = pick(script, messages)
        with open(RECORD, "a") as fh:
            fh.write(json.dumps({"rule": rule and rule["when"], "round": n, "messages": len(messages)}) + "\n")
        if rule is None:
            rnd = script["default"]
        else:
            rounds = rule["rounds"]
            rnd = rounds[min(n, len(rounds) - 1)]
            fail = rule.get("fail")
            if fail and n == 0:
                with lock:
                    seen = fails.get(rule["when"], 0)
                    fails[rule["when"]] = seen + 1
                if seen < fail["times"]:
                    self.reply(fail["status"], {"type": "error", "error": {"type": "invalid_request_error" if fail["status"] == 400 else "overloaded_error", "message": "scripted failure"}})
                    return
        time.sleep(rnd.get("hold", 0))
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True
        pace = rnd.get("pace", 0)
        out = self.wfile
        out.write(sse("message_start", {"type": "message_start", "message": {"id": "msg_parity", "type": "message", "role": "assistant", "model": body.get("model", "m"), "content": [], "usage": {"input_tokens": 12, "output_tokens": 1}}}))
        index = 0
        if rnd.get("thinking"):
            out.write(sse("content_block_start", {"type": "content_block_start", "index": index, "content_block": {"type": "thinking", "thinking": ""}}))
            for chunk in split(rnd["thinking"]):
                out.write(sse("content_block_delta", {"type": "content_block_delta", "index": index, "delta": {"type": "thinking_delta", "thinking": chunk}}))
                out.flush()
                time.sleep(pace)
            out.write(sse("content_block_delta", {"type": "content_block_delta", "index": index, "delta": {"type": "signature_delta", "signature": "parity-signature"}}))
            out.write(sse("content_block_stop", {"type": "content_block_stop", "index": index}))
            index += 1
        if rnd.get("text"):
            out.write(sse("content_block_start", {"type": "content_block_start", "index": index, "content_block": {"type": "text", "text": ""}}))
            for chunk in split(rnd["text"]):
                out.write(sse("content_block_delta", {"type": "content_block_delta", "index": index, "delta": {"type": "text_delta", "text": chunk}}))
                out.flush()
                time.sleep(pace)
            out.write(sse("content_block_stop", {"type": "content_block_stop", "index": index}))
            index += 1
        tool = rnd.get("tool")
        if tool:
            out.write(sse("content_block_start", {"type": "content_block_start", "index": index, "content_block": {"type": "tool_use", "id": "toolu_parity_%d" % n, "name": tool["name"], "input": {}}}))
            out.write(sse("content_block_delta", {"type": "content_block_delta", "index": index, "delta": {"type": "input_json_delta", "partial_json": json.dumps(tool["input"])}}))
            out.write(sse("content_block_stop", {"type": "content_block_stop", "index": index}))
        out.write(sse("message_delta", {"type": "message_delta", "delta": {"stop_reason": "tool_use" if tool else "end_turn"}, "usage": {"output_tokens": 20}}))
        out.write(sse("message_stop", {"type": "message_stop"}))
        out.flush()


def split(text, size=24):
    return [text[i:i + size] for i in range(0, len(text), size)] or [""]


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
