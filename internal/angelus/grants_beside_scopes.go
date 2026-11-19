package angelus

import (
	"encoding/json"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func (h *handlers) keepGrantsBesideScopes(method, id string, params json.RawMessage) (json.RawMessage, error) {
	if method != rpc.MethodSet || !strings.Contains(string(params), `"grants"`) {
		return params, nil
	}
	var req rpc.SetRequest
	if json.Unmarshal(params, &req) != nil {
		return params, nil
	}
	if e, ok := req.Patch.Entry(form.GrantsKey); !ok || !e.IsRemoval() {
		return params, nil
	}
	if e, ok := req.Patch.Entry(form.ScopesKey); !ok || e.IsRemoval() {
		return params, nil
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return params, nil
	}
	if stored, has := snap.Get(form.GrantsKey); !has || form.ShapeObject().Check(stored) != nil {
		return params, nil
	}
	set := map[string]json.RawMessage{}
	var drops []string
	for _, e := range req.Patch.Entries() {
		switch {
		case e.Key == form.GrantsKey:
		case e.IsRemoval():
			drops = append(drops, e.Key)
		default:
			set[e.Key] = e.New
		}
	}
	req.Patch = form.Build(form.Snapshot{}, set, drops)
	return remarshalWithSender(params, req)
}
