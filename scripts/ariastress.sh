#!/usr/bin/env bash
# ariastress.sh: many arias, one daemon, one build — with the footprint
# numbers a multi-process run can actually be believed about.
#
# WHY THIS EXISTS
# ---------------
# The twelve-aria recipe in skills/tmux-testing.md proves CORRECTNESS under
# concurrency (does everyone get an answer, is anyone's state anyone else's).
# It reports `VmRSS` for the daemon, which is right for one process and wrong
# the moment you compare two builds across a fleet: RSS double-counts every
# page of shared binary text, once per process. An earlier aria measuring this
# box saw 26.57 GB RSS against 15.78 GB PSS — 10.8 GB of pure double-counting.
# Measure PSS, and read Pss_Anon specifically: anon is the only number that
# means "we allocated this".
#
# The second trap it defends against: idle daemons with 300 MB resident and
# 800 MB swapped out. RSS looked fine; committed was four times that. So every
# row here carries Swap beside PSS.
#
# The third: figaro daemons leak. One census found 41 orphans across 9
# versions, oldest 8 days. A load run that strands a daemon per iteration
# inflates every measurement after it, so this script censuses before and
# after and refuses to start dirty.
#
# USAGE
#   scripts/ariastress.sh --label after --arias 12 --outfit sonn5-copilot
#   scripts/ariastress.sh --label before --bin /path/to/other/figaro
#
# Everything is isolated: FIGARO_STATE_DIR, FIGARO_RUNTIME_DIR and
# FIGARO_CONFIG_DIR all live under a private root, so the run cannot find (or
# be found by) the user's daemon. Nothing here touches ~/.local/state/figaro.
#
# COST: each aria spends one real turn against a real provider. Twelve arias
# with a one-word answer is small; do not point this at a big prompt.

set -uo pipefail

LABEL=stress
ARIAS=12
BIN=""
MODEL="claude-sonnet-5"
PROVIDER="copilot"
PROMPT='reply with the single word ok'
ROOT=""
KEEP=0
STUDY=0
STUDY_PATCHES=500

usage() { sed -n '2,40p' "$0"; exit "${1:-0}"; }

# `bc` is not on this box, and a missing arithmetic tool must not silently
# blank out the numbers the whole run exists to produce.
secs() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.2f", b-a}'; }

while [ $# -gt 0 ]; do
  case "$1" in
    --label)  LABEL=$2; shift 2 ;;
    --arias)  ARIAS=$2; shift 2 ;;
    --bin)    BIN=$2; shift 2 ;;
    --model)  MODEL=$2; shift 2 ;;
    --provider) PROVIDER=$2; shift 2 ;;
    --prompt) PROMPT=$2; shift 2 ;;
    --root)   ROOT=$2; shift 2 ;;
    --keep)   KEEP=1; shift ;;
    --study)  STUDY=1; shift ;;
    --study-patches) STUDY_PATCHES=$2; shift 2 ;;
    -h|--help) usage 0 ;;
    *) echo "ariastress: unknown flag $1" >&2; usage 64 ;;
  esac
done

command -v jq >/dev/null || { echo "ariastress: jq required" >&2; exit 1; }

REPO="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
REV="$(git -C "$REPO" rev-parse --short HEAD)"
[ -n "$ROOT" ] || ROOT="/var/tmp/ariastress-$LABEL-$$"

# --- guard rails -------------------------------------------------------------

