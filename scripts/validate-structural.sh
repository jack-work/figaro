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
#   * the completion sentinel must not appear in the command you type.
#     send-keys echoes the line before the shell runs it, so `grep -q VDONE`
#     matched at once, the capture read an empty pane, two checks passed on
#     nothing and a third failed work that had not started. Wait on a FILE.
#   * grep the capture for something only OUTPUT can contain. The typed line
#     already holds the binary's path, so finding it there proves nothing;
#     the stamped revision proves it.
set -uo pipefail

# The repo root, not scripts/: `go build ./cmd/figaro` resolves from the root.
ROOT=${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}
[ -d "$ROOT/cmd/figaro" ] || { echo "not a figaro checkout: $ROOT" >&2; exit 2; }

# The migration proof needs a store that has not migrated yet, and an aria in
# it whose board we can read back. Neither has a sane default: unset used to
# mean the step silently proved nothing.
SEED_STORE=${SEED_STORE:-}
SEED_ARIA=${SEED_ARIA:-}
if [ -z "$SEED_STORE" ] || [ -z "$SEED_ARIA" ]; then
  cat >&2 <<'USAGE'
usage: SEED_STORE=<dir> SEED_ARIA=<id> scripts/validate-structural.sh

  SEED_STORE  a copy of a PRE-migration store directory, e.g.
                mkdir -p /tmp/seed && cp -a ~/.local/state/figaro/arias /tmp/seed/arias
                SEED_STORE=/tmp/seed/arias
  SEED_ARIA   an aria id inside it, carrying a mantra, a model and skills
USAGE
  exit 2
fi
[ -f "$SEED_STORE/schema.json" ] || { echo "no schema.json under $SEED_STORE" >&2; exit 2; }
if ! grep -q '"form": *1' "$SEED_STORE/schema.json"; then
  echo "seed store is already migrated (form != 1); the migration step would prove nothing" >&2
  exit 2
fi

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
  # Only processes whose EXECUTABLE lives under $RUN are ours. Matching the
  # command line caught the shell that invoked this script, which carries
  # RUN=... in its argv, and reported a leak on every clean run.
  local leaked=0 p exe
  for d in /proc/[0-9]*; do
    exe=$(readlink "$d/exe" 2>/dev/null) || continue
    case "$exe" in
      "$RUN"*) p=${d#/proc/}; leaked=$((leaked+1)); echo "    leaked: $p $exe";;
    esac
  done
  if [ "$leaked" -eq 0 ]; then ok "no process left behind"; else
    bad "$leaked process(es) still alive under $RUN"
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
cp -a "$SEED_STORE" "$STATE/arias" || { bad "could not seed the store"; exit 1; }
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
# bash, explicitly: a pane inherits the LOGIN shell, which here is fish, where
# `$?` is a syntax error rather than the exit status. The command then never
# runs and the step waits out its timeout on a pane that only holds an error.
tmux -S "$SOCK" new-session -d -s v -x 200 -y 51 "bash --noprofile --norc" 2>/dev/null
h=$(tmux -S "$SOCK" display -p -t v '#{pane_height}' 2>/dev/null)
[ -n "$h" ] && ok "pane is $h rows (asked for 51)" || { bad "tmux would not start"; exit 1; }

# Absolute path: -e PATH= is silently ignored, so PATH would run the INSTALLED
# figaro and the whole run would be about the wrong binary.
# The completion signal is a FILE. A word inside the command line is echoed by
# send-keys before the shell runs anything, so it matches on the first poll and
# the capture below reads a pane that has not painted yet.
DONE="$RUN/pty.done"
rm -f "$DONE"
env_prefix="FIGARO_RUNTIME_DIR=$RT FIGARO_STATE_DIR=$STATE"
tmux -S "$SOCK" send-keys -t v \
  "$env_prefix $BIN -A version; $env_prefix $BIN -A list -n 5; echo \$? > $DONE" Enter
for _ in $(seq 1 120); do
  [ -f "$DONE" ] && break
  sleep 0.5
done
cap="$LOG/pane.txt"
tmux -S "$SOCK" capture-pane -p -S - -t v >"$cap" 2>/dev/null

if [ -f "$DONE" ]; then
  st=$(cat "$DONE")
  [ "$st" = "0" ] && ok "the pane ran to completion (list exited 0)" \
                  || bad "list exited $st in the pty"
else
  bad "the pane never finished within 60s"; tail -5 "$cap"
fi
# The typed line already names $BIN, so finding that path proves nothing about
# what ran. The stamped revision can only come from the binary's own output.
grep -q "${SHA:0:8}" "$cap" && ok "the pane ran the binary under test (${SHA:0:8})" \
  || { bad "the pane may have run a different figaro"; head -5 "$cap"; }
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
