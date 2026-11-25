#!/usr/bin/env python3
import argparse
import datetime
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "..", ".."))
SPINNER = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
PIT_KEYS = {"control": "!", "state": "S", "tree": "l", "queue": "Q", "help": "h"}
WIDTH, HEIGHT = 110, 40


def sh(*argv, env=None, check=True, timeout=60):
    p = subprocess.run(argv, env=env, capture_output=True, text=True, timeout=timeout)
    if check and p.returncode != 0:
        raise RuntimeError("%s: %s%s" % (" ".join(argv), p.stdout, p.stderr))
    return p


def build(dest):
    commit = sh("git", "-C", REPO, "rev-parse", "HEAD").stdout.strip()
    flag = "-X github.com/jack-work/figaro/internal/boot.commit=" + commit
    subprocess.run(["go", "build", "-ldflags", flag, "-o", dest, "./cmd/figaro"], cwd=REPO, check=True)


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


class Unit:
    def __init__(self, root, binary, doc):
        self.root = root
        self.bin = binary
        self.app = "figaro-parity-" + os.path.basename(root).rsplit("-", 1)[-1]
        self.port = free_port()
        self.tmux_sock = os.path.join(root, "tmux.sock")
        self.gateway = None
        self.doc = doc
        self.ids = {}
        for sub in ("run", "state", "cache", "config/outfits", "hush", "dev"):
            os.makedirs(os.path.join(root, sub), mode=0o700, exist_ok=True)
        with open(os.path.join(root, "hush-passphrase"), "w") as fh:
            fh.write("parity-" + os.path.basename(root))
        os.chmod(os.path.join(root, "hush-passphrase"), 0o600)
        with open(os.path.join(root, "config", "config.toml"), "w") as fh:
            fh.write('default_outfit = "default"\n\n[door.web]\npath = "%s"\nauthn = "upstream"\n\n[authz]\nowner = "user:gluck@parity-edge.invalid"\n\n[authz.peercred.uids]\n"%d" = "user:gluck@parity-edge.invalid"\n\n[authz.upstream]\nissuer = "https://parity-edge.invalid"\n' % (os.path.join(root, "run", "door.sock"), os.getuid()))
        with open(os.path.join(root, "config", "outfits", "default.toml"), "w") as fh:
            fh.write('[system]\nprovider = %s\nmodel = "claude-sonnet-4-5"\ncredo = "You are a test agent."\n' % json.dumps(doc["system"]["provider"]))
        self.script = os.path.join(root, "provider.json")
        with open(self.script, "w") as fh:
            json.dump(doc["provider"], fh)
        self.requests = os.path.join(root, "requests.jsonl")

    def env(self):
        base = {k: v for k, v in os.environ.items() if not k.startswith(("FIGARO_", "ANTHROPIC_", "HUSH_")) and k != "TMUX"}
        base.update({
            "FIGARO_RUNTIME_DIR": os.path.join(self.root, "run"),
            "FIGARO_STATE_DIR": os.path.join(self.root, "state"),
            "FIGARO_CACHE_DIR": os.path.join(self.root, "cache"),
            "FIGARO_CONFIG_DIR": os.path.join(self.root, "config"),
            "FIGARO_DEV_ROOT": os.path.join(self.root, "dev"),
            "FIGARO_HUSH_APP": self.app,
            "FIGARO_HUSH_DIR": os.path.join(self.root, "hush"),
            "FIGARO_HUSH_PASSPHRASE": open(os.path.join(self.root, "hush-passphrase")).read(),
            "FIGARO_HUSH_TTL": "10m",
            "FIGARO_NO_ANGELUS_SCOPE": "1",
            "HUSH_NO_AGENT_SCOPE": "1",
            "ANTHROPIC_API_KEY": "sk-ant-parity-unused",
            "ANTHROPIC_BASE_URL": "http://127.0.0.1:%d" % self.port,
        })
        return base

    def start_provider(self):
        self.gateway = subprocess.Popen([sys.executable, os.path.join(HERE, "fake-anthropic.py"), str(self.port), self.script, self.requests])
        for _ in range(50):
            try:
                socket.create_connection(("127.0.0.1", self.port), 0.2).close()
                return
            except OSError:
                time.sleep(0.1)
        raise RuntimeError("the fake provider never listened")

    def fig(self, *args, check=True):
        return sh(self.bin, *args, env=self.env(), check=check)

    def fig_json(self, *args):
        p = self.fig(*args, check=False)
        if p.returncode != 0 or not p.stdout.strip():
            return None
        return json.loads(p.stdout)

    def new_aria(self, name):
        out = self.fig("new", "-j").stdout.strip().splitlines()[-1]
        self.ids[name] = json.loads(out)["aria_id"]
        return self.ids[name]

    def tmux(self, *args, check=True):
        return sh("tmux", "-S", self.tmux_sock, *args, check=check)

    def pane(self):
        rc = os.path.join(self.root, "panerc")
        env = self.env()
        with open(rc, "w") as fh:
            for k, v in env.items():
                if k.startswith(("FIGARO_", "ANTHROPIC_", "HUSH_")):
                    fh.write("export %s=%s\n" % (k, shell_quote(v)))
            fh.write("unset FIGARO_ARIA FIGARO_NO_BIND\nexport PATH=%s:$PATH\nPS1='dp$ '\n" % shell_quote(os.path.dirname(self.bin)))
        subprocess.run(["tmux", "-S", self.tmux_sock, "new-session", "-d", "-s", "p", "-x", str(WIDTH), "-y", str(HEIGHT + 1), "bash --noprofile --init-file " + rc], env=dict(os.environ, TMUX=""), check=True)
        self.tmux("set", "-g", "status", "off")
        self.tmux("resize-window", "-t", "p", "-x", str(WIDTH), "-y", str(HEIGHT))
        height = self.tmux("display", "-p", "-t", "p", "#{pane_height}").stdout.strip()
        if height != str(HEIGHT):
            raise RuntimeError("pane is %s rows, asked for %d" % (height, HEIGHT))
        self.wait_screen(lambda s: s.rstrip().endswith("dp$"), "a shell prompt")

    def screen(self, escapes=False):
        args = ["capture-pane", "-p", "-t", "p"] + (["-e"] if escapes else [])
        return self.tmux(*args).stdout

    def wait_screen(self, pred, what, timeout=20):
        end = time.time() + timeout
        while time.time() < end:
            s = self.screen()
            if pred(s):
                return s
            time.sleep(0.15)
        raise RuntimeError("never saw %s:\n%s" % (what, self.screen()))

    def type(self, text, gap=0.015):
        for ch in text:
            self.tmux("send-keys", "-t", "p", "-l", ch)
            time.sleep(gap)

    def key(self, *keys):
        self.tmux("send-keys", "-t", "p", *keys)

    def settle(self, quiet=0.5, limit=10):
        end = time.time() + limit
        last = None
        while time.time() < end:
            now = re.sub("[" + SPINNER + "]", "", self.screen())
            if now == last:
                return
            last = now
            time.sleep(quiet)

    def daemons(self):
        mine = "FIGARO_RUNTIME_DIR=%s" % os.path.join(self.root, "run")
        found = []
        for pid in filter(str.isdigit, os.listdir("/proc")):
            try:
                with open("/proc/%s/environ" % pid, "rb") as fh:
                    if mine.encode() in fh.read().split(b"\0"):
                        found.append(int(pid))
            except OSError:
                continue
        return found

    def down(self):
        subprocess.run(["tmux", "-S", self.tmux_sock, "kill-server"], capture_output=True)
        subprocess.run([self.bin, "stop"], env=self.env(), capture_output=True)
        subprocess.run([self.bin, "vault", "forget", "--yes"], env=self.env(), capture_output=True)
        if self.gateway:
            self.gateway.terminate()
            self.gateway.wait(5)
        for pid in self.daemons():
            try:
                os.kill(pid, signal.SIGTERM)
            except OSError:
                pass
        subprocess.run(["secret-tool", "clear", "service", self.app], capture_output=True)

    def leaks(self):
        found = []
        if subprocess.run(["tmux", "-S", self.tmux_sock, "list-sessions"], capture_output=True).returncode == 0:
            found.append("tmux server")
        if self.daemons():
            found.append("processes %s" % self.daemons())
        if self.gateway and self.gateway.poll() is None:
            found.append("fake provider")
        probe = subprocess.run(["secret-tool", "search", "service", self.app], capture_output=True, text=True)
        if self.app in (probe.stdout + probe.stderr):
            found.append("keyring entry for " + self.app)
        for base in (os.path.expanduser("~/.local/state"), os.path.expanduser("~/.config"), os.path.expanduser("~/.cache")):
            if os.path.exists(os.path.join(base, self.app)):
                found.append(os.path.join(base, self.app))
        return found


