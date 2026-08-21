# Does a lifetime actually end an aria? The reaper only takes a DORMANT node,
# so this waits for real hibernation -- no daemon restart, which is the shortcut
# that makes this test pass without proving anything.
#
# Run it in the dev shell:  nix develop --command bash scripts/live/ttllive.sh
set -uo pipefail
ROOT=$(mktemp -d /var/tmp/figttllive.XXXX)
BIN=$ROOT/figaro
go build -o "$BIN" ./cmd/figaro || exit 1
mkdir -p "$ROOT/state" "$ROOT/rt" "$ROOT/config"
cp -a "${HOME}/.config/figaro/." "$ROOT/config/" 2>/dev/null
python3 - "$ROOT/config/config.toml" <<'PY'
import sys, re
p = sys.argv[1]; s = open(p).read()
# The two clocks this test bends: hibernate after a minute, sweep every 3s.
for key, val in (("sweep_interval_seconds", "3"), ("dormant_after_minutes", "1")):
    if re.search(rf'(?m)^{key}\s*=.*$', s):
        s = re.sub(rf'(?m)^{key}\s*=.*$', f'{key} = {val}', s)
    elif '[memory]' in s:
        s = s.replace('[memory]', f'[memory]\n{key} = {val}', 1)
    else:
        s += f'\n[memory]\n{key} = {val}\n'
open(p, 'w').write(s)
PY
export FIGARO_STATE_DIR="$ROOT/state" FIGARO_RUNTIME_DIR="$ROOT/rt" FIGARO_CONFIG_DIR="$ROOT/config"
unset FIGARO_ARIA FIGARO_NO_BIND FIGARO_PROMPT

fail() { echo "FAIL: $*"; $BIN stop >/dev/null 2>&1; exit 1; }
alive() { $BIN ls -g 2>/dev/null | grep -c "$1"; }

DOOMED=$($BIN new 2>&1 | grep -oE "\b[0-9a-f]{8}\b" | head -1)
KEEP=$($BIN new 2>&1 | grep -oE "\b[0-9a-f]{8}\b" | head -1)
FORM=$($BIN form new --set brief=doomed 2>&1 | grep -oE "@[0-9a-f]+" | head -1)
[ -n "$DOOMED" ] && [ -n "$KEEP" ] && [ -n "$FORM" ] || fail "could not mint the fixtures"
echo "doomed aria=$DOOMED  control aria=$KEEP  doomed form=$FORM"

$BIN set --id "$DOOMED" system.ttl 5s >/dev/null || fail "set ttl on the aria"
$BIN set --id "$FORM" system.ttl 5s >/dev/null || fail "set ttl on the form"

echo "--- the deadline is readable without waking anything:"
$BIN doctor ttl | sed 's/^/    /'
[ "$($BIN doctor ttl -j | grep -c '"expired"')" -eq 2 ] || fail "doctor ttl does not see both lifetimes"

echo "--- the form has no agent: it goes on the first sweep past its deadline"
sleep 10
[ "$(alive "$FORM")" -eq 0 ] || fail "the form outlived its lifetime"
echo "    form: gone"

echo "--- the aria is LIVE, so it is spared and says so, every tick"
[ "$(alive "$DOOMED")" -ge 1 ] || fail "a live aria was deleted out from under its agent"
echo "    aria: still here, as designed"

echo "--- now wait for real hibernation (dormant_after_minutes = 1), no restart"
for i in $(seq 1 40); do
  sleep 5
  if [ "$(alive "$DOOMED")" -eq 0 ]; then
    echo "    aria: reaped after $((i * 5))s, on hibernation + sweep alone"
    break
  fi
done
[ "$(alive "$DOOMED")" -eq 0 ] || fail "the aria survived hibernation and $((40 * 5))s of sweeps"
[ "$(alive "$KEEP")" -ge 1 ] || fail "the control aria, which states no lifetime, was taken"
echo "    control aria: untouched"

echo "--- the daemon's own account:"
python3 - "$ROOT/state/logs.jsonl" <<'PY'
import json, sys
for line in open(sys.argv[1], errors="replace"):
    try: d = json.loads(line)
    except Exception: continue
    v = str(d.get("Body", {}).get("Value", ""))
    if "lifetime" in v or "hibernated aria" in v:
        a = {x["Key"]: x["Value"]["Value"] for x in d.get("Attributes", [])}
        print("   ", d["Timestamp"][11:19], v, {k: a[k] for k in ("node", "aria") if k in a})
PY
$BIN stop >/dev/null 2>&1
rm -rf "$ROOT"
echo "PASS: a lifetime ends a node, and only once it is dormant."
