#!/usr/bin/env bash
# The self-cast, live: an aria casting ITSELF into a role. Before phase 9
# this hung when issued from inside a turn, and the study mark it wrote could
# land between a tool_use and its result.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/incant
ROOT=$(mktemp -d /var/tmp/figcast.XXXX)
BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT
ARIA=$("$BIN" new 2>&1 | grep -oE "[0-9a-f]{8}" | head -1)
ROLE=$("$BIN" form new --set name="the role" 2>&1 | grep -oE "@[0-9a-f]+" | head -1)
echo "aria=$ARIA role=$ROLE"
echo "--- cast (dormant aria, hub path)"
t0=$(date +%s.%N); "$BIN" cast "$ARIA" "$ROLE" 2>&1 | head -3; t1=$(date +%s.%N)
awk -v a=$t0 -v b=$t1 'BEGIN{printf "  cast wall %.2f s\n", b-a}'
echo "--- the role points back, and the aria studies it"
"$BIN" state get --id "$ROLE" 2>&1 | grep -E "target-aria" | head -2
"$BIN" study "$ARIA" 2>&1 | head -3
"$BIN" doctor mem 2>&1 | grep librettos
"$BIN" stop >/dev/null 2>&1; sleep 1
"$BIN" doctor librettos --dry-run 2>&1 | head -6
echo "ROOT=$ROOT"
