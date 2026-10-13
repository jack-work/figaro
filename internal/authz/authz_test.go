package authz

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

var user = rpc.Principal{Kind: rpc.PrincipalUser, ID: "1000"}

func forkParams(t *testing.T, target, caller string) json.RawMessage {
	t.Helper()
	raw, err := rpc.WithCaller(rpc.ForkRequest{FigaroID: target}, caller, nil)
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	return raw
}

// The params field is attribution: it lands in FigaroID and never touches the
// principal, which only the connection can set.
func TestAriaHeaderNeverPromotesParamsToAPrincipal(t *testing.T) {
	id := AriaHeader{}.Authenticate(user, forkParams(t, "aria0001", "aria0001"))
	if id.FigaroID != "aria0001" || id.Principal != user {
		t.Fatalf("got %+v, want asserted aria0001 under %+v", id, user)
	}
	anon := AriaHeader{}.Authenticate(rpc.Principal{}, forkParams(t, "aria0001", "aria0001"))
	if anon.Principal != (rpc.Principal{}) {
		t.Fatalf("a params field minted a principal: %+v", anon)
	}
}

func TestAriaHeaderRejectsAbsentAndMalformed(t *testing.T) {
	for name, params := range map[string]json.RawMessage{
		"nil":       nil,
		"no key":    json.RawMessage(`{"figaro_id":"x"}`),
		"traversal": json.RawMessage(`{"x-internal-figaro-id":"../../etc"}`),
	} {
		if id := (AriaHeader{}).Authenticate(user, params); id.FigaroID != "" {
			t.Fatalf("%s: got %+v, want no aria", name, id)
		}
	}
}

func TestSelfTargetedNeedsAnIdentity(t *testing.T) {
	// With no aria there is no self, and "" == "" would deny every anonymous
	// call to a targetless method.
	anon := Request{Method: rpc.MethodFork, Params: forkParams(t, "", "")}
	if anon.SelfTargeted() {
		t.Fatal("anonymous request reported as self-targeted")
	}
	self := Request{Identity: Identity{FigaroID: "aria0001"}, Method: rpc.MethodFork, Params: forkParams(t, "aria0001", "aria0001")}
	if !self.SelfTargeted() {
		t.Fatal("self-fork not detected")
	}
	other := Request{Identity: Identity{FigaroID: "aria0001"}, Method: rpc.MethodFork, Params: forkParams(t, "aria0002", "aria0001")}
	if other.SelfTargeted() {
		t.Fatal("cross-aria fork reported as self-targeted")
	}
}

// Each case is a LEGITIMATE fork that must not be denied.
func TestNoSelfForkDuringTurn(t *testing.T) {
	busy := func(string) bool { return true }
	idle := func(string) bool { return false }
	aria := Identity{Principal: user, FigaroID: "aria0001"}

	t.Run("denies self-fork while the turn runs", func(t *testing.T) {
		d := NoSelfForkDuringTurn(busy).Check(Request{Identity: aria, Method: rpc.MethodFork, Params: forkParams(t, "aria0001", "aria0001")})
		if d.Allow {
			t.Fatal("want deny")
		}
		for _, want := range []string{"deadlock", "setsid", "FIGARO_ARIA", "aria0001", "fork.json"} {
			if !strings.Contains(d.Reason, want) {
				t.Fatalf("reason missing %q:\n%s", want, d.Reason)
			}
		}
	})
	t.Run("allows self-fork when idle", func(t *testing.T) {
		d := NoSelfForkDuringTurn(idle).Check(Request{Identity: aria, Method: rpc.MethodFork, Params: forkParams(t, "aria0001", "aria0001")})
		if !d.Allow {
			t.Fatalf("want allow, got %q", d.Reason)
		}
	})
	t.Run("allows a different aria forking this one", func(t *testing.T) {
		d := NoSelfForkDuringTurn(busy).Check(Request{Identity: aria, Method: rpc.MethodFork, Params: forkParams(t, "aria0002", "aria0001")})
		if !d.Allow {
			t.Fatalf("want allow, got %q", d.Reason)
		}
	})
	t.Run("allows anonymous callers", func(t *testing.T) {
		d := NoSelfForkDuringTurn(busy).Check(Request{Identity: Identity{Principal: user}, Method: rpc.MethodFork, Params: forkParams(t, "aria0001", "")})
		if !d.Allow {
			t.Fatalf("want allow, got %q", d.Reason)
		}
	})
	t.Run("ignores other methods", func(t *testing.T) {
		d := NoSelfForkDuringTurn(busy).Check(Request{Identity: aria, Method: rpc.MethodKill, Params: forkParams(t, "aria0001", "aria0001")})
		if !d.Allow {
			t.Fatalf("want allow, got %q", d.Reason)
		}
	})
}

