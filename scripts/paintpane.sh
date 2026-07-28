#!/usr/bin/env bash
# paintpane.sh — a sourceable harness for driving figaro's transcript pager in a
# REAL pty, for hunting paint bugs (resize duplication, gap contamination,
# status-bar bleed).
#
# Read PAINT-REPRO.md before using this. Every function here exists because a
# trap in the tmux-testing skill cost somebody a wrong answer; the trap number
# is cited at each one.
#
# Usage:
#   source scripts/paintpane.sh
#   pp_init basilio            # stamped build + scratch store, seeded
#   pp_up 100 40               # a pane of EXACTLY 40 usable rows
#   pp_pager 8566c903          # figaro listen <aria> + ^T, wait for chrome
#   pp_key C-n; pp_stable
#   pp_resize 100 24
#   pp_cap  > /tmp/a.txt       # visible pane, ANSI stripped
#   pp_hist > /tmp/b.txt       # FULL SCROLLBACK (trap #4)
#   pp_down                    # tmux server AND scratch daemon (trap #10)
#
# pp_init installs an EXIT trap, so an aborted script still cleans up.

set -o pipefail

# --------------------------------------------------------------------------
# Naming contract, agreed with BERTA the watchdog. Do not invent your own.
#   tmux socket : /tmp/paint-<hunter>/tmux.sock   (PRIVATE server, never the
#                 default socket — so kill-server can never touch the user's
#                 sessions 0/dev/figaro-qua/fx/gw4/iq/iq2)
#   session     : paint-<hunter>-<tag>
#   scratch store: /var/tmp/paint-<hunter>/{state,run,config}
# A sweeper attributes daemons by env:
#   FIGARO_RUNTIME_DIR=/var/tmp/paint-*
# --------------------------------------------------------------------------

PP_HUNTER=""
PP_DIR=""
PP_SOCK=""
PP_SESS=""
PP_STORE=""
PP_BIN=""
PP_H=0
PP_W=0
PP_REPO=""

pp_die() { echo "paintpane: $*" >&2; return 1; }

# pp_init <hunter> — build a STAMPED binary and prepare an isolated store.
#
# Trap #2: a plain `go build` in a git WORKTREE records no VCS revision at all
# (a worktree's .git is a file, not a directory, so Go's autodetection never
# fires — and -buildvcs=true neither helps nor complains). An unstamped binary
# makes `figaro --version` say "unknown" and leaves the CLI/daemon build
# handshake with nothing to compare. Always stamp.
pp_init() {
  PP_HUNTER="${1:?pp_init <hunter>}"
  PP_REPO="$(git rev-parse --show-toplevel)" || return 1
  PP_DIR="/tmp/paint-$PP_HUNTER"
  PP_SOCK="$PP_DIR/tmux.sock"
  PP_STORE="/var/tmp/paint-$PP_HUNTER"
  PP_BIN="$PP_DIR/figaro"
  mkdir -p "$PP_DIR" "$PP_STORE"/{state,run,config} || return 1

  ( cd "$PP_REPO" && go build \
      -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse --short=12 HEAD)" \
      -o "$PP_BIN" ./cmd/figaro ) || return 1

  # Prove which binary we will drive. Trap #11: two arms that produce identical
  # output are more often ONE BINARY than one bug. Print identity, always.
  echo "paintpane: $("$PP_BIN" --version | head -1)"
  echo "paintpane: md5 $(md5sum "$PP_BIN" | cut -c1-12)  path $PP_BIN"

  trap pp_down EXIT
}

# pp_seed — copy the REAL aria store into the scratch store, read-only on the
# source. This is how the pager gets hundreds of messages of real content for
# ZERO tokens and without a provider round-trip: `figaro listen <id>` + ^T
# renders history and never calls figaro.qua.
#
# The real store is only ever READ. The scratch daemon writes to the copy.
pp_seed() {
  local src="${FIGARO_REAL_STATE:-$HOME/.local/state/figaro}"
  [ -d "$src/arias" ] || pp_die "no aria store at $src/arias" || return 1
  [ -d "$PP_STORE/state/arias" ] || cp -r "$src/arias" "$PP_STORE/state/arias" || return 1
  [ -e "$PP_STORE/config/config.toml" ] || cp -r "$HOME/.config/figaro/." "$PP_STORE/config/" 2>/dev/null
  echo "paintpane: seeded $(du -sh "$PP_STORE/state" | cut -f1) into $PP_STORE/state"
}

# pp_env — the env every command in the pane must carry.
#
# FIGARO_ARIA / FIGARO_NO_BIND are SCRUBBED deliberately. An aria's bash tool
# exports FIGARO_ARIA=<its own id>, which is an IDENTITY: inherited into the
# pane it makes every `figaro list` scope to the hunting aria and every
# `figaro send` talk to itself. Measured: `figaro list -a` in a seeded 305-aria
# store returned exactly ONE row until these were unset.
pp_env() {
  printf '%s\n' \
    "FIGARO_STATE_DIR=$PP_STORE/state" \
    "FIGARO_RUNTIME_DIR=$PP_STORE/run" \
    "FIGARO_CONFIG_DIR=$PP_STORE/config"
}