case "$(readlink -f "$ROOT")" in
  "$HOME"/.local/state*|"$HOME"/.config*|/home/*/.local/state*)
    echo "ariastress: refusing to run under a real state dir: $ROOT" >&2; exit 1 ;;
esac

# pgrep -f matches this script's own command line (it contains the pattern),
# which once produced a teardown loop that reported "2 processes remaining"
# forever. Match the executable name, and exclude our own pid.
census() {
  ps -eo pid,ppid,etime,rss,args --no-headers 2>/dev/null \
    | grep -E '/(bin/)?figaro( |$)' | grep -v " $$ " || true
}

BEFORE_DAEMONS=$(census | wc -l)
echo "== census before: $BEFORE_DAEMONS figaro processes on this box"

# --- the build ---------------------------------------------------------------

mkdir -p "$ROOT"/{state,rt,config,out}
chmod 700 "$ROOT"

if [ -z "$BIN" ]; then
  BIN="$ROOT/figaro"
  # Stamp the revision. An unstamped CLI reports `unknown` and will happily
  # talk to a wrong-version daemon, which has cost at least one aria twenty
  # minutes of debugging "a bug that was two binaries".
  ( cd "$REPO" && go build -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse HEAD)" -o "$BIN" ./cmd/figaro ) \
    || { echo "ariastress: build failed" >&2; exit 1; }
fi
echo "== binary: $BIN ($REV)"

# Real credentials and real outfits are READ from the user's config; the dev
# root is where everything WRITTEN goes. Config is copied rather than shared so
# a run cannot rewrite an outfit.
if [ -d "${FIGARO_CONFIG_DIR:-$HOME/.config/figaro}" ]; then
  cp -a "${FIGARO_CONFIG_DIR:-$HOME/.config/figaro}/." "$ROOT/config/" 2>/dev/null || true
fi

export FIGARO_STATE_DIR="$ROOT/state"
export FIGARO_RUNTIME_DIR="$ROOT/rt"
export FIGARO_CONFIG_DIR="$ROOT/config"
export FIGARO_PPROF=1

cleanup() {
  if [ -f "$FIGARO_RUNTIME_DIR/angelus.pid" ]; then
    pid=$(cat "$FIGARO_RUNTIME_DIR/angelus.pid" 2>/dev/null || true)
    "$BIN" rest >/dev/null 2>&1
    sleep 1
    # Only reap what has no peers: killing a daemon with a live client once
    # disconnected somebody's session.
    if [ -n "${pid:-}" ] && kill -0 "$pid" 2>/dev/null; then
      peers=$(ss -xp 2>/dev/null | grep -c "pid=$pid," || echo 0)
      [ "$peers" = "0" ] && kill "$pid" 2>/dev/null
    fi
  fi
  AFTER_DAEMONS=$(census | wc -l)
  echo "== census after: $AFTER_DAEMONS figaro processes (was $BEFORE_DAEMONS)"
  [ "$KEEP" = "1" ] || rm -rf "$ROOT"
}
trap cleanup EXIT

# --- footprint ---------------------------------------------------------------
#
# smaps_rollup is the honest one: Pss splits shared pages by sharer count, and
# Swap catches the daemon that looks small only because it has been paged out.
footprint() {
  local pid=$1 tag=$2
  [ -r "/proc/$pid/smaps_rollup" ] || { echo "$tag: no smaps_rollup for $pid"; return; }
  awk -v tag="$tag" '
    /^Rss:/       {rss=$2}
    /^Pss:/       {pss=$2}
    /^Pss_Anon:/  {anon=$2}
    /^Pss_File:/  {file=$2}
    /^Swap:/      {swap=$2}
    END {printf "%-10s rss=%.1fM pss=%.1fM pss_anon=%.1fM pss_file=%.1fM swap=%.1fM\n",
                tag, rss/1024, pss/1024, anon/1024, file/1024, swap/1024}
  ' "/proc/$pid/smaps_rollup"
}

# --- the run -----------------------------------------------------------------

started=$(date +%s.%N)
"$BIN" ls >/dev/null 2>&1   # start the daemon
sleep 1
DPID=$(cat "$FIGARO_RUNTIME_DIR/angelus.pid" 2>/dev/null || true)
[ -n "$DPID" ] || { echo "ariastress: daemon did not start" >&2; exit 1; }
echo "== daemon pid $DPID"
footprint "$DPID" "idle"

# The do-nothing control. Without it you credit the optimization with process
# spawn + connect + RPC, which at CLI granularity IS the measurement: an
# earlier run clocked `list -j` at 12.16 ms against `set` at 12.06 ms.
ctl_start=$(date +%s.%N)
for i in $(seq 1 "$ARIAS"); do "$BIN" ls -j >/dev/null 2>&1; done
ctl_end=$(date +%s.%N)
echo "== control ($ARIAS x 'ls -j', no turn): $(secs "$ctl_start" "$ctl_end")s"

# --- the studied-form arm ---------------------------------------------------
#
# The ephemeral arm above exercises the provider and the inbox but touches no
# board: `-er` arias have no store, so the form/patch accessor is never asked
# anything. The path this script exists to load is the OTHER one: N backed
# arias all observing one form with a long history, where every provider Send
# asks that form what changed. One key, many patches, so the CONTEXT stays
# tiny while the HISTORY gets long: it is the history the accessor walks, not
# the content, and conflating the two spends tokens to measure nothing.
STUDIED=""
if [ "$STUDY" = "1" ]; then
  STUDIED=$("$BIN" form new -S "brief=v0" -j 2>/dev/null | jq -r '.form_id // .aria_id // .id // empty')
  [ -n "$STUDIED" ] || { echo "ariastress: could not mint the studied form" >&2; exit 1; }
  echo "== studied form $STUDIED: applying $STUDY_PATCHES patches"
  hist_start=$(date +%s.%N)
  for k in $(seq 1 "$STUDY_PATCHES"); do
    "$BIN" state set --id "$STUDIED" "brief=v$k" >/dev/null 2>&1
  done
  echo "== history built in $(secs "$hist_start" "$(date +%s.%N)")s"
fi

turn_start=$(date +%s.%N)
for i in $(seq 1 "$ARIAS"); do
  (
    if [ "$STUDY" = "1" ]; then
      id=$("$BIN" new -S "system.provider=$PROVIDER,system.model=$MODEL,mantra=stress-$i" -j 2>/dev/null | jq -r '.aria_id // .figaro_id // .id // empty')
      if [ -n "$id" ]; then
        "$BIN" study "$id" "$STUDIED" > "$ROOT/out/$i.study" 2>&1
        # -r attaches and blocks until the turn ends: the answer is the proof
        # the whole path ran, and a `send -f` would report success before the
        # provider had been asked anything.
        "$BIN" send -r --id "$id" -- "$PROMPT" > "$ROOT/out/$i.out" 2>&1
        echo $? > "$ROOT/out/$i.rc"
      else
        echo 1 > "$ROOT/out/$i.rc"
      fi
    else
      "$BIN" send -er -S "system.provider=$PROVIDER,system.model=$MODEL,mantra=stress-$i" \
        -- "$PROMPT" > "$ROOT/out/$i.out" 2>&1
      echo $? > "$ROOT/out/$i.rc"
    fi
  ) &
done
wait
turn_end=$(date +%s.%N)

answered=$(grep -li ok "$ROOT"/out/*.out 2>/dev/null | wc -l)
failed=$(grep -L '^0$' "$ROOT"/out/*.rc 2>/dev/null | wc -l)

echo "== turns: $answered/$ARIAS answered, $failed nonzero exits, $(secs "$turn_start" "$turn_end")s wall"
footprint "$DPID" "loaded"

# MemStatus is the in-process view: the heap and the caches, which is where an
# O(history) regression lives. smaps cannot see the difference between a heap
# that is busy and a heap that is wasteful.
mem=$("$BIN" doctor mem -j 2>/dev/null || true)
if [ -n "$mem" ]; then
  echo "$mem" | jq -c 'with_entries(select(.key|test("heap|goroutine|arias|resident|ir_")))' 2>/dev/null || echo "$mem"
fi

if [ -S "$FIGARO_RUNTIME_DIR/pprof.sock" ]; then
  echo "== pprof socket armed at $FIGARO_RUNTIME_DIR/pprof.sock"
  echo "   go tool pprof -http=: 'http+unix://$FIGARO_RUNTIME_DIR/pprof.sock/debug/pprof/heap'"
  [ "$KEEP" = "1" ] && curl --unix-socket "$FIGARO_RUNTIME_DIR/pprof.sock" \
      -so "$ROOT/heap-$LABEL.pb.gz" 'http://x/debug/pprof/heap' \
      && echo "   heap profile: $ROOT/heap-$LABEL.pb.gz"
fi

echo "== total wall: $(secs "$started" "$(date +%s.%N)")s  label=$LABEL rev=$REV"
