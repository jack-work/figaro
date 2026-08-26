package authz

// THE AUTHORIZATION MIDDLEWARE: the one place a request is checked.
//
// Guard (below) is the same decision applied to a jkrpc handler map, and it
// is implemented in terms of this so there are not two policy paths that can
// disagree. Whichever door a method is served on, the decision is made here.

import (
	"context"
	"encoding/json"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/middleware"
	"github.com/jack-work/jkrpc"
)

// Middleware authenticates the caller and consults the policy before the
// request reaches anything else.
//
// IT MUST BE OUTERMOST. Everything it wraps is a thing an unauthorized caller
// should not be able to reach -- most sharply the dressing middleware, which
// resolves outfit names by reading files from disk. See middleware.Chain on
// why the order is tested rather than commented.
//
// Neither argument is nilable. "No authentication" is AriaHeader{Enabled:
// false} and "no policy" is AllowAll(), both of which someone has to write.
func Middleware(authn Authenticator, policy Policy) middleware.Middleware[rpc.MethodHandler] {
	if authn == nil {
		authn = AriaHeader{Enabled: false}
	}
	if policy == nil {
		policy = AllowAll()
	}
	return func(next rpc.MethodHandler) rpc.MethodHandler {
		return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			req := Request{
				Identity: authn.Authenticate(method, params),
				Method:   method,
				Params:   params,
			}
			if d := policy.Check(req); !d.Allow {
				// Short-circuit: next is never called, so a refusal PREVENTS
				// rather than merely observes.
				return nil, &jkrpc.Error{Code: ErrCode, Message: d.Reason}
			}
			return next(ctx, method, params)
		}
	}
}

// Guard applies Middleware to a map of jkrpc handlers, for the doors that are
// still shaped as maps (the angelus socket). It is an adapter, not a second
// implementation: the decision comes from Middleware either way.
func Guard(handlers map[string]jkrpc.HandlerFunc, authn Authenticator, policy Policy) map[string]jkrpc.HandlerFunc {
	mw := Middleware(authn, policy)
	out := make(map[string]jkrpc.HandlerFunc, len(handlers))
	for name, h := range handlers {
		method, next := name, h
		// Lift the map's handler into a MethodHandler (it already knows its
		// own method, so the name is dropped), decorate, and lower it back.
		guarded := mw(func(ctx context.Context, _ string, params json.RawMessage) (any, error) {
			return next(ctx, params)
		})
		out[method] = func(ctx context.Context, params json.RawMessage) (any, error) {
			return guarded(ctx, method, params)
		}
	}
	return out
}
