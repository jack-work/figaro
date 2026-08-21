#!/usr/bin/env bash
# What the MODEL actually sees after §12.5: the studied block is derived from
# the LIBRETTO, so a source patch must reach the context, and the libretto's
# own machinery (system.libretto.at / .refs) must not.
#
# The evidence is the wire dump, not the answer: system.environment.figaro_wire_dir
# writes the exact provider request, so this asserts on bytes rather than on
# what a model chose to say about them.
set -uo pipefail
cd /home/gluck/dev/figaro-qua/incant
ROOT=$(mktemp -d /var/tmp/figrender.XXXX)
BIN=$ROOT/figaro
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

FORM=$("$BIN" form new --set brief="the studied thing" 2>&1 | grep -oE "@[0-9a-f]+" | head -1)
ARIA=$("$BIN" new 2>&1 | grep -oE "[0-9a-f]{8}" | head -1)
echo "form=$FORM aria=$ARIA"

"$BIN" set --id "$ARIA" system.environment.figaro_wire_dir "$ROOT/wire" || exit 1
"$BIN" study "$ARIA" "$FORM" || exit 1

# The source moves AFTER the study: this is the transition the libretto folds
# and the only reason the block should say anything at all.
# NOT >/dev/null: a set that fails silently is a live script that cannot
# fail, and this one did exactly that (wrong argument shape) while still
# printing green.
"$BIN" set --id "$FORM" status merged || exit 1
"$BIN" set --id "$FORM" sha 8b12f128 || exit 1
sleep 1   # the fold is asynchronous and durable; the stamp names the COPY

SEND_OUT=$("$BIN" send --id "$ARIA" -- "Reply with the single word: ok" 2>&1); assert_send "Reply with the single word: ok" "$SEND_OUT"
# A second turn: the FIRST record after a study carries the whole copy as its
# baseline, so the transition only shows as a delta on the next one.
"$BIN" set --id "$FORM" phase ga || exit 1
sleep 1
SEND_OUT=$("$BIN" send --id "$ARIA" -- "Reply with the single word: done" 2>&1); assert_send "Reply with the single word: done" "$SEND_OUT"

# THE SOURCE DIES. Gluck's ruling (durable-forms §12.7b): a deleted source is
# reported IN BAND, as a key -- system.libretto.alive goes false on a copy
# that outlives its source, and that transition renders like any other key
# change. The projection never learns about liveness. This is the half of the
# ruling that had never been seen on a wire.
"$BIN" kill "$FORM" || exit 1
sleep 2
SEND_OUT=$("$BIN" send --id "$ARIA" -- "Reply with the single word: four" 2>&1); assert_send "Reply with the single word: four" "$SEND_OUT"

REQ=$(ls -t "$ROOT"/wire/*/*.req.http 2>/dev/null | head -1)
if [ -z "$REQ" ]; then echo "FAIL: no wire dump under $ROOT/wire"; echo "ROOT=$ROOT"; exit 1; fi
echo "--- wire: $REQ"

fail=0
# PATTERNS ARE FIXED STRINGS (-F), deliberately. The body is JSON inside JSON,
# so every quote arrives as \" and a natural-looking regex like '"ga"' or
# 'exists":false' matches NOTHING. That produced two false FAILs in one
# session, each of which cost a full cycle of chasing a product bug that did
# not exist. A check that cannot pass is exactly as expensive as one that
# cannot fail.
check() { # check <what> <expected: yes|no> <fixed-string>
  n=$(grep -cF "$3" "$REQ" 2>/dev/null || true)
  case "$2:$n" in
    yes:0) echo "FAIL  $1 (absent)"; fail=1 ;;
    no:0)  echo "ok    $1 (absent, as it must be)" ;;
    no:*)  echo "FAIL  $1 (present $n times, and must not be)"; fail=1 ;;
    *)     echo "ok    $1 (present $n)" ;;
  esac
}

check "the study block is rendered"        yes 'system-reminder name=\"study'
check "the source's new value reached it"  yes 'merged'
check "the second patch too"               yes "8b12f128"
check "the LATER delta (phase ga)"         yes 'phase\":\"ga'
check "the libretto's at is hidden"        no  'system.libretto.at'
check "the libretto's refcount is hidden"  no  'system.libretto.refs'
check "no stump name leaked into the block" no '@libretto::'
check "the death arrives in band"          yes 'exists\":false'
check "and nothing calls it an error"      no  'no longer exists'

echo "--- the block, verbatim:"
# BOUNDED. The unbounded version matched to the end of a 40 KB request body
# and dumped the whole thing into whatever was reading the log.
grep -oE 'name=\\"study[^}]{0,200}}' "$REQ" | head -4 | cut -c1-220

echo "ROOT=$ROOT  (exit $fail)"
exit $fail
