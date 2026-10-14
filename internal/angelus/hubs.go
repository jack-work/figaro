package angelus

import (
	"context"
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/authz"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
)

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

// closeAll is for daemon shutdown only: a hub outliving its agent is the normal state.
func (hs *hubs) closeAll() {
	for _, hb := range hs.all() {
		hb.Close()
	}
}

// hubFor opens the endpoint on first use; it constructs no agent.
func (h *handlers) hubFor(id string) (*ariaHub, error) {
	if hb := h.angelus.Hubs.get(id); hb != nil {
		return hb, nil
	}
	hb := newAriaHub(id, filepath.Join(h.angelus.FigaroSocketDir(), id+".sock"), authz.AllowAll())
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

// bindAgentToHub returns the unbind; running it leaves the endpoint standing.
func (h *handlers) bindAgentToHub(id string, agent subscribableAgent) (func(), error) {
	hb, err := h.hubFor(id)
	if err != nil {
		return nil, err
	}
	return hb.bind(agent), nil
}

// writeForHub lands mutations on a dormant aria or an unbound form without
// an agent; it is the only path that can patch provider keys into a naked figaro.
func (h *handlers) writeForHub(id, method string, params json.RawMessage) (any, bool, error) {
	switch method {
	case rpc.MethodSet:
	case rpc.MethodStudy, rpc.MethodDrop:
		var req rpc.StudyRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, true, err
			}
		}
		if req.FormID == "" && method == rpc.MethodStudy {
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
	if req.Patch.IsIdentity() {
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
	// Report and fan out what landed, not what was asked for: a no-op set is not an event.
	if applied.IsIdentity() {
		return rpc.SetResponse{OK: true, Outcome: rpc.OutcomeUnchanged, Version: version}, true, nil
	}
	var set []string
	for _, ent := range applied.Entries() {
		k := ent.Key
		if ent.IsRemoval() {
			continue
		}
		set = append(set, k)
	}
	if hb := h.angelus.Hubs.get(id); hb != nil {
		_ = hb.Notify(rpc.MethodFormDelta, rpc.FormDelta{
			Schema: rpc.FormDeltaSchema, AriaID: id, Version: version,
			Patch: applied, At: time.Now().UnixMilli(),
		})
	}
	return rpc.SetResponse{
		OK: true, Set: set, Remove: removedKeys(applied),
		Outcome: rpc.OutcomeApplied, Version: version,
	}, true, nil
}

func (h *handlers) wakeForHub(ctx context.Context, id, method string) (figaro.AgentServer, error) {
	f, err := h.restoreByID(ctx, id, method)
	if err != nil {
		return nil, err
	}
	srv, _ := f.(figaro.AgentServer)
	return srv, nil
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
