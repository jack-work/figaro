#!/usr/bin/env bash
# Does the segment cache actually get swept on a LIVE daemon? Two knobs in
# this project have shipped configured-but-unwired; this is the end-to-end
# proof for the third.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/incant
ROOT=$(mktemp -d /var/tmp/figsweep.XXXX)
BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
cp -a --reflink=auto "${HOME}/.local/state/figaro/arias" "$ROOT/state/arias" || exit 1
cat >> "$ROOT/config/config.toml" <<CFG

[memory]
segment_cache_mb       = 32
dormant_after_minutes  = 1
sweep_interval_seconds = 2
handle_idle_minutes    = -1
CFG
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT
# The daemon is auto-started by the first CLI call; there is no `serve`
# subcommand, and scripts that ran one were greping an empty log.
sleep 6
"$BIN" ls -j >/dev/null 2>&1
echo "--- right after a full listing"
"$BIN" doctor mem 2>&1 | grep figwal
for i in 1 2 3 4 5 6; do
  sleep 5
  echo "--- +$((i*5))s idle"
  "$BIN" doctor mem 2>&1 | grep figwal
done
echo "--- and a read still works afterwards"
"$BIN" ls -j 2>/dev/null | head -c 80; echo
# (the daemon logs to its own journal, not to $ROOT)
grep "released idle segment payloads" "$ROOT/daemon.log" | tail -2
"$BIN" rest >/dev/null 2>&1
echo "ROOT=$ROOT"
