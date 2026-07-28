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
  # RESTRICTED BEFORE ANYTHING LANDS. -m 700 on creation, then chmod anyway:
  # /var/tmp is 1777 and world-traversable, umask on this box yields 755, and a
  # dir that is briefly 755 is a dir that was briefly public. See pp_seed.
  mkdir -p -m 700 "$PP_DIR" "$PP_STORE" || return 1
  chmod 700 "$PP_DIR" "$PP_STORE" || return 1
  mkdir -p -m 700 "$PP_STORE"/state "$PP_STORE"/run "$PP_STORE"/config || return 1
  chmod 700 "$PP_STORE"/state "$PP_STORE"/run "$PP_STORE"/config || return 1

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
#
# ------------------------------------------------------------------------
# SECURITY, and this was a real incident. See pp_env for the full story.
# In one sentence: NEVER RELY ON A PARENT DIRECTORY TO PROTECT A FILE YOU ARE
# ABOUT TO MOVE. Credentials are no longer copied at all (config is shared by
# reference), content is now synthetic (pp_fixture), and pp_down deletes what
# little remains.
# ------------------------------------------------------------------------
# pp_seed — DEPRECATED AND DISARMED. It copied the master's real aria store
# (119 MB of his conversations) and his whole config (credentials) into a
# world-traversable /var/tmp. Both are now refused. Use pp_fixture for content
# and pp_config_copy if you must isolate config.
#
# Kept as a loud failure rather than deleted, because three hunters were told to
# call it and a silent no-op would leave them driving an empty store and blaming
# the pager.
pp_seed() {
  if [ -n "$PP_SEED_REAL_STORE_I_ACCEPT_THE_PRIVACY_COST" ]; then
    local src="${FIGARO_REAL_STATE:-$HOME/.local/state/figaro}"
    mkdir -p -m 700 "$PP_STORE/state"; chmod 700 "$PP_STORE" "$PP_STORE/state"
    [ -d "$PP_STORE/state/arias" ] || cp -r "$src/arias" "$PP_STORE/state/arias" || return 1
    chmod -R go-rwx "$PP_STORE/state"
    echo "paintpane: WARNING seeded the REAL store at $PP_STORE/state (mode 700, deleted by pp_down)"
    return 0
  fi
  cat >&2 <<'EOF'
paintpane: pp_seed is DISABLED.

  It used to copy ~/.config/figaro (PROVIDER CREDENTIALS) and 119 MB of the
  master's real aria store (HIS CONVERSATION HISTORY) into /var/tmp, which is
  world-traversable AND survives reboot. providers/anthropic.toml is mode 644
  and was protected only by its 700 parent; the copy moved it out from under
  the only thing defending it.

  Use instead:
    pp_fixture 400        deterministic synthetic content (one cheap turn),
                          N numbered rows — contamination is self-evident
    pp_config_copy        isolate config WITHOUT providers/ or hush/
    (config is otherwise SHARED BY REFERENCE — see pp_env)

  If you truly need real history, set
  PP_SEED_REAL_STORE_I_ACCEPT_THE_PRIVACY_COST=1 and say so in your write-up.
EOF
  return 1
}

# pp_env — the env every command in the pane must carry.
#
# FIGARO_CONFIG_DIR IS SHARED BY REFERENCE, NOT COPIED. This is the documented
# preset shape (see the figaro skill: isolate runtime+state, share config) and it
# is here for a security reason, not a tidiness one.
#
# THE INCIDENT. pp_seed used to `cp -r ~/.config/figaro` into /var/tmp. `cp`
# preserved modes faithfully — that was never the problem. The problem is that
# providers/anthropic.toml is itself mode 644 and was safe in the real config
# ONLY BECAUSE ITS PARENT IS 700. Copying it out from under that parent put the
# master's Anthropic credential world-readable inside /var/tmp, which is 1777 AND
# SURVIVES REBOOT. Four copies, for hunters who are painting frames and do not
# need his credentials at all.
#
# NEVER RELY ON A PARENT DIRECTORY TO PROTECT A FILE YOU ARE ABOUT TO MOVE.
#
# A reference cannot be left behind with the wrong mode, so sharing is not merely
# cheaper — it removes the failure mode. If you genuinely must isolate config,
# use pp_config_copy, which EXCLUDES providers/ entirely.
#
# Separately: FIGARO_ARIA / FIGARO_NO_BIND are SCRUBBED wherever this env is
# used (pp_run, pp_up). An aria's bash tool exports FIGARO_ARIA=<its own id>,
# which is an IDENTITY: inherited into the pane it makes every `figaro list`
# scope to the hunting aria and every `figaro send` talk to itself. Measured:
# `figaro list -a` returned exactly ONE row out of 305 until these were unset.
pp_env() {
  printf '%s\n' \
    "FIGARO_STATE_DIR=$PP_STORE/state" \
    "FIGARO_RUNTIME_DIR=$PP_STORE/run" \
    "FIGARO_CONFIG_DIR=${PP_CONFIG:-$HOME/.config/figaro}"
}

