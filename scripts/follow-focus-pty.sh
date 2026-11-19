#!/usr/bin/env bash
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1
source scripts/devpane.sh
OUT="${FF_OUT:-/var/tmp/follow-focus-pty}"
mkdir -p "$OUT"
dp_init follow || exit 1
fail=0
check() { if eval "$2"; then echo "ok    $1"; else echo "FAIL  $1"; fail=1; fi; }
keep() { dp_cap > "$OUT/$1.cap"; }

dp_reply default <<< "A reply long enough to scroll. $(printf 'line %s. ' $(seq 1 60))"
ARIA=$(dp_new)
for q in first second third; do
  dp_fig send -f --id "$ARIA" -- "$q question" >/dev/null
  dp_wait_idle "$ARIA" || exit 1
done

dp_pane 100 24 follow || exit 1
dp_run "figaro listen $ARIA"
sleep 3
dp_key C-t; dp_stable

dp_key h; dp_stable; keep focused-help
dp_key X; dp_stable; keep focused-X
check "X in a focused pit is not the transcript's hang-up" "! grep -q 'nothing to interrupt' '$OUT/focused-X.cap'"
dp_key j; dp_stable; keep focused-j
check "j moves the focused help list" "! diff -q '$OUT/focused-X.cap' '$OUT/focused-j.cap' >/dev/null"

dp_key T; dp_stable; keep receded
dp_key X; dp_stable; keep receded-X
check "X over the receded pit reaches hang-up-drop" "grep -q 'nothing to interrupt' '$OUT/receded-X.cap'"

dp_key g; dp_key g; dp_stable; keep receded-top
dp_key M-n; dp_stable; keep receded-Mn
check "M-n over the receded pit moves the transcript" "! diff -q '$OUT/receded-top.cap' '$OUT/receded-Mn.cap' >/dev/null"
dp_key M-p; dp_stable; keep receded-Mp
check "M-p over the receded pit moves it back" "! diff -q '$OUT/receded-Mn.cap' '$OUT/receded-Mp.cap' >/dev/null"

dp_key Escape; dp_stable; keep receded-esc
check "Esc still closes the receded pit" "! diff -q '$OUT/receded-Mp.cap' '$OUT/receded-esc.cap' >/dev/null"

dp_down
echo "captures in $OUT"
exit $fail
