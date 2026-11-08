package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

func (h *handlers) trustTraceparent(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if method != rpc.MethodQua || len(params) == 0 {
		return params, nil
	}
	var req rpc.QuaRequest
	if json.Unmarshal(params, &req) != nil || req.TraceParent == "" {
		return params, nil
	}
	if figOtel.ValidTraceParent(req.TraceParent) && h.callerIsOwner(ctx) {
		return params, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(params, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	for k := range raw {
		if strings.EqualFold(k, "traceparent") {
			delete(raw, k)
		}
	}
	return json.Marshal(raw)
}

func (h *handlers) callerIsOwner(ctx context.Context) bool {
	sub, err := authz.SubjectOf(ctx)
	return err == nil && authz.IsOwner(h.angelus.owner(), sub)
}