# pp_config_copy — isolate config WITHOUT duplicating credentials.
#
# Copies loadouts/credo/skills and deliberately omits providers/ and hush/, then
# refuses to continue if anything group/world-readable survived. Auth then has to
# come from the environment (e.g. ANTHROPIC_API_KEY) or the dev-hush path the
# figaro skill documents — which is the correct posture for a throwaway store.
#
# DEREFERENCE. `cp -r` — and `tar` without -h, which is what this used to do —
# copies a SYMLINK AS A SYMLINK, so a "isolated" config silently reaches back
# into the original. SUSANNA measured it: her scratch config's skills/plaid and
# skills/pishot.md were links into the master's LIVE ~/dev trees, so an arm that
# believed it was reading an isolated skill was reading the live file, and a test
# that believed it was hermetic was not. Verified here: ~/.config/figaro/skills
# contains exactly those two links today. So: -h (tar) / -L (cp), and then ASSERT
# no symlink survived, because a hermeticity claim is worth nothing unchecked.
#
# Same family as the credential rule below: A COPY THAT SILENTLY REACHES BACK
# INTO THE ORIGINAL.
pp_config_copy() {
  local dst="$PP_STORE/config"
  mkdir -p -m 700 "$dst" || return 1
  chmod 700 "$dst"
  ( cd "$HOME/.config/figaro" 2>/dev/null && tar cfh - \
      --exclude=providers --exclude=hush --exclude='*.age' --exclude='*.key' . ) \
    | ( cd "$dst" && tar xf - ) || return 1
  chmod -R go-rwx "$dst"
  local leaky links
  leaky="$(find "$dst" -type f \( -perm -g+r -o -perm -o+r \) 2>/dev/null | wc -l)"
  [ "$leaky" = 0 ] || { pp_die "REFUSING: $leaky group/world-readable file(s) under $dst"; return 1; }
  links="$(find "$dst" -type l 2>/dev/null | wc -l)"
  [ "$links" = 0 ] || { pp_die "REFUSING: $links symlink(s) under $dst — NOT hermetic, they reach back into the original"; return 1; }
  if [ -e "$dst/providers" ] || [ -e "$dst/hush" ]; then
    pp_die "REFUSING: providers/ or hush/ leaked into $dst"; return 1
  fi
  PP_CONFIG="$dst"
  echo "paintpane: isolated config at $dst (no providers/, no hush/, no symlinks)"
}

