import argparse
import json
import os
import shutil
import sys
import tempfile
import time

import pager

STEPS = [
    ("state-open", ["S"], 1.0),
    ("state-down", ["j"], 0.5),
    ("state-expand-l", ["j", "j", "j", "j", "l"], 0.6),
    ("state-expand-enter", ["Enter"], 0.6),
    ("state-collapse-h", ["h"], 0.6),
    ("state-collapse-parent-h", ["h"], 0.6),
    ("state-close", ["Escape"], 0.6),
    ("tree-open", ["l"], 1.2),
    ("tree-down", ["j"], 0.6),
    ("tree-down2", ["j"], 0.6),
    ("tree-collapse-h", ["h"], 0.6),
    ("tree-expand-l", ["l"], 0.6),
    ("tree-enter-attends", ["Enter"], 1.5),
]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True)
    ap.add_argument("--bin", required=True)
    args = ap.parse_args()
    doc = json.load(open(os.path.join(pager.HERE, "scenarios.json")))
    root = tempfile.mkdtemp(prefix="parity-unit-", dir="/var/tmp")
    u = pager.Unit(root, args.bin, doc)
    os.makedirs(args.out, exist_ok=True)
    try:
        u.start_provider()
        u.pane()
        main_id = u.new_aria("main")
        for text in ("first question", "second question", "third question"):
            u.fig("send", "--id", main_id, "--", text)
        u.fig("set", "--id", main_id, "mood", "calm")
        u.fig("set", "--id", main_id, "deep.inner.leaf", "one")
        u.fig("set", "--id", main_id, "deep.inner.other", "two")
        u.fig("set", "--id", main_id, "deep.side", "three")
        for turn in (2, 3):
            u.fig("fork", "--id", main_id + ":%d" % turn, "--stay", "-f", "-j", "--", "branch at %d" % turn, check=False)
        u.type("figaro attend %s && figaro listen" % main_id, gap=0)
        u.key("Enter")
        u.wait_screen(lambda s: "live" in s, "the pager")
        u.settle()
        for name, keys, wait in STEPS:
            for k in keys:
                u.key(k)
                time.sleep(0.15)
            time.sleep(wait)
            u.settle()
            with open(os.path.join(args.out, name + ".txt"), "w") as fh:
                fh.write(u.screen())
            with open(os.path.join(args.out, name + ".ansi"), "w") as fh:
                fh.write(u.screen(escapes=True))
            print("captured", name)
    finally:
        u.down()
        leaks = u.leaks()
        if leaks:
            print("LEAK", leaks)
        shutil.rmtree(root, ignore_errors=True)


main()
