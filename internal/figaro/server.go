package figaro

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/internal/livelog/aria"

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
	rpc.MethodQueued,
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
		cursor := int(a.ariaSrv.LastTurn())
		active := a.turnActive()
		a.SubmitPrompt(req)
		return rpc.QuaResponse{OK: true, Cursor: cursor, Active: active}, nil

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
		// Snapshot and version must come from ONE read of the published board,
		// or a patch landing between them yields a snapshot labelled with a
		// version it does not contain — and the client resumes past a patch it
		// never saw. ChalkboardAt returns the pair atomically.
		snap, version := a.ChalkboardAt()
		return rpc.ChalkboardResponse{Snapshot: snap, Version: version}, nil

	case rpc.MethodQueued:
		return rpc.QueuedResponse{Prompts: a.QueuedPrompts()}, nil

	case rpc.MethodRead:
		var req rpc.ReadRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		// Before names a turn cursor; Limit is a byte budget hint. A zero
		// Before with a backward read means the tail.
		if req.Before > 0 {
			at := aria.Anchor{Turn: uint64(req.Before), Node: uint64(req.BeforeNode)}
			return a.ReadBefore(at, req.Limit), nil
		}
		return a.Read(aria.Anchor{Turn: uint64(req.SinceLT)}, req.Limit), nil
	}
	return nil, fmt.Errorf("unknown method: %s", method)
}
