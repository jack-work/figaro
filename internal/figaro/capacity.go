package figaro

import (
	"encoding/json"
	"log/slog"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
)

func (a *Agent) PublishModelFacts() {
	if a.form == nil || a.backend == nil || a.models == nil {
		return
	}
	a.capacityMu.Lock()
	defer a.capacityMu.Unlock()
	patch := ModelFactsPatch(a.models.Snapshot(), a.form.Snapshot())
	if patch.IsIdentity() {
		return
	}
	if _, err := a.backend.ApplyFormPrivileged(a.id, patch); err != nil {
		slog.Warn("publish model facts", "aria", a.id, "err", err)
		return
	}
	a.form.Apply(patch)
}

func ModelFactsPatch(catalog, bound form.Snapshot) form.Patch {
	set := map[string]json.RawMessage{}
	var gone []string
	if limit := models.ContextLimit(catalog, bound); limit > 0 {
		set[form.ContextLimitKey], _ = json.Marshal(limit)
	} else {
		gone = append(gone, form.ContextLimitKey)
	}
	return form.Build(bound, set, presentOf(bound, gone))
}
