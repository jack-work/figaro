#!/usr/bin/env bash
# chalkbench.sh — end-to-end chalkboard timing against an ISOLATED daemon.
#
# `figaro set` / `unset` / `loadout` / `state` are pure state operations:
# no LLM round-trip, no tokens, no money. So we can time the real CLI
# against a real daemon — as long as that daemon is not the user's.
#
# Everything runs in a fresh temp dir:
#   FIGARO_RUNTIME_DIR=$TMP/run   FIGARO_STATE_DIR=$TMP/state
# while FIGARO_CONFIG_DIR / FIGARO_HUSH_APP are inherited so the real
# loadout + skills are visible (we want a realistic board). The script
# refuses to run if those point at the user's live store, tears the
# daemon down on exit, and removes the temp dir.
#
# Usage:  scripts/chalkbench.sh [-L <loadout>] [-k <ops>] [-i <inflate keys>]
# Output: a table of milliseconds on stdout.

set -euo pipefail

LOADOUT=""
OPS=50
INFLATE=1000
INFLATE_VALUE_BYTES=2048

while getopts "L:k:i:h" opt; do
  case "$opt" in
    L) LOADOUT="$OPTARG" ;;
    k) OPS="$OPTARG" ;;
    i) INFLATE="$OPTARG" ;;
    h) sed -n '2,20p' "$0"; exit 0 ;;
    *) exit 64 ;;
  esac
done

command -v jq >/dev/null || { echo "chalkbench: jq required" >&2; exit 1; }

die() { printf 'chalkbench: %s\n' "$*" >&2; exit 1; }

REPO="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
cd "$REPO"

# --- guards -----------------------------------------------------------
REAL_STATE="${HOME}/.local/state/figaro"
REAL_RUNTIME="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/figaro"

TMP="$(mktemp -d "${TMPDIR:-/tmp}/chalkbench.XXXXXXXX")"
export FIGARO_RUNTIME_DIR="$TMP/run"
export FIGARO_STATE_DIR="$TMP/state"
# Make the repo's first-party skills visible the way an installed
# binary would (share/figaro/skills next to the exe).
export FIGARO_BUNDLED_SKILLS="${FIGARO_BUNDLED_SKILLS:-$REPO}"
mkdir -p "$FIGARO_RUNTIME_DIR" "$FIGARO_STATE_DIR"

[ -n "${FIGARO_STATE_DIR:-}" ] || die "FIGARO_STATE_DIR unset"
[ -n "${FIGARO_RUNTIME_DIR:-}" ] || die "FIGARO_RUNTIME_DIR unset"
case "$(readlink -f "$FIGARO_STATE_DIR")" in
  "$(readlink -f "$REAL_STATE")"|"$HOME/.local/state"*) die "refusing to run against the live store: $FIGARO_STATE_DIR" ;;
esac
case "$(readlink -f "$FIGARO_RUNTIME_DIR")" in
  "$(readlink -f "$REAL_RUNTIME" 2>/dev/null || echo /nonexistent)") die "refusing to run against the live runtime dir" ;;
esac

FIG="$TMP/figaro"
fig() { "$FIG" "$@"; }

cleanup() {
  local rc=$?
  if [ -x "$FIG" ]; then "$FIG" rest >/dev/null 2>&1 || true; fi
  rm -rf "$TMP"
  exit $rc
}
trap cleanup EXIT

# --- build ------------------------------------------------------------
echo "chalkbench: building $REPO/cmd/figaro -> $FIG" >&2
go build -o "$FIG" ./cmd/figaro || die "build failed"

if [ -z "$LOADOUT" ]; then
  cfg="${FIGARO_CONFIG_DIR:-$HOME/.config/figaro}/config.toml"
  LOADOUT="$(sed -n 's/^default_loadout[[:space:]]*=[[:space:]]*"\(.*\)"/\1/p' "$cfg" | head -1)"
  [ -n "$LOADOUT" ] || die "no default_loadout in $cfg; pass -L <name>"
fi

# --- timing helpers ---------------------------------------------------
now_ms() { local t=${EPOCHREALTIME/,/.}; echo "$(( ${t%.*} * 1000 + 10#${t#*.} / 1000 ))"; }