def shell_quote(s):
    return "'" + s.replace("'", "'\"'\"'") + "'"


def flatten(prefix, value, out):
    if isinstance(value, dict):
        for k, v in value.items():
            flatten((prefix + "." if prefix else "") + k, v, out)
    else:
        out[prefix] = value


def node_facts(n, screen):
    out = {"type": n.get("type"), "role": n.get("role", "output")}
    if n.get("type") == "tool":
        out["name"] = n.get("name")
        out["status"] = n.get("status")
        out["output"] = n.get("output", "")
        out["args"] = n.get("args")
        out["open"] = n.get("output", "").strip().splitlines()[-1:] == [] or n.get("output", "").strip().splitlines()[-1] in screen
    else:
        out["text"] = n.get("markdown", "")
        out["open"] = n.get("markdown", "").strip() in screen or n.get("markdown", "").strip()[:30] in screen
    return out


def turn_facts(part, aria_id, screen):
    deltas = []
    for key, d in (part.get("formDeltas") or {}).items():
        name = key[len(d["form"]) + 1:] if key.startswith(d["form"] + ".") and d["form"] == aria_id else key
        deltas.append({"key": name, "old": d.get("prev"), "new": d.get("value"), "event": d.get("event")})
    deltas.sort(key=lambda d: d["key"])
    header = None
    if deltas:
        pat = re.compile(r"^> .*\s%d Δ\s*$" % part["turn"], re.M)
        header = "delta" if pat.search(screen) else None
    return {
        "turn": part["turn"],
        "inquiry": part.get("inquiry", ""),
        "nodes": [node_facts(n, screen) for n in part.get("nodes", [])],
        "deltas": [dict(d, header_glyph=header) for d in deltas],
    }


