package gateway

// EVERY CELL OF THE TABLE.
//
// The point of a table is that it can be enumerated, so this enumerates it.
// A new authenticator or a new exposure class fails TestTableIsTotal until
// someone decides what the new cells mean, which is the only way a matrix
// like this stays honest as it grows.

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestRefusalTable(t *testing.T) {
	cases := []struct {
		authn Authn
		reach reach
		allow bool
		why   string
	}{
		// none: the kernel is the only gate it has.
		{AuthnNone, reachUnix, true, "filesystem permissions are a real gate"},
		{AuthnNone, reachLoopback, false, "every local uid is an admin"},
		{AuthnNone, reachOpen, false, "anonymous remote shell execution"},

		// upstream: believable only where nothing but the proxy can arrive.
		{AuthnUpstream, reachUnix, true, "only the proxy can reach the inode"},
		{AuthnUpstream, reachLoopback, true, "the house pattern: proxy on the same box"},
		{AuthnUpstream, reachOpen, false, "anyone can claim Remote-Groups: admin"},

		// doorkey: carries its own proof, so exposure is a TLS question and
		// Config.Check owns it, not this table.
		{AuthnDoorkey, reachUnix, true, "belt and braces"},
		{AuthnDoorkey, reachLoopback, true, "a credential on every request"},
		{AuthnDoorkey, reachOpen, true, "a credential on every request"},
	}

	for _, c := range cases {
		err := admit(c.authn, c.reach)
		if c.allow && err != nil {
			t.Errorf("admit(%s, %s) refused but should allow (%s): %v",
				c.authn, c.reach, c.why, err)
		}
		if !c.allow {
			if err == nil {
				t.Errorf("admit(%s, %s) ALLOWED but must refuse: %s", c.authn, c.reach, c.why)
				continue
			}
			// A refusal that does not say what to do instead is a wall.
			if !strings.Contains(err.Error(), "unix socket") &&
				!strings.Contains(err.Error(), "loopback") &&
				!strings.Contains(err.Error(), "authenticator") {
				t.Errorf("admit(%s, %s) refuses without a remedy: %v", c.authn, c.reach, err)
			}
		}
	}
}

// If someone adds an Authn or a reach, this fails until the table above
// covers the new cells. That is the entire value of writing it as a table.
func TestTableIsTotal(t *testing.T) {
	reaches := []reach{reachUnix, reachLoopback, reachOpen}
	if len(KnownAuthn)*len(reaches) != 9 {
		t.Fatalf("the table has %d cells; TestRefusalTable enumerates 9. "+
			"Add the new cells there, deliberately, rather than letting them default.",
			len(KnownAuthn)*len(reaches))
	}
	for _, a := range KnownAuthn {
		for _, r := range reaches {
			// Must not panic and must give a definite answer either way.
			_ = admit(a, r)
		}
	}
	if err := admit(Authn("invented"), reachUnix); err == nil {
		t.Fatal("an unknown authenticator was admitted: it must refuse")
	}
}

// THE CLASSIFIER IS THE LOAD-BEARING PART. A config-string check gets every
// one of these wrong, which is why the decision is made against a bound
// listener instead.
func TestClassifyJudgesTheBoundAddressNotTheString(t *testing.T) {
	t.Run("unix", func(t *testing.T) {
		sock := t.TempDir() + "/s.sock"
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Skip(err)
		}
		defer ln.Close()
		if got := classify(ln); got != reachUnix {
			t.Fatalf("unix socket classified as %s", got)
		}
	})

	t.Run("explicit v4 loopback", func(t *testing.T) {
		ln := mustListen(t, "127.0.0.1:0")
		defer ln.Close()
		if got := classify(ln); got != reachLoopback {
			t.Fatalf("127.0.0.1 classified as %s", got)
		}
	})

	t.Run("v6 loopback", func(t *testing.T) {
		ln, err := net.Listen("tcp", "[::1]:0")
		if err != nil {
			t.Skip("no ipv6 loopback here")
		}
		defer ln.Close()
		if got := classify(ln); got != reachLoopback {
			t.Fatalf("::1 classified as %s -- IsLoopback must be asked of a parsed IP", got)
		}
	})

	// THE ONE A STRING CHECK ALWAYS MISSES. ":0" and "0.0.0.0:0" bind every
	// interface including the routable ones, but neither string contains
	// anything a naive check would flag.
	t.Run("bare port is wildcard, not loopback", func(t *testing.T) {
		ln := mustListen(t, ":0")
		defer ln.Close()
		if got := classify(ln); got != reachOpen {
			t.Fatalf(`":0" classified as %s -- a bare port binds EVERY interface`, got)
		}
	})

	t.Run("0.0.0.0 is open", func(t *testing.T) {
		ln := mustListen(t, "0.0.0.0:0")
		defer ln.Close()
		if got := classify(ln); got != reachOpen {
			t.Fatalf("0.0.0.0 classified as %s", got)
		}
	})
}

