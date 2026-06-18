package figaro

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/jkrpc"
	"github.com/jack-work/figaro/internal/rpc"
)

// AgentServer is the figaro-side JSON-RPC contract.
type AgentServer interface {
	Handle(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// connSession wraps the agent for one connection so per-connection
// concerns (open-tail delta mode) can be negotiated from requests. It
// holds the connection's notifier (the jkrpc server) so qua/read can
// flip this subscription's fanout mode.
type connSession struct {
	a   *Agent
	sub Notifier
}

func (s *connSession) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case rpc.MethodQua:
		var req rpc.QuaRequest
		if json.Unmarshal(params, &req) == nil && req.DeltaMode {
			s.a.SetSubscriberMode(s.sub, true)
		}
	case rpc.MethodRead:
		var req rpc.ReadRequest
		if json.Unmarshal(params, &req) == nil && req.DeltaMode {
			s.a.SetSubscriberMode(s.sub, true)
		}
	}
	return s.a.Handle(ctx, method, params)
}

// agentMethods is the set of methods the figaro socket exposes.
var agentMethods = []string{
	rpc.MethodQua,
	rpc.MethodRead,
	rpc.MethodContext,
	rpc.MethodInterrupt,
	rpc.MethodSet,
	rpc.MethodLoadout,
	rpc.MethodChalkboard,
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
		idx := a.SubmitPrompt(req)
		return rpc.QuaResponse{OK: true, Index: idx}, nil

	case rpc.MethodRead:
		var req rpc.ReadRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		return a.Read(req), nil

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
	}
	return nil, fmt.Errorf("unknown method: %s", method)
}
