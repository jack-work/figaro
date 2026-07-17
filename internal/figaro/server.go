package figaro

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/jkrpc"
)

// AgentServer is the figaro-side JSON-RPC contract.
type AgentServer interface {
	Handle(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// agentMethods is the set of methods the figaro socket exposes.
var agentMethods = []string{
	rpc.MethodQua,
	rpc.MethodContext,
	rpc.MethodInterrupt,
	rpc.MethodSet,
	rpc.MethodLoadout,
	rpc.MethodChalkboard,
	rpc.MethodRead,
}

// buildHandlers wires AgentServer.Handle into the jsonrpc handler map.
func buildHandlers(srv AgentServer) map[string]jkrpc.HandlerFunc {
	handlers := make(map[string]jkrpc.HandlerFunc, len(agentMethods))
	for _, m := range agentMethods {
		method := m
		handlers[method] = func(ctx context.Context, params json.RawMessage) (any, error) {
			return srv.Handle(ctx, method, params)
		}
	}
	return handlers
}

// Handle dispatches RPC methods.
func (a *Agent) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case rpc.MethodQua:
		var req rpc.QuaRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		cursor := a.ariaSrv.LastCommittedLT()
		a.SubmitPrompt(req)
		return rpc.QuaResponse{OK: true, Cursor: cursor}, nil

	case rpc.MethodContext:
		msgs := a.Context()
		out := make([]any, len(msgs))
		for i, m := range msgs {
			out[i] = m
		}
		return rpc.ContextResponse{Messages: out, Metrics: a.sessionMetrics()}, nil

	case rpc.MethodInterrupt:
		a.Interrupt()
		return rpc.InterruptResponse{OK: true}, nil

	case rpc.MethodSet:
		var req rpc.SetRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		patch := chalkboard.Patch{Set: req.Patch.Set, Remove: req.Patch.Remove}
		set, removed, err := a.Set(patch)
		if err != nil {
			return nil, err
		}
		return rpc.SetResponse{OK: true, Set: set, Remove: removed}, nil

	case rpc.MethodLoadout:
		var req rpc.LoadoutRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		set, err := a.ApplyLoadout(req.Name)
		if err != nil {
			return nil, err
		}
		return rpc.LoadoutResponse{OK: true, Set: set}, nil

	case rpc.MethodChalkboard:
		return rpc.ChalkboardResponse{Snapshot: a.Snapshot()}, nil

	case rpc.MethodRead:
		var req rpc.ReadRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		if req.Before > 0 {
			return a.ReadBefore(req.Before, req.Limit), nil
		}
		return a.Read(req.SinceLT), nil
	}
	return nil, fmt.Errorf("unknown method: %s", method)
}
