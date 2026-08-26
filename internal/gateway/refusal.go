package gateway

// THE REFUSAL TABLE.
//
// Every cell of (authenticator × bind reachability), decided once, here.
// The rule this file exists to enforce: a combination that is an
// authentication bypass must be UNREPRESENTABLE, not merely discouraged.
//
// Two lessons are baked in, both from adversarial review of the plan:
//
//  1. THE ADDRESS CANNOT BE JUDGED AS A STRING. ":9090", "0.0.0.0", "::",
//     "[::1]", "::ffff:127.0.0.1", a hostname that resolves to a LAN
//     address, and `localhost` repointed in /etc/hosts all defeat a config
//     parse. So we BIND FIRST and interrogate the listener's resolved
//     address, which is the only thing that cannot lie about itself.
//
//  2. LOOPBACK IS NOT A PRINCIPAL BOUNDARY. Any local uid reaches it, a
//     container with host networking or a published port re-exposes it, and
//     `ssh -L` tunnels it from anywhere. Loopback earns you "the reverse
//     proxy can be the only route IF the box is not shared" and nothing
//     more. Where that is not enough, the answer is a credential, not a
//     tighter address check -- and the table says so.

import (
	"fmt"
	"net"
	"strings"
)

// Authn names an authenticator. A closed set: an unknown value is refused
// rather than defaulted, for the same reason an unknown authz policy is.
type Authn string

const (
	// AuthnNone trusts everyone who can reach the socket. Legitimate for a
	// unix socket, where reaching it already required being the right uid.
	AuthnNone Authn = "none"
	// AuthnUpstream believes Remote-User / Remote-Groups, which only a
	// reverse proxy should be able to set.
	AuthnUpstream Authn = "upstream"
	// AuthnDoorkey requires a shared bearer secret.
	AuthnDoorkey Authn = "doorkey"
)

// KnownAuthn is the closed set, for validation and for error prose.
var KnownAuthn = []Authn{AuthnNone, AuthnUpstream, AuthnDoorkey}

// reach is how exposed a bound address turned out to be.
type reach int

const (
	// reachUnix: a filesystem socket. The kernel's permission check is the
	// gate, and it is a real one.
	reachUnix reach = iota
	// reachLoopback: bound to 127.0.0.0/8 or ::1. Reachable by every local
	// uid, and by anything that can publish or tunnel the port.
	reachLoopback
	// reachOpen: bound to a routable or wildcard address. On a network.
	reachOpen
)

func (r reach) String() string {
	switch r {
	case reachUnix:
		return "a unix socket"
	case reachLoopback:
		return "loopback"
	default:
		return "a routable address"
	}
}

// classify interrogates a BOUND listener. Nothing here parses a config
// string, which is the whole point: net.Listener.Addr() reports what the
// kernel actually did, including what a wildcard bind resolved to and which
// family an ambiguous host landed in.
func classify(ln net.Listener) reach {
	addr := ln.Addr()
	if addr.Network() == "unix" || addr.Network() == "unixpacket" {
		return reachUnix
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		// Cannot tell: assume the worst. An address we cannot parse is not
		// an address we may call safe.
		return reachOpen
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return reachOpen
	}
	if ip.IsUnspecified() {
		// 0.0.0.0 or ::. Bound to EVERY interface, including the routable
		// ones. This is the case a naive "is it 127.0.0.1?" check misses.
		return reachOpen
	}
	// IsLoopback handles ::1 and the v4-mapped ::ffff:127.0.0.1 correctly,
	// which is why we ask the parsed IP rather than the string.
	if ip.IsLoopback() {
		return reachLoopback
	}
	return reachOpen
}

// admit is the table. It answers one question -- may this authenticator
// serve on an address this exposed -- and it is the only place that answers
// it.
//
// The shape to read off it: `none` needs the kernel; `upstream` needs to be
// unreachable except through the proxy; `doorkey` carries its own proof and
// so is the only cell that survives exposure, and even then only with TLS
// terminated in front.
func admit(a Authn, r reach) error {
	switch a {
	case AuthnNone:
		if r == reachUnix {
			return nil
		}
		return fmt.Errorf(
			"authn=none on %s is an open door to a daemon that runs shell commands.\n"+
				"Anyone who can reach the port is an administrator.\n"+
				"Bind a unix socket, or choose an authenticator", r)

	case AuthnUpstream:
		switch r {
		case reachUnix:
			return nil
		case reachLoopback:
			// The house pattern: a reverse proxy on the same box strips
			// inbound Remote-* and forward-auths. Sound only while nothing
			// ELSE local is hostile -- see the doc comment on Config.
			return nil
		default:
			return fmt.Errorf(
				"authn=upstream trusts Remote-* headers, but %s is reachable off-host.\n"+
					"Anyone who can reach it can claim any identity, including admin.\n"+
					"Bind loopback or a unix socket and let the proxy reach you there", r)
		}

	case AuthnDoorkey:
		// A bearer proves something on every request, so exposure is a
		// transport-confidentiality question rather than an authentication
		// one. Config.Check gates the plaintext case separately.
		return nil

	default:
		names := make([]string, len(KnownAuthn))
		for i, k := range KnownAuthn {
			names[i] = string(k)
		}
		return fmt.Errorf("unknown authn %q: want one of %s", a, strings.Join(names, ", "))
	}
}
