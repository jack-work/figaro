#!/usr/bin/env bash
# devpane.sh: a sourceable harness for driving figaro's TUI inside an ISOLATED
# nix dev shell, on an ISOLATED tmux server, against a FAKE provider.
#
# Read skills/figaro/contributing/isolated-pane.md before using this.
#
#   source scripts/devpane.sh
#   dp_init quote                 # dirs, fake gateway, dev-shell figaro
#   dp_reply 1 <<< "a paragraph"  # what the provider says, per request
#   ARIA=$(dp_new)
#   dp_fig send -f --id "$ARIA" -- "hello"
#   dp_pane 100 40                # a pane INSIDE `nix develop .#clean`
#   dp_run figaro listen "$ARIA"
#   dp_key C-t; dp_stable; dp_cap
#   dp_down                       # tmux server, daemon, gateway, unit dir
#
# dp_init installs an EXIT trap, so an aborted script still cleans up.

set -o pipefail

DP_UNIT=""
DP_DIR=""
DP_SOCK=""
DP_SESS=""
DP_REPO=""
DP_PORT=""
DP_GW=""
DP_FIG=""
DP_W=0
DP_H=0
DP_SHELL="${DP_SHELL:-.#clean}"

dp_die() { echo "devpane: $*" >&2; return 1; }

dp_env() {
  printf '%s\n' \
    "FIGARO_DEV_ROOT=$DP_DIR/dev" \
    "FIGARO_STATE_DIR=$DP_DIR/state" \
    "FIGARO_RUNTIME_DIR=$DP_DIR/run" \
    "FIGARO_CONFIG_DIR=$DP_DIR/config" \
    "FIGARO_CACHE_DIR=$DP_DIR/cache" \
    "FIGARO_HUSH_APP=figaro-devpane-$DP_UNIT" \
    "FIGARO_HUSH_DIR=$DP_DIR/hush" \
    "FIGARO_HUSH_PASSPHRASE=$(cat "$DP_DIR/hush-passphrase")"
}

dp_exports() { local v; while IFS= read -r v; do printf 'export %q; ' "$v"; done < <(dp_env); }

dp_freeport() {
  python3 - <<'EOF'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
EOF
}

