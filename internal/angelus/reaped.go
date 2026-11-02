package angelus

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/store"
)

type reapedKeeper interface {
	Reaped() []store.ReapedEntry
	ApplyForm(id string, patch form.Patch) (uint64, error)
}

func (a *Angelus) softDelete(e store.TTLEntry, taken []string, grace time.Duration) error {
	keeper, ok := a.Backend.(reapedKeeper)
	if !ok {
		return nil
	}
	raw, _ := json.Marshal(e.ID)
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemReapedKey: raw}, []string{store.SystemReapedUntilKey})
	marked := 0
	var failed error
	for _, id := range taken {
		if _, err := keeper.ApplyForm(id, patch); err != nil {
			slog.Warn("lifetime spent, soft delete failed", "node", id, "root", e.ID, "err", err)
			failed = errors.Join(failed, fmt.Errorf("soft delete %s: %w", id, err))
			continue
		}
		marked++
	}
	slog.Info("soft-deleted a node whose lifetime was spent",
		"node", e.ID, "ttl", e.TTL, "taken", marked,
		"purge", time.Now().Add(grace).Format(time.RFC3339))
	return failed
}

type reapedGroup struct {
	root    string
	atMS    int64
	untilMS int64
	nodes   []string
}

func (g reapedGroup) purgeAt(grace time.Duration) time.Time {
	if g.untilMS > 0 {
		return time.UnixMilli(g.untilMS)
	}
	return time.UnixMilli(g.atMS).Add(grace)
}

func (g reapedGroup) killed() bool { return g.untilMS > 0 }

func (a *Angelus) reapedGraceOf(g reapedGroup, daemon time.Duration) time.Duration {
	if _, any := a.Settings.ShortestBoxPurgeGrace(); !any || g.untilMS > 0 {
		return daemon
	}
	return a.purgeGraceIn(a.sandboxOf(g.root), daemon)
}

func groupReaped(entries []store.ReapedEntry) []reapedGroup {
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
			g.atMS, g.untilMS = e.AtMS, e.UntilMS
		}
	}
	out := make([]reapedGroup, 0, len(order))
	for _, r := range order {
		out = append(out, *byRoot[r])
	}
	return out
}

func (a *Angelus) reapedGroups() []reapedGroup {
	rk, ok := a.Backend.(reapedKeeper)
	if !ok {
		return nil
	}
	return groupReaped(rk.Reaped())
}

func (a *Angelus) mayBePastGrace(g reapedGroup, now time.Time) bool {
	grace := a.Settings.ReapedGrace()
	least, boxed := a.Settings.ShortestBoxPurgeGrace()
	if !boxed || least > grace {
		least = grace
	}
	return g.atMS > 0 && !g.purgeAt(least).After(now)
}

func (a *Angelus) pastGrace(g reapedGroup, now time.Time) bool {
	return g.atMS > 0 && !g.purgeAt(a.reapedGraceOf(g, a.Settings.ReapedGrace())).After(now)
}

func (a *Angelus) purge(keeper ttlKeeper, g reapedGroup) error {
	roles, ready := a.roleTargets()
	if !ready {
		slog.Debug("reaped node past its grace, waiting for the role index", "node", g.root)
		return errNotYet
	}
	if node, why := a.liveIn(keeper.DeleteSet(g.root), a.Registry.BoundPIDsByFigaro(), roles); why != "" && !g.killed() {
		slog.Info("reaped node past its grace, kept: "+why, "node", g.root, "held_by", node)
		return nil
	}
	if !g.killed() {
		if err := a.scanForRoleIn(append(keeper.DeleteSet(g.root), g.root), g.root); err != nil {
			return err
		}
	}
	if err := a.RemoveAria(g.root, true); err != nil {
		return err
	}
	keeper.TTLForget(g.root)
	slog.Info("purged a reaped node", "node", g.root, "nodes", len(g.nodes),
		"reaped", time.UnixMilli(g.atMS))
	return nil
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
