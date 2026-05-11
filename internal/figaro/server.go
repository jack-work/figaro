package figaro

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/jsonrpc"
	"github.com/jack-work/figaro/internal/rpc"
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
	rpc.MethodReloadConfig,
	rpc.MethodSet,
	rpc.MethodChalkboard,
}

// buildHandlers wires AgentServer.Handle into the jsonrpc handler map.
func buildHandlers(srv AgentServer) map[string]jsonrpc.HandlerFunc {
	handlers := make(map[string]jsonrpc.HandlerFunc, len(agentMethods))
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
		a.SubmitPrompt(req)
		return rpc.QuaResponse{OK: true}, nil

	case rpc.MethodContext:
		msgs := a.Context()
		out := make([]any, len(msgs))
		for i, m := range msgs {
			out[i] = m
		}
		return rpc.ContextResponse{Messages: out}, nil

	case rpc.MethodInterrupt:
		a.Interrupt()
		return rpc.InterruptResponse{OK: true}, nil

	case rpc.MethodReloadConfig:
		var req rpc.ReloadConfigRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		set, removed, applied, err := a.Rehydrate(req.DryRun)
		if err != nil {
			return nil, err
		}
		return rpc.ReloadConfigResponse{Applied: applied, SetKeys: set, RemoveKeys: removed}, nil

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

	case rpc.MethodChalkboard:
		return rpc.ChalkboardResponse{Snapshot: a.Snapshot()}, nil
	}
	return nil, fmt.Errorf("unknown method: %s", method)
}
