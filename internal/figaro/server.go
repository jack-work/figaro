package figaro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jack-work/figaro/api/form"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/jkrpc"
)

type AgentServer interface {
	Handle(ctx context.Context, method string, params json.RawMessage) (any, error)
}

var agentMethods = []string{
	rpc.MethodQua,
	rpc.MethodStudy,
	rpc.MethodDrop,
	rpc.MethodCast,
	rpc.MethodContext,
	rpc.MethodInterrupt,
	rpc.MethodSet,
	rpc.MethodForm,
	rpc.MethodQueued,
	rpc.MethodQueueUpdate,
	rpc.MethodQueueDelete,
	rpc.MethodRead,
}

// AgentMethods is served by the angelus's aria endpoint whether or not an agent is resident.
func AgentMethods() []string {
	out := make([]string, len(agentMethods))
	copy(out, agentMethods)
	return out
}

func (a *Agent) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case rpc.MethodStudy:
		var req rpc.StudyRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		if req.FormID == "" {
			return rpc.StudyResponse{OK: true, Studies: a.StudyList()}, nil
		}
		studies, err := a.Study(req.FormID)
		if err != nil {
			return nil, err
		}
		return rpc.StudyResponse{OK: true, Studies: studies}, nil

	case rpc.MethodDrop:
		var req rpc.StudyRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		studies, err := a.Drop(req.FormID)
		if err != nil {
			return nil, err
		}
		return rpc.StudyResponse{OK: true, Studies: studies}, nil

	case rpc.MethodCast:
		var req rpc.CastRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		res, err := a.Cast(ctx, req.FormID, req.RolePatch)
		if err != nil {
			// A partial verdict carries what landed before the failing step.
			return nil, err
		}
		return rpc.CastResponse{RoleID: res.roleID, Studied: res.studied, Patched: res.patched}, nil

	case rpc.MethodQua:
		var req rpc.QuaRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		// The hub dresses on the way in; a missing spec already failed the qua.
		cursor := int(a.ariaSrv.LastTurn())
		active := a.turnActive()
		if err := a.SubmitPromptFrom(ctx, req, rpc.SenderFrom(params, a.dukeTitle)); err != nil {
			return nil, err
		}
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
		// An unknown disposition is refused, not guessed: a typo must not become the destructive verb.
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
		// A bound form has its own actor; nothing is queued and req.Wait names nothing.
		version, applied, err := a.SetAwaiting(ctx, req.Patch, req.IfVersion, req.Assert)
		if err != nil {
			// A patch may name layers, so a set can fail with a closure the caller can draw.
			return nil, outfitError(err)
		}
		if applied.IsIdentity() {
			return rpc.SetResponse{OK: true, Outcome: rpc.OutcomeUnchanged, Version: version}, nil
		}
		var keys []string
		for _, ent := range applied.Entries() {
			k := ent.Key
			if ent.IsRemoval() {
				continue
			}
			keys = append(keys, k)
		}
		return rpc.SetResponse{
			OK: true, Set: keys, Remove: removedKeys(applied),
			Outcome: rpc.OutcomeApplied, Version: version,
		}, nil

	case rpc.MethodForm:
		var req rpc.FormRequest
		if len(params) > 0 {
			// No params and `null` both mean the board.
			_ = json.Unmarshal(params, &req)
		}
		if name := req.Intrinsic; name != "" && name != IntrinsicState {
			c := a.Intrinsic(name)
			if c == nil {
				return nil, fmt.Errorf("no intrinsic %q on %s (have: %s, %s; %q is the host's own form)",
					name, a.id, IntrinsicRuntime, IntrinsicQueue, IntrinsicState)
			}
			snap, version := c.Snapshot()
			return rpc.FormResponse{Snapshot: snap, Version: version, Intrinsic: name}, nil
		}
		return rpc.FormResponse{Snapshot: a.Snapshot(), Version: a.Version()}, nil

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
		// A request naming nothing is malformed: there is no id whose fate could be reported.
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
		// Empty text would silently turn a question into a form carrier: malformed, not a rejection.
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
		if req.Backward {
			return a.ReadBefore(req.At, req.Floor, req.Limit), nil
		}
		return a.Read(req.At, req.Limit), nil
	}
	return nil, fmt.Errorf("unknown method: %s", method)
}

// outfitError types a failed outfit resolution for the wire; a missing layer carries its closure.
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

// OutfitClosureWire lives here because internal/outfit does not know the wire
// and angelus needs the same conversion.
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

func removedKeys(p form.Patch) []string {
	var out []string
	for _, e := range p.Entries() {
		if e.IsRemoval() {
			out = append(out, e.Key)
		}
	}
	return out
}
