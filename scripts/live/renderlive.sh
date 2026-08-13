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
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt" "$ROOT/wire"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT

FORM=$("$BIN" form new --set brief="the studied thing" 2>&1 | grep -oE "@[0-9a-f]+" | head -1)
ARIA=$("$BIN" new 2>&1 | grep -oE "[0-9a-f]{8}" | head -1)
echo "form=$FORM aria=$ARIA"

"$BIN" set --id "$ARIA" system.environment.figaro_wire_dir "$ROOT/wire" >/dev/null 2>&1
"$BIN" study "$ARIA" "$FORM" >/dev/null 2>&1

# The source moves AFTER the study: this is the transition the libretto folds
# and the only reason the block should say anything at all.
"$BIN" set "$FORM" status merged >/dev/null 2>&1
"$BIN" set "$FORM" sha 8b12f128 >/dev/null 2>&1
sleep 1   # the fold is asynchronous and durable; the stamp names the COPY

"$BIN" send --id "$ARIA" -e -- "Reply with the single word: ok" >/dev/null 2>&1

REQ=$(ls -t "$ROOT"/wire/*/*request* 2>/dev/null | head -1)
if [ -z "$REQ" ]; then echo "FAIL: no wire dump under $ROOT/wire"; echo "ROOT=$ROOT"; exit 1; fi
echo "--- wire: $REQ"

fail=0
check() { # check <what> <expected: yes|no> <pattern>
  n=$(grep -c "$3" "$REQ" 2>/dev/null || true)
  case "$2:$n" in
    yes:0) echo "FAIL  $1 (absent)"; fail=1 ;;
    no:0)  echo "ok    $1 (absent, as it must be)" ;;
    no:*)  echo "FAIL  $1 (present $n times, and must not be)"; fail=1 ;;
    *)     echo "ok    $1 (present $n)" ;;
  esac
}

check "the study block is rendered"        yes 'system-reminder name=\\"study'
check "the source's new value reached it"  yes 'merged'
check "the second patch too"               yes '8b12f128'
check "the libretto's at is hidden"        no  'system.libretto.at'
check "the libretto's refcount is hidden"  no  'system.libretto.refs'
check "no stump name leaked into the block" no '@libretto::'

echo "--- the block, verbatim:"
grep -oE 'system-reminder name=\\"study[^<]*' "$REQ" | head -4

echo "ROOT=$ROOT  (exit $fail)"
exit $fail
