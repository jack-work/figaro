#!/usr/bin/env bash
#
# term-ambiwidth.sh: does your terminal agree with figaro about glyph widths?
#
# figaro measures every row with go-runewidth and then trusts that measurement
# when it clips, pads and wraps. If the TERMINAL draws a glyph wider than
# go-runewidth thinks, every row containing that glyph is built too long and
# runs past the right edge: visibly wrapped under tmux, silently clipped where
# wrapping is off.
#
# U+2500 (─) and U+2502 (│) are East Asian AMBIGUOUS: one cell in most
# terminals, two where ambiguous-wide is configured. figaro draws every rule out
# of U+2500 and every thinking gutter out of U+2502, so a disagreement there is
# not cosmetic: it doubles the width of the most common decorations on screen.
#
# Run it in the terminal that misbehaves. Expected: 5 / 5 / 5 / 3 / 6 / 6.
# Anything else is the bug, and it is in the terminal's width table, not in the
# renderer.
#
# Ask the TERMINAL how wide it really drew a glyph, via DSR (ESC[6n).
# figaro measures with go-runewidth; if the terminal disagrees, every row it
# builds is wrong by the difference, and rule lines are made of U+2500.
probe() {
  local label="$1" s="$2"
  printf '\r'                      # column 1
  printf '%s' "$s"                 # draw it
  printf '\033[6n'                 # ask where the cursor is
  IFS=';' read -rs -d R -a pos </dev/tty
  local col=$(( ${pos[1]} - 1 ))   # cells advanced
  printf '\r\033[2K%-22s drew %s cells\n' "$label" "$col"
}
printf 'LANG=%s LC_CTYPE=%s TERM=%s\n' "${LANG:-unset}" "${LC_CTYPE:-unset}" "$TERM"
probe "ASCII x5"        "xxxxx"
probe "U+2500 ─ x5"     "─────"
probe "U+2502 │ x5"     "│││││"
probe "U+2026 … x5"     "………"
probe "CJK 日本語"       "日本語"
probe "emoji 🎉x3"      "🎉🎉🎉"
