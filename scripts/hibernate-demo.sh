#!/usr/bin/env bash
# One command that shows aria hibernation working, in a real terminal, with
# your own eyes. Opens N arias, attaches a `figaro listen` pane to each in
# tmux, waits out an aggressively short dormancy window, and reports the
# counters as the sweep reclaims them.
#
# What you are looking for, in order:
#
#   1. live=N, attached-clients=N          -- everything resident and watched
#   2. live=0, attached-clients=N          -- every AGENT reclaimed while every
#                                             TERMINAL stayed connected. This is
#                                             the state that was unreachable
#                                             before the hub: killing an agent
#                                             used to close its socket and EOF
#                                             every client.
#   3. prompt a reclaimed aria             -- it wakes, and its listener renders
#                                             the reply incrementally on the
#                                             SAME connection.
#
# Runs against an ISOLATED runtime, state and config dir. Your own daemon,
# store and tmux sessions are never touched. Cleans up the daemon as well as
# the session on exit -- an orphaned scratch daemon is how seventeen agents
# once left 230 processes behind.
#
# Each aria is SEEDED with one short turn before the sweep, so when you attach
# you see real transcripts that happen to be hibernated. Without that the panes
# are blank -- correct, since an unprompted aria has no history, but useless: a
# blank pane cannot tell you "connected and healthy" apart from "hung".
#
#   scripts/hibernate-demo.sh                 5 arias, one short turn each
#   ARIAS=3 scripts/hibernate-demo.sh         fewer panes
#   NO_TOKENS=1 scripts/hibernate-demo.sh     skip seeding: no model calls at
#                                             all, and blank transcripts
#   WAKE=1 scripts/hibernate-demo.sh          also prompt one AFTER it hibernates,
#                                             to watch frames stream in
#   KEEP=1 scripts/hibernate-demo.sh          leave it running to poke at
#
# Seeding and waking need a real provider, borrowed from ~/.config/figaro. A few
# hundred tokens per run.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# Per-run identity. An earlier version hardcoded one path, so a second run
# rm -rf'd the first run's box out from under its live daemon while its author
# was still looking at it. Nothing shared, nothing to collide.
RUN=${RUN:-$$}
BOX=/tmp/figaro-hibdemo-$RUN
RT=/run/user/$(id -u)/figaro-hibdemo-$RUN
BIN=$BOX/figaro
SESSION=hibdemo-$RUN
ARIAS=${ARIAS:-5}
NO_TOKENS=${NO_TOKENS:-0}
WAKE=${WAKE:-0}
KEEP=${KEEP:-0}

