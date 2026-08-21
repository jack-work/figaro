package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/formdelta"
	"github.com/jack-work/figaro/internal/store"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/uiir"
)

// reader builds the store-backed reader on demand. The projector is
// constructed with an empty tool registry: projection needs tool NAMES to
// render a call's shape, and a sealed history already carries them in the
// IR. Nothing here executes a tool.
func (h *handlers) reader() *AriaReader {
	h.readerOnce.Do(func() {
		h.readerInst = NewAriaReaderBounded(h.angelus.Backend, uiir.New(nil), h.angelus.UICache)
	})
	return h.readerInst
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

// readFromStore answers a read WITHOUT an agent, and it is the only place
// that knows how. Both doors reach it through routeRead, and the hub hands it
// to a per-aria connection directly: one implementation, so "what a dormant
// aria reads like" cannot differ between the two.
func (h *handlers) readFromStore(id, method string, params json.RawMessage) (any, bool, error) {
	r := h.reader()
	switch method {
	case rpc.MethodForm:
		snap, version, err := r.Form(id)
		if err != nil {
			return nil, true, err
		}
		return rpc.FormResponse{Snapshot: snap, Version: version}, true, nil

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
		at := aria.Anchor{Turn: uint64(req.SinceLT)}
		before := req.Before > 0 || req.Backward
		if req.Before > 0 {
			at = aria.Anchor{Turn: uint64(req.Before), Node: uint64(req.BeforeNode)}
		}
		page, err := r.Page(id, at, req.Limit, before)
		if err != nil {
			return nil, true, err
		}
		return page, true, nil
	}
	return nil, false, nil
}

// routeRead is THE router, and the only one. A read has one name on both
// doors; this decides where the answer comes from. A live agent holds the
// open streaming region, partial tool arguments and the in-flight turn, none
// of which are in the store, so it answers for itself; a dormant aria is
// answered from the store without being woken. The hub calls this for the
// per-aria socket and the three handlers below call it for the angelus door,
// so the two cannot drift -- which they had, as aria.page/figaro.read and
// their two copies of the same predicate.
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

// read serves one window of an aria's history: MethodRead, arriving on the
// angelus door with the aria named in the request.
func (h *handlers) read(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ReadRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, fmt.Errorf("%s: parse params: %w", rpc.MethodRead, err)
	}
	return h.routeRead(ctx, req.FigaroID, rpc.MethodRead, params)
}

// context serves the fig IR plus render metrics.
func (h *handlers) context(ctx context.Context, params json.RawMessage) (interface{}, error) {
	id, err := ariaIDParam(params, rpc.MethodContext)
	if err != nil {
		return nil, err
	}
	return h.routeRead(ctx, id, rpc.MethodContext, nil)
}

// form serves an aria's durable board.
func (h *handlers) form(ctx context.Context, params json.RawMessage) (interface{}, error) {
	id, err := ariaIDParam(params, rpc.MethodForm)
	if err != nil {
		return nil, err
	}
	return h.routeRead(ctx, id, rpc.MethodForm, nil)
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

// ariaRead serves IR entries for an aria through the shared LogCache.
// Live agents share the same Log instance, so reads run lock-free
// against the agent's writes. For dormant arias the cache opens on
// miss and the entry TTLs out naturally.
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

	// The backend returns the same shared, memoized IR instance the live
	// agent holds, so reads run lock-free against its writes.
	log, err := h.angelus.Backend.OpenFigIR(req.FigaroID)
	if err != nil {
		return nil, fmt.Errorf("aria.read: open: %w", err)
	}

	limit := req.Limit
	if limit <= 0 || limit > ariaReadHardCap {
		limit = ariaReadHardCap
	}

	// Keyset pagination: Before takes precedence over From.
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

// ariaReadDeltas assembles a page's form deltas hub-side, seeding the
// cursors from the record PRECEDING the page: a backward page that seeds
// zero attributes everything before its first record to that record,
// which is over-attribution in the direction a reader cannot detect
// (formdelta.Seed).
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