func TestRulesFirstDenialWins(t *testing.T) {
	deny := Rule{Name: "deny", Check: func(Request) Decision { return Deny("first") }}
	deny2 := Rule{Name: "deny2", Check: func(Request) Decision { return Deny("second") }}
	if d := (Rules{deny, deny2}).Check(Request{}); d.Reason != "first" {
		t.Fatalf("reason = %q, want first", d.Reason)
	}
	if d := (Rules{}).Check(Request{}); !d.Allow {
		t.Fatal("empty table should allow")
	}
	if d := (Rules{{Name: "nil"}, deny}).Check(Request{}); d.Reason != "first" {
		t.Fatalf("nil rule not skipped: %+v", d)
	}
}

// Guard wraps the MAP so the guarded set is the served set by construction.
func TestGuardAppliesToEveryMethod(t *testing.T) {
	called := map[string]int{}
	mk := func(name string) jkrpc.HandlerFunc {
		return func(context.Context, json.RawMessage) (any, error) {
			called[name]++
			return "ok", nil
		}
	}
	handlers := map[string]jkrpc.HandlerFunc{rpc.MethodFork: mk("fork"), rpc.MethodKill: mk("kill")}
	guarded := Guard(handlers, AriaHeader{}, Rules{NoSelfForkDuringTurn(func(string) bool { return true })})
	if len(guarded) != len(handlers) {
		t.Fatalf("guarded %d methods, served %d", len(guarded), len(handlers))
	}
	ctx := context.Background()
	_, err := guarded[rpc.MethodFork](ctx, forkParams(t, "aria0001", "aria0001"))
	var jerr *jkrpc.Error
	if !errors.As(err, &jerr) {
		t.Fatalf("error = %v (%T), want *jkrpc.Error", err, err)
	}
	if jerr.Code != ErrCode || !strings.Contains(jerr.Message, "deadlock") {
		t.Fatalf("denial = %+v", jerr)
	}
	if called["fork"] != 0 {
		t.Fatal("denied request reached the handler")
	}
	if _, err := guarded[rpc.MethodKill](ctx, forkParams(t, "aria0001", "aria0001")); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if called["kill"] != 1 {
		t.Fatalf("kill called %d times, want 1", called["kill"])
	}
}

func TestGuardHandsTheConnectionPrincipalToThePolicy(t *testing.T) {
	var seen Identity
	policy := PolicyFunc(func(r Request) Decision { seen = r.Identity; return Allow() })
	guarded := Guard(map[string]jkrpc.HandlerFunc{
		rpc.MethodFork: func(context.Context, json.RawMessage) (any, error) { return "ok", nil },
	}, AriaHeader{}, policy)
	ctx := context.WithValue(context.Background(), principalKey{}, user)
	if _, err := guarded[rpc.MethodFork](ctx, forkParams(t, "aria0001", "aria0002")); err != nil {
		t.Fatal(err)
	}
	if seen.Principal != user || seen.FigaroID != "aria0002" {
		t.Fatalf("policy saw %+v", seen)
	}
}
