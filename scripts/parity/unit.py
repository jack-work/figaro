#!/usr/bin/env python3
import argparse
import json
import os
import subprocess
import sys
import tempfile

import pager


def up(args):
    doc = json.load(open(os.path.join(pager.HERE, "scenarios.json")))
    binary = args.bin or "/var/tmp/parity-bin/figaro"
    if not args.bin:
        os.makedirs(os.path.dirname(binary), exist_ok=True)
        pager.build(binary)
    root = tempfile.mkdtemp(prefix="parity-unit-", dir="/var/tmp")
    unit = pager.Unit(root, binary, doc)
    unit.gateway = subprocess.Popen([sys.executable, os.path.join(pager.HERE, "fake-anthropic.py"), str(unit.port), unit.script, unit.requests], start_new_session=True)
    env = {k: v for k, v in unit.env().items() if k.startswith(("FIGARO_", "ANTHROPIC_", "HUSH_"))}
    state = {"root": root, "bin": binary, "app": unit.app, "provider_pid": unit.gateway.pid, "env": env}
    with open(args.out, "w") as fh:
        json.dump(state, fh, indent=1)
    os.chmod(args.out, 0o600)
    print(args.out)


def down(args):
    state = json.load(open(args.unit))
    env = dict(os.environ, **state["env"])
    subprocess.run([state["bin"], "stop"], env=env, capture_output=True)
    subprocess.run([state["bin"], "vault", "forget", "--yes"], env=env, capture_output=True)
    try:
        os.kill(state["provider_pid"], 15)
    except OSError:
        pass
    subprocess.run(["secret-tool", "clear", "service", state["app"]], capture_output=True)
    subprocess.run(["rm", "-rf", state["root"], args.unit])


def spent(state):
    root = state["root"]
    script = json.load(open(os.path.join(root, "provider.json")))
    path = os.path.join(root, "requests.jsonl")
    asked = {}
    if os.path.exists(path):
        for line in open(path):
            row = json.loads(line)
            if row["round"] == 0 and row["rule"]:
                asked[row["rule"]] = asked.get(row["rule"], 0) + 1
    return [(r["when"], r["fail"]["times"]) for r in script["rules"] if r.get("fail") and asked.get(r["when"], 0) >= r["fail"]["times"]]


def check(args):
    state = json.load(open(args.unit))
    used = spent(state)
    if used:
        names = ", ".join("%r (fails %d time%s)" % (w, n, "" if n == 1 else "s") for w, n in used)
        sys.exit("unit %s: the provider's scripted failure for %s is already consumed, so the error scenario would see no failure; run unit.py down and unit.py up for a fresh unit" % (args.unit, names))


ap = argparse.ArgumentParser()
sub = ap.add_subparsers(dest="cmd", required=True)
u = sub.add_parser("up")
u.add_argument("--out", required=True)
u.add_argument("--bin")
d = sub.add_parser("down")
d.add_argument("unit")
c = sub.add_parser("check", help="exit 1 when a scripted provider failure is already consumed")
c.add_argument("unit")
a = ap.parse_args()
{"up": up, "down": down, "check": check}[a.cmd](a)
