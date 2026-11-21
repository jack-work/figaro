package angelus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

var methodsTakingAPatch = map[string]bool{
	rpc.MethodSet:        true,
	rpc.MethodCreate:     true,
	rpc.MethodFork:       true,
	rpc.MethodFormCreate: true,
}

func refusingShapelessPatches(method string, next jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	if !methodsTakingAPatch[method] {
		return next
	}
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		var p struct {
			Patch json.RawMessage `json:"patch"`
		}
		if json.Unmarshal(params, &p) == nil {
			if err := form.CheckPatchJSON(p.Patch); err != nil {
				return nil, fmt.Errorf("%s: %w", method, err)
			}
		}
		return next(ctx, params)
	}
}
