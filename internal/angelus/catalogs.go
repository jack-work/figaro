package angelus

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
)

var catalogIDs = []string{models.FormID, rpc.ReapedFormID, rpc.TroupeFormID, rpc.SuccessionsFormID}

func isCatalogID(id string) bool {
	for _, d := range catalogIDs {
		if id == d {
			return true
		}
	}
	return false
}

func (h *handlers) catalog(id string) *store.Form {
	switch id {
	case models.FormID:
		if h.angelus.Models != nil {
			return h.angelus.Models.Form()
		}
	case rpc.ReapedFormID:
		return h.reapedForm
	case rpc.TroupeFormID:
		return h.troupeForm
	case rpc.SuccessionsFormID:
		return h.successionsForm()
	}
	return nil
}

func (h *handlers) readCatalog(id, method string, params json.RawMessage) (any, bool, error) {
	if id == rpc.SuccessionsFormID && h.succ != nil {
		h.succ.load()
	}
	f := h.catalog(id)
	if f == nil {
		return nil, true, fmt.Errorf("%s: this daemon does not keep it", id)
	}
	if method != rpc.MethodForm {
		return nil, true, fmt.Errorf("%s is a catalog: %s does not apply, read it with %s", id, method, rpc.MethodForm)
	}
	var req rpc.FormRequest
	if len(params) > 0 {
		_ = json.Unmarshal(params, &req)
	}
	if req.Intrinsic != "" && req.Intrinsic != figaro.IntrinsicState {
		return nil, true, fmt.Errorf("%s has no intrinsic %q", id, req.Intrinsic)
	}
	return figaro.FormResponseOf(f.Since(req.Since), ""), true, nil
}

func (h *handlers) publishCatalogs() {
	for _, id := range catalogIDs {
		f := h.catalog(id)
		if f == nil {
			continue
		}
		f.OnCommit(func(c store.Commit) {
			hb := h.angelus.Hubs.get(id)
			if hb == nil {
				return
			}
			_ = hb.Notify(rpc.MethodFormDelta, rpc.FormDelta{
				Schema: rpc.FormDeltaSchema, AriaID: id, Version: c.Version,
				Patch: c.Patch, At: time.Now().UnixMilli(),
			})
		})
	}
}

type reapedIndex interface {
	Reaped() []store.ReapedEntry
	OnReapedChange(fn func())
}

func (h *handlers) startReapedForm() {
	idx, ok := h.angelus.Backend.(reapedIndex)
	if !ok {
		return
	}
	h.reapedForm = store.NewMemForm()
	h.refreshReapedForm()
	idx.OnReapedChange(h.refreshReapedForm)
}

func (h *handlers) refreshReapedForm() {
	idx, ok := h.angelus.Backend.(reapedIndex)
	if !ok || h.reapedForm == nil {
		return
	}
	h.reapedMu.Lock()
	defer h.reapedMu.Unlock()
	grace := h.config.ReapedGrace()
	keys := map[string]json.RawMessage{}
	put := func(k string, v any) {
		raw, _ := json.Marshal(v)
		keys[k] = raw
	}
	for _, g := range groupReaped(idx.Reaped()) {
		base := "items." + g.root + "."
		put(base+"nodes", g.nodes)
		if g.by != "" {
			put(base+"by", g.by)
		}
		if g.atMS > 0 {
			put(base+"at", g.atMS)
			put(base+"purge_at", g.purgeAt(h.angelus.reapedGraceOf(g, grace)).UnixMilli())
		}
		if snap, err := h.angelus.Backend.FormState(g.root); err == nil {
			if m := snap.Lookup("mantra"); m != nil && *m != "" {
				put(base+"mantra", *m)
			}
		}
	}
	put("grace_hours", strconv.Itoa(int(grace/time.Hour)))
	cur, _ := h.reapedForm.Snapshot()
	next := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, keys, nil))
	if patch := next.Diff(cur); !patch.IsIdentity() {
		_, _, _ = h.reapedForm.ApplyEffect(patch, 0)
	}
}

func isLocalFormID(id string) bool { return isCatalogID(id) || isCallerFormID(id) || isNavFormID(id) }
