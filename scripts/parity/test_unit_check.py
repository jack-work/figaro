import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = {"rules": [{"when": "doomed", "fail": {"status": 400, "times": 1}, "rounds": [{"text": "ok"}]}], "default": {"text": "ok"}}


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class UnitCheck(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp(prefix="parity-check-")
        with open(os.path.join(self.root, "provider.json"), "w") as fh:
            json.dump(SCRIPT, fh)
        self.unit = os.path.join(self.root, "unit.json")
        with open(self.unit, "w") as fh:
            json.dump({"root": self.root}, fh)
        self.port = free_port()
        self.provider = subprocess.Popen([sys.executable, os.path.join(HERE, "fake-anthropic.py"), str(self.port), os.path.join(self.root, "provider.json"), os.path.join(self.root, "requests.jsonl")])
        end = time.time() + 10
        while time.time() < end:
            try:
                socket.create_connection(("127.0.0.1", self.port), 0.2).close()
                return
            except OSError:
                time.sleep(0.05)
        self.fail("fake provider did not start")

    def tearDown(self):
        self.provider.terminate()
        self.provider.wait()

    def ask(self, text):
        req = urllib.request.Request("http://127.0.0.1:%d/v1/messages" % self.port, data=json.dumps({"messages": [{"role": "user", "content": text}]}).encode(), method="POST")
        try:
            return urllib.request.urlopen(req, timeout=10).status
        except urllib.error.HTTPError as e:
            return e.code

    def check(self):
        return subprocess.run([sys.executable, os.path.join(HERE, "unit.py"), "check", self.unit], capture_output=True, text=True)

    def test_a_fresh_unit_passes_the_check(self):
        self.assertEqual(self.check().returncode, 0)

    def test_a_unit_whose_scripted_failure_was_served_is_refused(self):
        self.assertEqual(self.ask("doomed turn"), 400)
        out = self.check()
        self.assertEqual(out.returncode, 1)
        self.assertIn("already consumed", out.stderr)
        self.assertIn("unit.py up", out.stderr)

    def test_a_request_that_matches_no_failing_rule_leaves_the_unit_fresh(self):
        self.assertEqual(self.ask("hello"), 200)
        self.assertEqual(self.check().returncode, 0)


if __name__ == "__main__":
    unittest.main()
