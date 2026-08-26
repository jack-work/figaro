package angelus

// THE ORDERING INVARIANT, pinned on the real hub chain.
//
// middleware.Chain has its own order test, but that one proves the combinator
// composes left-to-right. This one proves the thing that MATTERS: on an aria
// endpoint, authorization runs before dressing, so a caller the policy refuses
// never reaches the step that reads outfit files off disk.
//
// It is a separate test from the Chain test because it can regress
// independently -- someone reorders two lines in hubFor and every Chain test
// still passes.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/middleware"
)

func TestAuthzRunsBeforeDressing(t *testing.T) {
	dressed := false
	routed := false

	dispatch := middleware.Chain(
		authz.Middleware(
			authz.AriaHeader{Enabled: false},
			authz.PolicyFunc(func(authz.Request) authz.Decision {
				return authz.Deny("no")
			}),
		),
		dressing(func(_ string, p json.RawMessage) (json.RawMessage, error) {
			dressed = true
			return p, nil
		}),
	)(func(context.Context, string, json.RawMessage) (any, error) {
		routed = true
		return nil, nil
	})

	_, err := dispatch(context.Background(), rpc.MethodQua, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("a denied request was not refused")
	}
	if dressed {
		t.Fatal("DRESSING RAN FOR A REFUSED CALLER. " +
			"Dressing resolves outfit names by reading files from disk, so it must sit " +
			"INSIDE the authorization middleware, never outside or beside it. " +
			"Check the Chain() argument order in hubFor.")
	}
	if routed {
		t.Fatal("a denied request reached route()")
	}
}

// And the allowed path must still dress: an ordering fix that refused
// everything would pass the test above and break the daemon.
func TestAllowedRequestIsStillDressed(t *testing.T) {
	var sawParams string
	dispatch := middleware.Chain(
		authz.Middleware(authz.AriaHeader{Enabled: false}, authz.AllowAll()),
		dressing(func(_ string, _ json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"dressed":true}`), nil
		}),
	)(func(_ context.Context, _ string, p json.RawMessage) (any, error) {
		sawParams = string(p)
		return nil, nil
	})

	if _, err := dispatch(context.Background(), rpc.MethodQua, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("allowed request refused: %v", err)
	}
	if sawParams != `{"dressed":true}` {
		t.Fatalf("route saw %q: dressing did not reach it", sawParams)
	}
}

// A dressing failure must surface as the caller's error, not be swallowed.
// This is the `fig form outfit test` case: a spec naming nothing on disk has
// to fail at the boundary rather than land as a literal on a board.
func TestDressingFailureStopsTheChain(t *testing.T) {
	routed := false
	dispatch := middleware.Chain(
		authz.Middleware(authz.AriaHeader{Enabled: false}, authz.AllowAll()),
		dressing(func(_ string, _ json.RawMessage) (json.RawMessage, error) {
			return nil, errNoSuchOutfit
		}),
	)(func(context.Context, string, json.RawMessage) (any, error) {
		routed = true
		return nil, nil
	})

	_, err := dispatch(context.Background(), rpc.MethodQua, json.RawMessage(`{}`))
	if err != errNoSuchOutfit {
		t.Fatalf("dressing error not surfaced: %v", err)
	}
	if routed {
		t.Fatal("a request with an unresolvable outfit still reached route()")
	}
}

var errNoSuchOutfit = &outfitTestError{}

type outfitTestError struct{}

func (*outfitTestError) Error() string { return "no such outfit" }
