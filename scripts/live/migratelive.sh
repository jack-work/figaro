#!/usr/bin/env bash
# The migration, observed WITHOUT stopping the daemon: a store carrying
# studies from before phase 9 boots, the sweep repairs in the background, and
# doctor mem says what it did.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/incant
ROOT=$(mktemp -d /var/tmp/figmig.XXXX)
BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
cp -a --reflink=auto "${HOME}/.local/state/figaro/arias" "$ROOT/state/arias" || exit 1
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT
"$BIN" ls -j >/dev/null 2>&1     # boots the daemon; the sweep starts behind it
for i in 1 2 3 4; do
  sleep 4
  echo "--- +$((i*4))s"
  "$BIN" doctor mem 2>&1 | grep -E "librettos|boot sweep"
done
"$BIN" stop >/dev/null 2>&1
echo "ROOT=$ROOT"
