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
# AN EMPTY QUEUE IS AN EMPTY PIT. The bar says the queue is open; a "(none)"
# row is a row spent saying what no rows already said.
if echo "$qtail" | grep -q "(none)"; then
  echo "FAIL: the empty queue still draws a (none) row"; fail=1
else
  echo "ok   an empty queue draws no placeholder"
fi
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

# 6b. THE QUEUE'S CURSOR, and the bug it exists for: the pit is opened by hand
#     while the queue is EMPTY -- one row reading "(none)", which is chrome --
#     and the rows arrive afterwards. The picker used to be told at birth
#     whether it had a cursor, so it stayed cursorless for the rest of its life
#     and ^N did nothing until you closed the pit and opened it again.
hl() { tmux -S "$SOCK" capture-pane -p -e -t bar:0 | grep -a "48;5;240m" | head -1 | sed 's/\x1b\[[0-9;]*m//g' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'; }

tmux -S "$SOCK" send-keys -t bar:0 Escape; sleep 0.4   # section 6 left the pit open
tmux -S "$SOCK" send-keys -t bar:0 Q; sleep 1          # open it EMPTY
if [[ -n "$(hl)" ]]; then
  echo "FAIL: an empty queue highlights a row: [$(hl)]"; fail=1
else
  echo "ok   the empty queue pit selects nothing"
fi
# A turn that takes its time, so the next two prompts are QUEUED rather than run.
echo 8 > "$DIR/req.jsonl.delay"
"$BIN" send -f --id "$ARIA" -- "slow turn" >/dev/null 2>&1
sleep 1
"$BIN" send -f --id "$ARIA" -- "queued alpha" >/dev/null 2>&1
"$BIN" send -f --id "$ARIA" -- "queued beta" >/dev/null 2>&1
sleep 3
echo "daemon queue: [$("$BIN" queue ls --id "$ARIA" 2>&1 | tr '\n' '|')]"
qrows=$(tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -c "queued ")
first=$(hl)
echo "queue rows: $qrows, first highlight: [$first]"
if (( qrows >= 2 )); then
  echo "ok   the queued prompts reached the open pit"
else
  echo "FAIL: the queue pit never filled ($qrows rows)"; fail=1
  tmux -S "$SOCK" capture-pane -p -t bar:0 | tail -6 | sed 's/^/    |/'
fi
if [[ -n "$first" ]]; then
  echo "ok   the refreshed queue selects a row: [$first]"
else
  echo "FAIL: the queue filled and nothing is selected"; fail=1
fi
tmux -S "$SOCK" send-keys -t bar:0 C-n; sleep 0.8
second=$(hl)
if [[ -n "$second" && "$second" != "$first" ]]; then
  echo "ok   ^N moves the selection in a queue that filled while open: [$second]"
else
  echo "FAIL: ^N did nothing in the refreshed queue ([$first] → [$second])"; fail=1
fi
echo 0 > "$DIR/req.jsonl.delay"
tmux -S "$SOCK" send-keys -t bar:0 Escape; sleep 0.4
for _ in $(seq 60); do
  [[ "$("$BIN" status "$ARIA" -j 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin).get("state",""))')" == idle ]] && break
  sleep 1
done

# 6c. THE RULE ENDS WITH THE POSITION. It used to be capped with " ───", so the
#     one figure on the rule stopped three cells short of the edge the context
#     figure below it is flush to.
rule=$(tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -m1 "─.*/" | sed 's/[[:space:]]*$//')
echo "rule: [${rule: -28}]"
if [[ "$rule" == *"───" ]]; then
  echo "FAIL: the rule still caps the position with dashes"; fail=1
else
  echo "ok   the page position is the last thing on the rule"
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

# 8. READING A FORM IS ONE THING. `form show` and `form listen` are the same
#    live pit now -- `show` used to be captured as TEXT and pasted into an
#    output pit whose rows are lines, which is why its selection skipped whole
#    properties. Both spellings must land on the same view, with a selection.
for verb in "form listen" "form show" "state show" "form"; do
  tmux -S "$SOCK" send-keys -t bar:0 ':'; sleep 0.5
  tmux -S "$SOCK" send-keys -t bar:0 -l "$verb"; sleep 0.5
  tmux -S "$SOCK" send-keys -t bar:0 Enter; sleep 2
  formrow=$(bar)
  if [[ "$formrow" == *"웃"* ]]; then
    echo "ok   :$verb opens the form pit (웃)"
  else
    echo "FAIL: :$verb did not open the form pit: [$formrow]"; fail=1
  fi
  if [[ "$formrow" == *"𝄞"* ]]; then
    echo "FAIL: the form view borrowed the notification clef"; fail=1
  fi
  if [[ -z "$(hl)" ]]; then
    echo "FAIL: :$verb opened a form pit with no selected row"; fail=1
  fi
  tmux -S "$SOCK" send-keys -t bar:0 Escape; sleep 0.5
done

# 8a. ENTER EXPANDS A BRANCH, which is only possible if the pit's selection is
#     visible to the view's own verbs. It was not: selected() answered for a
#     list pit only, so a hosted form drew a highlight nothing could act on.
tmux -S "$SOCK" send-keys -t bar:0 ':'; sleep 0.4
tmux -S "$SOCK" send-keys -t bar:0 -l "form show"; sleep 0.4
tmux -S "$SOCK" send-keys -t bar:0 Enter; sleep 2
# Walk down to a BRANCH row -- one that says how many keys are under it,
# "system (8)"; branches carry no arrow any more -- and open it.
found=0
for _ in $(seq 12); do
  row=$(hl)
  if [[ ! "$row" =~ \([0-9]+\)$ ]]; then
    tmux -S "$SOCK" send-keys -t bar:0 C-n; sleep 0.4
    continue
  fi
  found=1
  kids=$(echo "$row" | grep -o "([0-9]*)" | tr -d "()")
  before=$(tmux -S "$SOCK" capture-pane -p -t bar:0)
  tmux -S "$SOCK" send-keys -t bar:0 Enter; sleep 1.2
  if [[ "$(tmux -S "$SOCK" capture-pane -p -t bar:0)" == "$before" ]]; then
    echo "FAIL: Enter on branch [$row] changed nothing in the pit"; fail=1
  else
    echo "ok   Enter expanded the branch [$row] ($kids keys)"
  fi
  if [[ "$(hl)" == "$row" ]]; then
    echo "ok   the cursor stayed on the branch it opened"
  else
    echo "FAIL: Enter moved the selection off the branch ([$row] -> [$(hl)])"; fail=1
  fi
  break
done
(( found )) || { echo "FAIL: no branch row was reachable with ^N in the form pit"; fail=1; }
# AND THE PAGER IS STILL ALIVE. A verb that repaints from inside a key handler
# takes the render lock twice and the whole pager stops; every check after it
# then reads a frozen screen and passes or fails for the wrong reason.
tmux -S "$SOCK" send-keys -t bar:0 Escape; sleep 0.5
tmux -S "$SOCK" send-keys -t bar:0 '?'; sleep 1
if tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -q "close help\|exit; keeps the turn running"; then
  echo "ok   the pager still takes keys after Enter in the form pit"
else
  echo "FAIL: the pager is frozen after Enter in the form pit"; fail=1
fi
tmux -S "$SOCK" send-keys -t bar:0 Escape; sleep 0.4

# 8b. FULLSCREEN. 'F' gives the pit the pane; the transcript stays BEHIND it,
#     dimmed, so leaving puts the reader back where they were.
tmux -S "$SOCK" send-keys -t bar:0 '?'; sleep 1
small=$(tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -c "^  ")
tmux -S "$SOCK" send-keys -t bar:0 F; sleep 1
big=$(tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -c "^  ")
echo "help rows: $small → $big"
if (( big > small )); then
  echo "ok   F grew the pit ($small → $big rows)"
else
  echo "FAIL: F did not grow the pit ($small → $big)"; fail=1
fi
# FULLSCREEN OBSCURES. The conversation is not dimmed behind the pit any more:
# it is gone until the pit closes. ("look around" is no good as the probe -- it
# is the mantra, and the mantra is on the bar; the prompt echo is the row that
# only exists in the transcript.)
if tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -q "^  aria "; then
  echo "FAIL: fullscreen left the transcript on screen"; fail=1
else
  echo "ok   fullscreen obscures the transcript"
fi
tmux -S "$SOCK" send-keys -t bar:0 F; sleep 1
back=$(tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -c "^  ")
if (( back == small )); then
  echo "ok   F toggles back to the small pit"
else
  echo "FAIL: F did not restore the pit ($back vs $small)"; fail=1
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

# 10. LEAVING IS SILENT. Hang up, then disconnect: the pager used to keep a
#     "pending report" of everything it had said and REPRINT it to the shell on
#     the way out, so one hangup became two lines and a disconnect four. Gluck:
#     "there should not be duplicates, there should also not be ANYTHING."
# THREE PHRASES, AND NO MORE. With no turn in flight there is nothing to stop,
# and that is all it says.
tmux -S "$SOCK" send-keys -t bar:0 H; sleep 2
if [[ "$(bar)" == *"nothing to interrupt"* ]]; then
  echo "ok   H with no turn says nothing to interrupt"
else
  echo "FAIL: H with no turn said: [$(bar)]"; fail=1
fi
for word in "hanging up" "staying attached" "Ctrl-C again" "printed on exit"; do
  if [[ "$(bar)" == *"$word"* ]]; then
    echo "FAIL: the bar still explains itself ('$word')"; fail=1
  fi
done
# And with a turn RUNNING: interrupting, then interrupted.
echo 6 > "$DIR/req.jsonl.delay"
"$BIN" send -f --id "$ARIA" -- "a turn to interrupt" >/dev/null 2>&1
sleep 2
tmux -S "$SOCK" send-keys -t bar:0 H; sleep 1
mid=$(bar)
sleep 7
after=$(bar)
echo 0 > "$DIR/req.jsonl.delay"
# The daemon answers in milliseconds, so the sample may already be the settled
# word; what must never appear is the old paragraph.
if [[ "$mid" == *"interrupting"* || "$mid" == *"interrupted"* ]]; then
  echo "ok   H on a live turn says interrupting/interrupted"
else
  echo "FAIL: H on a live turn said: [$mid]"; fail=1
fi
if [[ "$mid" == *"nothing to interrupt"* ]]; then
  echo "FAIL: H on a live turn claimed there was nothing to interrupt"; fail=1
fi
if [[ "$after" == *"interrupted"* || "$mid" == *"interrupted"* ]]; then
  echo "ok   and interrupted once it settled"
else
  echo "FAIL: the interrupt never settled into 'interrupted': [$after]"; fail=1
fi
tmux -S "$SOCK" send-keys -t bar:0 C-d; sleep 2
after=$(tmux -S "$SOCK" capture-pane -p -t bar:0)
noisy=0
for word in "hung up" "hanging up" "interrupting" "interrupted" "disconnected"; do
  if [[ "$after" == *"$word"* ]]; then
    echo "FAIL: '$word' was printed to the shell on the way out"; noisy=1
  fi
done
(( noisy )) && { fail=1; echo "$after" | tail -8 | sed 's/^/    |/'; }
(( noisy )) || echo "ok   leaving the session printed nothing at all"

# 11. `form listen` IS `listen`, with the form pit open fullscreen. No second
#     program, no alt screen of its own, no key loop of its own.
# A FORM LONG ENOUGH TO NEED THE ROOM: fullscreen can only be seen on a list
# that does not already fit, and the fixture's form has five rows.
for i in $(seq 1 20); do "$BIN" set --id "$ARIA" "k$i" "v$i" >/dev/null 2>&1; done
tmux -S "$SOCK" send-keys -t bar:0 "$BIN form listen $ARIA" Enter; sleep 4
# The pit's height is the distance from the rule above it to the bar below it,
# which is the only measure that does not depend on how much the form happens
# to have in it. A blank row inside the pit is still the pit.
pitheight() {
  tmux -S "$SOCK" capture-pane -p -t bar:0 | awk '
    /^─+/ { rule = NR }
    END   { print NR - rule - 1 }'
}
pane=$(tmux -S "$SOCK" capture-pane -p -t bar:0)
formrows=$(echo "$pane" | grep -c "♮\|웃")
big=$(pitheight)
if [[ "$pane" == *"웃"* ]]; then
  echo "ok   form listen opens the form pit ($formrows marked rows)"
else
  echo "FAIL: form listen did not open the form pit"; fail=1
  echo "$pane" | tail -8 | sed 's/^/    |/'
fi
tmux -S "$SOCK" send-keys -t bar:0 F; sleep 1     # F: back to the ordinary pit
small=$(pitheight)
if (( small < big )); then
  echo "ok   F reduces the fullscreen pit to pit height ($big → $small rows)"
else
  echo "FAIL: F did not reduce the pit ($big → $small)"; fail=1
fi
# 11b. ENTER ON A LEAF SPELLS THE VALUE OUT, and a value that parses as JSON is
#      indented. The fixture carries one on purpose.
"$BIN" set --id "$ARIA" blob '{"filePath":"/tmp/x","frontmatter":"name: t"}' >/dev/null 2>&1
sleep 1
for _ in $(seq 30); do
  row=$(hl)
  [[ "$row" == *"blob"* ]] && break
  tmux -S "$SOCK" send-keys -t bar:0 C-n; sleep 0.25
done
if [[ "$(hl)" == *"blob"* ]]; then
  tmux -S "$SOCK" send-keys -t bar:0 Enter; sleep 1.2
  opened=$(tmux -S "$SOCK" capture-pane -p -t bar:0)
  if echo "$opened" | grep -q '"filePath": "/tmp/x"'; then
    echo "ok   Enter pretty-printed the value under the key"
  else
    echo "FAIL: Enter did not open the value"; fail=1
    echo "$opened" | tail -8 | sed 's/^/    |/'
  fi
  tmux -S "$SOCK" send-keys -t bar:0 Enter; sleep 1
  if tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -q '"filePath": "/tmp/x"'; then
    echo "FAIL: Enter did not close the value again"; fail=1
  else
    echo "ok   Enter closes the value again"
  fi
else
  echo "FAIL: never reached the blob key with ^N"; fail=1
fi

# 11c. NO ARROW ON A BRANCH: the indent says it, and so does the count.
if tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -q "▸"; then
  echo "FAIL: a branch row still carries the ▸ marker"; fail=1
else
  echo "ok   branches carry no arrow"
fi

# 11c2. S IS THE FORM, T IS THE FOCUS, AND FULLSCREEN IS THE PAGER'S.
tmux -S "$SOCK" send-keys -t bar:0 Escape; sleep 0.6
tmux -S "$SOCK" send-keys -t bar:0 S; sleep 2.5
if tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -q "웃"; then
  echo "ok   S opens the form pit"
else
  echo "FAIL: S did not open the form pit"; fail=1
fi
tmux -S "$SOCK" send-keys -t bar:0 F; sleep 1
fullrows=$(pitheight)
tmux -S "$SOCK" send-keys -t bar:0 T; sleep 1     # the conversation takes the screen
halfrows=$(pitheight)
if (( halfrows < fullrows )); then
  echo "ok   T made the fullscreen pit recede ($fullrows → $halfrows rows)"
else
  echo "FAIL: T did not shrink the fullscreen pit ($fullrows → $halfrows)"; fail=1
fi
if tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -q "웃"; then
  echo "ok   and the pit is still open behind the conversation"
else
  echo "FAIL: T closed the pit instead of receding it"; fail=1
fi
tmux -S "$SOCK" send-keys -t bar:0 T; sleep 1     # and back
if (( $(pitheight) > halfrows )); then
  echo "ok   T again gives the pit the screen back"
else
  echo "FAIL: T did not restore the fullscreen pit"; fail=1
fi
# The pager's disposition SURVIVES the pit: close it, open another, still full.
tmux -S "$SOCK" send-keys -t bar:0 Escape; sleep 0.6
tmux -S "$SOCK" send-keys -t bar:0 '?'; sleep 1
if (( $(pitheight) > halfrows )); then
  echo "ok   fullscreen is the pager's, not the pit's (help opened full)"
else
  echo "FAIL: the next pit forgot fullscreen"; fail=1
fi
tmux -S "$SOCK" send-keys -t bar:0 F; sleep 0.8   # back to the ordinary size
tmux -S "$SOCK" send-keys -t bar:0 Escape; sleep 0.6

# 11d. Esc CLOSES THE PIT AND SHOWS THE TRANSCRIPT, and so does 'q' -- they are
#      one gesture, and q leaves the session only when there is no pit to close.
tmux -S "$SOCK" send-keys -t bar:0 Escape; sleep 1
if tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -q "look around"; then
  echo "ok   Esc closes the form pit and shows the transcript"
else
  echo "FAIL: Esc did not put the transcript back"; fail=1
fi
tmux -S "$SOCK" send-keys -t bar:0 ':'; sleep 0.4
tmux -S "$SOCK" send-keys -t bar:0 -l "form show"; sleep 0.4
tmux -S "$SOCK" send-keys -t bar:0 Enter; sleep 2
tmux -S "$SOCK" send-keys -t bar:0 q; sleep 1
after=$(tmux -S "$SOCK" capture-pane -p -t bar:0)
if [[ "$after" == *"웃"* ]]; then
  echo "FAIL: q did not close the form pit"; fail=1
elif [[ "$after" == *"look around"* ]]; then
  echo "ok   q closed the pit and left the transcript up"
else
  echo "FAIL: q left the session instead of closing the pit"; fail=1
  echo "$after" | tail -6 | sed 's/^/    |/'
fi
tmux -S "$SOCK" send-keys -t bar:0 q; sleep 1.5
if tmux -S "$SOCK" capture-pane -p -t bar:0 | grep -q '^bash-.*\$'; then
  echo "ok   a second q, with no pit open, leaves the session"
else
  echo "FAIL: q with no pit open did not leave"; fail=1
  tmux -S "$SOCK" capture-pane -p -t bar:0 | tail -6 | sed 's/^/    |/'
fi

exit $fail
