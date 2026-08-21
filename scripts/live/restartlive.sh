# Does a studied form still FOLD after a daemon restart, with no new verb?
# Nothing re-attaches on boot except the study verb itself, so if the answer
# is no, every studied form goes stale the moment figaro restarts.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/incant
ROOT=$(mktemp -d /var/tmp/figrestart.XXXX); BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1

assert_send() { # $1 = label, $2 = the send's whole output
  case "$2" in
    *"completed ✓"*) echo "    send ($1): completed" ;;
    *) echo "FAIL: the send ($1) did not complete: $(echo "$2" | tr -d '
' | tail -c 200)"; exit 1 ;;
  esac
}

mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt" "$ROOT/wire"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT
FORM=$("$BIN" form new --set brief=b0 2>&1 | grep -oE "@[0-9a-f]+" | head -1)
ARIA=$("$BIN" new 2>&1 | grep -oE "[0-9a-f]{8}" | head -1)
"$BIN" set --id "$ARIA" system.environment.figaro_wire_dir "$ROOT/wire" >/dev/null
"$BIN" study "$ARIA" "$FORM" >/dev/null
SEND_OUT=$("$BIN" send --id "$ARIA" -- "say: one" 2>&1); assert_send "say: one" "$SEND_OUT"

echo "--- STOP the daemon (as Gluck will)"
"$BIN" stop >/dev/null 2>&1; sleep 2
echo "--- patch the studied form; the daemon restarts to serve this"
"$BIN" set --id "$FORM" afterrestart yes
"$BIN" doctor mem 2>&1 | grep -iE "librett" || echo "  (no libretto line: nothing is following)"
sleep 3
SEND_OUT=$("$BIN" send --id "$ARIA" -- "say: two" 2>&1); assert_send "say: two" "$SEND_OUT"
# TWO turns, because the first one after a restart is known to be one behind
# and the thing that must never be true is that it is LOST. See the notes:
# attaching the fold at boot does NOT fix the first turn, so the cause is not
# what it looks like.
# THE MECHANISM, PRINTED BESIDE THE SYMPTOM. The stamp on a fig IR entry reads
# the LIBRETTO's version (store.observedCursors -> formTail(LibrettoID(fid))),
# and the libretto is a COPY kept current by an ASYNCHRONOUS fold goroutine
# (store/libretto.go: `go l.fold(...)`). If the libretto has not folded the
# patch when the entry is stamped, the entry carries the OLD version, there is
# no delta to render, and the change waits for the next entry -- which is
# exactly one turn late.
#
# HYPOTHESIS, NOT YET CONFIRMED: a previous bearer recorded that "attaching the
# fold at boot does NOT fix the first turn, so the cause is not what it looks
# like", so do not treat this note as the answer. THE DISCRIMINATING NUMBERS
# are the source form's version and the libretto's, read at the instant before
# the send. If they differ, the fold is behind and the stamp is telling the
# truth about a stale copy. If they AGREE and the turn is still one behind, the
# cause is downstream of the stamp and this comment is wrong.
echo "--- source vs libretto version, immediately before the first send:"
"$BIN" state show --id "$FORM" -j 2>/dev/null | python3 -c 'import sys,json;d=json.load(sys.stdin);print("    source form keys:",sorted(k for k in d if not k.startswith("libretto")))' 2>/dev/null || echo "    (could not read the form)"

echo "--- first turn after the restart:"
REQ=$(ls -t "$ROOT"/wire/*/*.req.http | head -1)
echo "    afterrestart: $(grep -c afterrestart "$REQ")  (0 is the known lag)"
SEND_OUT=$("$BIN" send --id "$ARIA" -- "say: three" 2>&1); assert_send "say: three" "$SEND_OUT"
REQ=$(ls -t "$ROOT"/wire/*/*.req.http | head -1)
n=$(grep -c afterrestart "$REQ")
if [ "$n" -gt 0 ]; then echo "PASS: the change survived the restart (one turn late)"
else echo "FAIL: a studied form went STALE across a restart -- the window was LOST"; fi
grep -oE 'name=\\"study[^}]{0,120}}' "$REQ" | tail -2 | cut -c1-160
echo "ROOT=$ROOT"
