#!/usr/bin/env bash
#
# End-to-end: a real angelus, a real gateway with `--authn upstream`, and the
# real CLI reaching the daemon through it -- with Caddy's headers simulated
# rather than assumed.
#
# WHAT THIS PROVES, and what it deliberately does not. It exercises the
# figaro half of the spain deployment: the refusal table, the upstream
# authenticator, the Host allowlist, and a full JSON-RPC round trip over a
# WebSocket. It does NOT prove Cloudflare's idle reaper or Caddy's upgrade
# handling -- those need the real edge, and the King's evidence for them is
# Synapse's /sync holding open through the identical path.

set -uo pipefail

REPO=${REPO:-$(cd "$(dirname "$0")/.." && pwd)}
RT=$(mktemp -d /tmp/figaro-e2e.XXXXXX)

# BUILD THE BINARY UNDER TEST, always. The first run of this script used a
# path from $PATH that happened to be 48 minutes stale -- built before the
# authenticator existed -- and it reported an unauthenticated upgrade
# succeeding. That is the exact failure this whole suite is meant to catch,
# so the suite must never be able to test yesterday's binary.
FIG="$RT/figaro"

PORT=${PORT:-19098}
HOSTNAME_ALLOWED=fig.test.local
# Caddy preserves the original Host; a direct client sends host:port. The
# allowlist admits both so this exercises the real production name AND the
# loopback address the probe below has to use.
HOSTS="$HOSTNAME_ALLOWED,127.0.0.1"
pass=0 fail=0

cleanup() {
	[ -n "${GW3_PID:-}" ] && kill "$GW3_PID" 2>/dev/null
	[ -n "${GW2_PID:-}" ] && kill "$GW2_PID" 2>/dev/null
	[ -n "${GW_PID:-}" ] && kill "$GW_PID" 2>/dev/null
	[ -n "${DAEMON_PID:-}" ] && kill "$DAEMON_PID" 2>/dev/null
	sleep 0.2
	rm -rf "$RT"
}
trap cleanup EXIT

ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail+1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

export FIGARO_RUNTIME_DIR="$RT/run"
export FIGARO_STATE_DIR="$RT/state"
mkdir -p "$FIGARO_RUNTIME_DIR" "$FIGARO_STATE_DIR"

step "0. build the binary under test"
if ! (cd "$REPO" && go build -o "$FIG" ./cmd/figaro); then
	echo "  build failed"; exit 1
fi
echo "  $FIG (fresh from $REPO)"
"$FIG" --version 2>&1 | head -1 | sed 's/^/  /'

step "1. refusal table -- the combinations that must not start"
check_refused() {
	local desc=$1; shift
	local out; out=$("$FIG" serve "$@" 2>&1)
	if grep -qi "refusing to serve\|error:" <<<"$out"; then
		ok "$desc"
	else
		bad "$desc -- it STARTED. $out"
	fi
}
check_refused "authn=none on 0.0.0.0"      --listen "tcp://0.0.0.0:$PORT" --authn none
check_refused "authn=upstream on 0.0.0.0"  --listen "tcp://0.0.0.0:$PORT" --authn upstream
check_refused "unknown authenticator"      --listen "tcp://127.0.0.1:$PORT" --authn sudo
check_refused "doorkey with no secret"     --listen "tcp://127.0.0.1:$PORT" --authn doorkey

step "2. start the angelus"
"$FIG" --angelus >"$RT/angelus.log" 2>&1 &
DAEMON_PID=$!
for _ in $(seq 1 100); do
	[ -S "$FIGARO_RUNTIME_DIR/angelus.sock" ] && break
	sleep 0.2
done
if [ -S "$FIGARO_RUNTIME_DIR/angelus.sock" ]; then ok "angelus is listening"
else bad "angelus never bound"; tail -5 "$RT/angelus.log"; exit 1; fi

step "3. the CLI works over the unix socket (the control)"
if "$FIG" ls -j >/dev/null 2>&1; then ok "figaro ls direct"
else bad "figaro ls direct"; fi

step "4. start the gateway the way the nix module does"
"$FIG" serve --listen "tcp://127.0.0.1:$PORT" --authn upstream \
	--host "$HOSTS" --max-conn-age 8h >"$RT/gw.log" 2>&1 &
GW_PID=$!
for _ in $(seq 1 60); do
	curl -sf -m 1 -H "Host: $HOSTNAME_ALLOWED" "http://127.0.0.1:$PORT/v1/health" >/dev/null 2>&1 && break
	sleep 0.2
done
if curl -sf -m 2 -H "Host: $HOSTNAME_ALLOWED" "http://127.0.0.1:$PORT/v1/health" >/dev/null; then
	ok "gateway is up on loopback with authn=upstream"
else
	bad "gateway never came up"; tail -5 "$RT/gw.log"; exit 1
fi

step "5. the door refuses what it should"
code() { curl -s -o /dev/null -w '%{http_code}' -m 3 "$@"; }

c=$(code -H "Host: evil.example" "http://127.0.0.1:$PORT/v1/health")
[ "$c" = "421" ] && ok "wrong Host -> 421 (DNS rebinding closed)" || bad "wrong Host -> $c, want 421"

