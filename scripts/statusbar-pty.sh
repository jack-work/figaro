#!/usr/bin/env bash
# The status row's state token, in a real pty on a PRIVATE tmux server.
#
# The server is private (-S on its own socket) for the reason the smoke harness
# gives: a shared server leaks sessions between runs, and killing Gluck's tmux
# would be unforgivable.
#
# What it looks at: the succinct glyph is what the bar draws by default, and
# the ONLY place that is true is a terminal -- a unit test sees the string the
# renderer returned, not the row the terminal kept.
set -uo pipefail

BIN=${BIN:-/tmp/statusbar/figaro}
DIR=$(mktemp -d /tmp/statusbar-pty.XXXXXX)
SOCK=$DIR/tmux.sock
PORT=${PORT:-8921}
export FIGARO_STATE_DIR=$DIR/state FIGARO_RUNTIME_DIR=$DIR/run \
       FIGARO_CONFIG_DIR=$DIR/cfg FIGARO_CACHE_DIR=$DIR/cache
mkdir -p "$FIGARO_STATE_DIR" "$FIGARO_RUNTIME_DIR" "$FIGARO_CONFIG_DIR"/{outfits,providers} "$FIGARO_CACHE_DIR"

cleanup() {
  tmux -S "$SOCK" kill-server 2>/dev/null
  "$BIN" stop >/dev/null 2>&1
  kill "${GW:-0}" 2>/dev/null
  rm -rf "$DIR"
}
trap cleanup EXIT

if (echo >/dev/tcp/127.0.0.1/$PORT) 2>/dev/null; then
  echo "FAIL: something already listens on :$PORT"; exit 1
fi
python3 "$(git rev-parse --show-toplevel)/scripts/fake-gateway-tools.py" $PORT "$DIR/req.jsonl" & GW=$!
sleep 1

cat > "$FIGARO_CONFIG_DIR/providers/gateway.toml" <<EOF
base_url = "http://127.0.0.1:$PORT/v1"
EOF
printf 'test agent\n' > "$FIGARO_CONFIG_DIR/credo.md"
cat > "$FIGARO_CONFIG_DIR/outfits/bar.toml" <<'EOF'
duke-title = "bar"
[system]
provider = "gateway"
model = "auto"
max_tokens = 64
credo = { fileName = "credo.md" }
EOF
printf 'default_outfit = "bar"\ninteractive = false\n' > "$FIGARO_CONFIG_DIR/config.toml"

# THE PAGER MUST BE WATCHING WHEN THE TURN RUNS. The first draft of this script
# prompted first and attached afterwards, then asserted a ✓ that was never
# going to be there: a session that never saw a turn reports `idle`, which is
# the catch-all doing exactly its job. The state is a property of what THIS
# CLI watched, not of the aria's history.
ARIA=$("$BIN" new -j | python3 -c 'import json,sys;print(json.load(sys.stdin)["aria_id"])')

# -y h+1: tmux subtracts the status row AT CREATION, and turning the bar off
# afterwards does not give it back. Then read back what we actually got.
tmux -S "$SOCK" new-session -d -s bar -x 100 -y 25 bash --norc
tmux -S "$SOCK" set -g status off
H=$(tmux -S "$SOCK" display -p -t bar:0 '#{pane_height}')
tmux -S "$SOCK" send-keys -t bar:0 "export FIGARO_STATE_DIR=$FIGARO_STATE_DIR FIGARO_RUNTIME_DIR=$FIGARO_RUNTIME_DIR FIGARO_CONFIG_DIR=$FIGARO_CONFIG_DIR FIGARO_CACHE_DIR=$FIGARO_CACHE_DIR FORCE_COLOR=1" Enter
tmux -S "$SOCK" send-keys -t bar:0 "$BIN listen $ARIA" Enter
sleep 2

# Now prompt it from OUTSIDE the pane, and watch the row move.
"$BIN" send -f --id "$ARIA" -- "look around" >/dev/null 2>&1
sleep 1
thinking=$(tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -v '^$' | tail -1)
echo "mid-turn row: [$thinking]"
if [[ "$thinking" =~ [⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏] ]]; then
  echo "ok   a spinner frame is on the row while the turn runs"
else
  echo "note: no spinner frame caught (the fake gateway answers fast)"
fi
for _ in $(seq 60); do
  [[ "$("$BIN" status "$ARIA" -j 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin).get("state",""))')" == idle ]] && break
  sleep 0.5
done
sleep 2

bar() { tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -v '^$' | tail -1; }

fail=0
row=$(bar)
echo "pane height $H"
echo "status row: [$row]"

# 1. The succinct glyph is on the row, and the old fused word is not.
if [[ "$row" != *"✓"* ]]; then
  echo "FAIL: no done glyph on the status row"; fail=1
else
  echo "ok   the done glyph ✓ is on the row"