# pp_fixture [rows] [aria-out] — build a SYNTHETIC pager fixture.
#
# REPLACES the old pp_seed, which copied 119 MB of the master's real aria store —
# his actual conversation history — into /var/tmp, four times. The painters need
# ENOUGH CONTENT TO FILL A PAGER, not his history.
#
# This makes its own content instead, and the content is BETTER than real history
# for the job: one tool call emitting `seq 1 N`, so the pager holds N strictly
# increasing numbered rows. Contamination is then self-evident — a row reading
# 247 where 312 belongs is a bug you can see without an oracle, and a gap row
# holding text at all is a bug, because every legitimate body row is a number.
#
# Cost: ONE cheap turn. The N rows are TOOL OUTPUT, not model output, so the
# model emits only a call and a word. No privacy exposure, and deterministic.
pp_fixture() {
  local rows="${1:-400}" out
  out="$(pp_run new -j -- "Call the bash tool exactly once with the command: seq 1 $rows
Then reply with the single word DONE and nothing else." 2>&1)" || {
    pp_die "fixture turn failed: $out"; return 1
  }
  PP_ARIA="$(printf '%s' "$out" | grep -o '"aria_id":"[^"]*"' | head -1 | cut -d'"' -f4)"
  [ -n "$PP_ARIA" ] || { pp_die "could not read aria id from: $out"; return 1; }
  # Human line to STDERR so a caller can capture the id cleanly on stdout.
  echo "paintpane: fixture aria $PP_ARIA (~$rows numbered rows)" >&2
  printf '%s\n' "$PP_ARIA"
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

# pp_alive — is OUR tmux server actually running?
#
# NOT `[ -S "$PP_SOCK" ]`. Found by BERTA the watchdog: `kill-server` leaves the
# socket INODE behind, so a dead server still has a live-looking socket file and
# a file test reports it as up. That is the trap #10 family — the artifact
# outlives the process. Ask tmux, not the filesystem.
pp_alive() { tmux -S "$PP_SOCK" has-session -t "$PP_SESS" 2>/dev/null; }

# pp_server_alive — is any session left on our server?
pp_server_alive() { tmux -S "$PP_SOCK" list-sessions >/dev/null 2>&1; }

# pp_tmux_servers — every tmux server belonging to a paint hunter.
#
# DO NOT USE `pgrep -x tmux`. It matches NOTHING, ever: tmux rewrites its
# process title, so /proc/<pid>/comm is literally "tmux: server" or
# "tmux: client" and never "tmux". BERTA measured it against a machine with FOUR
# live servers — including two of ours — and got ZERO hits. A teardown check
# written `pgrep -x tmux || echo clean` therefore reports CLEAN over a field of
# orphans, which is the exact shape of the incident that produced trap #10's 230
# orphaned processes.
#
# `pgrep tmux` (no -x) does work, because pgrep substring-matches comm. Only the
# exact-match flag is fatal. We do not rely on either: we require comm to START
# with "tmux" (so a shell whose command line merely mentions tmux — like the one
# running this function — cannot match) and then match on the socket path.
pp_tmux_servers() {
  local p pid comm args
  for p in /proc/[0-9]*; do
    pid="${p#/proc/}"
    comm="$(cat "$p/comm" 2>/dev/null)" || continue
    case "$comm" in tmux*) ;; *) continue ;; esac
    args="$(tr '\0' ' ' < "$p/cmdline" 2>/dev/null)"
    case "$args" in *"/tmp/paint-"*) printf '%s\t%s\t%s\n' "$pid" "$comm" "$args" ;; esac
  done
}

# pp_figaro_daemons — every figaro process on a paint hunter's scratch store.
#
# NO NAME MATCHING. I first wrote this as `[ "$comm" = figaro ]` and it was blind
# for exactly the reason `pgrep -x tmux` is blind, one function above — I
# committed BERTA's trap while fixing BERTA's trap. `comm` is the binary
# BASENAME, and an A/B arm is called `figaro-after`, `figaro-probe`,
# `figaro-after2`… Measured: BASILIO had a live daemon named `figaro-after` on
# /var/tmp/paint-basilio/run and the equality test reported ZERO daemons.
#
# So the environment is the only sound discriminator: FIGARO_RUNTIME_DIR cannot
# be renamed and is what actually decides which store a process owns. Scan every
# readable environ and match on that, with no assumption about the name at all.
pp_figaro_daemons() {
  local p pid rd
  for p in /proc/[0-9]*; do
    pid="${p#/proc/}"
    # -r FIRST. `tr ... < "$p/environ" 2>/dev/null` does NOT silence this: the
    # redirection is performed by the SHELL and fails before tr ever runs, so the
    # error comes from bash and 2>/dev/null on tr cannot catch it. Measured: 300+
    # "Permission denied" lines for other users' processes, which is worse than
    # useless — a diagnostic that floods is a diagnostic people learn to ignore,
    # and this one exists to be believed.
    [ -r "$p/environ" ] || continue
    rd="$( { tr '\0' '\n' < "$p/environ" | grep -m1 '^FIGARO_RUNTIME_DIR=/var/tmp/paint-'; } 2>/dev/null )" || continue
    [ -n "$rd" ] || continue
    printf '%s\t%s\t%s\n' "$pid" "$(cat "$p/comm" 2>/dev/null)" "${rd#FIGARO_RUNTIME_DIR=}"
  done
}

