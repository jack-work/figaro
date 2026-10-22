package angelus

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
)

var daemonFormIDs = []string{models.FormID}

func isDaemonFormID(id string) bool {
	for _, d := range daemonFormIDs {
		if id == d {
			return true
		}
	}
	return false
}

func (h *handlers) daemonForm(id string) *store.Form {
	switch id {
	case models.FormID:
		if h.angelus.Models != nil {
			return h.angelus.Models.Form()
		}
	}
	return nil
}

func (h *handlers) readDaemonForm(id, method string, params json.RawMessage) (any, bool, error) {
	f := h.daemonForm(id)
	if f == nil {
		return nil, true, fmt.Errorf("%s: this daemon does not keep it", id)
	}
	if method != rpc.MethodForm {
		return nil, true, fmt.Errorf("%s is a daemon form: %s does not apply, read it with %s", id, method, rpc.MethodForm)
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

func (h *handlers) publishDaemonForms() {
	for _, id := range daemonFormIDs {
		f := h.daemonForm(id)
		if f == nil {
			continue
		}
		f.OnCommit(func(version uint64, patch message.Patch) {
			hb := h.angelus.Hubs.get(id)
			if hb == nil {
				return
			}
			_ = hb.Notify(rpc.MethodFormDelta, rpc.FormDelta{
				Schema: rpc.FormDeltaSchema, AriaID: id, Version: version,
				Patch: patch, At: time.Now().UnixMilli(),
			})
		})
	}
}
