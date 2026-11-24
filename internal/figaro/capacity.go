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
	snap := a.form.Snapshot()
	set := map[string]json.RawMessage{}
	var gone []string
	if limit := a.contextLimit(); limit > 0 {
		set[form.ContextLimitKey], _ = json.Marshal(limit)
	} else {
		gone = append(gone, form.ContextLimitKey)
	}
	if usage := a.planUsage(); usage != nil {
		set[form.PlanUsageKey], _ = json.Marshal(usage)
	} else {
		gone = append(gone, form.PlanUsageKey)
	}
	patch := form.Build(snap, set, presentOf(snap, gone))
	if patch.IsIdentity() {
		return
	}
	if _, err := a.backend.ApplyFormPrivileged(a.id, patch); err != nil {
		slog.Warn("publish model facts", "aria", a.id, "err", err)
		return
	}
	a.form.Apply(patch)
}

func (a *Agent) planUsage() *models.PlanUsage {
	provider, model := models.Subject(a.Snapshot())
	return models.PlanUsageOf(a.models.Snapshot(), provider, model)
}
