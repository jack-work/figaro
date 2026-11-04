#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat >&2 <<'USAGE'
usage: compat-pair.sh CLI DAEMON EXPECT

Starts DAEMON in a throwaway root (every FIGARO_* path under a fresh mktemp
directory), drives it with CLI, and checks the CLI's verdict on the pair:
  accept   talks without a word about the build
  warn     talks after warning that the daemon is another minor release
  refuse   refuses the pair, naming the build or the protocol
USAGE
	exit 2
}

[ $# -eq 3 ] || usage

cli=$1
daemon=$2
expect=$3

root=$(mktemp -d "${TMPDIR:-/tmp}/compat.XXXXXX")
mkdir -p "$root/run" "$root/state" "$root/config" "$root/cache" "$root/home"
export HOME="$root/home"
export FIGARO_RUNTIME_DIR="$root/run" XDG_RUNTIME_DIR="$root/run"
export FIGARO_STATE_DIR="$root/state" FIGARO_CONFIG_DIR="$root/config" FIGARO_CACHE_DIR="$root/cache"
export FIGARO_NO_SELF_SPAWN=1 NO_COLOR=1

"$daemon" --angelus >"$root/daemon.log" 2>&1 &
dpid=$!
cleanup() {
	kill "$dpid" 2>/dev/null || true
	wait "$dpid" 2>/dev/null || true
	rm -rf "$root"
}
trap cleanup EXIT

for _ in $(seq 1 100); do
	[ -S "$root/run/angelus.sock" ] && break
	kill -0 "$dpid" 2>/dev/null || { cat "$root/daemon.log" >&2; echo "compat: daemon exited" >&2; exit 1; }
	sleep 0.1
done
[ -S "$root/run/angelus.sock" ] || { echo "compat: no socket after 10s" >&2; exit 1; }

fail() { echo "compat FAIL ($(basename "$cli") -> $(basename "$daemon"), $expect): $*" >&2; exit 1; }

set +e
out=$("$cli" form new -S probe=1 -j 2>"$root/err")
rc=$?
set -e
err=$(cat "$root/err")

case $expect in
refuse)
	[ $rc -ne 0 ] || fail "the pair was accepted: $out"
	echo "$err" | grep -qE 'different build|protocol' || fail "refused without naming the build or protocol: $err"
	echo "compat ok: $(basename "$cli") -> $(basename "$daemon") refused"
	exit 0
	;;
accept)
	[ $rc -eq 0 ] || fail "refused: $err"
	[ -z "$(echo "$err" | grep -i 'minor\|build')" ] || fail "warned on an accepted pair: $err"
	;;
warn)
	[ $rc -eq 0 ] || fail "refused: $err"
	echo "$err" | grep -q 'another minor release' || fail "no minor-release warning: $err"
	;;
*) fail "unknown expectation" ;;
esac

id=$(echo "$out" | sed -n 's/.*"form_id": *"\([^"]*\)".*/\1/p')
[ -n "$id" ] || fail "no form id in: $out"
"$cli" set --id "$id" color blue >/dev/null 2>&1 || fail "set refused"
"$cli" state show "$id" -j 2>/dev/null | grep -q '"color": *"blue"' || fail "state show lost the set"
"$cli" ls -j >/dev/null 2>&1 || fail "ls refused"

echo "compat ok: $(basename "$cli") -> $(basename "$daemon") $expect"
