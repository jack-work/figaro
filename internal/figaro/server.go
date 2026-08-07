package figaro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/outfit"
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
	rpc.MethodSet,
	rpc.MethodOutfit,
	rpc.MethodChalkboard,
	rpc.MethodQueued,
	rpc.MethodQueueUpdate,
	rpc.MethodQueueDelete,
	rpc.MethodRead,
}

// AgentMethods is the method set an aria endpoint exposes. Exported because
// the angelus now owns that endpoint (see angelus.ariaHub) and must serve
// exactly this set whether or not an agent is resident behind it.
func AgentMethods() []string {
	out := make([]string, len(agentMethods))
	copy(out, agentMethods)
	return out
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
		//
		// The duke placeholder is resolved HERE, against THIS aria's
		// chalkboard, because the caller cannot know what this aria calls
		// its end user — that is precisely why it sends a placeholder.
		a.SubmitPromptFrom(req, rpc.SenderFrom(params, a.dukeTitle))
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

	case rpc.MethodOutfit:
		var req rpc.OutfitRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		set, err := a.ApplyOutfit(req.Names)
		if err != nil {
			return nil, outfitError(err)
		}
		return rpc.OutfitResponse{OK: true, Set: set}, nil

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

// outfitError types a failed outfit resolution for the wire. A missing layer
// carries its closure, because "which layer, and where in the graph" is the
// question the caller actually has; anything else travels as-is.
func outfitError(err error) error {
	var missing *outfit.MissingError
	if !errors.As(err, &missing) {
		return err
	}
	data, mErr := json.Marshal(rpc.ErrorData{
		Name:          strings.Join(missing.Missing, ","),
		OutfitClosure: OutfitClosureWire(missing.Closure),
	})
	if mErr != nil {
		return err
	}
	return &jkrpc.Error{Code: rpc.ErrOutfitNotFound, Message: err.Error(), Data: data}
}

// OutfitClosureWire converts a resolved layer closure to its wire shape. It
// lives here because internal/outfit is a domain package and does not know
// about the wire, and angelus needs the same conversion for its mint path.
func OutfitClosureWire(c *outfit.Closure) *rpc.OutfitLayer {
	if c == nil {
		return nil
	}
	out := &rpc.OutfitLayer{Name: c.Name, Path: c.Path, Found: c.Found, Cycle: c.Cycle}
	for _, l := range c.Layers {
		out.Layers = append(out.Layers, OutfitClosureWire(l))
	}
	return out
}
