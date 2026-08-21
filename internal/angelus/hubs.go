package angelus

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
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
func (h *handlers) hubFor(id string) (*ariaHub, error) {
	if hb := h.angelus.Hubs.get(id); hb != nil {
		return hb, nil
	}
	hb := newAriaHub(id, filepath.Join(h.angelus.FigaroSocketDir(), id+".sock"))
	hb.wake = h.wakeForHub
	hb.read = h.readFromStore
	hb.write = h.writeForHub
	hb.dress = h.dressParams
	if h.angelus.Backend != nil {
		if n, ok := h.angelus.Backend.Node(id); ok {
			hb.kind = n.Kind
		}
	}

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

// writeForHub applies mutations the store can absorb without an agent -
// today exactly figaro.set. Serving it here (after read, before wake) is
// what lets a patch land on a DORMANT aria without restoring it, and on an
// unbound form that will never have an agent at all. It also breaks the
// naked-figaro deadlock: `fig bind null` births a figaro whose wake fails
// for want of provider keys, and this is the only path that can patch
// those keys in.
func (h *handlers) writeForHub(id, method string, params json.RawMessage) (any, bool, error) {
	switch method {
	case rpc.MethodSet:
		// below
	case rpc.MethodStudy, rpc.MethodDrop:
		var req rpc.StudyRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, true, err
			}
		}
		if req.FormID == "" && method == rpc.MethodStudy {
			// A bare `fig study` is a LISTING, and a listing needs no agent.
			snap, err := h.angelus.Backend.FormState(id)
			if err != nil {
				return nil, true, err
			}
			return rpc.StudyResponse{OK: true, Studies: figaro.StudiesFromSnapshot(snap)}, true, nil
		}
		studies, err := h.studyForHub(id, req.FormID, method == rpc.MethodDrop)
		if err != nil {
			return nil, true, err
		}
		return rpc.StudyResponse{OK: true, Studies: studies}, true, nil
	case rpc.MethodCast:
		var req rpc.CastRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, true, err
		}
		res, err := h.castForHub(id, req)
		if err != nil {
			return nil, true, err
		}
		return res, true, nil
	default:
		return nil, false, nil
	}
	var req rpc.SetRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, true, err
	}
	if req.Patch.IsEmpty() {
		v, _ := h.angelus.Backend.FormVersion(id)
		return rpc.SetResponse{OK: true, Outcome: rpc.OutcomeUnchanged, Version: v}, true, nil
	}
	intent := store.Ensure
	if req.Assert {
		intent = store.Assert
	}
	version, applied, err := h.angelus.Backend.ApplyFormEffectIntent(id, req.Patch, req.IfVersion, intent)
	if err != nil {
		return nil, true, err
	}
	// Report and fan out what LANDED, not what was asked for. A set of a value
	// the board already holds is not an event: the writer dropped it, so this
	// says so, no delta goes out, and an aria observing this form derives no
	// transition from it.
	if applied.IsEmpty() {
		return rpc.SetResponse{OK: true, Outcome: rpc.OutcomeUnchanged, Version: version}, true, nil
	}
	var set []string
	for k := range applied.Set {
		set = append(set, k)
	}
	if hb := h.angelus.Hubs.get(id); hb != nil {
		_ = hb.Notify(rpc.MethodFormDelta, rpc.FormDelta{
			Schema: rpc.FormDeltaSchema, AriaID: id, Version: version,
			Patch: applied, At: time.Now().UnixMilli(),
		})
	}
	return rpc.SetResponse{
		OK: true, Set: set, Remove: applied.Remove,
		Outcome: rpc.OutcomeApplied, Version: version,
	}, true, nil
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
