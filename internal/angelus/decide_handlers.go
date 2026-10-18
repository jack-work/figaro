package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/decide"
	"github.com/jack-work/figaro/internal/methods"
)

func (h *handlers) decide(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.DecideRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("%s: parse params: %w", rpc.MethodDecide, err)
	}
	if h.decider == nil {
		return nil, errors.New("decide: this daemon has no decision model configured")
	}
	src := decide.Source{Context: req.Context, Defaults: h.defaultForm()}
	if req.FigaroID != "" {
		if err := h.requireAria(req.FigaroID); err != nil {
			return nil, err
		}
		log, err := h.angelus.Backend.OpenFigIR(req.FigaroID)
		if err != nil {
			return nil, fmt.Errorf("decide: open %s: %w", req.FigaroID, err)
		}
		v, err := h.routeRead(ctx, req.FigaroID, rpc.MethodForm, nil)
		if err != nil {
			return nil, fmt.Errorf("decide: form of %s: %w", req.FigaroID, err)
		}
		board, ok := v.(rpc.FormResponse)
		if !ok {
			return nil, fmt.Errorf("decide: form of %s: unexpected %T", req.FigaroID, v)
		}
		src.Log, src.Board = log, board.Snapshot
	}
	return h.decider.Decide(ctx, src, req)
}

func (h *handlers) defaultForm() form.Snapshot {
	patch, err := h.dressDefault()
	if err != nil {
		return form.Snapshot{}
	}
	return form.Snapshot{}.Apply(patch)
}

type defaultedAsker struct{ h *handlers }

func (d defaultedAsker) Ask(ctx context.Context, src decide.Source, q decide.Question) (rpc.DecideResponse, error) {
	src.Defaults = d.h.defaultForm()
	return d.h.decider.Ask(ctx, src, q)
}

func (h *handlers) methodAsker() methods.Asker {
	if h.decider == nil {
		return nil
	}
	return defaultedAsker{h}
}
