#!/usr/bin/env bash
# paint-jogdiff.sh — the JOG-AND-DIFF oracle as a runnable sweep.
#
# Captures the pager's frame, moves the viewport away and back to the SAME
# offset, captures again, and diffs. Any difference means the first frame was a
# lie. Self-validating: it needs no model of what the content should be, which
# is the point (see PAINT-REPRO.md §5).
#
#   scripts/paint-jogdiff.sh <hunter> [aria-id|-] [binary]
#
# e.g. scripts/paint-jogdiff.sh bartolo abc12345      # use an aria already in the store
#      PP_ALLOW_TURN=1 scripts/paint-jogdiff.sh bartolo -   # MINT one — SPENDS A TURN
#      scripts/paint-jogdiff.sh bartolo abc12345 /var/tmp/paint-bartolo/figaro-fixed
#
# THIS SCRIPT SPENDS A PROVIDER TURN IF YOU ASK IT TO MINT A FIXTURE, and only
# then. Minting is gated behind PP_ALLOW_TURN=1 because FIGARO_CONFIG_DIR is the
# REAL config by reference — see the guard below.
#
# CONTENT. This used to call pp_seed, which copied the master's real aria store.
# That is disarmed for privacy, so with no aria id this now MINTS A SYNTHETIC
# FIXTURE via pp_fixture (one cheap turn; PP_FIXTURE_ROWS to size it). BASILIO
# caught the stale call: the script exited 1 before doing anything, and every
# instruction built on top of it was a document stating an intent the code did not
# implement — the same family we had just spent the night auditing.
#
# KEEP FIXTURES TABLE-FREE (SUSANNA): a table's row count at a given width
# changes after feat/table-wrap, so a resize across a table measures the merge
# rather than the bug. pp_fixture is table-free by construction — bare integers —
# which has a second virtue: every legitimate body row is an increasing integer,
# so a gap row containing ANYTHING is self-evidently a bug and no oracle is needed.
#
# Exit 0 = every comparable gesture was CLEAN. Exit 1 = at least one
# CONTAMINATED. Comparisons whose offset moved are reported SKIP and do not
# count either way — never silently compare two different viewports.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$HERE/paintpane.sh"

HUNTER="${1:?usage: paint-jogdiff.sh <hunter> [aria-id|-] [binary]}"
ARIA="${2:--}"
ALTBIN="${3:-}"

pp_init "$HUNTER" || exit 1
[ -n "$ALTBIN" ] && { PP_BIN="$ALTBIN"; echo "paintpane: OVERRIDE bin=$PP_BIN md5=$(md5sum "$PP_BIN" | cut -c1-12) ver=$("$PP_BIN" --version | head -1)"; }

# One arm per binary means one daemon per binary: the CLI/daemon build handshake
# refuses a mismatched pair outright. Always stop before starting.
pp_run stop --force >/dev/null 2>&1; sleep 1

if [ "$ARIA" = "-" ]; then
  # THIS IS THE ONLY PATH THAT SPENDS MONEY, AND IT IS NOW GATED.
  #
  # CHERUBINO caught the footgun, which was mine: pp_env resolves
  # FIGARO_CONFIG_DIR to ${PP_CONFIG:-$HOME/.config/figaro} — the REAL config, by
  # reference — and pp_fixture calls `pp_run new -j`. So on any machine with
  # PP_CONFIG unset, `paint-jogdiff.sh <hunter> -` would resolve the master's REAL
  # credentials and spend a REAL provider turn, SILENTLY, as a side effect of a
  # script whose name says "jogdiff". A stand-down would then be one unset
  # variable away from being violated by whoever ran the obvious command.
  #
  # He proposed a line in the docs. A GUARD BEATS A DOC LINE: a document stating
  # an intent the code does not enforce is the exact family we spent the night
  # auditing. So the mint is opt-in, and the refusal says how to proceed.
  if [ -z "$PP_ALLOW_TURN" ]; then
    cat >&2 <<EOF
paint-jogdiff: REFUSING to mint a fixture, because that SPENDS A REAL PROVIDER TURN.

  pp_fixture calls 'figaro new', and FIGARO_CONFIG_DIR resolves to the REAL config
  BY REFERENCE, so this would use the master's real credentials and cost real
  tokens as a side effect of a script whose name says "jogdiff".

  Either point it at content that already exists:
      scripts/paint-jogdiff.sh $HUNTER <aria-id>
  or say so explicitly:
      PP_ALLOW_TURN=1 scripts/paint-jogdiff.sh $HUNTER -
EOF
    exit 2
  fi
  ARIA="$(pp_fixture "${PP_FIXTURE_ROWS:-400}")" || exit 1
  [ -n "$ARIA" ] || { echo "paint-jogdiff: pp_fixture produced no aria id" >&2; exit 1; }
fi
echo "paint-jogdiff: driving aria $ARIA"

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
#
# NOTE ON GEOMETRY, since a stale comment here is how BASILIO's repro rotted: the
# ABSOLUTE offset this lands on depends on the fixture size (it was 219-240 in a
# 1058-row real aria; it will be elsewhere in a 400-row fixture). That does not
# matter, and deliberately so — the oracle never asserts a position. It asserts
# only that the two captures share the SAME footer range, and SKIPs when they do
# not. Do not add an assertion about where this lands.
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
