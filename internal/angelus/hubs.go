package angelus

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/rpc"
)

// hubs is the daemon's set of aria endpoints, one per aria that has been
// addressed this lifetime. It outlives every agent in it.
type hubs struct {
	mu sync.Mutex
	m  map[string]*ariaHub
}

func newHubs() *hubs { return &hubs{m: map[string]*ariaHub{}} }

func (hs *hubs) get(id string) *ariaHub {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	return hs.m[id]
}

func (hs *hubs) put(id string, hb *ariaHub) {
	hs.mu.Lock()
	hs.m[id] = hb
	hs.mu.Unlock()
}

func (hs *hubs) drop(id string) *ariaHub {
	hs.mu.Lock()
	hb := hs.m[id]
	delete(hs.m, id)
	hs.mu.Unlock()
	return hb
}

func (hs *hubs) all() []*ariaHub {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	out := make([]*ariaHub, 0, len(hs.m))
	for _, hb := range hs.m {
		out = append(out, hb)
	}
	return out
}

// closeAll tears every endpoint down. Daemon shutdown only: a hub outliving
// its agent is the normal state, not a leak.
func (hs *hubs) closeAll() {
	for _, hb := range hs.all() {
		hb.Close()
	}
}

// hubFor returns the aria's endpoint, creating and binding the listener on
// first use. Idempotent, and cheap: a hub is a listener and a map, and
// building one constructs no agent.
//
// It must be called before anyone is handed the socket path, because a unix
// socket has no lazy activation — a client that dials a path with no listener
// gets ECONNREFUSED, not a wakeup.
func (h *handlers) hubFor(id string) (*ariaHub, error) {
	if hb := h.angelus.Hubs.get(id); hb != nil {
		return hb, nil
	}
	hb := newAriaHub(id, filepath.Join(h.angelus.FigaroSocketDir(), id+".sock"))
	hb.wake = h.wakeForHub
	hb.read = h.readForHub

	if err := hb.listen(h.ctx); err != nil {
		return nil, err
	}
	h.angelus.Hubs.put(id, hb)
	slog.Debug("aria endpoint open", "aria", id, "socket", hb.sockPath)
	return hb, nil
}

// bindAgentToHub points the aria's endpoint at a freshly built agent. The
// returned func unbinds; a caller tearing the agent down runs it and leaves
// the endpoint standing, which is the entire trick.
func (h *handlers) bindAgentToHub(id string, agent subscribableAgent) (func(), error) {
	hb, err := h.hubFor(id)
	if err != nil {
		return nil, err
	}
	return hb.bind(agent), nil
}

// wakeForHub restores an aria on demand for a method that needs a turn loop.
func (h *handlers) wakeForHub(ctx context.Context, id string) (figaro.AgentServer, error) {
	f, err := h.restoreByID(ctx, id)
	if err != nil {
		return nil, err
	}
	srv, _ := f.(figaro.AgentServer)
	return srv, nil
}

// readForHub answers the read methods from the store. ok=false hands the
// request back to the wake path, so an unclassified method can never be
// silently answered from stale bytes.
func (h *handlers) readForHub(id, method string, params json.RawMessage) (any, bool, error) {
	r := h.reader()
	switch method {
	case rpc.MethodChalkboard:
		snap, err := r.Chalkboard(id)
		if err != nil {
			return nil, true, err
		}
		return rpc.ChalkboardResponse{Snapshot: snap}, true, nil

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
		before := req.Before > 0
		if before {
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