fig() { FIGARO_RUNTIME_DIR=$RT FIGARO_STATE_DIR=$BOX/state FIGARO_CONFIG_DIR=$BOX/cfg "$BIN" "$@"; }
mem() { fig doctor mem 2>/dev/null | head -3 | tr -s ' '; }
counters() { fig doctor mem 2>/dev/null | grep -oE 'live=[0-9]+|attached-clients=[0-9]+|resident-rows=[0-9]+' | tr '\n' ' '; }
# Message count straight off the store, so it is true whether or not an agent
# is resident -- which after the sweep is the only way to ask.
msgcount() { fig status "$1" -j 2>/dev/null | python3 -c 'import sys,json
try: print(json.load(sys.stdin).get("message_count", 0))
except Exception: print(0)'; }

cleanup() {
  if [[ $KEEP == 1 ]]; then
    # ATTACH=1 hands the terminal straight to tmux, so the whole demo is one
    # command. exec, so this script's shell is replaced rather than left
    # waiting behind the session.
    if [[ ${ATTACH:-0} == 1 ]] && [[ -t 1 ]]; then
      echo; echo "attaching to $SESSION (Ctrl-b d to detach)"
      echo "  teardown: $BOX/teardown.sh"
      exec tmux attach -t "$SESSION"
    fi
    echo; echo "KEEP=1: left running. tmux attach -t $SESSION"
    echo "  teardown: $BOX/teardown.sh"
    return
  fi
  echo
  echo "== teardown"
  tmux kill-session -t $SESSION 2>/dev/null || true
  fig stop --force >/dev/null 2>&1 || true
  sleep 1
  rm -rf "$BOX" "$RT"
  echo "   daemon stopped, session killed, dirs removed"
}
trap cleanup EXIT

rm -rf "$BOX" "$RT"; mkdir -p "$BOX"/{cfg/outfits,state} "$RT"

# A teardown that does not require remembering four paths.
cat > "$BOX/teardown.sh" <<TD
#!/usr/bin/env bash
tmux kill-session -t $SESSION 2>/dev/null
FIGARO_RUNTIME_DIR=$RT $BIN stop --force >/dev/null 2>&1
sleep 1; rm -rf $BOX $RT
echo "hibdemo $RUN torn down"
TD
chmod +x "$BOX/teardown.sh"

# A plain `go build` in a git WORKTREE stamps no revision -- Go's VCS
# autodetection only fires when .git is a directory. Stamp by hand or
# `figaro version` says unknown and the CLI/daemon build handshake has nothing
# to compare.
echo "== building $(git -C "$ROOT" rev-parse --short HEAD)"
( cd "$ROOT" && go build \
    -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse HEAD)" \
    -o "$BIN" ./cmd/figaro )

# Egregiously short on purpose: one minute is the floor above "disabled", and a
# one-second sweep means you see the transition rather than wait for it.
cat > "$BOX/cfg/config.toml" <<'EOF'
default_outfit = "hibdemo"
check_updates = false

[memory]
dormant_after_minutes = 1
sweep_interval_seconds = 1
max_live_arias = 1
ir_window_mb = 1
EOF

cat > "$BOX/cfg/credo.md" <<'EOF'
You are a scratch aria in a hibernation demo. Answer briefly.
EOF

# Borrow a provider from the real config so no credentials are copied or read
# here; hush resolves them in the daemon. Falls back to a bare outfit, which
# is enough to MINT arias (no model call) but not to prompt one.
REAL=${FIGARO_REAL_CONFIG:-$HOME/.config/figaro}
if [[ -f "$REAL/outfits/default.toml" ]]; then
  cp "$REAL"/outfits/*.toml "$BOX/cfg/outfits/" 2>/dev/null || true
fi
cat > "$BOX/cfg/outfits/hibdemo.toml" <<'EOF'
[system]
provider = "anthropic"
model = "claude-sonnet-4-5"
credo = { fileName = "credo.md" }
EOF

echo "== starting an isolated angelus"
FIGARO_RUNTIME_DIR=$RT FIGARO_STATE_DIR=$BOX/state FIGARO_CONFIG_DIR=$BOX/cfg \
  FIGARO_PPROF=1 _FIGARO_DAEMON=1 setsid "$BIN" --angelus >"$BOX/daemon.out" 2>&1 &
for _ in $(seq 1 40); do [[ -S $RT/angelus.sock ]] && break; sleep 0.25; done
[[ -S $RT/angelus.sock ]] || { echo "daemon did not start; see $BOX/daemon.out"; exit 1; }

# `figaro new --outfit X` with no prompt mints an aria and takes no turn, so
# this costs nothing. (Bare `figaro new` unattends instead, and
# `figaro new -- <prompt>` would spend tokens.)
echo "== minting $ARIAS arias (no model calls)"
: > "$BOX/ids"
for i in $(seq 1 "$ARIAS"); do
  id=$(fig new --outfit hibdemo -j 2>/dev/null | python3 -c 'import sys,json;print(json.load(sys.stdin)["aria_id"])')
  echo "$id" >> "$BOX/ids"
  printf '   %s\n' "$id"
done

if [[ $NO_TOKENS == 1 ]]; then
  echo "== NO_TOKENS=1: skipping the seed turn, transcripts will be BLANK"
  echo "   (an unprompted aria has no history; that is correct and unwatchable)"
else
  echo "== seeding one short turn each, so the transcripts are not blank"
  # -f: submit and return. A streaming send would hold this script for the whole
  # turn, and five of them held it indefinitely once.
  i=0
  while read -r id; do
    i=$((i+1))
    fig send -f --id "$id" -- "Reply with one short sentence naming yourself as demo aria $i of $ARIAS." >/dev/null 2>&1 || true
  done < "$BOX/ids"

  # Poll the STORE for the replies rather than trusting the sends, and give up
  # rather than hang: a provider that cannot answer must not wedge the demo.
  #
  # Two is the target, not three: message_count excludes ceremonial rows, so a
  # seeded aria is exactly prompt + reply. Requiring three never matched, so
  # this waited out its whole timeout and the arias hibernated DURING seeding —
  # which made the "before the sweep" reading show live=0 and look broken.
  echo "   waiting for replies (up to 45s)"
  for _ in $(seq 1 45); do
    done_n=0
    while read -r id; do
      [[ $(msgcount "$id") -ge 2 ]] && done_n=$((done_n+1))
    done < "$BOX/ids"
    [[ $done_n -ge $ARIAS ]] && break
    sleep 1
  done
  while read -r id; do
    printf '   %s  messages=%s\n' "$id" "$(msgcount "$id")"
  done < "$BOX/ids"
  if [[ ${done_n:-0} -lt $ARIAS ]]; then
    echo "   NOTE: not every aria answered. Transcripts will be thin or blank."
    echo "         Check the provider in $BOX/cfg/outfits/hibdemo.toml."
  fi
fi

echo "== attaching a listener to each, in tmux"
tmux kill-session -t $SESSION 2>/dev/null || true
tmux new-session -d -s $SESSION -x 200 -y 51
for _ in $(seq 2 "$ARIAS"); do tmux split-window -t $SESSION -d; done
tmux select-layout -t $SESSION tiled >/dev/null
n=0
while read -r id; do
  # Absolute path, never PATH: `tmux new-session -e PATH=...` is silently
  # ignored, which has made A/B runs execute the same binary twice.
  tmux send-keys -t $SESSION.$n \
    "export FIGARO_RUNTIME_DIR=$RT FIGARO_STATE_DIR=$BOX/state FIGARO_CONFIG_DIR=$BOX/cfg; $BIN listen $id" Enter
  n=$((n+1))
done < "$BOX/ids"
sleep 5

echo
echo "== before the sweep"
mem

echo
echo "== watching the sweep (dormancy is 1 min; sweep every 1s)"
for t in 20 40 55 62 70 80; do
  sleep $(( t - ${prev:-0} )); prev=$t
  printf '   t=%3ds  %s\n' "$t" "$(counters)"
done

echo
echo "== after the sweep"
mem
echo
echo "   Read that as: live=0 means every AGENT was reclaimed."
echo "   attached-clients=$ARIAS means every TERMINAL is still connected."
echo "   Both at once is the point."

echo
echo "== the daemon's own account"
python3 - "$BOX/state/logs.jsonl" <<'PY'
import json, sys
for line in open(sys.argv[1]):
    if not line.strip():
        continue
    d = json.loads(line)
    msg = (d.get("Body") or {}).get("Value", "")
    if any(k in msg for k in ("hibernat", "cap", "released idle", "trimmed resident", "restored")):
        attrs = {a["Key"]: list(a["Value"].values())[-1] for a in d.get("Attributes", [])}
        print(f"   [{d['SeverityText']}] {msg} {attrs}")
PY

if [[ $WAKE == 1 ]]; then
  id=$(head -1 "$BOX/ids")
  echo
  echo "== prompting the reclaimed aria $id (listener is pane 0)"
  fig send -f --id "$id" -- "Output exactly 6 lines. Line N is: ZQ<N> then one short sentence about tides." >/dev/null 2>&1
  prev=-1
  for i in $(seq 1 100); do
    c=$(tmux capture-pane -t $SESSION.0 -p -S - | grep -cE 'ZQ[0-9]+ ' || true)
    if [[ $c != "$prev" ]]; then
      printf '   ~%5dms  lines_rendered=%s  %s\n' "$(( i * 300 ))" "$c" "$(counters)"
      prev=$c
    fi
    [[ $c -ge 6 ]] && break
    sleep 0.3
  done
  echo
  echo "   Rising line counts = frames arriving INCREMENTALLY on the connection"
  echo "   that was already open before the aria was reclaimed."
fi

echo
echo "== what you should SEE in the panes"
if [[ $NO_TOKENS == 1 ]]; then
  echo "   Blank transcripts (NO_TOKENS=1 skipped the seed) with a live footer."
else
  echo "   A real prompt and reply in every pane, with a footer naming the aria."
  echo "   Those arias are all hibernated. The transcript is served from the"
  echo "   store by the endpoint, with no agent behind it."
fi
echo
echo "== look at it yourself"
echo "   tmux attach -t $SESSION        (Ctrl-b d to detach)"
echo "   $BIN doctor mem               (with the env vars above)"
if [[ $KEEP != 1 ]]; then
  echo
  read -rp "   press enter to tear down (or Ctrl-C, then clean up by hand) " _ || true
fi
