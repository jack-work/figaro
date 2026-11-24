package figaro

import (
	"encoding/json"
	"log/slog"

	"github.com/jack-work/figaro/api/form"
)

func (a *Agent) PublishContextLimit() {
	if a.form == nil || a.backend == nil || a.models == nil {
		return
	}
	a.capacityMu.Lock()
	defer a.capacityMu.Unlock()
	snap := a.form.Snapshot()
	var patch form.Patch
	if limit := a.contextLimit(); limit > 0 {
		raw, _ := json.Marshal(limit)
		patch = form.RuntimeSet(snap, map[string]json.RawMessage{form.ContextLimitKey: raw})
	} else {
		patch = form.Build(snap, nil, presentOf(snap, []string{form.ContextLimitKey}))
	}
	if patch.IsIdentity() {
		return
	}
	if _, err := a.backend.ApplyFormPrivileged(a.id, patch); err != nil {
		slog.Warn("publish context limit", "aria", a.id, "err", err)
		return
	}
	a.form.Apply(patch)
}
