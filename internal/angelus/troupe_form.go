package angelus

import (
	"encoding/json"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

const troupeFormTick = time.Second

func (h *handlers) startTroupeForm() {
	h.troupeForm = store.NewMemForm()
	seen := h.refreshTroupeForm(^uint64(0))
	if h.angelus.Peers == nil || h.ctx == nil {
		return
	}
	go func() {
		t := time.NewTicker(troupeFormTick)
		defer t.Stop()
		for {
			select {
			case <-h.ctx.Done():
				return
			case <-t.C:
				seen = h.refreshTroupeForm(seen)
			}
		}
	}()
}

func (h *handlers) refreshTroupeForm(seen uint64) uint64 {
	m := h.angelus.Peers
	version := uint64(0)
	if m != nil {
		version = m.Version()
	}
	if version == seen {
		return seen
	}
	keys := map[string]json.RawMessage{}
	put := func(k string, v any) {
		raw, _ := json.Marshal(v)
		keys[k] = raw
	}
	self := h.nodeID()
	if self != "" {
		put(self+".self", true)
		put(self+".form", rpc.AriasFormID)
	}
	if m != nil {
		for _, node := range m.Nodes() {
			if node == self {
				continue
			}
			put(node+".self", false)
			put(node+".form", node+rpc.NodeSeparator+rpc.AriasFormID)
		}
	}
	next := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, keys, nil))
	cur, _ := h.troupeForm.Snapshot()
	if patch := next.Diff(cur); !patch.IsIdentity() {
		_, _, _ = h.troupeForm.ApplyEffect(patch, 0)
	}
	return version
}
