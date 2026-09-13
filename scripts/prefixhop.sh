#!/usr/bin/env bash
# prefixhop.sh: what one aria-to-aria hop COSTS, measured in a real pane.
#
# Needs the fixture scripts/prefixcorpus.sh built (reads $BOX/ids.env). Drives
# `figaro listen` in tmux, types `:attend` three times -- parent to branch,
# branch back to parent, parent to cousin -- and reports, per hop:
#
#   wall    keystroke to the last byte the daemon sent for that hop
#   reads   figaro.read calls the client made
#   bytes   bytes the daemon sent back on them
#   scroll  did the first body row of the pane change
#
# The tape (`listen --record`) is the instrument: every JSON-RPC message with
# the time it crossed. Nothing here believes the screen about the wire, or the
# wire about the screen.
set -uo pipefail

BOX=${BOX:-/tmp/figaro-prefix}
LABEL=${LABEL:-before}
. "$BOX/ids.env"

# HOPBIN RUNS A DIFFERENT BINARY AGAINST THE SAME STORE, which is the whole of
# an A/B: two arms measured on one corpus. The binary is named by ABSOLUTE PATH
# into the pane, never found on PATH, because a pane inherits the installed
# figaro and an A/B whose arms are the same binary reports no difference and
# means nothing.
BIN=${HOPBIN:-$BIN}

SESS=prefixhop$$
TAPE=$BOX/hop-$LABEL.tape
MARKS=$BOX/hop-$LABEL.marks
OUT=$BOX/hop-$LABEL.txt
rm -f "$TAPE" "$MARKS" "$OUT"

cleanup() {
  tmux -L "$SESS" kill-server 2>/dev/null
  "$BIN" stop >/dev/null 2>&1
}
trap cleanup EXIT

now() { python3 -c 'import time;print("%.3f"%time.time())'; }

pane() { tmux -L "$SESS" capture-pane -p -t w 2>/dev/null; }

# settle waits until the pane has been still for most of a second. A SHORTER
# STILLNESS LIED: at two polls of 100ms it returned while the switch's read was
# still in flight, and the capture showed the aria we had just left with the
# footer of the one we had just reached.
settle() {
  local prev="" cur="" same=0
  for _ in $(seq 200); do
    cur=$(pane | md5sum)
    if [[ "$cur" == "$prev" ]]; then
      same=$((same + 1))
      [[ $same -ge 6 ]] && return 0
    else
      same=0
    fi
    prev=$cur
    sleep 0.15
  done
  return 1
}

# A RUN WAITS FOR THE PREVIOUS DAEMON TO BE GONE. Every run stops the daemon on
# its way out, and back-to-back runs then raced the shutdown: the pane's listen
# found a socket whose daemon was already leaving, exited at once, and the
# harness reported that the pager never opened. Three arms of an A/B died that
# way before the fourth ran cleanly.
for _ in $(seq 40); do
  pgrep -f "$BIN" >/dev/null 2>&1 || break
  sleep 0.25
done

# The pane runs the binary DIRECTLY, through a runner that exports the box's
# environment. A send-keys line into the user's login shell was the first
# version, and it ran fish, printed a fastfetch banner and never started
# figaro at all.
cat > "$BOX/run.sh" <<EOF
#!/usr/bin/env bash
. "$BOX/ids.env"
export FIGARO_MARKS=$BOX/marks-$LABEL.jsonl
exec "$BIN" listen "$PARENT" --record "$TAPE" --note hop-$LABEL
EOF
rm -f "$BOX/marks-$LABEL.jsonl"
chmod +x "$BOX/run.sh"

tmux -L "$SESS" new-session -d -s w -x 120 -y 51 "$BOX/run.sh"
tmux -L "$SESS" set-option -g status off
settle
sleep 1
# A PANE THAT NEVER STARTED MUST NOT REPORT A RESULT. Without this the whole
# run continued against a dead session, every capture came back empty, and the
# scroll check compared two empty files and printed the word it prints when
# nothing moved.
if ! pane | grep -q "live\|[0-9]–[0-9]"; then
  echo "the pager never opened; the pane says:" >&2
  pane | tail -5 >&2
  exit 1
fi

hop() { # hop <name> <target>
  local name=$1 target=$2
  local t0 t1
  t0=$(now)
  tmux -L "$SESS" send-keys -t w ":attend $target" Enter
  settle
  # A SECOND SETTLE AFTER A DELIBERATE PAUSE. Stillness alone is not arrival:
  # the cousin hop went quiet for a second with the previous aria on screen and
  # the new aria's footer under it, then repainted 2.2 seconds in when the last
  # read landed. Everything measured off that first capture was a lie.
  sleep 2
  settle
  t1=$(now)
  echo "$name $target $t0 $t1" >> "$MARKS"
  pane > "$BOX/pane-$LABEL-$name.txt"
  sleep 0.4
}

