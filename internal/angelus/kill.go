package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/store"
)

type killReaper interface {
	reapedKeeper
	DeleteSet(id string) []string
}

func (h *handlers) reapKilled(ctx context.Context, by hierarchy.Principal, id string, recursive, lift bool, until time.Time, authorized []string) (time.Time, bool, error) {
	var at time.Time
	var reaped bool
	_, err := h.angelus.topology().Under(ctx, by, hierarchy.Plan{Op: hierarchy.Reap, Node: id, Lift: lift, Delete: authorized}, func(p hierarchy.Plan) error {
		var err error
		at, reaped, err = h.reapGroup(id, recursive, until, p.Delete, "")
		return err
	})
	return at, reaped, deleteSetChanged(err)
}

func (h *handlers) reapGroup(id string, recursive bool, until time.Time, expect []string, by string) (time.Time, bool, error) {
	rk, ok := h.angelus.Backend.(killReaper)
	if !ok {
		return time.Time{}, false, nil
	}
	if n, exists := h.angelus.Backend.Node(id); !exists || n.Kind != store.KindConversation {
		return time.Time{}, false, nil
	}
	root := id
	if meta, _ := h.angelus.Backend.Meta(id); meta != nil && meta.ReapedAtMS > 0 && meta.ReapedRoot != "" {
		root = meta.ReapedRoot
	}
	inGroup := map[string]bool{}
	reaped := map[string]bool{}
	var mark []string
	for _, e := range rk.Reaped() {
		reaped[e.ID] = true
		if e.Root == root {
			inGroup[e.ID] = true
			mark = append(mark, e.ID)
		}
	}
	taken := orSelf(rk.DeleteSet(id), id)
	if expect != nil && !sameMembers(taken, expect) {
		return time.Time{}, false, fmt.Errorf("%w: %q takes %v, decided on %v", store.ErrDeleteSetChanged, id, taken, expect)
	}
	var fresh []string
	for _, n := range taken {
		if !inGroup[n] {
			fresh = append(fresh, n)
		}
	}
	if !recursive {
		live := 0
		for _, n := range fresh {
			if n != id && !reaped[n] {
				live++
			}
		}
		if live > 0 {
			return time.Time{}, false, hierarchy.HasChildren(id, live)
		}
	}
	for _, n := range fresh {
		h.stopAria(n)
	}
	mark = append(mark, fresh...)
	rootRaw, _ := json.Marshal(root)
	untilRaw, _ := json.Marshal(store.ReapedUntilValue(until))
	keys := map[string]json.RawMessage{
		store.SystemReapedKey:      rootRaw,
		store.SystemReapedUntilKey: untilRaw,
	}
	var drops []string
	if by != "" {
		keys[store.SystemReapedByKey], _ = json.Marshal(by)
	} else {
		drops = append(drops, store.SystemReapedByKey)
	}
	patch := form.Build(form.Snapshot{}, keys, drops)
	for _, n := range mark {
		if _, err := rk.ApplyForm(n, patch); err != nil {
			return time.Time{}, true, fmt.Errorf("kill %s: reaping %s: %w", id, n, err)
		}
	}
	slog.Info("killed figaro, reaped until its grace runs out",
		"id", id, "root", root, "nodes", len(mark), "purge", until.Format(time.RFC3339), "by", by)
	return until, true, nil
}

func (h *handlers) stopAria(id string) {
	if h.angelus.Registry.Get(id) != nil {
		if err := h.angelus.Registry.Kill(id); err != nil {
			slog.Warn("kill: agent did not stop", "id", id, "err", err)
		}
	}
	if n := h.killSessions(id); n > 0 {
		slog.Info("killed aria sessions", "id", id, "sessions", n)
	}
}