pp_envargs() { local v; while read -r v; do printf ' %q' "$v"; done < <(pp_env); }

# pp_run <args...> — run the scratch binary against the scratch store from HERE
# (not inside the pane): absolute path, explicit env, no PATH involved.
pp_run() {
  env -u FIGARO_ARIA -u FIGARO_NO_BIND \
    "FIGARO_STATE_DIR=$PP_STORE/state" \
    "FIGARO_RUNTIME_DIR=$PP_STORE/run" \
    "FIGARO_CONFIG_DIR=$PP_STORE/config" \
    "$PP_BIN" "$@"
}

# pp_tmux — talk to OUR private server only.
pp_tmux() { tmux -S "$PP_SOCK" "$@"; }

# pp_up <w> <h> [tag] — a pane of EXACTLY h usable rows.
#
# Trap #1: `tmux new-session -y N` gives pane_height N-1, because the status bar
# takes a row — and turning the status bar off afterwards does NOT give the row
# back to a detached session, nor does resize-window. Measured: -y 30 -> 29
# either way; -y 31 -> 30. So ask for h+1 and then READ BACK #{pane_height},
# and report THAT number. An entire thread of "h=1 loses the reply" — three
# investigators, many trials — was measured at pane height ZERO, a state no
# user can reach.
pp_up() {
  PP_W="${1:?pp_up <w> <h>}"; local want="${2:?pp_up <w> <h>}"; local tag="${3:-p}"
  PP_SESS="paint-$PP_HUNTER-$tag"
  TMUX= pp_tmux new-session -d -s "$PP_SESS" -x "$PP_W" -y "$((want + 1))" bash --norc || return 1
  pp_tmux set -g status off
  pp_tmux set -g history-limit 10000
  PP_H="$(pp_tmux display -p -t "$PP_SESS" '#{pane_height}' | tr -d '[:space:]')"
  if [ "$PP_H" != "$want" ]; then
    echo "paintpane: pane height is $PP_H (asked $want) — ASSERT AGAINST $PP_H" >&2
  fi
  # Scrub the inherited aria identity inside the shell. Trap #11: `new-session
  # -e PATH=...` is silently IGNORED, so we never trust -e for anything that
  # matters and we never rely on PATH inside a pane — figaro is always invoked
  # by ABSOLUTE PATH.
  pp_tmux send-keys -t "$PP_SESS" 'unset FIGARO_ARIA FIGARO_NO_BIND; export'"$(pp_envargs)"'; PS1="pp$ "' Enter
  pp_tmux send-keys -t "$PP_SESS" 'clear' Enter
  echo "paintpane: session $PP_SESS  ${PP_W}x${PP_H} (usable rows: $PP_H)"
}

pp_send()  { pp_tmux send-keys -t "$PP_SESS" -l "$1"; }
pp_key()   { pp_tmux send-keys -t "$PP_SESS" "$@"; }

