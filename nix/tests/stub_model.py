import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

COMMAND = open(sys.argv[1]).read().strip()


def chunk(delta, finish=None):
    return {"id": "stub", "object": "chat.completion.chunk", "model": "stub",
            "choices": [{"index": 0, "delta": delta, "finish_reason": finish}]}


class Model(BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"object": "list", "data": [{"id": "stub", "object": "model"}]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def body(self):
        if self.headers.get("Content-Length"):
            return self.rfile.read(int(self.headers["Content-Length"]))
        out = b""
        while True:
            size = int(self.rfile.readline().strip(), 16)
            if size == 0:
                self.rfile.readline()
                return out
            out += self.rfile.read(size)
            self.rfile.readline()

    def do_POST(self):
        req = json.loads(self.body())
        with open("/tmp/stub-requests.jsonl", "a") as f:
            f.write(json.dumps(req) + "\n")
        answered = any(m.get("role") == "tool" for m in req.get("messages", []))
        if answered:
            frames = [chunk({"role": "assistant", "content": "done"}), chunk({}, "stop")]
        else:
            call = {"index": 0, "id": "call_1", "type": "function",
                    "function": {"name": "bash", "arguments": json.dumps({"command": COMMAND})}}
            frames = [chunk({"role": "assistant", "tool_calls": [call]}), chunk({}, "tool_calls")]
        frames.append({"id": "stub", "object": "chat.completion.chunk", "model": "stub", "choices": [],
                       "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for f in frames:
            self.wfile.write(f"data: {json.dumps(f)}\n\n".encode())
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def log_message(self, *a):
        pass


ThreadingHTTPServer(("0.0.0.0", 8099), Model).serve_forever()