pane > "$BOX/pane-$LABEL-start.txt"
hop toBranch "$BRANCH"
hop toParent "$PARENT"
hop toCousin "$COUSIN"

# THE PARKED CASE, which is the one the retention design is about: put the
# window ON the divergence (the last turn the two arias share), then hop.
# Everything above the divergence is the same bytes in both, so a client that
# keeps its prefix must not move the screen.
tmux -L "$SESS" send-keys -t w ":$((FORKTURN - 1))" Enter
settle
pane > "$BOX/pane-$LABEL-parked.txt"
hop parkedHop "$PARENT"

# THE IDLE COST. A pager that is doing nothing should cost nothing. Ten seconds
# of stillness, and whatever crosses the wire in them is what a reader pays for
# looking at a conversation.
IDLE0=$(now)
sleep 10
echo "idle - $IDLE0 $(now)" >> "$MARKS"
pane > "$BOX/pane-$LABEL-idle.txt"

tmux -L "$SESS" send-keys -t w q
sleep 0.3
tmux -L "$SESS" send-keys -t w C-d
sleep 1

python3 - "$TAPE" "$MARKS" "$BOX" "$LABEL" <<'PY' | tee "$OUT"
import json, sys, datetime
tape, marks, box, label = sys.argv[1:5]
with open(tape) as fh:
    lines = [json.loads(l) for l in fh if l.strip()]
head, frames = lines[0], lines[1:]
t0 = datetime.datetime.fromisoformat(head["started"]).timestamp()

calls = {}   # id -> (method, t)
events = []  # (wall, kind, method, bytes)
for f in frames:
    msg = json.loads(f["msg"]) if isinstance(f["msg"], str) else f["msg"]
    wall = t0 + f["t"]
    n = len(json.dumps(msg))
    if f["dir"] == "out" and "method" in msg:
        calls[msg.get("id")] = msg["method"]
        events.append((wall, "call", msg["method"], n))
    elif f["dir"] == "in" and "method" in msg:
        events.append((wall, "push", msg["method"], n))
    else:
        events.append((wall, "reply", calls.get(msg.get("id"), "?"), n))

marklist = [l.split() for l in open(marks)]

# A HOP OWNS ITS BURST, not everything up to the next keystroke: the pager
# re-asks for metrics on its own clock (a read of one message, every couple of
# seconds), and charging those to the hop put a third more bytes on every row.
# A burst ends when the wire has been quiet for QUIET seconds.
QUIET = 0.6


def burst(lo, hi):
    win = []
    last = lo
    for e in events:
        if e[0] < lo or e[0] >= hi:
            continue
        if e[0] - last > QUIET and win:
            break
        win.append(e)
        last = e[0]
    return win


def row(name, win, settled_at, lo):
    reads = sum(1 for e in win if e[1] == "call" and e[2] == "figaro.read")
    rb = sum(e[3] for e in win if e[1] == "reply" and e[2] == "figaro.read")
    calls = sum(1 for e in win if e[1] == "call")
    pb = sum(e[3] for e in win if e[1] == "push")
    last = max((e[0] for e in win), default=lo)
    wire = f"{last-lo:7.2f}" if lo else " " * 7
    settle = f"{settled_at-lo:9.2f}" if settled_at else " " * 9
    print(f"{name:<10} {wire} {settle} {reads:6d} {rb:11d} {calls:6d} {pb:10d}")


bounds = []
for i, (name, target, a, b) in enumerate(marklist):
    lo = float(a)
    hi = float(marklist[i + 1][2]) if i + 1 < len(marklist) else 1e18
    bounds.append((name, target, lo, hi, float(b)))

print(f"# {label}: {head.get('binary','')} · {len(frames)} frames")
print(f"{'hop':<10} {'wire_s':>7} {'settle_s':>9} {'reads':>6} {'read_bytes':>11} {'calls':>6} {'push_bytes':>10}")
row("(startup)", [e for e in events if e[0] < bounds[0][2]], 0, 0)
for name, target, lo, hi, settled in bounds:
    # The idle row is the WHOLE window, not a burst: the question there is what
    # a still pager spends, and a burst rule would report the first tick only.
    win = [e for e in events if lo <= e[0] < settled] if name == "idle" else burst(lo, hi)
    row(name, win, settled, lo)
PY

