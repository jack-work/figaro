#!/usr/bin/env bash
# paint-jogdiff.sh — the JOG-AND-DIFF oracle as a runnable sweep.
#
# Captures the pager's frame, moves the viewport away and back to the SAME
# offset, captures again, and diffs. Any difference means the first frame was a
# lie. Self-validating: it needs no model of what the content should be, which
# is the point (see PAINT-REPRO.md §5).
#
#   scripts/paint-jogdiff.sh <hunter> <aria-id> [binary]
#
# e.g. scripts/paint-jogdiff.sh bartolo 8566c903
#      scripts/paint-jogdiff.sh bartolo 8566c903 /tmp/paint-bartolo/figaro-fixed
#
# Exit 0 = every comparable gesture was CLEAN. Exit 1 = at least one
# CONTAMINATED. Comparisons whose offset moved are reported SKIP and do not
# count either way — never silently compare two different viewports.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$HERE/paintpane.sh"

HUNTER="${1:?usage: paint-jogdiff.sh <hunter> <aria-id> [binary]}"
ARIA="${2:?usage: paint-jogdiff.sh <hunter> <aria-id> [binary]}"
ALTBIN="${3:-}"

pp_init "$HUNTER" || exit 1
[ -n "$ALTBIN" ] && { PP_BIN="$ALTBIN"; echo "paintpane: OVERRIDE bin=$PP_BIN md5=$(md5sum "$PP_BIN" | cut -c1-12) ver=$("$PP_BIN" --version | head -1)"; }
pp_seed || exit 1

# One arm per binary means one daemon per binary: the CLI/daemon build handshake
# refuses a mismatched pair outright. Always stop before starting.
pp_run stop --force >/dev/null 2>&1; sleep 1

pp_up 100 40 jog || exit 1
pp_pager "$ARIA" || exit 1

OUT="$PP_DIR/jogdiff"; mkdir -p "$OUT"; rm -f "$OUT"/*
fails=0; skips=0; cleans=0

pp_pos() { grep -o '· [0-9]*–[0-9]*/[0-9]*+*' "$1" | tail -1; }

# jogdiff <tag> — the oracle. 6 half-pages up, 6 back down.
jogdiff() {
  local tag="$1" a b n w
  pp_stable 12 3 >/dev/null
  pp_cap > "$OUT/$tag-suspect.txt"
  for _ in $(seq 1 6); do pp_key u; sleep 0.12; done; pp_stable 10 2 >/dev/null
  for _ in $(seq 1 6); do pp_key d; sleep 0.12; done; pp_stable 12 3 >/dev/null
  pp_cap > "$OUT/$tag-truth.txt"
  a="$(pp_pos "$OUT/$tag-suspect.txt")"; b="$(pp_pos "$OUT/$tag-truth.txt")"
  if [ "$a" != "$b" ]; then
    # NOT a pass and NOT a failure. A taller viewport pulls more history, the
    # row total grows, and the jog lands somewhere else — diffing that would be
    # a meaningless number that looks like a result.
    printf '  %-10s SKIP   (offset moved: [%s] -> [%s])\n' "$tag" "$a" "$b"; skips=$((skips + 1)); return
  fi
  if [ "$(pp_chrome "$(cat "$OUT/$tag-suspect.txt")")" -eq 0 ]; then
    printf '  %-10s SKIP   (no pager chrome — not in the pager)\n' "$tag"; skips=$((skips + 1)); return
  fi
  n="$(diff "$OUT/$tag-suspect.txt" "$OUT/$tag-truth.txt" | grep -c '^<')"
  w="$(awk '{ print length($0) }' "$OUT/$tag-suspect.txt" | sort -rn | head -1)"
  if [ "$n" -eq 0 ]; then
    printf '  %-10s CLEAN  (widest row %s/%s)\n' "$tag" "$w" "$PP_W"; cleans=$((cleans + 1))
  else
    printf '  %-10s *** CONTAMINATED: %s divergent row(s) *** (widest row %s/%s)\n' "$tag" "$n" "$w" "$PP_W"
    diff "$OUT/$tag-suspect.txt" "$OUT/$tag-truth.txt" | grep '^<' | head -6 | sed 's/^/      /'
    fails=$((fails + 1))
  fi
}

# Deterministic start: top of the held window, then three half-pages down.
pp_key g; pp_key g; pp_stable 8 2 >/dev/null
for _ in 1 2 3; do pp_key d; sleep 0.3; done

echo "== gestures (expected clean: the bug is resize-only) =="
jogdiff baseline
for _ in 1 2 3; do pp_key C-n; sleep 0.3; done;  jogdiff select
pp_key Enter;                                    jogdiff expand
pp_key C-o;                                      jogdiff verbose
pp_key C-o;                                      jogdiff verboff

echo "== resizes =="
pp_resize 72 "$PP_H"  >/dev/null; jogdiff width-72
pp_resize 120 "$PP_H" >/dev/null; jogdiff width-120
pp_resize 120 56      >/dev/null; jogdiff height-56
pp_resize 64 20       >/dev/null; jogdiff both-64x20

echo
echo "SUMMARY: $cleans clean, $fails contaminated, $skips skipped   (captures in $OUT)"
pp_down
[ "$fails" -eq 0 ]
