#!/usr/bin/env bash
# chalkstress.sh: end-to-end concurrency stress for the figaro chalkboard.
#
# WHY THIS EXISTS
# ---------------
# `figaro set` / `unset` / `outfit` route through the agent inbox and are
# applied on the *agent* goroutine (Agent.act -> applyControlPatch ->
# chalkboard.State.Apply). `figaro state` (rpc.MethodChalkboard) calls
# Agent.Snapshot() inline on the *RPC* goroutine (StartSocket -> `go
# a.serveConn` -> jkrpc handler), with no inbox and no mutex in between.
# That is an unsynchronized publication of chalkboard.State.snapshot, proved
# by internal/figaro/chalkboard_race_test.go. This script hammers the same
# two paths through a real daemon over a real socket.
#
# ACCEPTANCE CRITERIA FOR THE FIX (atomic.Pointer publication)
# ------------------------------------------------------------
# The chalkboard race may be declared fixed when ALL of the following pass:
#
#   1. The repro test is clean under the race detector:
#        CHALK_RACE_REPRO=1 go test -race -count=1 \
#          -run 'TestChalkboardRPCRaceRepro|TestChalkboardStateRaceRepro' \
#          ./internal/figaro/
#      Zero "WARNING: DATA RACE". On main this emits 11 of them, 5/5 runs.
#
#   2. The whole suite is clean under the race detector:
#        go test -race ./...
#
#   3. The default suite is green with the gate unset:
#        go build ./... && go vet ./... && go test ./...
#
#   4. This script is clean at, at minimum:
#        STRESS_WRITERS=4 STRESS_READERS=6 STRESS_DURATION=30 \
#          scripts/chalkstress.sh
#      AND with a race-instrumented daemon:
#        STRESS_RACE=1 STRESS_WRITERS=4 STRESS_READERS=6 STRESS_DURATION=30 \
#          scripts/chalkstress.sh
#      "Clean" = exit 0, which asserts: the daemon survived, every key in
#      every writer's disjoint range holds that writer's own final value (no
#      lost updates, no cross-writer contamination), and the state that
#      survives a daemon restart equals the state read before it.
#
#   5. Re-run (1) and (4) once more after the merge to `main`, since the fix
#      lands on top of the immutable-tree swap.
#
# USAGE
# -----
#   scripts/chalkstress.sh
#
#   STRESS_WRITERS   concurrent set/unset workers            (default 4)
#   STRESS_READERS   concurrent `state` readers              (default 6)
#   STRESS_DURATION  seconds of hammering                    (default 20)
#   STRESS_KEYS      keys per writer range                   (default 24)
#   STRESS_OUTFIT   outfit to mint the aria with           (default: default)
#   STRESS_KEEP      keep the temp root for post-mortem      (default: unset)
#   STRESS_RACE      build the daemon with -race and hand-start it so its
#                    stderr is captured; ANY "WARNING: DATA RACE" in the
#                    daemon log fails the run. This is the end-to-end twin of
#                    the repro test: on main it fires, after the fix it must
#                    not.                                     (default: unset)
#   STRESS_RACE_LOG  where to copy the daemon log when races are found
#                                    (default /tmp/chalkstress-daemon-race.log)
#
# ISOLATION
# ---------
# Everything runs against a freshly built binary in a throwaway temp root with
# its own FIGARO_RUNTIME_DIR and FIGARO_STATE_DIR. FIGARO_CONFIG_DIR and
# FIGARO_HUSH_APP are inherited so the outfit is realistic (~30 skill blobs).
# No LLM turn is ever started, so this costs zero tokens. The user's live
# daemon must never be touched: see guard_isolation() below.

set -uo pipefail

log()  { printf '\033[36m[chalkstress]\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[33m[chalkstress]\033[0m %s\n' "$*" >&2; }
fail() { printf '\033[31m[chalkstress] FAIL:\033[0m %s\n' "$*" >&2; exit 1; }

