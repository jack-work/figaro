#!/usr/bin/env bash
# The notifications pit, in a real pty.
#
# Everything the pager said went through one slot of the status bar and was
# gone ten seconds later. This checks what a person sees now: the alert, the
# mark it leaves behind when it retires unread, the pit that holds the
# history, and that opening the pit is what reads it.
#
#   BIN=/path/to/figaro bash scripts/notifications-pty.sh     # default: builds one
#
# Hermetic: its own config, state and runtime dirs, its own tmux socket, no
# provider credentials. Cleans up after itself, daemon included.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BOX=$(mktemp -d /tmp/notifications-pty.XXXXXX)
SOCK=$BOX/tmux.sock
W=110 H=30
pass=0 fail=0

cleanup() {
  tmux -S "$SOCK" kill-server 2>/dev/null
  FIGARO_RUNTIME_DIR=$BOX/rt "$BIN" stop >/dev/null 2>&1
  rm -rf "$BOX"
}
trap cleanup EXIT

if [[ -z "${BIN:-}" ]]; then
  BIN=$BOX/figaro
  (cd "$ROOT" && go build -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse HEAD)" -o "$BIN" ./cmd/figaro) || exit 1
fi
mkdir -p "$BOX"/{cfg/outfits,state,rt,cache}
printf 'You are a test agent.\n' > "$BOX/cfg/credo.md"
cat > "$BOX/cfg/outfits/t.toml" <<'EOF'
[system]
provider = "anthropic"
model    = "claude-test"
credo    = { fileName = "credo.md" }
EOF
# A two-second alert, so the mark it leaves can be seen without a long wait.
printf 'default_outfit = "t"\ninteractive = true\n\n[cli]\nnotice_ttl = 2\n' > "$BOX/cfg/config.toml"
export FIGARO_CONFIG_DIR=$BOX/cfg FIGARO_STATE_DIR=$BOX/state FIGARO_RUNTIME_DIR=$BOX/rt \
       FIGARO_CACHE_DIR=$BOX/cache FIGARO_NO_BIND=1

A=$("$BIN" new -j 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin)["aria_id"])')
[[ -n "$A" ]] || { echo "FIXTURE: could not mint an aria"; exit 1; }

