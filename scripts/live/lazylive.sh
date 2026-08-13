#!/usr/bin/env bash
# lazylive.sh: the segment cache, on a real daemon against a COPY of the
# real store. Answers three things a unit test cannot: does a listing on a
# 500-aria store stay inside the budget, does `doctor mem` report it, and
# does a turn still work when payloads are not resident.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/incant
ROOT=$(mktemp -d /var/tmp/figlazy.XXXX)
BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
cp -a --reflink=auto "${HOME}/.local/state/figaro/arias" "$ROOT/state/arias" || exit 1
cat >> "$ROOT/config/config.toml" <<EOF

[memory]
segment_cache_mb = ${SEGCACHE_MB:-32}
EOF
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" \
       FIGARO_CONFIG_DIR="$ROOT/config" FIGARO_PPROF=1
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT
# The daemon is auto-started by the first CLI call; there is no `serve`
# subcommand, and scripts that ran one were greping an empty log.
sleep 6
echo "--- boot"
"$BIN" doctor mem 2>&1 | grep -E "figwal|heap |arias "
echo "--- one full listing"
t0=$(date +%s.%N); "$BIN" ls -j >/dev/null 2>&1; t1=$(date +%s.%N)
awk -v a=$t0 -v b=$t1 'BEGIN{printf "  ls wall %.3f s\n", b-a}' 
"$BIN" doctor mem 2>&1 | grep -E "figwal|heap |arias "
echo "--- second listing (the status-line case)"
t0=$(date +%s.%N); "$BIN" ls -j >/dev/null 2>&1; t1=$(date +%s.%N)
awk -v a=$t0 -v b=$t1 'BEGIN{printf "  ls wall %.3f s\n", b-a}' 
"$BIN" doctor mem 2>&1 | grep -E "figwal|heap "
echo "--- read a real aria's history back (payloads are not resident)"
ID=$("$BIN" ls -j 2>/dev/null | python3 -c 'import sys,json;d=json.load(sys.stdin);r=d if isinstance(d,list) else d.get("conversations",d.get("arias",[]));print(r[0]["id"])' 2>/dev/null)
echo "aria=$ID"
"$BIN" log --id "$ID" 2>/dev/null | tail -3
"$BIN" doctor mem 2>&1 | grep -E "figwal|heap "
echo "--- a new aria, a patch, a read"
NEW=$("$BIN" new 2>&1 | grep -oE "[0-9a-f]{8}" | head -1)
"$BIN" set --id "$NEW" brief "lazy" 2>&1
"$BIN" state get --id "$NEW" 2>&1 | head -3
grep -iE "panic|corrupt" "$ROOT/daemon.log" | tail -3
"$BIN" rest >/dev/null 2>&1
echo "ROOT=$ROOT"
