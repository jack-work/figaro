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
	rpc.MethodQueueUpdate,
	rpc.MethodQueueDelete,
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
		// Attribution comes off the request itself, not from the authn
		// provider: the agent socket has none, and a human — never
		// authenticated — is exactly the caller a confused aria needs named.
		a.SubmitPromptFrom(req, rpc.SenderFrom(params))
		return rpc.QuaResponse{OK: true, Cursor: cursor, Active: active}, nil

	case rpc.MethodContext:
		msgs := a.Context()
		out := make([]any, len(msgs))
		for i, m := range msgs {
			out[i] = m
		}
		return rpc.ContextResponse{Messages: out, Metrics: a.sessionMetrics()}, nil

	case rpc.MethodInterrupt:
		var req rpc.InterruptRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		// An unknown disposition is refused rather than guessed: the two verbs
		// differ by whether the caller's queued messages survive, and a typo
		// must not be resolved into the destructive one.
		switch req.Queue {
		case "", rpc.QueueKeep, rpc.QueueClear:
		default:
			return nil, fmt.Errorf("unknown queue disposition %q (want %q or %q)",
				req.Queue, rpc.QueueKeep, rpc.QueueClear)
		}
		return a.Hangup(req.Queue), nil

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

	case rpc.MethodQueued:
		var req rpc.QueuedRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		epoch, prompts := a.QueuedPrompts(req.IncludeCarriers)
		return rpc.QueuedResponse{Epoch: epoch, Prompts: prompts}, nil

	case rpc.MethodQueueDelete:
		var req rpc.QueueDeleteRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		// A request that names nothing is malformed, not a refusal: there is
		// no id whose fate could be reported, so it belongs on the error
		// channel rather than in an empty result list a caller might read as
		// success.
		if len(req.IDs) == 0 && !req.All {
			return nil, fmt.Errorf("queue delete: name ids or pass all")
		}
		if len(req.IDs) > 0 && req.All {
			return nil, fmt.Errorf("queue delete: ids and all are mutually exclusive")
		}
		epoch, results := a.DeleteQueued(req.Epoch, req.IDs, req.All)
		return rpc.QueueDeleteResponse{Epoch: epoch, Results: results}, nil

	case rpc.MethodQueueUpdate:
		var req rpc.QueueUpdateRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		if req.ID == 0 {
			return nil, fmt.Errorf("queue update: name the message id")
		}
		// Empty text would silently turn a question into a chalkboard carrier
		// — a shape the caller almost certainly did not mean and cannot see.
		// Malformed input, so: error, not a rejection reason.
		if req.Text == "" {
			return nil, fmt.Errorf("queue update: text is empty (delete it instead)")
		}
		epoch, result := a.UpdateQueued(req.Epoch, req.ID, req.Text)
		return rpc.QueueUpdateResponse{Epoch: epoch, Result: result}, nil

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