func mustListen(t *testing.T, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("cannot bind %s: %v", addr, err)
	}
	return ln
}

// The refusal has to happen at STARTUP, and the listener must not be left
// open when it does. A server that refuses but keeps the port bound has
// refused nothing.
func TestServeRefusesAndClosesTheListener(t *testing.T) {
	sock := t.TempDir() + "/angelus.sock"
	cfg := Config{
		Listen:        "tcp://0.0.0.0:0",
		AngelusSocket: sock,
		Authn:         AuthnNone,
	}.Defaults()

	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New should accept this: exposure is judged at Serve. got %v", err)
	}
	err = srv.Serve(t.Context())
	if err == nil {
		t.Fatal("authn=none on 0.0.0.0 was served")
	}
	if !strings.Contains(err.Error(), "open door") {
		t.Fatalf("refusal does not explain itself: %v", err)
	}
}

func TestDoorkeyValidation(t *testing.T) {
	base := Config{AngelusSocket: "/tmp/x.sock", Authn: AuthnDoorkey}

	t.Run("empty secret refuses", func(t *testing.T) {
		c := base
		c.Listen = "unix:///tmp/gw.sock"
		if err := c.Defaults().Check(); err == nil {
			t.Fatal("an empty doorkey authenticated everyone")
		}
	})

	t.Run("short secret refuses", func(t *testing.T) {
		c := base
		c.Listen, c.Doorkey = "unix:///tmp/gw.sock", "hunter2"
		if err := c.Defaults().Check(); err == nil {
			t.Fatal("a 7-byte doorkey was accepted")
		}
	})

	t.Run("plaintext tcp refuses without a TLS promise", func(t *testing.T) {
		c := base
		c.Listen, c.Doorkey = "tcp://0.0.0.0:9099", strings.Repeat("k", 32)
		err := c.Defaults().Check()
		if err == nil {
			t.Fatal("a bearer token was about to cross plaintext TCP")
		}
		if !strings.Contains(err.Error(), "plaintext") {
			t.Fatalf("wrong refusal: %v", err)
		}
	})

	t.Run("tcp is fine once TLS is asserted", func(t *testing.T) {
		c := base
		c.Listen, c.Doorkey, c.TLSTerminated = "tcp://0.0.0.0:9099", strings.Repeat("k", 32), true
		if err := c.Defaults().Check(); err != nil {
			t.Fatalf("refused a TLS-fronted doorkey: %v", err)
		}
	})
}

func TestUnknownAuthnRefuses(t *testing.T) {
	c := Config{Listen: "unix:///tmp/gw.sock", AngelusSocket: "/tmp/a.sock", Authn: "sudo"}
	err := c.Check()
	if err == nil {
		t.Fatal("an invented authenticator was accepted")
	}
	for _, k := range KnownAuthn {
		if !strings.Contains(err.Error(), string(k)) {
			t.Errorf("refusal does not list %q as an option: %v", k, err)
		}
	}
}

// Defaults must not invent permissiveness. Keepalive gets a value because
// silence kills a tunnel; authn gets the most restrictive one.
func TestDefaultsAreConservative(t *testing.T) {
	c := Config{}.Defaults()
	if c.Authn != AuthnNone {
		t.Fatalf("default authn is %q", c.Authn)
	}
	if c.Keepalive != 30*time.Second {
		t.Fatalf("default keepalive is %v; Cloudflare reaps at ~100s", c.Keepalive)
	}
	// And the default authn must then be unable to serve anything exposed.
	if admit(c.Authn, reachOpen) == nil {
		t.Fatal("the DEFAULT configuration would serve an open port")
	}
}
