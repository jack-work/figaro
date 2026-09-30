#!/usr/bin/env bash
# Tab's pit in the ':' box, in a real pty.
#
# The menu this replaced was broken in a way no unit test could see: `__complete`
# printed its candidates to the process's stdout, so inside the pager they were
# painted straight over the status bar and the menu itself received nothing.
# Its successor's first cut froze the pager on the first Tab (the completer
# took a lock its caller held). Both were found by looking at a terminal, which
# is what this does.
#
#   BIN=/path/to/figaro bash scripts/tabpit-pty.sh     # default: builds one
#
# Hermetic: its own config, state and runtime dirs, its own tmux socket, no
# provider credentials (nothing here asks a model anything). Cleans up after
# itself, daemon included.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BOX=$(mktemp -d /tmp/tabpit-pty.XXXXXX)
SOCK=$BOX/tmux.sock
W=100 H=30
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
mkdir -p "$BOX"/{cfg/outfits,state,rt,cache} "$BOX/tree/alpha" "$BOX/tree/alpine" "$BOX/tree/beta"
printf 'You are a test agent.\n' > "$BOX/cfg/credo.md"
cat > "$BOX/cfg/outfits/t.toml" <<'EOF'
[system]
provider = "anthropic"
model    = "claude-test"
credo    = { fileName = "credo.md" }
EOF
printf 'default_outfit = "t"\ninteractive = true\n' > "$BOX/cfg/config.toml"
export FIGARO_CONFIG_DIR=$BOX/cfg FIGARO_STATE_DIR=$BOX/state FIGARO_RUNTIME_DIR=$BOX/rt \
       FIGARO_CACHE_DIR=$BOX/cache FIGARO_NO_BIND=1

ids=()
for _ in 1 2 3; do
  ids+=("$("$BIN" new -j 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin)["aria_id"])')")
done
[[ ${#ids[@]} -eq 3 && -n "${ids[0]}" ]] || { echo "FIXTURE: could not mint arias"; exit 1; }

t() { tmux -S "$SOCK" "$@"; }
shot() { t capture-pane -p -t p:0; }
raw() { t capture-pane -p -e -t p:0; }
key() { for k in "$@"; do t send-keys -t p:0 "$k"; sleep 0.25; done; }
typ() { local s=$1 i; for ((i = 0; i < ${#s}; i++)); do t send-keys -t p:0 -l "${s:i:1}"; sleep 0.06; done; }
settle() { sleep "${1:-1.2}"; }
check() { # check <name> <condition exit code> [detail]
  if [[ $2 -eq 0 ]]; then pass=$((pass + 1)); echo "ok   $1"
  else fail=$((fail + 1)); echo "FAIL $1${3:+: $3}"; shot | sed 's/^/     | /' | tail -14; fi
}
reset_box() { key Escape Escape; settle 0.3; key ':'; }

t new-session -d -s p -x "$W" -y "$((H + 1))" "bash --norc"
t set -g status off
t send-keys -t p:0 -l "cd $BOX/tree; PS1='$ '; $BIN listen ${ids[0]}"
t send-keys -t p:0 Enter
settle 4

# 1. The pit opens, one candidate per row, and nothing lands on the bar.
reset_box; typ "attend "; settle 0.5
bar_before=$(shot | tail -1) # with the box open: ':' puts its glyph there
key Tab; settle
rows=$(shot | grep -cE "^  [0-9a-f]{8}")
check "Tab opens the pit with every aria in it" $(( rows >= 3 ? 0 : 1 )) "saw $rows id rows"
check "the status bar is untouched" $([[ "$(shot | tail -1)" == "$bar_before" ]]; echo $?) "bar is now: $(shot | tail -1)"
check "nothing chosen yet" $(shot | grep -q "♪"; [[ $? -ne 0 ]]; echo $?)

# 2. ^N chooses, puts it in the line, and marks it.
key C-n; settle 0.6
line=$(shot | grep -E "^:attend" | tail -1)
check "^N puts the choice in the line" $([[ "$line" =~ ^:attend\ [0-9a-f]{8}$ ]]; echo $?) "line: $line"
check "the chosen row carries the pit's marker" $(shot | grep -qE "^♪ [0-9a-f]{8}"; echo $?)
check "the chosen row is drawn selected" $(raw | grep "♪" | grep -q $'\x1b\[48;5;240m'; echo $?)

# 3. Esc puts the typed word back and keeps the box.
key Escape; settle 0.5
check "Esc restores the typed word" $([[ "$(shot | grep -E '^:attend' | tail -1)" == ":attend" ]]; echo $?)

# 4. Typing narrows, from the id's own letters.
key Tab; typ "${ids[1]:0:3}"; settle 0.8
rows=$(shot | grep -cE "^  [0-9a-f]{8}")
check "typing narrows the pit" $(( rows >= 1 && rows < 3 ? 0 : 1 )) "saw $rows rows"

# 5. Flags, from the command's own declaration.
reset_box; typ "status --"; key Tab; settle
check "a dash word offers flags, described" $(shot | grep -qE "^  --json +Emit"; echo $?)

# 6. Paths: the common prefix goes in, the directories are offered.
reset_box; typ "cd al"; key Tab; settle
check "paths: the shared prefix is inserted" $([[ "$(shot | grep -E '^:cd' | tail -1)" == ":cd alp" ]]; echo $?) "line: $(shot | grep -E '^:cd' | tail -1)"
check "paths: both directories are offered" $(shot | grep -qE "^  alpha/ +dir" && shot | grep -qE "^  alpine/ +dir"; echo $?)

# 7. The pager is alive after all of it: a completer that blocks freezes it.
reset_box; typ "at 1"
settle 0.5
check "the pager still takes keys" $(shot | grep -qE "^:at 1$"; echo $?)

echo
echo "tabpit-pty: $pass ok, $fail failed"
[[ $fail -eq 0 ]]