def screen_pit(screen):
    lines = screen.rstrip("\n").split("\n")
    rules = [i for i, line in enumerate(lines) if line.startswith("═")]
    if not rules:
        return None
    start = rules[-1]
    body = [line.rstrip() for line in lines[start + 1:-1]]
    return {"rows": [line for line in body if line.strip()], "footer": lines[-1].strip()}


class Runner:
    def __init__(self, unit, glyphs, outdir):
        self.u = unit
        self.glyphs = glyphs
        self.out = outdir
        self.current = None
        self.pit_name = None
        self.results = []

    def aria(self, name):
        return self.u.ids[name]

    def sub(self, text):
        for name, ident in self.u.ids.items():
            text = text.replace("{%s}" % name, ident)
        return text

    def attend(self, name):
        u = self.u
        for _ in range(6):
            if u.screen().rstrip().endswith("dp$"):
                break
            u.key("q")
            time.sleep(0.4)
        u.wait_screen(lambda s: s.rstrip().endswith("dp$"), "a shell prompt before attend")
        u.type("figaro attend %s && figaro listen" % self.aria(name), gap=0)
        u.key("Enter")
        u.wait_screen(lambda s: "live" in s.splitlines()[-1] or "live" in s, "the pager's live marker")
        self.current = name
        self.pit_name = None
        u.settle()

    def status_state(self):
        st = self.u.fig_json("status", self.aria(self.current), "-j")
        return st["state"] if st else None

    def wait(self, what, timeout=20):
        end = time.time() + timeout
        while time.time() < end:
            state = self.status_state()
            if what == "idle" and state == "idle":
                return
            if what == "running" and state not in ("idle", "dormant", None):
                return
            time.sleep(0.15)
        raise RuntimeError("aria never became %s (state %s)" % (what, self.status_state()))

    def send(self, text):
        u = self.u
        u.key("Escape")
        time.sleep(0.2)
        u.key(">")
        time.sleep(0.3)
        u.type(text)
        time.sleep(0.2)
        u.key("M-Enter")
        time.sleep(0.8)

    def fork(self, spec):
        before = {a["id"] for a in (self.u.fig_json("list", "-j") or [])}
        self.u.key("Escape")
        time.sleep(0.2)
        self.u.key(":")
        time.sleep(0.2)
        self.u.type("fork -f :%d" % spec["at_turn"])
        self.u.key("Enter")
        end = time.time() + 15
        while time.time() < end:
            for a in self.u.fig_json("list", "-j") or []:
                if a["id"] not in before and a.get("parent") == self.aria(spec["aria"]):
                    self.u.ids[spec["as"]] = a["id"]
                    return
            time.sleep(0.2)
        raise RuntimeError("no fork of %s appeared" % spec["aria"])

    def act(self, do):
        u = self.u
        if "attend" in do:
            self.attend(do["attend"])
        if "send" in do:
            self.send(do["send"])
        if "wait" in do:
            self.wait(do["wait"], do.get("timeout", 20))
            if "sleep" in do:
                time.sleep(do["sleep"])
        if "then" in do:
            self.act(do["then"])
        if "sleep" in do and "wait" not in do:
            time.sleep(do["sleep"])
        if "set" in do:
            u.fig("set", "--id", self.aria(self.current), do["set"]["key"], do["set"]["value"])
        if "fork" in do:
            self.fork(do["fork"])
        if "pit" in do:
            if do["pit"] == "close":
                u.key("Escape")
                self.pit_name = None
            else:
                u.key(PIT_KEYS[do["pit"]])
                self.pit_name = do["pit"]
            time.sleep(0.6)
        if "command_typed" in do:
            u.key("Escape")
            time.sleep(0.2)
            u.key(":")
            time.sleep(0.2)
            u.type(self.sub(do["command_typed"]))
        if do.get("command_enter"):
            u.key("Enter")
            time.sleep(0.8)

    def facts(self, screen, expect):
        u = self.u
        out = {}
        if self.current:
            ident = self.aria(self.current)
            shown = u.fig_json("show", ident, "-a", "-j")
            out["turns"] = [turn_facts(p, ident, screen) for p in (shown or {}).get("parts", [])]
            form = u.fig_json("state", "show", "--id", ident, "-j") or {}
            flat = {}
            flatten("", form, flat)
            out["state"] = flat
            out["running"] = self.status_state() not in ("idle", "dormant", None)
            queue = u.fig_json("queue", "ls", "--id", ident, "-j")
            out["queue"] = queue if queue is not None else []
        listing = u.fig_json("list", "-j") or []
        by_id = {i: n for n, i in u.ids.items()}
        edges = []
        for a in listing:
            if a["id"] in by_id and a.get("parent") in by_id:
                edges.append([by_id[a["parent"]], by_id[a["id"]]])
        out["tree"] = {"edges": sorted(edges)}
        out["pit"] = screen_pit(screen)
        if out["pit"]:
            out["pit"]["name"] = self.pit_name
        out["footer"] = screen.rstrip("\n").split("\n")[-1].strip()
        out["screen"] = {"text": screen, "glyphs": sorted(name for name, symbol in self.glyphs.items() if symbol in screen and not symbol.isascii())}
        return out