dp_init() {
  DP_UNIT="${1:?dp_init <unit>}"
  DP_REPO="$(git rev-parse --show-toplevel)" || return 1
  [ -f "$DP_REPO/flake.nix" ] || { dp_die "no flake.nix at $DP_REPO"; return 1; }
  DP_DIR="/var/tmp/devpane-$DP_UNIT"
  DP_SOCK="$DP_DIR/tmux.sock"
  mkdir -p -m 700 "$DP_DIR" || return 1
  chmod 700 "$DP_DIR"
  mkdir -p -m 700 "$DP_DIR"/{dev,state,run,config,cache,hush,replies} \
    "$DP_DIR"/config/{outfits,providers} || return 1

  if [ ! -f "$DP_DIR/hush-passphrase" ]; then
    ( umask 077; head -c 24 /dev/urandom | base64 | tr -d '\n' > "$DP_DIR/hush-passphrase" )
  fi

  DP_PORT="$(dp_freeport)" || return 1
  cat > "$DP_DIR/config/providers/gateway.toml" <<EOF
base_url = "http://127.0.0.1:$DP_PORT/v1"
EOF
  printf 'A test agent. Answer plainly.\n' > "$DP_DIR/config/credo.md"
  cat > "$DP_DIR/config/outfits/pane.toml" <<'EOF'
duke-title = "pane"
[system]
provider = "gateway"
model = "auto"
max_tokens = 1024
max_context_tokens = 200000
credo = { fileName = "credo.md" }
EOF
  printf 'default_outfit = "pane"\ninteractive = false\n' > "$DP_DIR/config/config.toml"

  python3 "$DP_REPO/scripts/fake-gateway-prose.py" "$DP_PORT" \
    "$DP_DIR/requests.jsonl" "$DP_DIR/replies" & DP_GW=$!
  trap dp_down EXIT

  # THE BINARY COMES OUT OF THE DEV SHELL, not out of `go build`. maintaining.md
  # names the trap: a worktree go build differs from the flake build in the Go
  # toolchain and the dependency closure, and either can decide whether a
  # rendering bug reproduces. Resolved once; the pane enters the same shell.
  local -a e=(); local v
  while IFS= read -r v; do e+=("$v"); done < <(dp_env)
  local link
  link="$(cd "$DP_REPO" && env "${e[@]}" nix develop "$DP_SHELL" --command bash -c 'command -v figaro' 2>/dev/null | tail -1)"
  DP_FIG="$(readlink -f "$link" 2>/dev/null)"
  case "$link:$DP_FIG" in
    "$DP_DIR"/dev/bin/figaro:/nix/store/*) : ;;
    *) dp_die "dev shell figaro is '$link' -> '$DP_FIG': expected $DP_DIR/dev/bin/figaro -> /nix/store/..."; return 1 ;;
  esac
  echo "devpane: unit $DP_DIR"
  echo "devpane: shell $DP_SHELL  figaro $DP_FIG"
  echo "devpane: $(dp_fig --version 2>/dev/null | head -1)"
  echo "devpane: gateway :$DP_PORT (pid $DP_GW)"
}

# dp_reply <n>: stdin becomes what the provider says on request <n>. "default"
# is the fallback for every request with no file of its own.
dp_reply() {
  local n="${1:?dp_reply <n|default>}"
  cat > "$DP_DIR/replies/$n.md" || return 1
}

# dp_fig: the dev shell's figaro, from here, against the isolated store.
dp_fig() {
  local -a e=(); local v
  while IFS= read -r v; do e+=("$v"); done < <(dp_env)
  env -u FIGARO_ARIA -u FIGARO_NO_BIND "${e[@]}" "$DP_FIG" "$@"
}

dp_new() {
  dp_fig new -j 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin)["aria_id"])'
}

# dp_wait_idle <aria>: block until the aria reports idle, or fail loudly.
dp_wait_idle() {
  local id="${1:?dp_wait_idle <aria>}" i state
  for i in $(seq 120); do
    state="$(dp_fig status "$id" -j 2>/dev/null | python3 -c 'import json,sys;print(json.load(sys.stdin).get("state",""))' 2>/dev/null)"
    [ "$state" = idle ] && return 0
    sleep 0.5
  done
  dp_die "aria $id never reached idle"
}

dp_tmux() { tmux -S "$DP_SOCK" "$@"; }

# dp_pane <w> <h> [tag]: a pane of EXACTLY h usable rows, running the dev
# shell. tmux subtracts the status row at creation and will not give it back,
# so ask for h+1 and report what came out.
dp_pane() {
  DP_W="${1:?dp_pane <w> <h>}"; local want="${2:?dp_pane <w> <h>}"; local tag="${3:-p}"
  DP_SESS="devpane-$DP_UNIT-$tag"
  TMUX= dp_tmux new-session -d -s "$DP_SESS" -x "$DP_W" -y "$want" bash --norc || return 1
  dp_tmux set -g status off
  dp_tmux set -g history-limit 20000
  # tmux may or may not charge the session a row for the status bar, depending
  # on when the bar went off, and turning it off does not always give the row
  # back. So resize until #{pane_height} IS the number asked for, and report
  # what came out either way.
  DP_H="$(dp_tmux display -p -t "$DP_SESS" '#{pane_height}' | tr -d '[:space:]')"
  if [ "$DP_H" != "$want" ]; then
    dp_tmux resize-window -t "$DP_SESS" -x "$DP_W" -y "$((want + want - DP_H))"
    DP_H="$(dp_tmux display -p -t "$DP_SESS" '#{pane_height}' | tr -d '[:space:]')"
  fi
  [ "$DP_H" = "$want" ] || echo "devpane: pane height is $DP_H (asked $want), ASSERT AGAINST $DP_H" >&2
  # -e is silently ignored by new-session, so the environment is exported in
  # the shell instead, before the dev shell inherits it.
  dp_tmux send-keys -t "$DP_SESS" "unset FIGARO_ARIA FIGARO_NO_BIND; $(dp_exports) cd $DP_REPO; PS1='dp$ '" Enter
  dp_tmux send-keys -t "$DP_SESS" "nix develop $DP_SHELL --command bash --norc" Enter
  local i want_bin="$DP_DIR/dev/bin/figaro"
  for i in $(seq 120); do
    sleep 1
    dp_tmux send-keys -t "$DP_SESS" 'command -v figaro' Enter
    sleep 0.4
    if dp_hist | grep -qxF "$want_bin"; then
      dp_tmux send-keys -t "$DP_SESS" 'clear' Enter
      sleep 0.3
      echo "devpane: session $DP_SESS  ${DP_W}x${DP_H}, figaro from the $DP_SHELL shell"
      return 0
    fi
  done
  dp_die "the pane never reached a dev shell with figaro at $want_bin"
}

dp_run()  { dp_tmux send-keys -t "$DP_SESS" -l "$*"; dp_tmux send-keys -t "$DP_SESS" Enter; }
dp_text() { dp_tmux send-keys -t "$DP_SESS" -l "$1"; }
dp_key()  { dp_tmux send-keys -t "$DP_SESS" "$@"; }
# dp_type: one character per read, with a gap. send-keys -l of a whole string
# arrives as a SINGLE read, which no human can produce.
dp_type() { local s="$1" c i; for ((i = 0; i < ${#s}; i++)); do c="${s:i:1}"; dp_text "$c"; sleep 0.1; done; }

dp_cap()  { dp_tmux capture-pane -p -t "$DP_SESS"; }
dp_raw()  { dp_tmux capture-pane -p -e -t "$DP_SESS"; }
dp_hist() { dp_tmux capture-pane -p -S - -t "$DP_SESS"; }

# dp_stable: wait until two consecutive captures agree, then once more.
dp_stable() {
  local a b i
  a="$(dp_cap)"
  for i in $(seq 40); do
    sleep 0.25
    b="$(dp_cap)"
    [ "$a" = "$b" ] && { sleep 0.25; [ "$(dp_cap)" = "$b" ] && return 0; }
    a="$b"
  done
  echo "devpane: pane never settled" >&2
  return 1
}

dp_resize() {
  DP_W="${1:?dp_resize <w> <h>}"; local want="${2:?dp_resize <w> <h>}"
  dp_tmux resize-window -t "$DP_SESS" -x "$DP_W" -y "$((want + 1))"
  DP_H="$(dp_tmux display -p -t "$DP_SESS" '#{pane_height}' | tr -d '[:space:]')"
  echo "devpane: ${DP_W}x${DP_H}"
}

# dp_daemons: pids whose ENVIRONMENT points at this unit. A figaro daemon's
# argv says nothing about which store it opened, so attribution reads
# /proc/<pid>/environ and nothing else.
dp_daemons() {
  local p
  for p in /proc/[0-9]*; do
    [ -r "$p/environ" ] || continue
    if { tr '\0' '\n' < "$p/environ" | grep -qx "FIGARO_RUNTIME_DIR=$DP_DIR/run"; } 2>/dev/null; then
      echo "${p#/proc/}"
    fi
  done
}

dp_verify_clean() {
  local bad=0 leak
  if tmux -S "$DP_SOCK" list-sessions >/dev/null 2>&1; then
    echo "devpane: LEAK tmux server still up on $DP_SOCK" >&2; bad=1
  fi
  leak="$(dp_daemons | tr '\n' ' ')"
  if [ -n "${leak// /}" ]; then
    echo "devpane: LEAK process(es) still hold $DP_DIR: $leak" >&2; bad=1
  fi
  if [ -n "$DP_GW" ] && kill -0 "$DP_GW" 2>/dev/null; then
    echo "devpane: LEAK gateway $DP_GW still listening on :$DP_PORT" >&2; bad=1
  fi
  [ "$bad" = 0 ] && echo "devpane: clean"
  return "$bad"
}

dp_down() {
  trap - EXIT
  [ -n "$DP_SOCK" ] && tmux -S "$DP_SOCK" kill-server 2>/dev/null
  [ -n "$DP_FIG" ] && dp_fig stop >/dev/null 2>&1
  [ -n "$DP_GW" ] && kill "$DP_GW" 2>/dev/null
  local pids; pids="$(dp_daemons)"
  [ -n "$pids" ] && kill $pids 2>/dev/null
  [ -n "${DP_KEEP:-}" ] && { echo "devpane: keeping $DP_DIR (DP_KEEP set)"; return 0; }
  [ -n "$DP_DIR" ] && rm -rf "$DP_DIR"
  echo "devpane: down"
}
