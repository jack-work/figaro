#!/usr/bin/env bash
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1
source scripts/devpane.sh
dp_init keys || exit 1
fail=0
check() { if eval "$2"; then echo "ok    $1"; else echo "FAIL  $1"; fail=1; fi; }

dp_reply default <<< "A reply long enough to sit under the question. $(printf 'line %s. ' $(seq 1 40))"
ARIA=$(dp_new)
for q in first second third fourth fifth; do
  dp_fig send -f --id "$ARIA" -- "$q question" >/dev/null
  dp_wait_idle "$ARIA" || exit 1
done

dp_fig keys init > /dev/null 2>&1
check "keys init writes the shipped recipes" "grep -q 'prefix:panels' '$DP_DIR/config/keys.toml'"
check "doctor keys accepts what init wrote" "dp_fig doctor keys >/dev/null 2>&1"
check "a second init refuses" "! dp_fig keys init >/dev/null 2>&1"

cat > "$DP_DIR/config/keys.toml" <<'EOF'
[bind.motions]
J = "half-down"

[bind.leader]
w = "status"

[layer.z]
modes = ["transcript"]
prefix = true

[layer.z.bind]
q = "queue"

[bind.transcript]
z = "prefix:z"
EOF

echo "--- figaro keys (dev-shell binary, isolated config)"
dp_fig keys > "$DP_DIR/keys.out" 2>&1
sed -n '1,12p' "$DP_DIR/keys.out"
check "keys lists the user's motion"   "grep -q '^  J  *half-down' '$DP_DIR/keys.out'"
check "keys lists the user prefix"     "grep -q '^z  (transcript)' '$DP_DIR/keys.out'"
check "doctor keys accepts the file"   "dp_fig doctor keys >/dev/null 2>&1"

dp_pane 100 20 keys || exit 1
dp_run "figaro listen $ARIA"
sleep 3
dp_key C-t; dp_stable

dp_key w; dp_stable
dp_cap > "$DP_DIR/plainw.cap"
check "w alone is inert (so the next check can fail)" "! grep -q '^! ·' '$DP_DIR/plainw.cap'"
dp_key Space; dp_key w; dp_stable
dp_cap > "$DP_DIR/leaderw.cap"
check "Space w (a user leader chord) opens status" "grep -q '^! ·' '$DP_DIR/leaderw.cap'"
dp_key Escape; dp_stable
dp_key h; dp_stable
dp_cap > "$DP_DIR/leader.cap"
check "the help column follows the remap (J)" "grep -q '^ *J j k d u G g ' '$DP_DIR/leader.cap'"
dp_key j; dp_stable
dp_cap > "$DP_DIR/listj.cap"
check "j moves a cursor in the help list (list mode)" "grep -q '♭\\|▸\\|⌖' '$DP_DIR/listj.cap' || ! diff -q '$DP_DIR/leader.cap' '$DP_DIR/listj.cap' >/dev/null"
dp_key Escape; dp_stable

dp_key z; dp_key q; dp_stable
dp_cap > "$DP_DIR/prefix.cap"
check "z q (a user-declared prefix) runs the queue verb" "grep -q 'queue is empty\|𝄚' '$DP_DIR/prefix.cap'"
dp_key Escape; dp_stable

range() { dp_cap | grep -o '[0-9]*[–-][0-9]*/[0-9]*' | tail -1; }
dp_key g; dp_key g; dp_stable; top=$(range)
dp_key J; dp_stable; after=$(range)
dp_key G; dp_stable; bottom=$(range)
echo "   range after gg: $top   after J: $after   after G: $bottom"
check "the fixture scrolls at all (a range is shown)" "[ -n \"$top\" ] && [ \"$top\" != \"$bottom\" ]"
check "gg reaches the top" "echo \"$top\" | grep -q '^1[–-]'"
check "J (rebound to half-down) moves down from the top" "[ \"$after\" != \"$top\" ]"
dp_key j; dp_stable; jrange=$(range)
dp_key g; dp_key g; dp_key J; dp_key k; dp_stable
check "j still scrolls (the remap added J, it took nothing)" "[ -n \"$jrange\" ]"

echo "--- a refused binding is reported, and the rest stays in force"
cat >> "$DP_DIR/config/keys.toml" <<'EOF'

[bind.session]
"^C" = "listen"
EOF
dp_fig doctor keys > "$DP_DIR/doctor.out" 2>&1; code=$?
cat "$DP_DIR/doctor.out"
check "doctor exits nonzero on a refusal" "[ $code -ne 0 ]"
check "doctor names the refusal" "grep -q 'guaranteed way out' '$DP_DIR/doctor.out'"

[ -n "${DP_KEEP:-}" ] || dp_down
[ -n "${DP_KEEP:-}" ] || dp_verify_clean
exit $fail