WRITERS=${STRESS_WRITERS:-4}
READERS=${STRESS_READERS:-6}
DURATION=${STRESS_DURATION:-20}
KEYS=${STRESS_KEYS:-24}
OUTFIT=${STRESS_OUTFIT:-default}

command -v jq >/dev/null || fail "jq is required"
command -v go >/dev/null || fail "go is required"

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$REPO_ROOT" || fail "cannot cd to repo root"

# ---------------------------------------------------------------------------
# Temp root + isolation guard. This is the one unforgivable failure mode:
# never, under any circumstance, drive the user's live daemon.
# ---------------------------------------------------------------------------
TMPROOT=$(mktemp -d "${TMPDIR:-/tmp}/chalkstress.XXXXXXXX") || fail "mktemp"
export FIGARO_RUNTIME_DIR="$TMPROOT/runtime"
export FIGARO_STATE_DIR="$TMPROOT/state"
mkdir -p "$FIGARO_RUNTIME_DIR" "$FIGARO_STATE_DIR"

guard_isolation() {
	local live_state live_runtime real_state real_runtime
	[ -n "${FIGARO_STATE_DIR:-}" ]   || fail "FIGARO_STATE_DIR is unset/empty: refusing to run"
	[ -n "${FIGARO_RUNTIME_DIR:-}" ] || fail "FIGARO_RUNTIME_DIR is unset/empty: refusing to run"

	real_state=$(cd "$FIGARO_STATE_DIR" && pwd -P)     || fail "cannot resolve FIGARO_STATE_DIR"
	real_runtime=$(cd "$FIGARO_RUNTIME_DIR" && pwd -P) || fail "cannot resolve FIGARO_RUNTIME_DIR"

	# The real ones, as internal/cli/config.go and angelus_client.go compute them.
	live_state="${XDG_STATE_HOME:-$HOME/.local/state}/figaro"
	live_runtime="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/figaro"

	local d
	for d in "$real_state" "$real_runtime"; do
		case "$d" in
		"$live_state"|"$live_state"/*|"$live_runtime"|"$live_runtime"/*)
			fail "REFUSING TO RUN: '$d' is inside the user's live figaro dirs. This script must never touch the live daemon." ;;
		"$HOME"|"$HOME"/.config/*|"$HOME"/.local/state|"$HOME"/.local/state/*)
			fail "REFUSING TO RUN: '$d' resolves near the user's real state. Aborting." ;;
		"$TMPROOT"/*) ;;
		*)
			fail "REFUSING TO RUN: '$d' is not inside the throwaway root '$TMPROOT'. Aborting." ;;
		esac
	done
	# Belt and braces: the isolated dirs must be empty of a live daemon socket
	# we did not create ourselves.
	[ ! -e "$real_runtime/angelus.sock" ] || fail "a socket already exists at $real_runtime/angelus.sock, aborting"
	log "isolation OK: runtime=$real_runtime state=$real_state"
}

FIG="$TMPROOT/figaro"

fig() { "$FIG" "$@"; }

cleanup() {
	local rc=$?
	# NB: `set -u` is on and cleanup can fire before $FIG exists (e.g. the
	# isolation guard aborting), hence ${FIG:-}.
	if [ -n "${FIG:-}" ] && [ -x "${FIG:-}" ]; then
		fig rest >/dev/null 2>&1
		sleep 0.5
		local pidfile="$FIGARO_RUNTIME_DIR/angelus.pid"
		if [ -f "$pidfile" ]; then
			local pid
			pid=$(cat "$pidfile" 2>/dev/null)
			if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
				warn "angelus $pid still up after rest; SIGKILL"
				kill -9 "$pid" 2>/dev/null
			fi
		fi
	fi
	if [ -n "${STRESS_KEEP:-}" ]; then
		log "keeping temp root: $TMPROOT"
	else
		rm -rf "$TMPROOT"
	fi
	exit $rc
}
trap cleanup EXIT INT TERM

guard_isolation

# ---------------------------------------------------------------------------
# 1. Build + 3. mint an aria with NO prompt (no turn, no tokens).
# ---------------------------------------------------------------------------
# -ldflags stamps the revision: a plain `go build` in a git worktree records no
# vcs.revision, which silently disables the CLI/daemon build handshake.
BUILD_FLAGS=(-ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse --short=12 HEAD)")
if [ -n "${STRESS_RACE:-}" ]; then
	BUILD_FLAGS+=(-race)
	log "STRESS_RACE=1: building the daemon WITH the race detector"
fi
log "building ./cmd/figaro -> $FIG"
go build "${BUILD_FLAGS[@]}" -o "$FIG" ./cmd/figaro || fail "go build"

# With -race we start the angelus ourselves so we can keep its stderr: the
# auto-spawn path in internal/cli/angelus_client.go sets cmd.Stderr = nil,
# which would throw every race report away. `_FIGARO_DAEMON=1 figaro` is the
# daemon entry point (internal/cli/cli.go).
DAEMON_LOG="$TMPROOT/daemon.log"
if [ -n "${STRESS_RACE:-}" ]; then
	log "starting the isolated angelus by hand (stderr -> $DAEMON_LOG)"
	GORACE="halt_on_error=0" _FIGARO_DAEMON=1 "$FIG" >"$DAEMON_LOG" 2>&1 &
	DAEMON_BG=$!
	for _ in $(seq 1 100); do
		[ -S "$FIGARO_RUNTIME_DIR/angelus.sock" ] && break
		sleep 0.1
	done
	[ -S "$FIGARO_RUNTIME_DIR/angelus.sock" ] || fail "hand-started angelus never bound its socket"
fi

log "minting aria on outfit '$OUTFIT' (no prompt, no turn, no tokens)"
NEW_JSON=$(fig new --outfit "$OUTFIT" -j 2>/dev/null) || fail "figaro new failed: $NEW_JSON"
ARIA=$(printf '%s' "$NEW_JSON" | jq -r '.aria_id // empty')
[ -n "$ARIA" ] || fail "could not parse aria id from: $NEW_JSON"
log "aria $ARIA"

ANGELUS_PID=$(cat "$FIGARO_RUNTIME_DIR/angelus.pid" 2>/dev/null)
[ -n "$ANGELUS_PID" ] || fail "no angelus pid file"
log "isolated angelus pid $ANGELUS_PID"

BASE_KEYS=$(fig state --id "$ARIA" -j 2>/dev/null | jq -r 'keys|length')
log "baseline chalkboard: $BASE_KEYS keys"

ERRDIR="$TMPROOT/errs"
DIAG="$TMPROOT/worker-stderr.log"   # worker stderr, for post-mortem only
mkdir -p "$ERRDIR"
: >"$DIAG"

# ---------------------------------------------------------------------------
# 4. Fan out: writers on disjoint key ranges, readers in a tight loop,
#    periodic outfit re-applies, plus show/status readers.
# ---------------------------------------------------------------------------
END=$(( $(date +%s) + DURATION ))
running() { [ "$(date +%s)" -lt "$END" ]; }

# quiet <errfile> <label> -- <cmd...>
# Runs a figaro command with stdout+stderr swallowed into the diagnostic log,
# and records ONE line in <errfile> only if it exits non-zero. figaro chatters
# on stderr on success (`set k = v`, the `list` footer), so raw stderr is not
# a failure signal: the exit code is.
quiet() {
	local errfile=$1 label=$2; shift 3
	local rc=0
	"$FIG" "$@" >/dev/null 2>>"$DIAG" || rc=$?
	[ "$rc" -eq 0 ] || echo "$label: exit $rc" >>"$errfile"
}

writer() { # $1 = writer index
	local w=$1 i=0 k
	while running; do
		for k in $(seq 0 $((KEYS - 1))); do
			running || break
			quiet "$ERRDIR/w$w.err" "set stress.w$w.k$k" -- \
				set --id "$ARIA" "stress.w$w.k$k" "\"w$w:$k:$i\""
			# Churn: set then remove a scratch key in this writer's own range.
			if [ $((k % 4)) -eq 0 ]; then
				quiet "$ERRDIR/w$w.err" "set stress.w$w.tmp$k" -- \
					set --id "$ARIA" "stress.w$w.tmp$k" '"scratch"'
				quiet "$ERRDIR/w$w.err" "unset stress.w$w.tmp$k" -- \
					unset --id "$ARIA" "stress.w$w.tmp$k"
			fi
		done
		i=$((i + 1))
	done
	# Sealing pass: the timed loop can be cut mid-round (very likely under
	# STRESS_RACE, where the daemon is 10x slower), which would leave keys
	# from the tail of the range unwritten and make the lost-update assertion
	# meaningless. Write every key in the range exactly once, unconditionally.
	for k in $(seq 0 $((KEYS - 1))); do
		quiet "$ERRDIR/w$w.err" "seal stress.w$w.k$k" -- \
			set --id "$ARIA" "stress.w$w.k$k" "\"w$w:$k:final\""
	done
	echo "$i" >"$ERRDIR/w$w.rounds"
}

reader() { # $1 = reader index
	local r=$1 n=0
	while running; do
		quiet "$ERRDIR/r$r.err" "state" -- state --id "$ARIA" -j
		n=$((n + 1))
	done
	echo "$n" >"$ERRDIR/r$r.reads"
}

outfit_reapplier() {
	local n=0
	while running; do
		quiet "$ERRDIR/outfit.err" "outfit" -- outfit --id "$ARIA" "$OUTFIT"
		sleep 0.7
		n=$((n + 1))
	done
	echo "$n" >"$ERRDIR/outfit.rounds"
}

misc_reader() {
	local n=0
	while running; do
		quiet "$ERRDIR/misc.err" "status" -- status --id "$ARIA" -j
		quiet "$ERRDIR/misc.err" "show" -- show "$ARIA" -n 3 -l
		quiet "$ERRDIR/misc.err" "list" -- list
		n=$((n + 1))
	done
	echo "$n" >"$ERRDIR/misc.rounds"
}

log "stressing for ${DURATION}s: $WRITERS writers x $KEYS keys, $READERS readers, 1 outfit re-applier, 1 misc reader"
pids=()
for w in $(seq 0 $((WRITERS - 1))); do writer "$w" & pids+=($!); done
for r in $(seq 0 $((READERS - 1))); do reader "$r" & pids+=($!); done
outfit_reapplier & pids+=($!)
misc_reader & pids+=($!)

for p in "${pids[@]}"; do wait "$p"; done
log "workers done"

# ---------------------------------------------------------------------------
# 5. Assertions.
# ---------------------------------------------------------------------------
FAILURES=0
note_fail() { printf '\033[31m  ✗ %s\033[0m\n' "$*" >&2; FAILURES=$((FAILURES + 1)); }
note_ok()   { printf '\033[32m  ✓ %s\033[0m\n' "$*" >&2; }

# 5a. Daemon alive.
if kill -0 "$ANGELUS_PID" 2>/dev/null; then
	note_ok "angelus $ANGELUS_PID still alive"
else
	note_fail "angelus $ANGELUS_PID is GONE: the daemon died under concurrent set+state"
fi

# Worker errors (a dead socket shows up here first).
werr=$(cat "$ERRDIR"/*.err 2>/dev/null | grep -c .)
if [ "$werr" -gt 0 ]; then
	note_fail "$werr failed worker command(s):"
	sort "$ERRDIR"/*.err 2>/dev/null | uniq -c | sort -rn | head -20 >&2
else
	note_ok "every worker command exited 0"
fi

# Quiesce: sets are enqueued on the inbox, so the last few may still be in
# flight. Wait for the agent to report idle.
for _ in $(seq 1 40); do
	st=$(fig status --id "$ARIA" -j 2>/dev/null | jq -r '.state // empty')
	[ "$st" = "active" ] || break
	sleep 0.25
done

SNAP_BEFORE="$TMPROOT/snap-before.json"
fig state --id "$ARIA" -j >"$SNAP_BEFORE" 2>/dev/null || note_fail "final state read failed"

# 5b. No lost updates / no cross-writer contamination. Every key in writer w's
# range must exist and must hold a value stamped with writer w's own index.
for w in $(seq 0 $((WRITERS - 1))); do
	missing=0 wrong=0
	for k in $(seq 0 $((KEYS - 1))); do
		v=$(jq -r --arg k "stress.w$w.k$k" '.[$k] // empty' "$SNAP_BEFORE")
		if [ -z "$v" ]; then
			missing=$((missing + 1))
			continue
		fi
		case "$v" in
		"w$w:$k:final") ;;
		*) wrong=$((wrong + 1)); warn "stress.w$w.k$k = $v (expected w$w:$k:final)" ;;
		esac
	done
	if [ "$missing" -ne 0 ] || [ "$wrong" -ne 0 ]; then
		note_fail "writer $w: $missing missing key(s), $wrong contaminated value(s) of $KEYS"
	else
		note_ok "writer $w: all $KEYS keys present with own values"
	fi
done

# Scratch keys must all be gone (every tmp set was followed by an unset).
leaked=$(jq -r '[keys[] | select(startswith("stress.") and contains(".tmp"))] | length' "$SNAP_BEFORE")
if [ "$leaked" -eq 0 ]; then
	note_ok "no leaked scratch keys (set/unset pairs all landed)"
else
	note_fail "$leaked scratch key(s) survived their unset"
fi

# 5c. Persisted state == fresh read. Stop the daemon, let the next command
# respawn it, and re-read: the chalkboard must replay identically.
log "restarting the isolated daemon to check durability"
fig rest >/dev/null 2>&1
sleep 1
SNAP_AFTER="$TMPROOT/snap-after.json"
fig state --id "$ARIA" -j >"$SNAP_AFTER" 2>/dev/null || note_fail "post-restart state read failed"

before=$(jq -S '[to_entries[] | select(.key|startswith("stress."))] | from_entries' "$SNAP_BEFORE" 2>/dev/null)
after=$(jq -S '[to_entries[] | select(.key|startswith("stress."))] | from_entries' "$SNAP_AFTER" 2>/dev/null)
if [ -n "$before" ] && [ "$before" = "$after" ]; then
	note_ok "persisted state matches the fresh read ($(printf '%s' "$before" | jq 'keys|length') stress keys)"
else
	note_fail "persisted state DIVERGED from the pre-restart read"
	diff <(printf '%s\n' "$before") <(printf '%s\n' "$after") | head -30 >&2
fi

# 5d. Race-instrumented daemon: any DATA RACE report is a hard failure.
if [ -n "${STRESS_RACE:-}" ]; then
	# grep -c exits 1 when it finds nothing, and `$(grep -c … || echo 0)`
	# would then yield the two-line string "0\n0" and break the -eq test
	# below. Only reachable once the race is actually fixed.
	nraces=$(grep -c "WARNING: DATA RACE" "$DAEMON_LOG" 2>/dev/null) || nraces=0
	if [ "$nraces" -eq 0 ]; then
		note_ok "race-instrumented daemon reported no data races"
	else
		note_fail "race-instrumented daemon reported $nraces DATA RACE(s):"
		sed -n '/WARNING: DATA RACE/,/^==================$/p' "$DAEMON_LOG" | head -40 >&2
		cp "$DAEMON_LOG" "${STRESS_RACE_LOG:-/tmp/chalkstress-daemon-race.log}" 2>/dev/null \
			&& warn "full daemon log copied to ${STRESS_RACE_LOG:-/tmp/chalkstress-daemon-race.log}"
	fi
fi

# Summary.
sum() { cat "$@" 2>/dev/null | awk '{n+=$1} END{print n+0}'; }
rounds=$(sum "$ERRDIR"/w*.rounds)
reads=$(sum "$ERRDIR"/r*.reads)
log "writer rounds: $rounds  |  state reads: $reads  |  final keys: $(jq -r 'keys|length' "$SNAP_BEFORE" 2>/dev/null)"

if [ "$FAILURES" -eq 0 ]; then
	log "PASS"
	exit 0
fi
fail "$FAILURES assertion(s) failed"