c=$(code -H "Host: $HOSTNAME_ALLOWED" \
	-H "Connection: Upgrade" -H "Upgrade: websocket" \
	-H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==" \
	"http://127.0.0.1:$PORT/v1/socket")
[ "$c" = "401" ] && ok "upgrade with no Remote-User -> 401" || bad "no Remote-User -> $c, want 401"

step "5b. the group gate -- authenticated is not the same as admitted"
# Authelia's rule for the hostname is two_factor with NO subject restriction,
# so every directory user who passes 2FA reaches this port. The group check
# is the application's, and this is it.
GATE_PORT=$((PORT + 2))
"$FIG" serve --listen "tcp://127.0.0.1:$GATE_PORT" --authn upstream \
	--host "$HOSTS" --require-group figaro-admin >"$RT/gw3.log" 2>&1 &
GW3_PID=$!
for _ in $(seq 1 60); do
	curl -sf -m 1 -H "Host: $HOSTNAME_ALLOWED" "http://127.0.0.1:$GATE_PORT/v1/health" >/dev/null 2>&1 && break
	sleep 0.2
done
up() {
	code -H "Host: $HOSTNAME_ALLOWED" -H "Remote-User: $1" ${2:+-H "Remote-Groups: $2"} \
		-H "Connection: Upgrade" -H "Upgrade: websocket" \
		-H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==" \
		"http://127.0.0.1:$GATE_PORT/v1/socket"
}
c=$(up gluck figaro-admin);  [ "$c" = "101" ] && ok "admin admitted" || bad "admin -> $c"
c=$(up someone keel-admin);  [ "$c" = "401" ] && ok "2FA user in the WRONG group refused" || bad "wrong group -> $c, want 401"
c=$(up someone "");          [ "$c" = "401" ] && ok "2FA user in NO group refused" || bad "no group -> $c, want 401"

step "6. an authenticated upgrade succeeds (Caddy's headers, simulated)"
c=$(code -H "Host: $HOSTNAME_ALLOWED" \
	-H "Remote-User: gluck" -H "Remote-Groups: figaro-admin" \
	-H "Connection: Upgrade" -H "Upgrade: websocket" \
	-H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==" \
	"http://127.0.0.1:$PORT/v1/socket")
[ "$c" = "101" ] && ok "authenticated upgrade -> 101 Switching Protocols" || bad "authenticated upgrade -> $c, want 101"

# The probe is written BEFORE the steps that run it. It was not, once,
# and four assertions failed on a missing file rather than on anything
# real -- a test that fails for its own reasons teaches nothing.
cat > "$RT/probe.go" <<'GO'
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
)

func main() {
	ep := transport.Endpoint{
		Scheme:  "http",
		Address: os.Args[1],
		Bearer:  os.Getenv("FIGARO_DOORKEY"),
	}
	cli, err := sdk.DialAngelus(ep)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := cli.Status(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		os.Exit(1)
	}
	l, err := cli.List(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "list:", err)
		os.Exit(1)
	}
	fmt.Printf("uptime=%dms arias=%d\n", st.Uptime, len(l.Figaros))
}
GO

step "7. the tunnel refuses an unauthenticated RPC client"
# Not a bug: the probe presents no Remote-User, so `upstream` refuses it.
# Asserting this is the point -- it proves the door is shut to a client that
# reaches the port directly rather than through the proxy.
if out=$(cd "$REPO" && go run "$RT/probe.go" "127.0.0.1:$PORT" 2>&1); then
	bad "an unauthenticated client got through: $out"
else
	grep -qi "401\|refused this credential" <<<"$out" \
		&& ok "unauthenticated RPC client refused (401)" \
		|| bad "refused, but not with 401: $out"
fi

step "8. a full RPC round trip through a doorkey tunnel"
# doorkey is the authenticator a PEER uses -- machine to machine, no browser
# session -- and it is the one an Endpoint can present, so this is the round
# trip that exercises tunnel + authentication + envelope rewrite together.
DOORKEY=$(head -c 32 /dev/urandom | base64 | tr -d "=+/" | head -c 32)
printf %s "$DOORKEY" > "$RT/doorkey"
chmod 600 "$RT/doorkey"
PORT2=$((PORT + 1))
"$FIG" serve --listen "tcp://127.0.0.1:$PORT2" --authn doorkey \
	--doorkey-file "$RT/doorkey" --host 127.0.0.1 >"$RT/gw2.log" 2>&1 &
GW2_PID=$!
for _ in $(seq 1 60); do
	curl -sf -m 1 "http://127.0.0.1:$PORT2/v1/health" >/dev/null 2>&1 && break
	sleep 0.2
done

c=$(code "http://127.0.0.1:$PORT2/v1/socket" \
	-H "Connection: Upgrade" -H "Upgrade: websocket" \
	-H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==")
[ "$c" = "401" ] && ok "doorkey tunnel refuses a missing bearer" || bad "missing bearer -> $c"

c=$(code "http://127.0.0.1:$PORT2/v1/socket" -H "Authorization: Bearer wrong-token-entirely" \
	-H "Connection: Upgrade" -H "Upgrade: websocket" \
	-H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==")
[ "$c" = "401" ] && ok "doorkey tunnel refuses a wrong bearer" || bad "wrong bearer -> $c"

if out=$(cd "$REPO" && FIGARO_DOORKEY="$DOORKEY" go run "$RT/probe.go" "127.0.0.1:$PORT2" 2>&1); then
	ok "angelus.status + figaro.list over the tunnel: $out"
else
	bad "RPC through the doorkey tunnel failed: $out"
fi
# The CLI has no --origin yet (remoting belongs to the daemon in the peer
# design), so the tunnel is driven by the same SDK path the CLI uses.
step "results"
printf '  %d passed, %d failed\n\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