# pp_type <s> — one character per read, with a gap.
#
# Trap #5: `send-keys -l "whole string"` arrives as a SINGLE read; a human types
# one byte at a time. Composer tests fed whole strings and passed while a
# byte-vs-rune bug mojibaked every non-ASCII character. If the thing under test
# is INPUT, type slowly.
pp_type() { local s="$1" c; for ((i = 0; i < ${#s}; i++)); do c="${s:i:1}"; pp_send "$c"; sleep 0.12; done; }

# pp_raw  — visible pane WITH escapes (-e), for asserting on SGR/styling.
# pp_cap  — visible pane, escapes stripped by tmux itself.
# pp_hist — FULL SCROLLBACK.
#
# Trap #4: capture the scrollback, not the pane. Frames that existed for
# MILLISECONDS are preserved verbatim in history — that is how the duplicated
# submit-time footer was photographed after fast polling failed to catch it.
# The right instrument was a SHORT PANE, not a faster loop.
pp_cap()  { pp_tmux capture-pane -p -t "$PP_SESS"; }
pp_raw()  { pp_tmux capture-pane -p -e -t "$PP_SESS"; }
pp_hist() { pp_tmux capture-pane -p -S - -t "$PP_SESS"; }

# pp_stable [timeout_s] [needed] — poll until the pane stops changing.
#
# Trap #7: never sleep a fixed guess. Returns 0 once the pane has been
# byte-identical for `needed` consecutive samples, 1 on timeout.
pp_stable() {
  local budget="${1:-20}" needed="${2:-3}" last="" cur stable=0 deadline
  deadline=$(( $(date +%s) + budget ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    sleep 0.35
    cur="$(pp_cap)"
    if [ "$cur" = "$last" ]; then
      stable=$((stable + 1)); [ "$stable" -ge "$needed" ] && return 0
    else stable=0; fi
    last="$cur"
  done
  return 1
}

# pp_chrome [capture] — count transcript-pager chrome markers.
#
# Trap #3: GATE EVERY ABSENCE ON THIS. A long turn auto-promotes to the pager,
# where earlier content sits above the tail window and is therefore not in your
# capture. An absence inside a pager is not an absence. Assert chrome>0 before
# believing you are in the pager, and chrome==0 before believing any absence
# measured outside it.
pp_chrome() {
  local cap="${1:-$(pp_cap)}"
  awk 'BEGIN{n=0} /\? help|! status/{n++} END{print n}' <<<"$cap"
}

# pp_pager <aria-id> [budget] — open the pager on an existing aria.
#
# `figaro listen` attaches WITHOUT calling figaro.qua: no prompt, no provider,
# no tokens. ^T promotes to the transcript pager. Waits until chrome appears.
pp_pager() {
  local id="${1:?pp_pager <aria-id>}" budget="${2:-40}" deadline
  pp_send "$PP_BIN listen $id"; pp_key Enter
  pp_stable 15 2 >/dev/null
  pp_key C-t
  deadline=$(( $(date +%s) + budget ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    sleep 0.3
    [ "$(pp_chrome)" -gt 0 ] && { pp_stable 10 2 >/dev/null; echo "paintpane: pager up on $id"; return 0; }
  done
  pp_die "pager never came up on $id (chrome=0); capture follows"
  pp_cap >&2
  return 1
}

# pp_resize <w> <h> — resize the WINDOW (which is what a user's terminal does).
# Same +1 correction as pp_up, then read back.
pp_resize() {
  local w="${1:?pp_resize <w> <h>}" want="${2:?pp_resize <w> <h>}"
  pp_tmux resize-window -t "$PP_SESS" -x "$w" -y "$((want + 1))" 2>/dev/null \
    || pp_tmux resize-window -t "$PP_SESS" -x "$w" -y "$want"
  PP_W="$w"
  PP_H="$(pp_tmux display -p -t "$PP_SESS" '#{pane_height}' | tr -d '[:space:]')"
  echo "paintpane: resized to ${PP_W}x${PP_H}"
}

# pp_leave — exit the pager and the CLI politely, so the daemon is not orphaned.
pp_leave() { pp_key q; sleep 0.4; pp_key C-d; sleep 0.6; }

# pp_down — tear down BOTH halves.
#
# Trap #10: `tmux kill-server` leaves the scratch daemon RUNNING. On one night
# seventeen agents each left one behind: 230 orphaned processes, 1.2 GB of
# tmpfs, and a memory-pressure alert with processes already stalling. Every one
# of them had been told to stop the daemon BEFORE testing and never after.
pp_down() {
  trap - EXIT
  if [ -n "$PP_BIN" ] && [ -x "$PP_BIN" ]; then
    pp_run stop --force >/dev/null 2>&1
  fi
  [ -n "$PP_SOCK" ] && [ -S "$PP_SOCK" ] && pp_tmux kill-server >/dev/null 2>&1
  # Belt and braces: anything still holding our scratch runtime dir.
  local p
  for p in $(pgrep -x figaro 2>/dev/null); do
    if tr '\0' '\n' < "/proc/$p/environ" 2>/dev/null | grep -q "^FIGARO_RUNTIME_DIR=$PP_STORE/run$"; then
      kill "$p" 2>/dev/null
    fi
  done
  echo "paintpane: torn down ($PP_SESS)"
}

# --------------------------------------------------------------------------
# Assertion helpers that are honest.
# --------------------------------------------------------------------------

# pp_dupruns <file> — report REPEATED SEQUENCES of >=n non-blank rows.
#
# Trap #8: a naive adjacent-duplicate check MISSES REAL DUPLICATION. The
# body-duplication bug placed its two copies ~25 lines apart, separated by
# re-rendered thinking and tool output. Compare SEQUENCES, not neighbours.
pp_dupruns() {
  local f="${1:?pp_dupruns <file>}" n="${2:-3}"
  awk -v n="$n" '
    { gsub(/[ \t]+$/, ""); rows[NR] = $0 }
    END {
      for (i = 1; i + n - 1 <= NR; i++) {
        blank = 0
        for (k = 0; k < n; k++) if (rows[i+k] == "") blank = 1
        if (blank) continue
        key = ""
        for (k = 0; k < n; k++) key = key "\x01" rows[i+k]
        if (key in seen) printf "DUPRUN len=%d rows %d and %d: %s\n", n, seen[key], i, rows[i]
        else seen[key] = i
      }
    }' "$f"
}

# pp_overwide <file> [w] — rows wider than the viewport. A row that exceeds the
# width would wrap and desync the painter; invariant #1 says every row passes
# through clipToWidth (one physical line per row).
#
# CAVEAT from the using-tuis skill: a naive width counter miscounts ZWJ
# sequences and flag emoji. This counts display cells via python+wcwidth if
# available, else runes. Cross-check any flag against go-runewidth before
# believing it.
pp_overwide() {
  local f="${1:?pp_overwide <file>}" w="${2:-$PP_W}"
  awk -v w="$w" '{ n = 0; for (i = 1; i <= length($0); i++) n++; if (n > w) printf "OVERWIDE row %d: %d cols > %d\n", NR, n, w }' "$f"
}
