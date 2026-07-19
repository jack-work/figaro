package angelus

import (
	"context"
	"encoding/json"
	"log/slog"
)

// metaBackfill upgrades AriaMeta sidecars written by builds that predate the
// metadata-only dormant listing. Those sidecars carry the counts but not the
// chalkboard-derived identity fields (mantra, cwd, loadout, provider/model),
// so every dormant aria listed as a bare "aria <id>" row. The truth still
// lives in each aria's chalkboard; fold it in once and the sidecar is current
// forever after (live actors republish full meta on every turn).
//
// Runs in the background at startup. Only metas missing ALL identity fields
// are touched, so a completed sweep is a near-free scan on later starts, and
// an aria whose chalkboard genuinely has no identity is retried harmlessly.
// Live arias are skipped — their actor owns the sidecar.
func (a *Angelus) metaBackfill(ctx context.Context) {
	if a.Backend == nil {
		return
	}
	filled := 0
	for _, id := range a.Backend.ConversationIDs() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if a.Registry.Get(id) != nil {
			continue // live: the actor publishes its own meta
		}
		meta, err := a.Backend.Meta(id)
		if err != nil || meta == nil {
			continue
		}
		if meta.Mantra != "" || meta.LoadoutName != "" || meta.Cwd != "" {
			continue // already carries identity fields
		}
		snap, err := a.Backend.ChalkboardState(id)
		if err != nil {
			continue
		}
		get := func(key string) string {
			raw, ok := snap[key]
			if !ok {
				return ""
			}
			var v string
			_ = json.Unmarshal(raw, &v)
			return v
		}
		mantra := get("mantra")
		cwd := get("system.cwd")
		loadout := get("system.loadout_name")
		if mantra == "" && cwd == "" && loadout == "" {
			continue // nothing to fold in
		}
		meta.Mantra = mantra
		meta.Cwd = cwd
		meta.LoadoutName = loadout
		meta.LoadoutVersion = get("system.loadout_version")
		if meta.Provider == "" {
			meta.Provider = get("system.provider")
		}
		if meta.Model == "" {
			meta.Model = get("system.model")
		}
		if err := a.Backend.SetMeta(id, meta); err != nil {
			slog.Warn("meta backfill", "aria", id, "err", err)
			continue
		}
		filled++
	}
	if filled > 0 {
		slog.Info("meta backfill", "arias", filled)
	}
}