def match_node(want, got):
    bad = []
    for k in ("type", "role", "name", "status", "open"):
        if k in want and got.get(k) != want[k]:
            bad.append("%s: want %r got %r" % (k, want[k], got.get(k)))
    if "text_contains" in want and want["text_contains"] not in (got.get("text") or ""):
        bad.append("text lacks %r: %r" % (want["text_contains"], got.get("text")))
    if "output_contains" in want and want["output_contains"] not in (got.get("output") or ""):
        bad.append("output lacks %r: %r" % (want["output_contains"], got.get("output")))
    return bad


def check(expect, facts, glyphs):
    bad = []
    if "running" in expect and facts.get("running") != expect["running"]:
        bad.append("running: want %r got %r" % (expect["running"], facts.get("running")))
    if "turns" in expect:
        have = {t["turn"]: t for t in facts.get("turns", [])}
        if len(expect["turns"]) != len(have):
            bad.append("turn count: want %d got %d" % (len(expect["turns"]), len(have)))
        for want in expect["turns"]:
            got = have.get(want["turn"])
            if got is None:
                bad.append("turn %d missing" % want["turn"])
                continue
            if got["inquiry"] != want["inquiry"]:
                bad.append("turn %d inquiry: want %r got %r" % (want["turn"], want["inquiry"], got["inquiry"]))
            if len(got["nodes"]) != len(want["nodes"]):
                bad.append("turn %d node count: want %d got %d (%s)" % (want["turn"], len(want["nodes"]), len(got["nodes"]), [n["type"] for n in got["nodes"]]))
            for i, (wn, gn) in enumerate(zip(want["nodes"], got["nodes"])):
                bad += ["turn %d node %d %s" % (want["turn"], i, b) for b in match_node(wn, gn)]
            for wd in want.get("deltas", []):
                hit = [d for d in got["deltas"] if d["key"] == wd["key"]]
                if not hit:
                    bad.append("turn %d delta %s missing" % (want["turn"], wd["key"]))
                    continue
                d = hit[0]
                for k in ("old", "new", "header_glyph"):
                    if k in wd and d.get(k) != wd[k]:
                        bad.append("turn %d delta %s %s: want %r got %r" % (want["turn"], wd["key"], k, wd[k], d.get(k)))
    for k, v in expect.get("state", {}).items():
        if facts.get("state", {}).get(k) != v:
            bad.append("state %s: want %r got %r" % (k, v, facts.get("state", {}).get(k)))
    if "tree" in expect and facts["tree"]["edges"] != sorted(expect["tree"]["edges"]):
        bad.append("tree edges: want %r got %r" % (expect["tree"]["edges"], facts["tree"]["edges"]))
    if "pit" in expect:
        want, got = expect["pit"], facts.get("pit")
        if want is None:
            if got is not None:
                bad.append("a pit is open: %r" % got["rows"])
        elif got is None:
            if "footer" not in want or want["footer"] not in facts.get("footer", ""):
                bad.append("pit %s is not open" % want["name"])
        else:
            cursor = 0
            if "footer" in want and want["footer"] not in facts.get("footer", ""):
                bad.append("footer lacks %r: %r" % (want["footer"], facts.get("footer")))
            for row in want["rows"]:
                found = next((i for i in range(cursor, len(got["rows"])) if row in got["rows"][i]), None)
                if found is None:
                    bad.append("pit row %r missing or out of order in %r" % (row, got["rows"]))
                else:
                    cursor = found + 1
            if "glyph" in want and glyphs[want["glyph"]] not in "\n".join(got["rows"]):
                bad.append("pit glyph %s (%s) absent" % (want["glyph"], glyphs[want["glyph"]]))
    screen = expect.get("screen", {})
    for text in screen.get("contains", []):
        if text not in facts["screen"]["text"]:
            bad.append("screen lacks %r" % text)
    for name in expect.get("glyphs_on_screen", []) + screen.get("glyphs", []):
        if name not in facts["screen"]["glyphs"]:
            bad.append("glyph %s (%s) not on screen" % (name, glyphs[name]))
    return bad


