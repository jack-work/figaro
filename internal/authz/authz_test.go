package authz

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/jkrpc"
)

func forkParams(t *testing.T, target, caller string) json.RawMessage {
	t.Helper()
	raw, err := rpc.WithCaller(rpc.ForkRequest{FigaroID: target}, caller, nil)
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	return raw
}

// The switch IS the feature. Without it FIGARO_ARIA is ambient trivia the
// server has no standing to doubt; with it, "was this authenticated" is a real
// question with two answers.
func TestAriaHeaderIsDisableable(t *testing.T) {
	params := forkParams(t, "aria0001", "aria0001")

	on := AriaHeader{Enabled: true}.Authenticate(rpc.MethodFork, params)
	if !on.Authenticated || on.FigaroID != "aria0001" {
		t.Fatalf("enabled: got %+v, want authenticated aria0001", on)
	}

	// Disabled must be anonymous EVEN THOUGH a valid credential was presented.
	off := AriaHeader{Enabled: false}.Authenticate(rpc.MethodFork, params)
	if off.Authenticated || !off.Anonymous() {
		t.Fatalf("disabled: got %+v, want anonymous", off)
	}
}

func TestAriaHeaderRejectsAbsentAndMalformed(t *testing.T) {
	a := AriaHeader{Enabled: true}
	for name, params := range map[string]json.RawMessage{
		"nil":       nil,
		"no key":    json.RawMessage(`{"figaro_id":"x"}`),
		"traversal": json.RawMessage(`{"x-internal-figaro-id":"../../etc"}`),
	} {
		if id := a.Authenticate(rpc.MethodFork, params); id.Authenticated {
			t.Fatalf("%s: authenticated %+v, want anonymous", name, id)
		}
	}
}

// Authenticated == FigaroID != "" is TEMPTING AND WRONG: a disabled
// authenticator yields anonymous with a credential on the wire, and that
// difference is what makes the switch auditable.
func TestIdentityDistinguishesAnonymousFromUnauthenticated(t *testing.T) {
	if (Identity{}).String() != "anonymous" {
		t.Fatal("zero identity should render as anonymous")
	}
	if !(Identity{}).Anonymous() {
		t.Fatal("zero identity should be anonymous")
	}
	if (Identity{FigaroID: "a"}).Anonymous() {
		t.Fatal("identity with an id should not be anonymous")
	}
}

func TestSelfTargetedNeedsAnIdentity(t *testing.T) {
	// Anonymous must never be self-targeted: with no identity there is no self,
	// and "" == "" would deny every anonymous call to a targetless method.
	anon := Request{Identity: Identity{}, Method: rpc.MethodFork, Params: forkParams(t, "", "")}
	if anon.SelfTargeted() {
		t.Fatal("anonymous request reported as self-targeted")
	}
	self := Request{
		Identity: Identity{FigaroID: "aria0001", Authenticated: true},
		Method:   rpc.MethodFork,
		Params:   forkParams(t, "aria0001", "aria0001"),
	}
	if !self.SelfTargeted() {
		t.Fatal("self-fork not detected")
	}
	other := Request{
		Identity: Identity{FigaroID: "aria0001", Authenticated: true},
		Method:   rpc.MethodFork,
		Params:   forkParams(t, "aria0002", "aria0001"),
	}
	if other.SelfTargeted() {
		t.Fatal("cross-aria fork reported as self-targeted")
	}
}

