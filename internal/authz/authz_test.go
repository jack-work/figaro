package authz

import (
	"context"
	"encoding/json"
	"errors"
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

// The params field is attribution; only the connection sets the principal.
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

// Guard wraps the map, so the guarded set is the served set by construction.
func TestGuardAppliesToEveryMethod(t *testing.T) {
	called := map[string]int{}
	mk := func(name string) jkrpc.HandlerFunc {
		return func(context.Context, json.RawMessage) (any, error) {
			called[name]++
			return "ok", nil
		}
	}
	handlers := map[string]jkrpc.HandlerFunc{rpc.MethodFork: mk("fork"), rpc.MethodKill: mk("kill")}
	denyFork := PolicyFunc(func(r Request) Decision {
		if r.Method == rpc.MethodFork {
			return Deny("no forks for %s", r.Identity.FigaroID)
		}
		return Allow()
	})
	guarded := Guard(handlers, AriaHeader{}, denyFork)
	if len(guarded) != len(handlers) {
		t.Fatalf("guarded %d methods, served %d", len(guarded), len(handlers))
	}
	ctx := context.Background()
	_, err := guarded[rpc.MethodFork](ctx, forkParams(t, "aria0001", "aria0001"))
	var jerr *jkrpc.Error
	if !errors.As(err, &jerr) {
		t.Fatalf("error = %v (%T), want *jkrpc.Error", err, err)
	}
	if jerr.Code != ErrCode || jerr.Message != "no forks for aria0001" {
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
	ctx := WithPrincipal(context.Background(), user)
	if _, err := guarded[rpc.MethodFork](ctx, forkParams(t, "aria0001", "aria0002")); err != nil {
		t.Fatal(err)
	}
	if seen.Principal != user || seen.FigaroID != "aria0002" {
		t.Fatalf("policy saw %+v", seen)
	}
}
