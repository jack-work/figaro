#!/usr/bin/env bash
# Phase 9's first half, end to end on a live daemon: study mints and retains a
# libretto, fork inherits the reference, drop and kill give them back, and the
# reconciliation sweep agrees with the verbs at every step.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/incant
ROOT=$(mktemp -d /var/tmp/figstudy.XXXX)
BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT

FORM=$("$BIN" form new --set brief="the studied thing" 2>&1 | grep -oE "@[0-9a-f]+" | head -1)
echo "form=$FORM"
ARIA=$("$BIN" new 2>&1 | grep -oE "[0-9a-f]{8}" | head -1)
echo "aria=$ARIA"

echo "--- study"
"$BIN" study "$ARIA" "$FORM" 2>&1 | head -2
echo "--- the libretto stump (should be @libretto::<formid>, and NOT in ls)"
"$BIN" ls -g 2>&1 | grep -c "libretto" || true
echo "--- fork the observer"
"$BIN" fork "$ARIA" 2>&1 | head -2
echo "--- stop the daemon and audit"
"$BIN" stop >/dev/null 2>&1; sleep 1
"$BIN" doctor librettos --dry-run 2>&1
echo "--- drop, then audit again"
"$BIN" drop "$ARIA" "$FORM" 2>&1 | head -2
"$BIN" stop >/dev/null 2>&1; sleep 1
"$BIN" doctor librettos --dry-run 2>&1
echo "ROOT=$ROOT"
