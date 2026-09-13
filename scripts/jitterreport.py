#!/usr/bin/env python3
"""Read one jitterbench run and report the submit-jitter numbers.

Everything is per PAGER PROCESS: the marks file holds lines from the driver's
own short-lived CLI calls too, and counting their frames would be counting a
different screen.
"""
import json
import statistics
import sys

BUSY = {"thinking", "tooling"}


def load(path):
    out = []
    with open(path) as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                out.append(json.loads(line))
            except json.JSONDecodeError:
                pass
    out.sort(key=lambda r: r["t"])
    return out


def pager_pid(rows):
    # The pager is the cli process that painted the most frames.
    tally = {}
    for r in rows:
        if r["m"] == "frame" and r["proc"] == "cli":
            tally[r["pid"]] = tally.get(r["pid"], 0) + 1
    return max(tally, key=tally.get) if tally else None


def pct(xs, p):
    if not xs:
        return 0.0
    xs = sorted(xs)
    k = (len(xs) - 1) * p
    lo, hi = int(k), min(int(k) + 1, len(xs) - 1)
    return xs[lo] + (xs[hi] - xs[lo]) * (k - lo)


def ms(ns):
    return ns / 1e6


def main(out):
    rows = load(f"{out}/marks.jsonl")
    pid = pager_pid(rows)
    mine = [r for r in rows if r.get("pid") == pid and r["proc"] == "cli"]
    rounds = []
    with open(f"{out}/rounds.tsv") as fh:
        for line in fh:
            t, kind, n = line.split()
            rounds.append((int(t), kind, n))
    pit = []
    with open(f"{out}/pit.tsv") as fh:
        for line in fh:
            parts = line.split()
            if len(parts) == 2 and parts[0].isdigit():
                pit.append((int(parts[0]), int(parts[1])))

    print(f"pager pid {pid}: {len(mine)} marks of {len(rows)}")

    # 1. FRAME JITTER while the runtime is thinking or tooling, as marks.md
    #    defines it.
    busy_spans = []
    cur = None
    for r in mine:
        if r["m"] != "runtime":
            continue
        st = r.get("state")
        if st in BUSY and cur is None:
            cur = r["t"]
        elif st not in BUSY and cur is not None:
            busy_spans.append((cur, r["t"]))
            cur = None
    if cur is not None:
        busy_spans.append((cur, mine[-1]["t"]))

    def in_busy(t):
        return any(a <= t <= b for a, b in busy_spans)

    frames = [r["t"] for r in mine if r["m"] == "frame" and in_busy(r["t"])]
    gaps = [ms(b - a) for a, b in zip(frames, frames[1:])]
    quiet = sum(1 for r in mine if r["m"] == "frame.quiet" and in_busy(r["t"]))
    busy_ms = sum(ms(b - a) for a, b in busy_spans)
    print(f"\nframe jitter while thinking/tooling ({len(frames)} frames over "
          f"{busy_ms:.0f} ms of turn)")
    if gaps:
        print(f"  p50 {pct(gaps,0.5):.1f} ms  p95 {pct(gaps,0.95):.1f} ms  "
              f"max {max(gaps):.1f} ms  mean {statistics.mean(gaps):.1f} ms")
    print(f"  {len(frames)/(busy_ms/1000):.1f} frames/s, {quiet} quiet renders")

    # 2. THE SUBMIT WINDOW: from the Enter that submitted to the moment the
    #    runtime says thinking. This is where the flicker lives.
    submits = [r for r in mine if r["m"] == "submit.qua" and r.get("ms") is not None]
    print(f"\nsubmit windows ({len(submits)})")
    per = []
    for s in submits:
        t0 = s["t"]
        # the Enter that caused it: the last key mark before the call returned
        keys = [r["t"] for r in mine if r["m"] == "key" and r["t"] <= t0]
        start = keys[-1] if keys else t0
        nxt = [r["t"] for r in mine
               if r["m"] == "runtime" and r.get("state") in BUSY and r["t"] > start]
        end = nxt[0] if nxt else start + int(1.5e9)
        f = sum(1 for r in mine if r["m"] == "frame" and start <= r["t"] <= end)
        q = sum(1 for r in mine if r["m"] == "frame.quiet" and start <= r["t"] <= end)
        # how long the drawer held rows, and how long the mirror held a live row
        live = {}
        for r in mine:
            if r["m"] != "queue.row" or not (start <= r["t"] <= end + int(1e9)):
                continue
            span = live.setdefault(r["id"], [None, None])
            if r.get("state") in ("queued", "committing"):
                if span[0] is None:
                    span[0] = r["t"]
                span[1] = r["t"]
            elif span[0] is not None and span[1] is not None and span[1] >= span[0]:
                span[1] = max(span[1], r["t"])
        held = max((ms(b - a) for a, b in live.values() if a and b), default=0.0)
        # the drawer: from the first draw with rows to the next draw with none
        draws = [r for r in mine
                 if r["m"] == "queue.draw" and start <= r["t"] <= end + int(1e9)]
        shown, opened = 0.0, 0
        open_at = None
        for d in draws:
            if d["rows"] > 0 and open_at is None:
                open_at, opened = d["t"], opened + 1
            elif d["rows"] == 0 and open_at is not None:
                shown += ms(d["t"] - open_at)
                open_at = None
        per.append((ms(end - start), f, q, held, opened, shown))
    if per:
        print("   enter->thinking ms | frames | quiet | live row ms | drawer opens | drawer shown ms")
        for w, f, q, h, o, s in per:
            print(f"   {w:18.1f} | {f:6d} | {q:5d} | {h:11.1f} | {o:12d} | {s:15.1f}")
        print(f"   median frames in the submit window: "
              f"{statistics.median([f for _, f, _, _, _, _ in per]):.1f}")
        idle_w = [p for p in per if p[0] < 1000]
        if idle_w:
            print(f"   IDLE submits ({len(idle_w)}): drawer opened "
                  f"{sum(p[4] for p in idle_w)} times, shown "
                  f"{statistics.median([p[5] for p in idle_w]):.1f} ms median, "
                  f"live row {statistics.median([p[3] for p in idle_w]):.1f} ms median")
        busy_w = [p for p in per if p[0] >= 1000]
        if busy_w:
            print(f"   BUSY submits ({len(busy_w)}): drawer opened "
                  f"{sum(p[4] for p in busy_w)} times, shown "
                  f"{statistics.median([p[5] for p in busy_w]):.1f} ms median, "
                  f"live row {statistics.median([p[3] for p in busy_w]):.1f} ms median")

    # 3. THE SCREEN: episodes where the queue pit was actually visible.
    episodes = []
    start = None
    for t, n in pit:
        if n > 0 and start is None:
            start = t
        elif n == 0 and start is not None:
            episodes.append((start, t))
            start = None
    if start is not None:
        episodes.append((start, pit[-1][0]))
    print(f"\npit visible on screen: {len(episodes)} episodes")
    for a, b in episodes:
        kind = "?"
        for rt, k, n in rounds:
            if rt <= a:
                kind = f"{k}{n}"
        print(f"   {ms(b-a):8.0f} ms   after round {kind}")
    idle_eps = [e for e in episodes
                if next((k for rt, k, n in reversed(rounds) if rt <= e[0]), "?") == "idle"]
    print(f"   of those, {len(idle_eps)} began in an idle round "
          f"(the flicker), {len(episodes)-len(idle_eps)} in a busy one (wanted)")


if __name__ == "__main__":
    main(sys.argv[1])
