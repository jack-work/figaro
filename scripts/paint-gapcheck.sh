#!/usr/bin/env bash
# paint-gapcheck.sh — a COMPARISON-FREE detector for gap contamination.
#
# Why this exists, and why jog-and-diff is not enough on its own.
#
# The jog-and-diff oracle (PAINT-REPRO.md §5) captures a frame, moves the
# viewport away and back, and diffs. It is excellent, and it is how the bug was
# first caught. But it has a blind spot that is exactly the case ALMAVIVA told me
# to attack: the user said gaps are "TYPICALLY fixed upon return" — typically,
# not always. If a gesture leaves contamination that the jog does NOT repair,
# then the suspect frame and the "truth" frame are EQUALLY WRONG, the diff is
# empty, and jog-and-diff reports CLEAN. An oracle that compares the painter
# against itself cannot see a fault the painter makes consistently.
#
# So this detector asserts a STRUCTURAL INVARIANT of a single frame, with nothing
# to compare against:
#
#   A message separator is rendered as EXACTLY TWO ROWS — a blank, then a rule
#   (internal/cli/transcript_index.go, entryLine: `case 0: return ""`,
#   `case 1: return t.transRule()`), and the rule is dimTransRule(w): a full
#   width run of U+2500 and nothing else (internal/cli/transcript.go:2132).
#
#   THEREFORE: every row that is a pure full-width run of ─ must be immediately
#   preceded by a blank row.
#
# Two legitimate exceptions, both handled:
#   - the rule on row 0, whose blank has scrolled off the top of the viewport;
#   - the FOOTER rule, which is not a separator: it carries "aria <id> · N–M/T"
#     on the right (session_status.go:130) so it is never a pure run of ─.
#
# Usage:  paint-gapcheck.sh <capture.txt> [width]
# Exit 0 = clean. Exit 1 = at least one contaminated gap, printed.

f="${1:?usage: paint-gapcheck.sh <capture.txt> [width]}"
w="${2:-0}"

awk -v W="$w" '
  { rows[NR] = $0 }
  END {
    # Infer the width from the widest row when not told, so the detector works
    # on a capture whose geometry the caller has forgotten.
    if (W == 0) { for (i = 1; i <= NR; i++) { n = length(rows[i]); if (n > W) W = n } }
    bad = 0; rules = 0
    for (i = 1; i <= NR; i++) {
      line = rows[i]
      # A separator rule: only ─ characters, and at least most of the width.
      # (A capture may trim trailing blanks, so allow >= W-1.)
      if (line !~ /^─+$/) continue
      if (length(line) < W - 1) continue
      rules++
      if (i == 1) continue                       # blank scrolled off the top
      prev = rows[i-1]
      gsub(/[ \t]+$/, "", prev)
      if (prev == "") continue                   # the gap is properly blank
      bad++
      printf "GAP CONTAMINATION: row %d is a separator rule; row %d should be BLANK but holds:\n  %s\n", i, i-1, prev
    }
    printf "gapcheck: %d separator rule(s) examined, %d contaminated gap(s), width %d\n", rules, bad, W
    # A VERDICT ON NOTHING IS NOT A PASS.
    #
    # First run of this detector against a real pane reported "0 contaminated"
    # on a frame I had just proved contaminated by other means — because the
    # viewport was entirely inside one long tool output and contained no message
    # separator at all. Zero rules examined means the detector did not look at
    # the thing it exists to look at, and reporting that as clean is how a test
    # silently stops exercising its own reason for existing.
    #
    # So: exit 2, distinct from both clean (0) and contaminated (1). Point the
    # viewport at a message boundary and run it again.
    if (rules == 0) {
      printf "gapcheck: VACUOUS — no separator rule in this viewport, so this frame proves NOTHING.\n"
      printf "gapcheck: scroll to a message boundary (separators sit between messages, not inside one).\n"
      exit 2
    }
    exit (bad > 0 ? 1 : 0)
  }
' "$f"
