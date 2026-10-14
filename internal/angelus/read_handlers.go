package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/formdelta"
	"github.com/jack-work/figaro/internal/store"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/uiir"
)

// reader's projector has an empty tool registry: sealed IR carries the tool names.
func (h *handlers) reader() *AriaReader {
	h.readerOnce.Do(func() {
		h.readerInst = NewAriaReaderBounded(h.angelus.Backend, uiir.New(nil), h.angelus.UICache)
		h.readerInst.models = h.angelus.Models
	})
	return h.readerInst
}

func (h *handlers) liveAgent(id string) figaro.AgentServer {
	f := h.angelus.Registry.Get(id)
	if f == nil {
		return nil
	}
	srv, _ := f.(figaro.AgentServer)
	return srv
}

// readFromStore is the one agentless read path for both doors.
func (h *handlers) readFromStore(id, method string, params json.RawMessage) (any, bool, error) {
	if id == models.FormID {
		return h.readModels(method, params)
	}
	r := h.reader()
	switch method {
	case rpc.MethodForm:
		// An intrinsic form is not on disk; answering with the board would be a different form.
		var req rpc.FormRequest
		if len(params) > 0 {
			_ = json.Unmarshal(params, &req)
		}
		if name := req.Intrinsic; name != "" && name != figaro.IntrinsicState {
			return nil, true, fmt.Errorf(
				"%s is dormant, so it has no %q: an intrinsic form is live-only state and is "+
					"never written to disk. Wake it (send it something) or read its board instead",
				id, name)
		}
		since, err := r.FormSince(id, req.Since)
		if err != nil {
			return nil, true, err
		}
		return figaro.FormResponseOf(since, ""), true, nil

	case rpc.MethodContext:
		msgs, metrics, err := r.Context(id)
		if err != nil {
			return nil, true, err
		}
		out := make([]any, len(msgs))
		for i, m := range msgs {
			out[i] = m
		}
		return rpc.ContextResponse{Messages: out, Metrics: metrics}, true, nil

	case rpc.MethodRead:
		var req rpc.ReadRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, true, err
			}
		}
		page, err := r.Page(id, req.At, req.Floor, h.angelus.Settings.ClampPageBudget(req.Limit), req.Backward)
		if err != nil {
			return nil, true, err
		}
		return page, true, nil
	}
	return nil, false, nil
}

// routeRead is the only router: a live agent answers for itself (it holds
// the in-flight turn the store does not); a dormant aria is read without waking.
func (h *handlers) routeRead(ctx context.Context, id, method string, params json.RawMessage) (any, error) {
	if err := rpc.ValidateAriaID(id); err != nil {
		return nil, err
	}
	if srv := h.liveAgent(id); srv != nil {
		return srv.Handle(ctx, method, params)
	}
	v, ok, err := h.readFromStore(id, method, params)
	if !ok {
		return nil, fmt.Errorf("%s: not a read", method)
	}
	return v, err
}

func (h *handlers) read(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ReadRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("%s: parse params: %w", rpc.MethodRead, err)
	}
	return h.routeRead(ctx, req.FigaroID, rpc.MethodRead, params)
}

func (h *handlers) context(ctx context.Context, params json.RawMessage) (interface{}, error) {
	id, err := ariaIDParam(params, rpc.MethodContext)
	if err != nil {
		return nil, err
	}
	return h.routeRead(ctx, id, rpc.MethodContext, nil)
}