# pp_stale_pidfiles — angelus.pid files whose process is gone.
#
# Another artifact-outlives-the-process case: measured stale angelus.pid for two
# hunters whose daemons had already exited. Never treat a pid FILE as liveness.
pp_stale_pidfiles() {
  local f pid
  for f in /var/tmp/paint-*/run/angelus.pid; do
    [ -f "$f" ] || continue
    pid="$(cat "$f" 2>/dev/null)"
    [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null || printf '%s\t%s\n' "$f" "${pid:-empty}"
  done
}

# pp_verify_clean — assert teardown actually happened. Exit 0 = clean.
#
# Call this AFTER pp_down, and believe it rather than your intentions. I told the
# watchdog I had removed a socket that I had in fact never removed; a claim is
# not a measurement.
pp_verify_clean() {
  local bad=0 line
  while IFS= read -r line; do [ -n "$line" ] && { echo "LEAK tmux server: $line"; bad=1; }; done < <(pp_tmux_servers)
  while IFS= read -r line; do [ -n "$line" ] && { echo "LEAK figaro daemon: $line"; bad=1; }; done < <(pp_figaro_daemons)
  while IFS= read -r line; do [ -n "$line" ] && echo "note: stale pidfile $line"; done < <(pp_stale_pidfiles)
  for line in /tmp/paint-*/tmux.sock; do
    [ -e "$line" ] || continue
    tmux -S "$line" list-sessions >/dev/null 2>&1 \
      && { echo "LEAK live server on $line"; bad=1; } \
      || echo "note: dead socket inode $line (harmless; rm -f it)"
  done
  [ "$bad" -eq 0 ] && echo "pp_verify_clean: clean"
  return "$bad"
}

# pp_down — tear down BOTH halves.
#
# Trap #10: `tmux kill-server` leaves the scratch daemon RUNNING. On one night
# seventeen agents each left one behind: 230 orphaned processes, 1.2 GB of
# tmpfs, and a memory-pressure alert with processes already stalling. Every one
# of them had been told to stop the daemon BEFORE testing and never after.
#
# Three halves, really: the daemon, the server, and the socket inode.
pp_down() {
  trap - EXIT
  if [ -n "$PP_BIN" ] && [ -x "$PP_BIN" ]; then
    pp_run stop --force >/dev/null 2>&1
  fi
  if [ -n "$PP_SOCK" ]; then
    tmux -S "$PP_SOCK" kill-server >/dev/null 2>&1
    rm -f "$PP_SOCK"   # else the next run's liveness probe sees a ghost
  fi
  # Belt and braces: anything still holding our scratch runtime dir.
  local p
  for p in $(pgrep -f 'figaro' 2>/dev/null); do
    [ -r "/proc/$p/environ" ] || continue
    if { tr '\0' '\n' < "/proc/$p/environ" | grep -q "^FIGARO_RUNTIME_DIR=$PP_STORE/run$"; } 2>/dev/null; then
      kill "$p" 2>/dev/null
    fi
  done
  # THE CREDENTIAL COPY MUST GO. A teardown that leaves one behind is worse than
  # one that leaks a tmux server: the server dies at reboot and /var/tmp does
  # NOT. Config first, so an interrupted teardown removes secrets before bulk.
  # Guarded by a path pattern so a mis-set PP_STORE cannot rm -rf something real.
  case "$PP_STORE" in
    /var/tmp/paint-?*)
      rm -rf "$PP_STORE/config"
      [ -n "$PP_KEEP_STORE" ] || rm -rf "$PP_STORE/state"
      ;;
  esac
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