def run(args):
    doc = json.load(open(os.path.join(HERE, "scenarios.json")))
    glyphs = json.load(open(os.path.join(HERE, "glyphs.json")))
    binary = args.bin or "/var/tmp/parity-bin/figaro"
    if not args.bin:
        os.makedirs(os.path.dirname(binary), exist_ok=True)
        build(binary)
    outdir = os.path.abspath(args.out or "/var/tmp/parity-%s" % datetime.date.today().isoformat())
    pager_out = os.path.join(outdir, "pager")
    os.makedirs(pager_out, exist_ok=True)
    shutil.copy(os.path.join(HERE, "scenarios.json"), os.path.join(outdir, "scenarios.json"))
    shutil.copy(os.path.join(HERE, "glyphs.json"), os.path.join(outdir, "glyphs.json"))
    results = []
    failed = False
    for scenario in doc["scenarios"]:
        if args.scenario and scenario["id"] not in args.scenario:
            continue
        root = tempfile.mkdtemp(prefix="parity-unit-", dir="/var/tmp")
        unit = Unit(root, binary, doc)
        try:
            unit.start_provider()
            unit.pane()
            r = Runner(unit, glyphs, pager_out)
            unit.new_aria(scenario["setup"]["aria"])
            for prompt in scenario["setup"]["seed"]:
                unit.fig("send", "--id", unit.ids[scenario["setup"]["aria"]], "--", prompt)
            sdir = os.path.join(pager_out, scenario["id"])
            os.makedirs(sdir, exist_ok=True)
            for step in scenario["steps"]:
                problems = []
                try:
                    r.act(step["do"])
                    time.sleep(0.4)
                    if not step["expect"].get("running"):
                        unit.settle()
                    screen = unit.screen()
                    with open(os.path.join(sdir, step["id"] + ".txt"), "w") as fh:
                        fh.write(screen)
                    with open(os.path.join(sdir, step["id"] + ".ansi"), "w") as fh:
                        fh.write(unit.screen(escapes=True))
                    facts = r.facts(screen, step["expect"])
                    with open(os.path.join(sdir, step["id"] + ".facts.json"), "w") as fh:
                        json.dump(facts, fh, indent=1, ensure_ascii=False)
                    problems = check(step["expect"], facts, glyphs)
                except Exception as e:
                    problems = ["driver: %s" % e]
                results.append({"scenario": scenario["id"], "step": step["id"], "ok": not problems, "problems": problems})
                failed = failed or bool(problems)
                print("%s %s/%s" % ("ok  " if not problems else "FAIL", scenario["id"], step["id"]))
                for p in problems:
                    print("     " + p.replace("\n", "\n     "))
        finally:
            unit.down()
            leaks = unit.leaks()
            if leaks:
                print("LEAK after %s: %s" % (scenario["id"], leaks))
                failed = True
            if not args.keep:
                shutil.rmtree(root, ignore_errors=True)
    with open(os.path.join(pager_out, "results.json"), "w") as fh:
        json.dump(results, fh, indent=1)
    print("pager captures in %s" % pager_out)
    return 1 if failed else 0


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description="drive the real pager through scripts/parity/scenarios.json on an isolated daemon")
    ap.add_argument("--out")
    ap.add_argument("--bin")
    ap.add_argument("--scenario", action="append")
    ap.add_argument("--keep", action="store_true")
    sys.exit(run(ap.parse_args()))