declare -a ROWS=()
row() { ROWS+=("$1|$2|$3"); }

# time_block <label> <n> <command...> — runs the command n times with
# $IDX set to the iteration index. All output is swallowed.
time_block() {
  local label="$1" n="$2"; shift 2
  local start end
  start=$(now_ms)
  for ((IDX = 0; IDX < n; IDX++)); do "$@" >/dev/null 2>&1; done
  end=$(now_ms)
  row "$label" "$n" "$((end - start))"
}

IDX=0
TAG=small
do_set()     { fig set --id "$ARIA" "bench.k$IDX" "v$IDX-$TAG"; }
do_state()   { fig state --id "$ARIA" -j; }
do_unset()   { fig unset --id "$ARIA" "bench.k$IDX"; }
do_loadout() { fig loadout --id "$ARIA" "$LOADOUT"; }
do_list()    { fig list -j; }

# top-level key count of the aria's board
board_keys()  { fig state --id "$ARIA" -j | jq -r 'keys|length'; }
board_bytes() { fig state --id "$ARIA" -j | wc -c; }

# --- run --------------------------------------------------------------
start=$(now_ms)
ARIA="$(fig new --loadout "$LOADOUT" -j | sed -n 's/.*"aria_id":"\([^"]*\)".*/\1/p')"
end=$(now_ms)
[ -n "$ARIA" ] || die "could not create aria on loadout $LOADOUT"
row "new --loadout $LOADOUT (daemon cold start incl.)" 1 "$((end - start))"

KEYS0=$(board_keys)
BYTES0=$(board_bytes)

time_block "list -j (RPC + process overhead baseline)" "$OPS" do_list
TAG=small
time_block "set (small board)" "$OPS" do_set
time_block "state -j (small board)" "$OPS" do_state
time_block "loadout $LOADOUT re-apply (small board)" 1 do_loadout
time_block "unset (small board)" "$OPS" do_unset

# --- inflate ----------------------------------------------------------
BIG="$(head -c "$INFLATE_VALUE_BYTES" /dev/zero | tr '\0' 'x')"
start=$(now_ms)
for ((i = 0; i < INFLATE; i++)); do
  fig set --id "$ARIA" "bulk.k$i" "$BIG" >/dev/null 2>&1
done
end=$(now_ms)
row "inflate: $INFLATE sets of ${INFLATE_VALUE_BYTES}B" "$INFLATE" "$((end - start))"

KEYS1=$(board_keys)
BYTES1=$(board_bytes)

TAG=large
time_block "set (large board)" "$OPS" do_set
time_block "state -j (large board)" "$OPS" do_state
time_block "loadout $LOADOUT re-apply (large board)" 1 do_loadout
time_block "unset (large board)" "$OPS" do_unset

# --- cold replay ------------------------------------------------------
# Put the daemon to rest and re-read the aria: this pays the full
# chalkboard channel replay on open.
fig rest >/dev/null 2>&1 || true
sleep 0.3
time_block "state -j after daemon restart (cold replay)" 1 do_state

# --- report -----------------------------------------------------------
printf '\n'
printf 'chalkbench — isolated daemon, no LLM round-trips\n'
printf '  repo:     %s (%s)\n' "$REPO" "$(git rev-parse --short HEAD)"
printf '  loadout:  %s\n' "$LOADOUT"
printf '  board:    %s keys / %s bytes  ->  %s keys / %s bytes after inflate\n' \
  "$KEYS0" "$BYTES0" "$KEYS1" "$BYTES1"
printf '  store:    %s (removed on exit)\n\n' "$FIGARO_STATE_DIR"
printf '%-52s %6s %10s %10s\n' "operation" "n" "total ms" "ms/op"
printf '%-52s %6s %10s %10s\n' "----------------------------------------------------" "------" "----------" "----------"
for r in "${ROWS[@]}"; do
  IFS='|' read -r label n ms <<<"$r"
  printf '%-52s %6s %10s %10s\n' "$label" "$n" "$ms" "$(awk -v m="$ms" -v n="$n" 'BEGIN{printf "%.2f", m/n}')"
done
printf '\n'
