#!/usr/bin/env bash
# uilive.sh: the COMPOSED cache on a real daemon against a COPY of the real
# store. Two things a unit test cannot answer:
#
#   1. does a page served from a HIT equal the same page served after its
#      turns were EVICTED and faulted back? A cache that returns different
#      bytes on a refault is worse than no cache, and every fixture composer
#      in the suite returns whole turns BY CONSTRUCTION -- only the real
#      composer, over real forks and real legacy records, can be wrong here.
#   2. does `doctor mem` see it? A meter that reads zero when retention is
#      worst is the worst possible meter (S1).
#
# The budget is deliberately tiny, so eviction is the ordinary case.
set -uo pipefail
TREE=${1:-/home/gluck/dev/figaro-qua/main}
UIMB=${UIMB:-1}
cd "$TREE" || exit 1
ROOT=$(mktemp -d /var/tmp/figui.XXXX)
BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/config" "$ROOT/state" "$ROOT/rt"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null || true
cp -a --reflink=auto "${HOME}/.local/state/figaro/arias" "$ROOT/state/arias" || exit 1
sed -i '/^\[memory\]/,/^\[/{/^\[memory\]/d;/^\[/!d}' "$ROOT/config/config.toml"
cat >> "$ROOT/config/config.toml" <<CFG

[memory]
ui_window_mb           = $UIMB
sweep_interval_seconds = 2
dormant_after_minutes  = 1
CFG
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT

mapfile -t ARIAS < <("$BIN" ls -j 2>/dev/null | grep -oE '"id": *"[0-9a-f]+"' | grep -oE '[0-9a-f]{8}' | head -12)
echo "tree=$TREE ui_window_mb=$UIMB arias=${#ARIAS[@]}"
[ "${#ARIAS[@]}" -gt 0 ] || { echo "NO ARIAS -- the probe measured nothing"; exit 1; }

fail=0
for id in "${ARIAS[@]}"; do
  a=$("$BIN" show "$id" -n 40 -j 2>/dev/null)
  st=$?
  [ $st -eq 0 ] || { echo "  $id  READ FAILED (status $st)"; fail=1; continue; }
  # Push the whole set through again so the earlier arias' turns have been
  # evicted by the later ones, then re-read the first page.
  for other in "${ARIAS[@]}"; do "$BIN" show "$other" -n 40 -j >/dev/null 2>&1; done
  b=$("$BIN" show "$id" -n 40 -j 2>/dev/null)
  if [ "$a" != "$b" ]; then
    echo "  $id  REFAULT DIFFERS from the hit  ($(printf %s "$a" | wc -c) vs $(printf %s "$b" | wc -c) bytes)"
    diff <(printf %s "$a") <(printf %s "$b") | head -5
    fail=1
  else
    echo "  $id  ok  $(printf %s "$a" | wc -c) bytes, hit == refault"
  fi
done

echo
"$BIN" doctor mem 2>&1 | grep -iE 'ui window|composed' || echo "doctor mem reports NO ui window line"
"$BIN" stop >/dev/null 2>&1
echo "ROOT=$ROOT  (rm -rf it)"
exit $fail
