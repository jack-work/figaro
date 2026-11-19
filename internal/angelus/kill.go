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
	SubmitFormPrivileged(id string, patch form.Patch) (func() (uint64, error), error)
}

func (h *handlers) reapKilled(ctx context.Context, by hierarchy.Principal, id string, recursive, lift bool, until time.Time, authorized []string) (time.Time, bool, error) {
	var step reapStep
	_, err := h.angelus.topology().Under(ctx, by, hierarchy.Plan{Op: hierarchy.Reap, Node: id, Lift: lift, Delete: authorized}, func(p hierarchy.Plan) error {
		var err error
		step, err = h.markReaped(id, recursive, until, p.Delete, "")
		return err
	})
	if err != nil {
		h.settlePartialReap(step)
		return time.Time{}, step.reaped, deleteSetChanged(err)
	}
	return h.finishReap(step)
}

func (h *handlers) reapGroup(id string, recursive bool, until time.Time, expect []string, by string) (time.Time, bool, error) {
	step, err := h.markReaped(id, recursive, until, expect, by)
	if err != nil {
		h.settlePartialReap(step)
		return time.Time{}, step.reaped, err
	}
	return h.finishReap(step)
}

func (h *handlers) settlePartialReap(s reapStep) {
	if len(s.pending) == 0 {
		return
	}
	for _, n := range s.stop {
		h.stopAria(n)
	}
	for i, wait := range s.pending {
		if _, err := wait(); err != nil {
			slog.Warn("kill: a mark submitted before the kill failed did not land", "id", s.id, "node", s.marked[i], "err", err)
		}
	}
}

type reapStep struct {
	rk      killReaper
	id      string
	root    string
	by      string
	until   time.Time
	reaped  bool
	patch   form.Patch
	marked  []string
	stop    []string
	pending []func() (uint64, error)
}

func (h *handlers) markReaped(id string, recursive bool, until time.Time, expect []string, by string) (reapStep, error) {
	rk, ok := h.angelus.Backend.(killReaper)
	if !ok {
		return reapStep{}, nil
	}
	if n, exists := h.angelus.Backend.NodeLink(id); !exists || n.Kind != store.KindConversation {
		return reapStep{}, nil
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
		return reapStep{}, fmt.Errorf("%w: %q takes %v, decided on %v", store.ErrDeleteSetChanged, id, taken, expect)
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
			return reapStep{}, hierarchy.HasChildren(id, live)
		}
	}
	step := reapStep{rk: rk, id: id, root: root, by: by, until: until, reaped: true, stop: fresh}
	if err := h.refuseUnreadableBoxes(fresh); err != nil {
		return step, fmt.Errorf("kill %s: %w", id, err)
	}
	mark = append(mark, fresh...)
	rootRaw, _ := json.Marshal(root)
	untilRaw, _ := json.Marshal(store.ReapedUntilValue(until))
	keys := map[string]json.RawMessage{
		store.ReapedKey:      rootRaw,
		store.ReapedUntilKey: untilRaw,
	}
	var drops []string
	if by != "" {
		keys[store.ReapedByKey], _ = json.Marshal(by)
	} else {
		drops = append(drops, store.ReapedByKey)
	}
	step.patch = form.Build(form.Snapshot{}, keys, drops)
	for _, n := range mark {
		wait, err := rk.SubmitFormPrivileged(n, step.patch)
		if err != nil {
			return step, fmt.Errorf("kill %s: reaping %s: %w", id, n, err)
		}
		step.pending = append(step.pending, wait)
		step.marked = append(step.marked, n)
	}
	return step, nil
}

func (h *handlers) finishReap(s reapStep) (time.Time, bool, error) {
	if !s.reaped {
		return time.Time{}, false, nil
	}
	for _, n := range s.stop {
		h.stopAria(n)
	}
	for i, wait := range s.pending {
		if _, err := wait(); err != nil {
			return time.Time{}, true, fmt.Errorf("kill %s: reaping %s: %w", s.id, s.marked[i], err)
		}
	}
	late, err := h.reapLateBorn(s.rk, s.id, s.marked, s.patch)
	if err != nil {
		return time.Time{}, true, fmt.Errorf("kill %s: %w", s.id, err)
	}
	slog.Info("killed figaro, reaped until its grace runs out",
		"id", s.id, "root", s.root, "nodes", len(s.marked)+len(late), "purge", s.until.Format(time.RFC3339), "by", s.by)
	return s.until, true, nil
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

func (h *handlers) reapLateBorn(rk killReaper, id string, marked []string, patch form.Patch) ([]string, error) {
	done := make(map[string]bool, len(marked))
	for _, n := range marked {
		done[n] = true
	}
	var late []string
	for {
		var fresh []string
		for _, n := range rk.DeleteSet(id) {
			if !done[n] && !h.angelus.isReaped(n) {
				fresh = append(fresh, n)
			}
		}
		if len(fresh) == 0 {
			return late, nil
		}
		for _, n := range fresh {
			done[n] = true
			h.stopAria(n)
			if _, err := rk.ApplyFormPrivileged(n, patch); err != nil {
				return late, fmt.Errorf("reaping %s, born during the kill: %w", n, err)
			}
			late = append(late, n)
		}
	}
}