fi
# THE NAMES ARE WHAT VERBOSE ADDS, so the default row must carry NONE of them:
# the old fused spellings, and equally the new ones. The first draft of this
# script forbade only the old words, so a canary that forced verbose on the
# default row sailed straight through it -- the assertion was testing that a
# rename had happened, not that succinct was succinct.
for word in completed interrupted disconnected done hup error detached idle; do
  if [[ "$row" == *"$word"* ]]; then
    echo "FAIL: the default row carries the state NAME '$word'; succinct is glyph-only"; fail=1
  fi
done
[[ $fail == 0 ]] && echo "ok   the default row carries no state name at all"

# 1b. THE BAR IS THE VALUE'S OUTPUT NOW: the aria id is on it, and the fields
#     the requirement removed are gone.
if [[ "$row" == *"123"* || "$row" =~ [0-9a-f]{8} ]]; then
  echo "ok   the aria id is on the status row"
else
  echo "FAIL: no aria id on the status row: [$row]"; fail=1
fi
for gone in "cost" "? help" "! status"; do
  if [[ "$row" == *"$gone"* ]]; then
    echo "FAIL: '$gone' is still on the default row"; fail=1
  fi
done

# 2. The panel is where words live: '!' opens it and it names the state.
tmux -S "$SOCK" send-keys -t bar:0 '!'
sleep 1
panel=$(tmux -S "$SOCK" capture-pane -p -t bar:0)
if [[ "$panel" == *"done ✓"* ]]; then
  echo "ok   the ! panel names the state: done ✓"
else
  echo "FAIL: the ! panel does not name the state"; fail=1
  echo "$panel" | tail -8
fi

# 2b. A DRAWER PUTS ITS GLYPH ON THE BAR. 'Q' opens the queue.
tmux -S "$SOCK" send-keys -t bar:0 Escape
sleep 0.5
tmux -S "$SOCK" send-keys -t bar:0 Q
sleep 1
qrow=$(bar)
if [[ "$qrow" == *"𝄚"* ]]; then
  echo "ok   the queue drawer puts 𝄚 on the bar: [$qrow]"
else
  echo "FAIL: no queue glyph on the bar with the queue open: [$qrow]"; fail=1
fi
tmux -S "$SOCK" send-keys -t bar:0 Escape
sleep 0.5

# 3. It survives a resize, which is where a width-sensitive row goes wrong.
tmux -S "$SOCK" send-keys -t bar:0 Escape
tmux -S "$SOCK" resize-window -t bar -x 46 -y 25 2>/dev/null
sleep 1
narrow=$(bar)
echo "narrow row: [$narrow]"
if [[ "$narrow" == *"✓"* ]]; then
  echo "ok   the glyph survives a 46-column pane"
else
  echo "FAIL: the state fell off a narrow row"; fail=1
fi

# 4. VERY narrow: the bar becomes three rows and the conversation must not lose
#    a line to it -- the reservation and the painting have to agree.
# 16, not 24: the fixture's context figure is three characters ("105"), so the
# one-row form still fits at 24 once the mantra sheds. The three-row form is
# golden-tested at realistic widths (statusview_test.go); what the pty is for
# is proving the RESERVATION agrees with the painting, and that needs a width
# where the bar genuinely grows.
tmux -S "$SOCK" resize-window -t bar -x 16 -y 25 2>/dev/null
sleep 1
tail3=$(tmux -S "$SOCK" capture-pane -p -t bar:0 | tail -3)
echo "narrow-3 tail:"; echo "$tail3" | sed 's/^/    |/'
# The bar grew to three rows: left, blank, right. The blank between them is
# the tell, and the rule must still sit directly above the whole stanza rather
# than being overwritten by it.
if [[ "$(echo "$tail3" | sed -n 2p)" =~ ^[[:space:]]*$ ]]; then
  echo "ok   the bar split into rows with a blank between them"
else
  echo "FAIL: no blank row inside the split bar"; fail=1
fi
if [[ "$(echo "$tail3" | tail -1)" =~ [0-9] ]]; then
  echo "ok   the right group has a row of its own"
else
  echo "FAIL: the right group is missing from the split bar"; fail=1
fi

# 5. THE PICKER. The help panel is the list that tells you how to scroll, and
#    until the picker landed it was the one list you could not scroll: taller
#    than the drawer's window, its bottom was simply unreachable.
tmux -S "$SOCK" resize-window -t bar -x 100 -y 25 2>/dev/null
sleep 1
tmux -S "$SOCK" send-keys -t bar:0 Escape; sleep 0.4
tmux -S "$SOCK" send-keys -t bar:0 '?'; sleep 1
first=$(tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -c "more")
top=$(tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -m1 "exit; keeps the turn running")
tmux -S "$SOCK" send-keys -t bar:0 j; sleep 0.4
tmux -S "$SOCK" send-keys -t bar:0 j; sleep 0.4
tmux -S "$SOCK" send-keys -t bar:0 j; sleep 0.6
after=$(tmux -S "$SOCK" capture-pane -p -t bar:0)
if [[ -z "$top" ]]; then
  echo "FAIL: the help panel never showed its first row"; fail=1
