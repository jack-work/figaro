#!/usr/bin/env bash
# Phase 9 against a COPY OF THE REAL STORE: 515 arias, real boards, a
# topology form that has to migrate, and a reconciliation sweep that has to
# read every board. A fresh store proves the mechanism; this proves it meets
# the data that exists.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/incant
ROOT=$(mktemp -d /var/tmp/figreal.XXXX)
BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
cp -a --reflink=auto "${HOME}/.local/state/figaro/arias" "$ROOT/state/arias" || exit 1
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT

t0=$(date +%s.%N)
ROWS=$("$BIN" ls -j 2>/dev/null | python3 -c 'import sys,json;d=json.load(sys.stdin);r=d if isinstance(d,list) else d.get("conversations",d.get("arias",[]));print(len(r))')
t1=$(date +%s.%N)
awk -v a=$t0 -v b=$t1 -v n="$ROWS" 'BEGIN{printf "first listing: %s rows in %.2f s\n", n, b-a}'
ARIA=$("$BIN" ls -j 2>/dev/null | python3 -c 'import sys,json;d=json.load(sys.stdin);r=d if isinstance(d,list) else d.get("conversations",d.get("arias",[]));print(r[0]["id"])')
FORM=$("$BIN" form new --set brief="studied on the real store" 2>&1 | grep -oE "@[0-9a-f]+" | head -1)
echo "aria=$ARIA form=$FORM"
echo "--- study an EXISTING aria (dormant, with real history)"
"$BIN" study "$ARIA" "$FORM" 2>&1 | head -2
"$BIN" doctor mem 2>&1 | grep -E "librettos|figwal"
echo "--- fork it"
"$BIN" fork "$ARIA" 2>&1 | head -2
"$BIN" doctor mem 2>&1 | grep -E "librettos"
echo "--- drop"
"$BIN" drop "$ARIA" "$FORM" 2>&1 | head -2
"$BIN" doctor mem 2>&1 | grep -E "librettos|heap "
echo "--- the sweep over every real board"
"$BIN" stop >/dev/null 2>&1; sleep 1
t0=$(date +%s.%N)
"$BIN" doctor librettos --dry-run 2>&1
t1=$(date +%s.%N)
awk -v a=$t0 -v b=$t1 'BEGIN{printf "sweep wall: %.2f s\n", b-a}'
echo "ROOT=$ROOT"
