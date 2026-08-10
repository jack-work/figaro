package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/uiir"
)

// reader builds the store-backed reader on demand. The projector is
// constructed with an empty tool registry: projection needs tool NAMES to
// render a call's shape, and a sealed history already carries them in the
// IR. Nothing here executes a tool.
func (h *handlers) reader() *AriaReader {
	return NewAriaReader(h.angelus.Backend, uiir.New(nil))
}

// liveAgent returns the aria's agent if one is resident, else nil. This is
// the routing decision the whole reader rests on: a live agent holds the
// open streaming region, partial tool arguments and the in-flight turn,
// none of which are in the store, so serving a client the store's view of a
// live aria would look like a hang. Dormant, there is nothing to miss.
func (h *handlers) liveAgent(id string) figaro.AgentServer {
	f := h.angelus.Registry.Get(id)
	if f == nil {
		return nil
	}
	srv, _ := f.(figaro.AgentServer)
	return srv
}

// ariaPage serves one window of an aria's history without waking it.
func (h *handlers) ariaPage(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.AriaPageRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("aria.page: parse params: %w", err)
	}
	if err := rpc.ValidateAriaID(req.FigaroID); err != nil {
		return nil, err
	}
	if srv := h.liveAgent(req.FigaroID); srv != nil {
		return srv.Handle(ctx, rpc.MethodRead, mustJSON(rpc.ReadRequest{
			SinceLT: req.SinceLT, Before: req.Before,
			BeforeNode: req.BeforeNode, Limit: req.Limit,
		}))
	}
	at := aria.Anchor{Turn: uint64(req.SinceLT)}
	before := req.Before > 0
	if before {
		at = aria.Anchor{Turn: uint64(req.Before), Node: uint64(req.BeforeNode)}
	}
	return h.reader().Page(req.FigaroID, at, req.Limit, before)
}

// ariaContext serves an aria's fig IR without waking it.
func (h *handlers) ariaContext(ctx context.Context, params json.RawMessage) (interface{}, error) {
	id, err := ariaIDParam(params, "aria.context")
	if err != nil {
		return nil, err
	}
	if srv := h.liveAgent(id); srv != nil {
		return srv.Handle(ctx, rpc.MethodContext, nil)
	}
	msgs, metrics, err := h.reader().Context(id)
	if err != nil {
		return nil, fmt.Errorf("aria.context: %w", err)
	}
	out := make([]any, len(msgs))
	for i, m := range msgs {
		out[i] = m
	}
	return rpc.ContextResponse{Messages: out, Metrics: metrics}, nil
}

// ariaForm serves an aria's form without waking it.
func (h *handlers) ariaForm(ctx context.Context, params json.RawMessage) (interface{}, error) {
	id, err := ariaIDParam(params, "aria.form")
	if err != nil {
		return nil, err
	}
	if srv := h.liveAgent(id); srv != nil {
		return srv.Handle(ctx, rpc.MethodForm, nil)
	}
	snap, err := h.reader().Form(id)
	if err != nil {
		return nil, fmt.Errorf("aria.form: %w", err)
	}
	return rpc.FormResponse{Snapshot: snap}, nil
}

func ariaIDParam(params json.RawMessage, method string) (string, error) {
	var req rpc.AriaIDRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return "", fmt.Errorf("%s: parse params: %w", method, err)
	}
	if req.FigaroID == "" {
		return "", errors.New(method + ": empty figaro_id")
	}
	if err := rpc.ValidateAriaID(req.FigaroID); err != nil {
		return "", err
	}
	return req.FigaroID, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
