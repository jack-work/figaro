package rpc

// THE SERVING SHAPE: one handler that is TOLD which method.
//
// The alternative -- and what figaro had -- is a map of per-method closures,
// each having captured its own name. That shape cannot be decorated: to wrap
// it you must transform the whole map, which makes the unit of interception a
// map when the concern is a call. Every cross-cutting concern in the daemon
// (authorization, dressing, and the gateway's envelope rewriting) then grows
// its own spelling, and one of them ends up hand-inlined at the top of a
// dispatch function.
//
// With the method in the signature, all three are middleware.Middleware
// values over this one type, composed by one Chain, in an order that is
// readable at the call site.

import (
	"context"
	"encoding/json"
)

// MethodHandler serves one RPC call. It lives here, beside the method names
// it dispatches on, because both the authorization seam and the daemon need
// it and neither should have to import the other to say what a handler is.
type MethodHandler func(ctx context.Context, method string, params json.RawMessage) (any, error)
