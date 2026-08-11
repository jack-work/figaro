#!/usr/bin/env bash
# image-ab.sh: prove the tool-image fix by A/B against a real model.
#
# This is the only honest test of this change. Unit tests can assert that an
# image block exists in a struct; only a model can tell you whether it SAW the
# picture. So the script builds two binaries, gives them the same oversized
# screenshot, and asks each the same question:
#
#   BEFORE (origin/main):  the image is discarded in the turn loop.  -> BLIND
#   AFTER  (this branch):  it is fitted and carried to the model.    -> the code
#
# The fixture carries a RANDOM five-character code rendered in letters large
# enough to survive a downscale. That is what makes the check falsifiable: a
# model that genuinely sees the picture reports the code, and a model handed
# only the placeholder text has nothing whatsoever to guess.
#
# TRAP THIS SCRIPT EXISTS TO AVOID (tmux-testing, #11): an A/B that silently
# runs the SAME binary twice produces identical output and reads as "the fix
# does nothing". Both arms are built from explicit refs into explicit paths and
# their md5s are printed and COMPARED: the script aborts if they match.
#
# Everything runs against an ISOLATED daemon in a temp dir. FIGARO_CONFIG_DIR
# and FIGARO_HUSH_APP are inherited so real outfits and credentials are
# visible; the script refuses to run if the state/runtime dirs would be the
# user's own, and tears both daemons down on exit.
#
# COSTS REAL TOKENS. Two short turns per model, one tool call each.
#
# Usage:
#   scripts/image-ab.sh                          # default outfit
#   scripts/image-ab.sh -L copilot               # a named outfit
#   scripts/image-ab.sh -L opus5 -m claude-sonnet-5
#   scripts/image-ab.sh -b <ref>                 # compare against another ref
#
set -euo pipefail

OUTFIT=""
MODEL=""
BEFORE_REF="origin/main"
KEEP=0

while getopts "L:m:b:kh" opt; do
  case $opt in
    L) OUTFIT=$OPTARG ;;
    m) MODEL=$OPTARG ;;
    b) BEFORE_REF=$OPTARG ;;
    k) KEEP=1 ;;
    h) sed -n '2,40p' "$0"; exit 0 ;;
    *) exit 2 ;;
  esac
done

REPO=$(git rev-parse --show-toplevel)
cd "$REPO"

TMP=$(mktemp -d "${TMPDIR:-/var/tmp}/figaro-image-ab.XXXXXX")
BEFORE_TREE="$TMP/before-tree"
declare -a DAEMONS=()

cleanup() {
  for env_dir in "${DAEMONS[@]:-}"; do
    [ -z "$env_dir" ] && continue
    FIGARO_RUNTIME_DIR="$env_dir/run" FIGARO_STATE_DIR="$env_dir/state" \
      "$TMP/after" stop --force >/dev/null 2>&1 || true
  done
  git worktree remove --force "$BEFORE_TREE" >/dev/null 2>&1 || true
  git worktree prune >/dev/null 2>&1 || true
  if [ "$KEEP" = 1 ]; then echo "kept: $TMP"; else rm -rf "$TMP"; fi
}
trap cleanup EXIT

# ---------------------------------------------------------------- guard rails

: "${FIGARO_STATE_DIR:=}"
if [ -n "$FIGARO_STATE_DIR" ] && [ "${FIGARO_STATE_DIR#$TMP}" = "$FIGARO_STATE_DIR" ]; then
  : # inherited one is fine; we override it per arm below
fi

# ------------------------------------------------------------------- the arms
#
# Stamp the revision explicitly. A plain `go build` in a WORKTREE records no
# revision at all (Go's VCS autodetection needs .git to be a directory, and a
# worktree's is a file), so `figaro version` would say "unknown" in both arms.

stamp() { echo "-X github.com/jack-work/figaro/internal/cli.commit=$1"; }

AFTER_SHA=$(git rev-parse HEAD)
echo "building after  ($(git rev-parse --abbrev-ref HEAD) @ ${AFTER_SHA:0:7})"
go build -ldflags "$(stamp "$AFTER_SHA")" -o "$TMP/after" ./cmd/figaro

BEFORE_SHA=$(git rev-parse "$BEFORE_REF")
echo "building before ($BEFORE_REF @ ${BEFORE_SHA:0:7})"
git worktree add --detach "$BEFORE_TREE" "$BEFORE_SHA" >/dev/null 2>&1
( cd "$BEFORE_TREE" && go build -ldflags "$(stamp "$BEFORE_SHA")" -o "$TMP/before" ./cmd/figaro )

BEFORE_MD5=$(md5sum "$TMP/before" | cut -d' ' -f1)
AFTER_MD5=$(md5sum "$TMP/after" | cut -d' ' -f1)
echo "  before md5 $BEFORE_MD5"
echo "  after  md5 $AFTER_MD5"
if [ "$BEFORE_MD5" = "$AFTER_MD5" ]; then
  echo "ABORT: both arms are the same binary: the comparison would be meaningless" >&2
  exit 1