t() { tmux -S "$SOCK" "$@"; }
shot() { t capture-pane -p -t p:0; }
raw() { t capture-pane -p -e -t p:0; }
bar() { shot | tail -1; }
key() { for k in "$@"; do t send-keys -t p:0 "$k"; sleep 0.3; done; }
typ() { local s=$1 i; for ((i = 0; i < ${#s}; i++)); do t send-keys -t p:0 -l "${s:i:1}"; sleep 0.05; done; }
check() {
  if [[ $2 -eq 0 ]]; then pass=$((pass + 1)); echo "ok   $1"
  else fail=$((fail + 1)); echo "FAIL $1${3:+: $3}"; shot | sed 's/^/     | /' | tail -10; fi
}
cmd() { key Escape Escape; sleep 0.2; key ':'; typ "$1"; key Enter; sleep 1; }

t new-session -d -s p -x "$W" -y "$((H + 1))" "bash --norc"
t set -g status off
t send-keys -t p:0 -l "PS1='$ '; $BIN listen $A"
t send-keys -t p:0 Enter
sleep 4

# 1. A failed verb: the alert, red, with the mark beside it.
cmd "attend zzzzzzzz"
check "the failure is on the bar" $(bar | grep -q "zzzzzzzz"; echo $?) "bar: $(bar)"
check "an unread mark stands beside it" $(bar | grep -q "𝄞 1"; echo $?)

# 2. The alert retires; the mark does not, and it is red for an error.
sleep 3
check "the alert retired" $(bar | grep -q "zzzzzzzz"; [[ $? -ne 0 ]]; echo $?) "bar: $(bar)"
check "the mark stayed" $(bar | grep -q "𝄞 1"; echo $?) "bar: $(bar)"
# The MARK itself, not whatever else on the row is red: an alert is red too,
# and a check on "any red" passed against a build with no mark at all.
check "the mark is red, for an error" $(raw | tail -1 | grep -q $'38;5;167m\xf0\x9d\x84\x9e'; echo $?)

# 3. space n opens the history; reading it clears the mark.
key Space n; sleep 0.8
check "space n opens the pit with the failure in it" $(shot | grep -qE "✗ +cli +attend: .*zzzzzzzz"; echo $?)
check "the newest row is chosen" $(shot | grep -qE "^♩ "; echo $?)
check "opening it read it: no mark" $(bar | grep -q "𝄞 [0-9]"; [[ $? -ne 0 ]]; echo $?) "bar: $(bar)"

# 4. Enter spells the row out: the whole text, wrapped, and nothing else.
# The id is long on purpose, with a marker at its end: the row CANNOT hold it
# (checked below), so the marker on screen can only have come from Enter.
LONG=$(python3 -c "print('z' * 140 + 'QQEND')")
cmd "attend $LONG"
key Escape; sleep 0.3      # a note too long for the bar opens the note pit
key Space n; sleep 0.8
check "the long failure is the newest row" $(shot | grep -qE "^♩ .*✗ +cli +attend"; echo $?)
# THE CLIP IS THE PREMISE: if the marker were on the row already, the check
# below would pass without Enter doing anything.
check "the row cannot hold the whole text" $(shot | grep -q "QQEND"; [[ $? -ne 0 ]]; echo $?)
key Enter; sleep 0.5
check "Enter wraps the text the row clipped" $(shot | grep -qE "^ +z+QQEND"; echo $?)
check "Enter does not report the date and the source" $(shot | grep -q "from this pager"; [[ $? -ne 0 ]]; echo $?) "$(shot | tail -6)"

# 4b. The level is a colour. The newest row is selected, so its red rides the
# selection wash (the lifted spelling); the older failure below it wears the
# palette's own red.
check "the selected error row is red over the wash" \
  $(raw | grep "♩" | grep -q $'38;5;210'; echo $?)
check "an unselected error row is red" \
  $(raw | grep "✗" | grep -v "♩" | grep -q $'38;5;167'; echo $?)
# A guard, not a canary: this one holds on both builds, and is here so that
# painting every row would be a failure rather than a preference.
check "an info row is not painted" \
  $(raw | grep -E "·  cli" | grep -v "♩" | grep -q $'\033\[38;5;1'; [[ $? -ne 0 ]]; echo $?)
key Enter; sleep 0.3       # fold it back

# 5. f filters. Yanking a pit row is news ("yanked N bytes"), an info row.
key y; sleep 0.4
key Space n; sleep 0.4   # close
key Space n; sleep 0.6   # and open again, with the yank in it
rows_all=$(shot | grep -cE "^(♩|  )[0-9]{2}:[0-9]{2}:[0-9]{2}")
key f; sleep 0.4
rows_warn=$(shot | grep -cE "^(♩|  )[0-9]{2}:[0-9]{2}:[0-9]{2}")
check "f hides what is only news" $(( rows_warn < rows_all ? 0 : 1 )) "all=$rows_all warn+=$rows_warn"
key f f; sleep 0.3

# 6. space n again closes it; :notifications opens it from the box.
key Space n; sleep 0.5
check "space n closes it" $(shot | grep -qE "^♩ "; [[ $? -ne 0 ]]; echo $?)
cmd "notifications"
check ":notifications opens it" $(shot | grep -qE "✗ +cli +attend"; echo $?)

# 7. :notifications clear empties it.
cmd "notifications clear"
key Space n; sleep 0.6
check ":notifications clear empties it" $(shot | grep -qE "✗ +cli +attend"; [[ $? -ne 0 ]]; echo $?)

# 8. n alone is still search-repeat, not notifications.
key Escape Escape; sleep 0.3
key n; sleep 0.4
check "n alone does not open the pit" $(shot | grep -qE "^♩ "; [[ $? -ne 0 ]]; echo $?)

echo
echo "notifications-pty: $pass ok, $fail failed"
[[ $fail -eq 0 ]]
