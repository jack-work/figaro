package angelus

// THE GUARDED SET MUST EQUAL THE SERVED SET.
//
// Before 2026-08-25 it did not. authz.Guard wrapped the angelus map, and the
// per-aria hub socket beside it served figaro.qua, figaro.set, figaro.study,
// figaro.cast, figaro.drop, figaro.interrupt and the queue verbs with NO
// policy consulted at all -- the entire agency surface, ungated, on a door a
// gateway was about to expose.
//
// These tests fail if that regresses, and they fail by ENUMERATION rather
// than by naming the methods they know about, so a method added later cannot
// slip through by being absent from a list somebody forgot to update.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/middleware"
	"github.com/jack-work/jkrpc"
)

// TestHubHandlersAreGuarded walks every method the aria endpoint serves and
// asserts the policy saw it. Enumerated from figaro.AgentMethods(), so a new
// agent method is covered the moment it exists.
func TestHubHandlersAreGuarded(t *testing.T) {
	var seen []string
	hb := newAriaHub("test", "")
	hb.dispatch = middleware.Chain(
		authz.Middleware(authz.AriaHeader{Enabled: false},
			authz.PolicyFunc(func(r authz.Request) authz.Decision {
				seen = append(seen, r.Method)
				return authz.Deny("policy consulted")
			})),
	)(hb.route)

	served := hb.handlers()
	methods := figaro.AgentMethods()
	if len(served) != len(methods) {
		t.Fatalf("hub serves %d methods but AgentMethods lists %d", len(served), len(methods))
	}

	for _, m := range methods {
		h, ok := served[m]
		if !ok {
			t.Fatalf("%s is in AgentMethods but the hub does not serve it", m)
		}
		// The handler must refuse WITHOUT reaching route(): a guard that ran
		// after dispatch would have already done the thing it was meant to
		// prevent. hb has no wake/read/write here, so a leaked call panics or
		// errors differently than a policy denial.
		_, err := h(context.Background(), json.RawMessage(`{}`))
		if err == nil {
			t.Fatalf("%s: denied policy still returned success", m)
		}
		var jerr *jkrpc.Error
		if !errorsAs(err, &jerr) || jerr.Code != authz.ErrCode {
			t.Fatalf("%s: want an authz refusal, got %v", m, err)
		}
	}

	if len(seen) != len(methods) {
		t.Fatalf("policy saw %d methods, hub serves %d: %v", len(seen), len(methods), seen)
	}
}

// TestAgencyMethodsAreOnTheGuardedDoor is the regression this whole file
// exists for. These are the methods that grant AGENCY -- they run turns,
// write boards, and rewire what an aria observes -- and each must be served
// by the hub, which is now guarded. A method that quietly moved to an
// unguarded door would pass every other test in the tree.
func TestAgencyMethodsAreOnTheGuardedDoor(t *testing.T) {
	agency := []string{
		rpc.MethodQua,
		rpc.MethodSet,
		rpc.MethodStudy,
		rpc.MethodDrop,
		rpc.MethodCast,
		rpc.MethodInterrupt,
		rpc.MethodQueueUpdate,
		rpc.MethodQueueDelete,
	}
	served := map[string]bool{}
	for _, m := range figaro.AgentMethods() {
		served[m] = true
	}
	for _, m := range agency {
		if !served[m] {
			t.Errorf("%s grants agency but is not on the aria endpoint: "+
				"if it moved, confirm its new door is guarded", m)
		}
	}
}

// A nil dispatch falls back to bare route. That is kept ONLY so a test can
// build a hub without a policy, and it is pinned here so nobody mistakes it
// for the production path: hubFor always composes a chain.
func TestNilDispatchFallsBackToBareRoute(t *testing.T) {
	hb := newAriaHub("test", "")
	if hb.dispatch != nil {
		t.Fatal("a freshly built hub should carry no dispatch until hubFor sets one")
	}
	if len(hb.handlers()) != len(figaro.AgentMethods()) {
		t.Fatal("an unguarded hub should still serve the full method set")
	}
}

func errorsAs(err error, target **jkrpc.Error) bool {
	e, ok := err.(*jkrpc.Error)
	if ok {
		*target = e
	}
	return ok
}