func (h *handlers) form(ctx context.Context, params json.RawMessage) (interface{}, error) {
	id, err := ariaIDParam(params, rpc.MethodForm)
	if err != nil {
		return nil, err
	}
	// params travel: the intrinsic selector must reach the agent.
	return h.routeRead(ctx, id, rpc.MethodForm, params)
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

func (h *handlers) ariaRead(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.IRRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, fmt.Errorf("aria.read: parse params: %w", err)
		}
	}
	if req.FigaroID == "" {
		return nil, errors.New("aria.read: empty figaro_id")
	}

	log, err := h.angelus.Backend.OpenFigIR(req.FigaroID)
	if err != nil {
		return nil, fmt.Errorf("aria.read: open: %w", err)
	}

	limit := req.Limit
	if limit <= 0 || limit > ariaReadHardCap {
		limit = ariaReadHardCap
	}

	// Before takes precedence over From.
	if req.Before > 0 {
		selected, total := log.ReadPage(0, req.Before, limit)
		deltas := h.ariaReadDeltas(log, req.FigaroID, selected)
		entries := make([]rpc.IREntry, len(selected))
		for i, e := range selected {
			raw, _ := json.Marshal(e.Payload)
			entries[i] = rpc.IREntry{LT: e.LT, Payload: raw, FormDeltas: deltas[e.LT]}
		}
		var nextBefore uint64
		if len(selected) > 0 {
			nextBefore = selected[0].LT
		}
		return &rpc.IRResponse{Entries: entries, Total: total, NextFrom: nextBefore}, nil
	}

	selected, total := log.ReadPage(req.From, 0, limit+1)
	page := selected
	if len(page) > limit {
		page = page[:limit]
	}
	deltas := h.ariaReadDeltas(log, req.FigaroID, page)
	out := make([]rpc.IREntry, 0, len(page))
	for _, e := range page {
		raw, mErr := json.Marshal(e.Payload)
		if mErr != nil {
			return nil, fmt.Errorf("aria.read: marshal LT=%d: %w", e.LT, mErr)
		}
		out = append(out, rpc.IREntry{LT: e.LT, Payload: raw, FormDeltas: deltas[e.LT]})
	}
	var nextFrom uint64
	if len(selected) > limit {
		nextFrom = selected[limit].LT
	}
	return rpc.IRResponse{
		Entries:  out,
		Total:    total,
		NextFrom: nextFrom,
	}, nil
}

// ariaReadDeltas seeds cursors from the record preceding the page; a zero
// seed over-attributes everything before the page to its first record.
func (h *handlers) ariaReadDeltas(log store.Log[message.Message], figaroID string, page []store.Entry[message.Message]) map[uint64]map[string]livedoc.FormDelta {
	fb, ok := h.angelus.Backend.(formdelta.Backend)
	if !ok || len(page) == 0 {
		return nil
	}
	seed := formdelta.Seed{}
	if prev, _ := log.ReadPage(0, page[0].LT, 1); len(prev) > 0 {
		seed = formdelta.SeedFrom(prev[len(prev)-1])
	}
	return formdelta.PerRecordFrom(fb, figaroID, seed, page)
}

func (h *handlers) readModels(method string, params json.RawMessage) (any, bool, error) {
	if h.angelus.Models == nil {
		return nil, true, fmt.Errorf("%s: this daemon keeps no model catalog", models.FormID)
	}
	if method != rpc.MethodForm {
		return nil, true, fmt.Errorf("%s is a catalog: %s does not apply, read it with %s", models.FormID, method, rpc.MethodForm)
	}
	var req rpc.FormRequest
	if len(params) > 0 {
		_ = json.Unmarshal(params, &req)
	}
	if req.Intrinsic != "" && req.Intrinsic != figaro.IntrinsicState {
		return nil, true, fmt.Errorf("%s has no intrinsic %q", models.FormID, req.Intrinsic)
	}
	return figaro.FormResponseOf(h.angelus.Models.Form().Since(req.Since), ""), true, nil
}

func (h *handlers) modelsWant(_ context.Context, params json.RawMessage) (any, error) {
	var req rpc.ModelsWantRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("%s: parse params: %w", rpc.MethodModelsWant, err)
	}
	h.angelus.Models.Want(req.Provider, req.Model)
	return rpc.ModelsWantResponse{OK: true}, nil
}

func (h *handlers) publishModels() {
	cat := h.angelus.Models
	if cat == nil {
		return
	}
	cat.Form().OnCommit(func(version uint64, patch message.Patch) {
		hb := h.angelus.Hubs.get(models.FormID)
		if hb == nil {
			return
		}
		_ = hb.Notify(rpc.MethodFormDelta, rpc.FormDelta{
			Schema: rpc.FormDeltaSchema, AriaID: models.FormID, Version: version,
			Patch: patch, At: time.Now().UnixMilli(),
		})
	})
}
