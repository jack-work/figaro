#!/usr/bin/env bash
#
# validate.sh — prove feat/structural-patches on a rebased tree.
#
# Everything here is isolated: its own store, its own runtime dir, its own tmux
# SOCKET (not just its own session), and its own daemon. It touches neither the
# live store nor the user's tmux server.
#
# The rules encoded here come from skills/tmux-testing.md, each of which
# produced a confident wrong answer for somebody:
#
#   * -count=1 on every go test. A cached run REPLAYS the previous transcript
#     verbatim -- pane heights, PASS lines, everything -- without starting
#     anything. A green tick for work nobody did.
#   * stamp the binary. A worktree's .git is a FILE, so Go's VCS autodetection
#     never fires and `figaro --version` says "unknown", leaving the CLI/daemon
#     build handshake nothing to compare.
#   * absolute paths inside tmux. `tmux new-session -e PATH=...` is silently
#     ignored, so a pane runs the INSTALLED figaro and an A/B compares a binary
#     with itself.
#   * ask for h+1 rows and read back pane_height. The status bar takes one.
#   * capture scrollback (-S -), not the pane: frames that lived for
#     milliseconds are only there.
#   * kill the DAEMON, not just the session. kill-server leaves it running;
#     seventeen agents once left 230 orphans and 1.2GB of tmpfs.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")" && pwd)
RUN=${RUN:-/tmp/figaro-validate-$$}
SOCK="$RUN/tmux.sock"
STATE="$RUN/state"
RT="$RUN/rt"
BIN="$RUN/figaro"
LOG="$RUN/log"
mkdir -p "$STATE" "$RT" "$LOG"

pass=0; fail=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; pass=$((pass+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*"; fail=$((fail+1)); }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }

fig() { env FIGARO_RUNTIME_DIR="$RT" FIGARO_STATE_DIR="$STATE" "$BIN" -A "$@"; }

cleanup() {
  step "cleanup"
  # The daemon first: kill-server leaves it running, which is trap 10.
  fig stop >/dev/null 2>&1 || true
  sleep 1
  if [ -f "$RT/angelus.pid" ]; then
    p=$(cat "$RT/angelus.pid" 2>/dev/null)
    [ -n "$p" ] && kill -0 "$p" 2>/dev/null && { kill "$p" 2>/dev/null; sleep 1; kill -9 "$p" 2>/dev/null; }
  fi
  tmux -S "$SOCK" kill-server 2>/dev/null || true
  local leaked
  leaked=$(pgrep -af "$RUN" 2>/dev/null | grep -v "$$" | wc -l)
  if [ "$leaked" -eq 0 ]; then ok "no process left behind"; else
    bad "$leaked process(es) still alive under $RUN"; pgrep -af "$RUN" | head -3
  fi
  rm -rf "$RUN"
  printf '\n%d passed, %d failed\n' "$pass" "$fail"
  [ "$fail" -eq 0 ] || exit 1
}
trap cleanup EXIT

step "build (stamped: a worktree records no revision otherwise)"
SHA=$(cd "$ROOT" && git rev-parse HEAD)
if (cd "$ROOT" && nix develop -c go build \
      -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$SHA" \
      -o "$BIN" ./cmd/figaro) 2>"$LOG/build.err"; then
  ok "binary built"
else
  bad "build failed"; tail -5 "$LOG/build.err"; exit 1
fi
"$BIN" version 2>&1 | head -1 | grep -q "${SHA:0:8}" \
  && ok "binary carries its revision (${SHA:0:8})" \
  || bad "binary reports no revision: the handshake would have nothing to compare"

step "suite in the devshell (-count=1: a cached run is a replay)"
if (cd "$ROOT" && nix develop -c go test -count=1 ./... >"$LOG/test.out" 2>&1); then
  ok "all packages pass ($(grep -c '^ok' "$LOG/test.out") packages)"
else
  bad "suite failed"; grep -E "^(FAIL|---)" "$LOG/test.out" | head -8
fi
grep -q "(cached)" "$LOG/test.out" && bad "a cached result appeared despite -count=1" \
  || ok "no cached results"

step "migration: an old store converts itself on open"
cp -a "$SEED_STORE" "$STATE" 2>/dev/null || cp -a "$SEED_STORE/." "$STATE/"
before=$(python3 -c "import json;print(json.load(open('$STATE/arias/schema.json')).get('channels'))" 2>/dev/null)
fig list -n 1 >/dev/null 2>&1
after=$(python3 -c "import json;print(json.load(open('$STATE/arias/schema.json')).get('channels'))" 2>/dev/null)
echo "    before: $before"
echo "    after : $after"
echo "$after" | grep -q "'form': 2" && ok "form reached v2" || bad "form did not reach v2"
echo "$after" | grep -q "'ir': 5"   && ok "IR reached v5"   || bad "IR did not reach v5"

step "the board survived the migration"
board=$(fig state show "$SEED_ARIA" -j 2>/dev/null)
echo "$board" | python3 -c "
import json,sys
d=json.load(sys.stdin)
for name,v in (('mantra',d.get('mantra')),('system.model',d.get('system',{}).get('model'))):
    print(('  PASS' if v else '  FAIL'), name, '=', v)
print(('  PASS' if len(d.get('skills',{}))>0 else '  FAIL'), 'skills =', len(d.get('skills',{})))
" 2>/dev/null | while read -r l; do
  case "$l" in *PASS*) ok "${l#*PASS }";; *) bad "${l#*FAIL }";; esac
done

step "the binary in a real pty, on a private socket"
# h+1: the status bar takes a row and a detached session never gives it back.
tmux -S "$SOCK" new-session -d -s v -x 200 -y 51 2>/dev/null
h=$(tmux -S "$SOCK" display -p -t v '#{pane_height}' 2>/dev/null)
[ -n "$h" ] && ok "pane is $h rows (asked for 51)" || { bad "tmux would not start"; exit 1; }

# Absolute path: -e PATH= is silently ignored, so PATH would run the INSTALLED
# figaro and the whole run would be about the wrong binary.
tmux -S "$SOCK" send-keys -t v \
  "FIGARO_RUNTIME_DIR=$RT FIGARO_STATE_DIR=$STATE $BIN -A list -n 5; echo VDONE" Enter
for _ in $(seq 1 60); do
  tmux -S "$SOCK" capture-pane -p -S - -t v 2>/dev/null | grep -q VDONE && break
  sleep 0.5
done
cap="$LOG/pane.txt"
tmux -S "$SOCK" capture-pane -p -S - -t v >"$cap" 2>/dev/null

grep -q "$BIN" "$cap" && ok "the pane ran the binary under test" \
  || bad "the pane may have run a different figaro"
grep -qE "showing|ARIA" "$cap" && ok "list rendered in a real terminal" \
  || { bad "no list output"; tail -5 "$cap"; }
# An absence inside a pager is not an absence: gate on chrome.
if grep -qE '\? help|! status|[0-9]+–[0-9]+/[0-9]+' "$cap"; then
  bad "output promoted to the pager; counts above are not trustworthy"
else
  ok "no pager chrome: the capture is the whole output"
fi

step "a second open is idempotent"
c1=$(md5sum "$STATE/arias/schema.json" | cut -d' ' -f1)
fig list -n 1 >/dev/null 2>&1
c2=$(md5sum "$STATE/arias/schema.json" | cut -d' ' -f1)
[ "$c1" = "$c2" ] && ok "re-opening changed nothing" || bad "the sidecar moved on a second open"
