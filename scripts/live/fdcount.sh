#!/usr/bin/env bash
# How many file descriptors does a listing cost the daemon? I claimed lazy
# segment opening pays here; this is the check.
set -uo pipefail
TREE=${1:-/home/gluck/dev/figaro-qua/incant}
cd "$TREE" || exit 1
ROOT=$(mktemp -d /var/tmp/figfd.XXXX)
BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
cp -a --reflink=auto "${HOME}/.local/state/figaro/arias" "$ROOT/state/arias" || exit 1
cat >> "$ROOT/config/config.toml" <<CFG

[memory]
handle_idle_minutes    = -1
dormant_after_minutes  = 60
CFG
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT
"$BIN" ls -j >/dev/null 2>&1   # auto-starts the daemon
sleep 2
PID=$(cat "$ROOT/rt/angelus.pid" 2>/dev/null)
echo "tree=$TREE daemon=$PID"
echo "fds after boot+first listing: $(ls /proc/$PID/fd 2>/dev/null | wc -l)"
echo "  segment files among them:   $(ls -l /proc/$PID/fd 2>/dev/null | grep -cE '\.seg|\.jsonl')"
"$BIN" ls -j >/dev/null 2>&1
echo "fds after a second listing:   $(ls /proc/$PID/fd 2>/dev/null | wc -l)"
"$BIN" doctor mem 2>&1 | grep -E "figwal|heap " | head -2
"$BIN" stop >/dev/null 2>&1 || "$BIN" rest >/dev/null 2>&1
echo "ROOT=$ROOT"