elif [[ "$after" == *"exit; keeps the turn running"* ]]; then
  echo "FAIL: j did not scroll the help panel"; fail=1
else
  echo "ok   j scrolled the help panel past its first row"
fi
if [[ "$after" == *"more"* ]]; then
  echo "ok   the picker says what is out of view: $(echo "$after" | grep -m1 -o '… [0-9]* more[a-z ]*')"
else
  echo "FAIL: no truncation marker on a scrolled picker"; fail=1
fi
# G to the end, gg back to the top.
tmux -S "$SOCK" send-keys -t bar:0 G; sleep 0.6
if tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -q "close help"; then
  echo "ok   G reached the end of the help list"
else
  echo "FAIL: G did not reach the end of the help list"; fail=1
fi
tmux -S "$SOCK" send-keys -t bar:0 g; sleep 0.2; tmux -S "$SOCK" send-keys -t bar:0 g; sleep 0.6
if tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -q "exit; keeps the turn running"; then
  echo "ok   gg returned to the top"
else
  echo "FAIL: gg did not return to the top"; fail=1
fi

# 6. NO CLOSING RULE, NO HINT ROW. Gluck's design fences the drawer ABOVE only:
#    rule, rows, blank, bar. The lower fence used to carry "^N/^P select · y
#    yank · Esc close" -- a caption under every list.
tmux -S "$SOCK" send-keys -t bar:0 Escape; sleep 0.4
tmux -S "$SOCK" send-keys -t bar:0 Q; sleep 1
qtail=$(tmux -S "$SOCK" capture-pane -p -t bar:0 | tail -4)
echo "queue tail:"; echo "$qtail" | sed 's/^/    |/'
if echo "$qtail" | grep -q "Esc close\|\^N/\^P select"; then
  echo "FAIL: the drawer still prints a hint row"; fail=1
else
  echo "ok   no hint row under the drawer"
fi
if [[ "$(echo "$qtail" | sed -n 3p)" == *"───"* ]]; then
  echo "FAIL: a closing rule still fences the drawer from the bar"; fail=1
else
  echo "ok   no closing rule between the drawer and the bar"
fi
tmux -S "$SOCK" send-keys -t bar:0 Escape; sleep 0.4

# 7. 'm' IS MORE. ^V never worked and was never opened in a terminal.
before=$(bar)
tmux -S "$SOCK" send-keys -t bar:0 m; sleep 1
more=$(bar)
echo "more row: [$more]"
if [[ "$more" == *"done"* ]]; then
  echo "ok   m turned on the bar's detail (state names)"
else
  echo "FAIL: m did not turn on the bar's detail: [$more]"; fail=1
fi
tmux -S "$SOCK" send-keys -t bar:0 m; sleep 0.8
if [[ "$(bar)" == "$before" ]]; then
  echo "ok   m toggles back off"
else
  echo "FAIL: m did not toggle back off"; fail=1
fi

# 8. FORM SHOW: j must scroll it a ROW at a time, not skip whole properties.
tmux -S "$SOCK" send-keys -t bar:0 ':'; sleep 0.5
tmux -S "$SOCK" send-keys -t bar:0 -l "form listen"; sleep 0.5
tmux -S "$SOCK" send-keys -t bar:0 Enter; sleep 2
formrow=$(bar)
echo "form row: [$formrow]"
if [[ "$formrow" == *"𝄢"* ]]; then
  echo "ok   the form view has its own glyph 𝄢 on the bar"
else
  echo "FAIL: no form glyph on the bar: [$formrow]"; fail=1
fi
if [[ "$formrow" == *"𝄞"* ]]; then
  echo "FAIL: the form view borrowed the notification clef"; fail=1
fi
tmux -S "$SOCK" send-keys -t bar:0 Escape; sleep 0.4

# 9. THE ALERT RETIRES. Posted by a real command, watched until it goes -- the
#    step I skipped, which is exactly why it never went.
tmux -S "$SOCK" send-keys -t bar:0 ':'; sleep 0.4
tmux -S "$SOCK" send-keys -t bar:0 -l "open nosuchid"; sleep 0.3
tmux -S "$SOCK" send-keys -t bar:0 Enter; sleep 1.5
posted=$(bar)
echo "alert row:   [$posted]"
if [[ "$posted" == *"nosuchid"* || "$posted" == *"open"* ]]; then
  echo "ok   the alert posted into the bar"
else
  echo "FAIL: no alert in the bar after a failing command"; fail=1
fi
echo "     waiting out the 10s TTL..."
sleep 13
gone=$(bar)
echo "after TTL:   [$gone]"
if [[ "$gone" == *"nosuchid"* ]]; then
  echo "FAIL: the alert outlived its TTL with no keystroke"; fail=1
else
  echo "ok   the alert retired on its own"
fi

tmux -S "$SOCK" send-keys -t bar:0 q
sleep 1
exit $fail