echo
echo "== first body row, per hop (scroll evidence)"
for f in "$BOX"/pane-"$LABEL"-*.txt; do
  printf '%-34s %s\n' "$(basename "$f")" "$(sed -n '2p' "$f" | cut -c1-60)"
done

echo
echo "== TTFCP: keystroke to the first frame carrying content, per hop"
python3 - "$BOX/marks-$LABEL.jsonl" "$MARKS" <<'TTFCPPY' || true
import json, sys, datetime

marks = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
hops = [l.split() for l in open(sys.argv[2])]

# The mark clock is unix nanoseconds and the harness clock is unix seconds, so
# they join without translation.
keys = [m for m in marks if m.get("m") == "hop"]
frames = [m for m in marks if m.get("m") == "frame" and m.get("content")]
for name, target, a, b in hops:
    if name == "idle":
        continue
    start = float(a)
    hop = next((h for h in keys if h["t"] / 1e9 >= start), None)
    if hop is None:
        print("%-10s no hop mark" % name)
        continue
    at = hop["t"] / 1e9
    painted = next((f for f in frames if f["t"] / 1e9 >= at), None)
    if painted is None:
        print("%-10s no contentful frame" % name)
        continue
    print("%-10s %6.1f ms  (hop %5.1f ms, dial+lineage+read inside it)" % (
        name, (painted["t"] / 1e9 - at) * 1000, hop.get("ms", 0)))
TTFCPPY

echo
echo "== memory, from the mem marks (cli side)"
python3 - "$BOX/marks-$LABEL.jsonl" <<'MEMPY' || true
import json, sys
rows = []
for line in open(sys.argv[1]):
    try:
        m = json.loads(line)
    except ValueError:
        continue
    if m.get("m") == "mem":
        rows.append(m)
if not rows:
    print("no mem marks")
else:
    peak = max(rows, key=lambda r: r.get("heap_alloc", 0))
    last = rows[-1]
    for tag, r in (("peak", peak), ("last", last)):
        print("%-5s heap_alloc=%7.2fMB heap_inuse=%7.2fMB rss=%7.2fMB window_msgs=%s rowcache=%s" % (
            tag, r.get("heap_alloc", 0) / 1e6, r.get("heap_inuse", 0) / 1e6,
            r.get("rss", 0) / 1e6, r.get("window_msgs"), r.get("rowcache_rows")))
MEMPY

echo
echo "== parked on the divergence: what the hop did to the screen"
diff <(sed -n '2,48p' "$BOX/pane-$LABEL-parked.txt") \
     <(sed -n '2,48p' "$BOX/pane-$LABEL-parkedHop.txt") > "$BOX/parked-$LABEL.diff"
# THE COMPARISON IS OVER THE SHARED CONTENT ONLY. The divergence row itself
# MUST change: it is the first row the two arias do not share, and on this
# fixture it carries the branch's own question and its fork marker. Comparing
# whole panes reported four moved rows for the one reason they are allowed to
# move, and an earlier run only read as clean because the divergence happened
# to sit off-screen.
#
# The divergence is found in the pane rather than assumed: the aria being left
# is the cousin, and the row carrying its own first question is where the two
# conversations part.
python3 - "$BOX/pane-$LABEL-parked.txt" "$BOX/pane-$LABEL-parkedHop.txt" "cousin: say your piece" <<'SCROLLPY'
import sys
a = open(sys.argv[1]).read().splitlines()[1:48]
b = open(sys.argv[2]).read().splitlines()[1:48]
marker = sys.argv[3]

cut = next((i for i, row in enumerate(a) if marker in row), len(a))
# A TURN IS A BLOCK, NOT A LINE. The question is drawn under its own voice
# header, and on a fork point that header carries the fork glyph, so it belongs
# to the divergent turn and not to the shared prefix above it. Walk back to the
# rule that opens the block.
while cut > 0 and "\u2500" not in a[cut - 1]:
    cut -= 1
if cut == 0:
    sys.exit("the divergence is the first row on screen: the fixture parked in the wrong place")
shared_before, shared_after = a[:cut], b[:cut]
moved = [i for i, (x, y) in enumerate(zip(shared_before, shared_after)) if x != y]
print("shared rows on screen above the divergence: %d" % cut)
if moved:
    print("MOVED: %d of them differ, first at row %d" % (len(moved), moved[0] + 1))
    print("  was: %s" % shared_before[moved[0]][:70])
    print("  now: %s" % shared_after[moved[0]][:70])
    sys.exit(1)
print("KEPT: every shared row is byte-identical across the hop")
if cut < len(b):
    print("  the divergence row, which must change: %s" % b[cut][:70])
SCROLLPY