fi

# ---------------------------------------------------------------- the fixture

mkdir -p "$TMP/fixtures"
go run ./internal/tool/testdata/mkfixtures "$TMP/fixtures" > "$TMP/fixgen.txt"
cat "$TMP/fixgen.txt"
CODE=$(awk '$1=="huge.png"{sub(/^code=/,"",$NF); print $NF}' "$TMP/fixgen.txt")
FIXTURE="$TMP/fixtures/huge.png"
echo

PROMPT="Use the read tool on $FIXTURE: nothing else. The image contains one line \
of large text reading CODE followed by five characters. Reply with EXACTLY that, in \
the form CODE=XXXXX, and nothing else. If no image reached you, reply CODE=BLIND."

# --------------------------------------------------------------------- an arm

arm() {
  local name=$1 bin=$2
  local env_dir="$TMP/$name-env"
  mkdir -p "$env_dir/run" "$env_dir/state"
  DAEMONS+=("$env_dir")

  local args=(new)
  [ -n "$OUTFIT" ] && args+=(--outfit "$OUTFIT")

  local id reply
  id=$(FIGARO_RUNTIME_DIR="$env_dir/run" FIGARO_STATE_DIR="$env_dir/state" \
        "$bin" "${args[@]}" -j 2>/dev/null | sed -n 's/.*"aria_id":"\([^"]*\)".*/\1/p')
  if [ -z "$id" ]; then echo "  $name: could not create an aria" >&2; return 2; fi

  if [ -n "$MODEL" ]; then
    FIGARO_RUNTIME_DIR="$env_dir/run" FIGARO_STATE_DIR="$env_dir/state" \
      "$bin" set --id "$id" system.model "$MODEL" >/dev/null 2>&1
  fi

  reply=$(FIGARO_RUNTIME_DIR="$env_dir/run" FIGARO_STATE_DIR="$env_dir/state" \
           timeout 300 "$bin" send -r --id "$id" -- "$PROMPT" 2>&1) || true
  echo "$reply" > "$TMP/$name.out"
  printf '%s' "$reply"
}

# ------------------------------------------------------------------- the runs

echo "=== BEFORE: $BEFORE_REF ==="
BEFORE_OUT=$(arm before "$TMP/before" || true)
sed 's/^/  | /' <<<"$BEFORE_OUT" | tail -6
echo
echo "=== AFTER: this branch ==="
AFTER_OUT=$(arm after "$TMP/after" || true)
sed 's/^/  | /' <<<"$AFTER_OUT" | tail -6
echo

# ------------------------------------------------------------------- the ruling

# A near miss is not blindness. A model that answers 9X584 for 9XS84 has plainly
# SEEN the picture and misread one glyph; calling that "did not see it" blames
# the fix for the fixture. The verdict stays strict: four of five is still a
# fail: but it names WHICH failure it is, so nobody debugs the wrong thing.
# (The fixture alphabet has since been stripped of confusable pairs, so this
# should now only fire on a real problem.)
seen_code() { grep -oiE 'CODE=[A-Z0-9]{5}' <<<"$1" | head -1 | cut -d= -f2 | tr 'a-z' 'A-Z'; }
hamming() {
  local a=$1 b=$2 n=0 i
  [ "${#a}" -ne "${#b}" ] && { echo 99; return; }
  for ((i = 0; i < ${#a}; i++)); do [ "${a:i:1}" != "${b:i:1}" ] && n=$((n + 1)); done
  echo $n
}

BEFORE_SAW=$(seen_code "$BEFORE_OUT")
AFTER_SAW=$(seen_code "$AFTER_OUT")

rc=0
echo "──────────────────────────────────────────────────────────────"
printf 'the image says          CODE=%s\n' "$CODE"

if [ "$BEFORE_SAW" = "$CODE" ]; then
  echo "BEFORE                  saw it: THE BUG IS NOT REPRODUCED"
  echo "                        (is $BEFORE_REF really unfixed?)"
  rc=1
else
  printf 'BEFORE                  blind ✓  (answered %s)\n' "${BEFORE_SAW:-nothing}"
fi

if [ "$AFTER_SAW" = "$CODE" ]; then
  echo "AFTER                   saw it ✓  (the fix works)"
elif [ -n "$AFTER_SAW" ] && [ "$(hamming "$CODE" "$AFTER_SAW")" -le 1 ]; then
  printf 'AFTER                   answered %s: ONE GLYPH OFF\n' "$AFTER_SAW"
  echo "                        The image DID reach the model: blind is not a"
  echo "                        near miss. This is an OCR misread, not a"
  echo "                        delivery failure. Re-run before acting on it."
  rc=1
else
  printf 'AFTER                   answered %s: THE FIX FAILED\n' "${AFTER_SAW:-nothing}"
  rc=1
fi
echo "──────────────────────────────────────────────────────────────"

if [ $rc -eq 0 ]; then
  echo "PASS: an oversized tool image is invisible before and legible after."
else
  echo "FAIL: see the two transcripts above."
fi
exit $rc