// The rule has three necessary conditions. Each is a case where forking is
// LEGITIMATE and must not be denied.
func TestNoSelfForkDuringTurn(t *testing.T) {
	busy := func(string) bool { return true }
	idle := func(string) bool { return false }

	authed := Identity{FigaroID: "aria0001", Authenticated: true}

	t.Run("denies self-fork while the turn runs", func(t *testing.T) {
		d := NoSelfForkDuringTurn(busy).Check(Request{
			Identity: authed, Method: rpc.MethodFork,
			Params: forkParams(t, "aria0001", "aria0001"),
		})
		if d.Allow {
			t.Fatal("want deny")
		}
		// The reason must be actionable, not a label.
		for _, want := range []string{"deadlock", "setsid", "FIGARO_ARIA", "aria0001", "fork.json"} {
			if !strings.Contains(d.Reason, want) {
				t.Fatalf("reason missing %q:\n%s", want, d.Reason)
			}
		}
	})

	t.Run("allows self-fork when idle", func(t *testing.T) {
		// This is exactly what the detached workaround does; denying it would
		// make the advice the error gives impossible to follow.
		d := NoSelfForkDuringTurn(idle).Check(Request{
			Identity: authed, Method: rpc.MethodFork,
			Params: forkParams(t, "aria0001", "aria0001"),
		})
		if !d.Allow {
			t.Fatalf("want allow, got %q", d.Reason)
		}
	})

	t.Run("allows a different aria forking this one", func(t *testing.T) {
		// The caller's tool call blocks, but the TARGET's drain loop is free.
		d := NoSelfForkDuringTurn(busy).Check(Request{
			Identity: authed, Method: rpc.MethodFork,
			Params: forkParams(t, "aria0002", "aria0001"),
		})
		if !d.Allow {
			t.Fatalf("want allow, got %q", d.Reason)
		}
	})

	t.Run("allows anonymous callers", func(t *testing.T) {
		// A human or external script is not inside the turn.
		d := NoSelfForkDuringTurn(busy).Check(Request{
			Identity: Identity{}, Method: rpc.MethodFork,
			Params: forkParams(t, "aria0001", ""),
		})
		if !d.Allow {
			t.Fatalf("want allow, got %q", d.Reason)
		}
	})

	t.Run("ignores other methods", func(t *testing.T) {
		d := NoSelfForkDuringTurn(busy).Check(Request{
			Identity: authed, Method: rpc.MethodKill,
			Params: forkParams(t, "aria0001", "aria0001"),
		})
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
	// A nil Check must be skipped, not panic, a half-built table is a
	// configuration mistake, not a reason to take the daemon down.
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
	handlers := map[string]jkrpc.HandlerFunc{
		rpc.MethodFork: mk("fork"),
		rpc.MethodKill: mk("kill"),
	}

	guarded := Guard(handlers, AriaHeader{Enabled: true},
		Rules{NoSelfForkDuringTurn(func(string) bool { return true })})
	if len(guarded) != len(handlers) {
		t.Fatalf("guarded %d methods, served %d", len(guarded), len(handlers))
	}

	ctx := context.Background()
	// A denial must surface as a typed JSON-RPC error carrying the prose, and
	// must NOT reach the handler.
	_, err := guarded[rpc.MethodFork](ctx, forkParams(t, "aria0001", "aria0001"))
	if err == nil {
		t.Fatal("want denial")
	}
	var jerr *jkrpc.Error
	if !errors.As(err, &jerr) {
		t.Fatalf("error type = %T, want *jkrpc.Error", err)
	}
	if jerr.Code != ErrCode {
		t.Fatalf("code = %d, want %d", jerr.Code, ErrCode)
	}
	if !strings.Contains(jerr.Message, "deadlock") {
		t.Fatalf("message lost the reason: %s", jerr.Message)
	}
	if called["fork"] != 0 {
		t.Fatal("denied request reached the handler")
	}

	// An allowed request passes through untouched.
	if _, err := guarded[rpc.MethodKill](ctx, forkParams(t, "aria0001", "aria0001")); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if called["kill"] != 1 {
		t.Fatalf("kill called %d times, want 1", called["kill"])
	}
}

// Default config must be behaviour-neutral: nothing gated until something opts
// in.
func TestGuardWithDefaultsChangesNothing(t *testing.T) {
	var reached bool
	handlers := map[string]jkrpc.HandlerFunc{
		rpc.MethodFork: func(context.Context, json.RawMessage) (any, error) {
			reached = true
			return "ok", nil
		},
	}
	guarded := Guard(handlers, AriaHeader{Enabled: false}, AllowAll())
	if _, err := guarded[rpc.MethodFork](context.Background(),
		forkParams(t, "aria0001", "aria0001")); err != nil {
		t.Fatalf("default config denied: %v", err)
	}
	if !reached {
		t.Fatal("handler not reached under default config")
	}
}
