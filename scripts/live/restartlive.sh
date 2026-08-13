# Does a studied form still FOLD after a daemon restart, with no new verb?
# Nothing re-attaches on boot except the study verb itself, so if the answer
# is no, every studied form goes stale the moment figaro restarts.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/incant
ROOT=$(mktemp -d /var/tmp/figrestart.XXXX); BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt" "$ROOT/wire"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT
FORM=$("$BIN" form new --set brief=b0 2>&1 | grep -oE "@[0-9a-f]+" | head -1)
ARIA=$("$BIN" new 2>&1 | grep -oE "[0-9a-f]{8}" | head -1)
"$BIN" set --id "$ARIA" system.environment.figaro_wire_dir "$ROOT/wire" >/dev/null
"$BIN" study "$ARIA" "$FORM" >/dev/null
"$BIN" send --id "$ARIA" -- "say: one" >/dev/null 2>&1

echo "--- STOP the daemon (as Gluck will)"
"$BIN" stop >/dev/null 2>&1; sleep 2
echo "--- patch the studied form; the daemon restarts to serve this"
"$BIN" set --id "$FORM" afterrestart yes
"$BIN" doctor mem 2>&1 | grep -iE "librett" || echo "  (no libretto line: nothing is following)"
sleep 3
"$BIN" send --id "$ARIA" -- "say: two" >/dev/null 2>&1
# TWO turns, because the first one after a restart is known to be one behind
# and the thing that must never be true is that it is LOST. See the notes:
# attaching the fold at boot does NOT fix the first turn, so the cause is not
# what it looks like.
echo "--- first turn after the restart:"
REQ=$(ls -t "$ROOT"/wire/*/*.req.http | head -1)
echo "    afterrestart: $(grep -c afterrestart "$REQ")  (0 is the known lag)"
"$BIN" send --id "$ARIA" -- "say: three" >/dev/null 2>&1
REQ=$(ls -t "$ROOT"/wire/*/*.req.http | head -1)
n=$(grep -c afterrestart "$REQ")
if [ "$n" -gt 0 ]; then echo "PASS: the change survived the restart (one turn late)"
else echo "FAIL: a studied form went STALE across a restart -- the window was LOST"; fi
grep -oE 'name=\\"study[^}]{0,120}}' "$REQ" | tail -2 | cut -c1-160
echo "ROOT=$ROOT"
