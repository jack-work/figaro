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
	ApplyFormPrivileged(id string, patch form.Patch) (uint64, error)
	SubmitFormPrivileged(id string, patch form.Patch) (func() (uint64, error), error)
}

type softDeletion struct {
	e       store.TTLEntry
	grace   time.Duration
	nodes   []string
	pending []func() (uint64, error)
	failed  error
}

func (a *Angelus) submitSoftDelete(e store.TTLEntry, taken []string, grace time.Duration) softDeletion {
	d := softDeletion{e: e, grace: grace}
	keeper, ok := a.Backend.(reapedKeeper)
	if !ok {
		return d
	}
	raw, _ := json.Marshal(e.ID)
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{store.ReapedKey: raw}, []string{store.ReapedUntilKey})
	for _, id := range taken {
		wait, err := keeper.SubmitFormPrivileged(id, patch)
		if err != nil {
			slog.Warn("lifetime spent, soft delete failed", "node", id, "root", e.ID, "err", err)
			d.failed = errors.Join(d.failed, fmt.Errorf("soft delete %s: %w", id, err))
			continue
		}
		d.nodes = append(d.nodes, id)
		d.pending = append(d.pending, wait)
	}
	return d
}

func (d softDeletion) await() error {
	failed := d.failed
	marked := 0
	for i, wait := range d.pending {
		if _, err := wait(); err != nil {
			slog.Warn("lifetime spent, soft delete failed", "node", d.nodes[i], "root", d.e.ID, "err", err)
			failed = errors.Join(failed, fmt.Errorf("soft delete %s: %w", d.nodes[i], err))
			continue
		}
		marked++
	}
	if marked > 0 || failed == nil {
		purge := "when the reaped budget needs the room"
		if d.grace > 0 {
			purge = time.Now().Add(d.grace).Format(time.RFC3339)
		}
		slog.Info("soft-deleted a node whose lifetime was spent",
			"node", d.e.ID, "ttl", d.e.TTL, "taken", marked, "purge", purge)
	}
	return failed
}

type reapedGroup struct {
	root    string
	atMS    int64
	untilMS int64
	by      string
	nodes   []string
}

func (g reapedGroup) purgeAt(grace time.Duration) time.Time {
	if g.untilMS > 0 {
		return time.UnixMilli(g.untilMS)
	}
	return time.UnixMilli(g.atMS).Add(grace)
}

func (g reapedGroup) killed() bool { return g.untilMS > 0 }

func (a *Angelus) reapedCapOf(g reapedGroup) (time.Duration, bool) {
	if g.killed() {
		return 0, true
	}
	if _, any := a.Settings.ShortestBoxPurgeGrace(); !any {
		return a.Settings.ReapedAgeCap()
	}
	return a.purgeGraceIn(a.sandboxOf(g.root))
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
			g.atMS, g.untilMS, g.by = e.AtMS, e.UntilMS, e.By
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

func (a *Angelus) reapedGroupOf(root string) ([]string, bool) {
	for _, g := range a.reapedGroups() {
		if g.root == root {
			return g.nodes, true
		}
	}
	return nil, false
}

func (a *Angelus) mayBePastGrace(g reapedGroup, now time.Time) bool {
	if g.atMS == 0 {
		return false
	}
	if g.killed() {
		return !g.purgeAt(0).After(now)
	}
	if a.overBudget(g.root) {
		return true
	}
	least, capped := a.Settings.ReapedAgeCap()
	if box, boxed := a.Settings.ShortestBoxPurgeGrace(); boxed && (!capped || box < least) {
		least, capped = box, true
	}
	return capped && !g.purgeAt(least).After(now)
}

func (a *Angelus) pastGrace(g reapedGroup, now time.Time) bool {
	if g.atMS == 0 {
		return false
	}
	if !g.killed() && a.overBudget(g.root) {
		return true
	}
	return a.pastAgeCap(g, now)
}

func (a *Angelus) pastAgeCap(g reapedGroup, now time.Time) bool {
	grace, capped := a.reapedCapOf(g)
	return g.atMS > 0 && capped && !g.purgeAt(grace).After(now)
}

func (a *Angelus) purge(keeper ttlKeeper, g reapedGroup) error {
	roles, ready := a.roles()
	if !ready {
		slog.Debug("reaped node past its grace, waiting for the role index", "node", g.root)
		return errNotYet
	}
	if node, why := a.liveIn(keeper.DeleteSet(g.root), a.Registry.BoundPIDsByFigaro(), roles); why != "" && !g.killed() {
		a.held.keep(g.root, node, why, "reaped node past its grace, kept: "+why)
		return nil
	}
	a.held.release(g.root)
	if !g.killed() {
		if err := a.scanForRoleIn(append(keeper.DeleteSet(g.root), g.root), g.root); err != nil {
			return err
		}
	}
	if err := a.removeDecided(g.root, orSelf(keeper.DeleteSet(g.root), g.root)); err != nil {
		return err
	}
	keeper.TTLForget(g.root)
	slog.Info("purged a reaped node", "node", g.root, "nodes", len(g.nodes),
		"reaped", time.UnixMilli(g.atMS))
	return nil
}

func (a *Angelus) rolesIn(ids []string) []string {
	roles, _ := a.roles()
	var out []string
	for _, id := range ids {
		if roles.isRole(id) {
			out = append(out, id)
		}
	}
	return out
}

func (a *Angelus) liveIn(ids []string, bound map[string][]int, roles roleSet) (string, string) {
	for _, id := range ids {
		if roles.isRole(id) {
			return id, "role " + id + " is in the set"
		}
		if a.Registry.Get(id) != nil {
			return id, "an agent is live"
		}
		if a.Spool.Holds(id) {
			return id, "a prompt is spooled"
		}
		if len(bound[id]) > 0 {
			return id, "a shell is bound"
		}
		if role, ok := roles.pointingAt(id); ok {
			return id, "role " + role + " points at it"
		}
	}
	return "", ""
}
