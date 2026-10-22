package angelus

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/store"
)

type reapedKeeper interface {
	Reaped() []store.ReapedEntry
	ApplyForm(id string, patch form.Patch) (uint64, error)
}

func (a *Angelus) softDelete(e store.TTLEntry, taken []string, grace time.Duration) {
	keeper, ok := a.Backend.(reapedKeeper)
	if !ok {
		return
	}
	raw, _ := json.Marshal(e.ID)
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemReapedKey: raw}, nil)
	marked := 0
	for _, id := range taken {
		if _, err := keeper.ApplyForm(id, patch); err != nil {
			slog.Warn("lifetime spent, soft delete failed", "node", id, "root", e.ID, "err", err)
			continue
		}
		marked++
	}
	slog.Info("soft-deleted a node whose lifetime was spent",
		"node", e.ID, "ttl", e.TTL, "taken", marked,
		"purge", time.Now().Add(grace).Format(time.RFC3339))
}

type reapedGroup struct {
	root  string
	atMS  int64
	nodes []string
}

func reapedGroups(entries []store.ReapedEntry) []reapedGroup {
	byRoot := map[string]*reapedGroup{}
	var order []string
	for _, e := range entries {
		g := byRoot[e.Root]
		if g == nil {
			g = &reapedGroup{root: e.Root}
			byRoot[e.Root] = g
			order = append(order, e.Root)
		}
		g.nodes = append(g.nodes, e.ID)
		if e.ID == e.Root {
			g.atMS = e.AtMS
		}
	}
	out := make([]reapedGroup, 0, len(order))
	for _, r := range order {
		out = append(out, *byRoot[r])
	}
	return out
}

func (a *Angelus) purgeReaped(keeper ttlKeeper, now time.Time) {
	rk, ok := a.Backend.(reapedKeeper)
	if !ok {
		return
	}
	grace := a.Settings.ReapedGrace()
	bound := a.Registry.BoundPIDsByFigaro()
	var roles map[string]string
	for _, g := range reapedGroups(rk.Reaped()) {
		if g.atMS == 0 || time.UnixMilli(g.atMS).Add(grace).After(now) {
			continue
		}
		if roles == nil {
			roles = a.roleTargets()
		}
		if node, why := a.liveIn(keeper.DeleteSet(g.root), bound, roles); why != "" {
			slog.Info("reaped node past its grace, kept: "+why, "node", g.root, "held_by", node)
			continue
		}
		if err := a.RemoveAria(g.root, true); err != nil {
			slog.Warn("reaped node past its grace, removal failed", "node", g.root, "err", err)
			continue
		}
		keeper.TTLForget(g.root)
		slog.Info("purged a reaped node", "node", g.root, "nodes", len(g.nodes),
			"reaped", time.UnixMilli(g.atMS))
	}
}

func (a *Angelus) liveIn(ids []string, bound map[string][]int, roles map[string]string) (string, string) {
	for _, id := range ids {
		if a.Registry.Get(id) != nil {
			return id, "an agent is live"
		}
		if len(bound[id]) > 0 {
			return id, "a shell is bound"
		}
		if role, ok := roles[id]; ok {
			return id, "role " + role + " points at it"
		}
	}
	return "", ""
}
