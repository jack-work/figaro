package angelus

// DRESSING AS MIDDLEWARE.
//
// Resolving a request's outfit NAMES into keys is the API boundary's one
// materialization point: whatever the request reaches next -- a live agent's
// inbox, the store's agentless writer -- receives pure data, and no writer
// below this line reads a file.
//
// It used to be four lines at the top of ariaHub.route, which made it
// invisible as a concern and impossible to order relative to anything else.
// As middleware, "authorization runs before dressing" is a fact about the
// Chain() argument list rather than a fact about where a call happens to sit
// inside a function -- and that ordering matters, because dressing is the
// step that touches the filesystem.

import (
	"context"
	"encoding/json"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/middleware"
)

// DressFunc resolves outfit names in a request's params. A request naming no
// outfit is returned byte for byte.
type DressFunc func(method string, params json.RawMessage) (json.RawMessage, error)

// dressing rewrites params before the request is routed. A spec that names
// nothing on disk fails HERE, at the boundary, rather than landing as a
// literal on a board -- which is exactly what `fig form outfit test` did
// while the hub's writer applied patches verbatim.
func dressing(dress DressFunc) middleware.Middleware[rpc.MethodHandler] {
	return func(next rpc.MethodHandler) rpc.MethodHandler {
		if dress == nil {
			return next
		}
		return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			dressed, err := dress(method, params)
			if err != nil {
				return nil, err
			}
			return next(ctx, method, dressed)
		}
	}
}
