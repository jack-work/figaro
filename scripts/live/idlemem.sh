#!/usr/bin/env bash
# Does an idle daemon give its arena back? PSS is the number that matters,
# because it is what the fifteen idle daemons on this box are each holding.
set -uo pipefail
TREE=${1:-/home/gluck/dev/figaro-qua/incant}
cd "$TREE" || exit 1
ROOT=$(mktemp -d /var/tmp/figidle.XXXX)
BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
cp -a --reflink=auto "${HOME}/.local/state/figaro/arias" "$ROOT/state/arias" || exit 1
cat >> "$ROOT/config/config.toml" <<CFG

[memory]
sweep_interval_seconds = 3
dormant_after_minutes  = 1
CFG
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT
"$BIN" ls -j >/dev/null 2>&1
sleep 2
PID=$(cat "$ROOT/rt/angelus.pid" 2>/dev/null)
pss() { awk '/^Pss:/{print $2}' /proc/$PID/smaps_rollup 2>/dev/null; }
echo "tree=$TREE daemon=$PID"
printf "after a full listing:   PSS %s kB   %s\n" "$(pss)" "$("$BIN" doctor mem 2>&1 | grep -oE 'alloc=[0-9.]+ [KMG]iB' | head -1)"
for i in 1 2 3 4 5 6 7 8; do
  sleep 5
  printf "  +%2ds idle:            PSS %s kB   %s\n" $((i*5)) "$(pss)" "$("$BIN" doctor mem 2>&1 | grep -oE 'alloc=[0-9.]+ [KMG]iB' | head -1)"
done
"$BIN" ls -j >/dev/null 2>&1
printf "after a listing again:  PSS %s kB\n" "$(pss)"
"$BIN" stop >/dev/null 2>&1
echo "ROOT=$ROOT"
