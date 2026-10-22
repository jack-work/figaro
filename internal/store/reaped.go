package store

import (
	"sort"
)

const SystemReapedKey = "system.reaped"

type ReapedEntry struct {
	ID   string
	Root string
	AtMS int64
}

func reapedEntryOf(id string, meta *AriaMeta) (ReapedEntry, bool) {
	if meta == nil || meta.ReapedAtMS == 0 || meta.ReapedRoot == "" {
		return ReapedEntry{}, false
	}
	return ReapedEntry{ID: id, Root: meta.ReapedRoot, AtMS: meta.ReapedAtMS}, true
}

func (b *XwalBackend) reapedNotedLocked(id string, meta *AriaMeta) bool {
	if b.reaped == nil {
		return false
	}
	e, ok := reapedEntryOf(id, meta)
	prev, had := b.reaped[id]
	switch {
	case ok && (!had || prev != e):
		b.reaped[id] = e
		return true
	case !ok && had:
		delete(b.reaped, id)
		return true
	}
	return false
}

func (b *XwalBackend) Reaped() []ReapedEntry {
	b.ttlMu.Lock()
	b.seedLifetimesLocked()
	out := make([]ReapedEntry, 0, len(b.reaped))
	for _, e := range b.reaped {
		out = append(out, e)
	}
	b.ttlMu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Root != out[j].Root {
			return out[i].Root < out[j].Root
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (b *XwalBackend) ReapedForget(ids ...string) {
	b.ttlMu.Lock()
	changed := false
	for _, id := range ids {
		if _, ok := b.reaped[id]; ok {
			delete(b.reaped, id)
			changed = true
		}
	}
	b.ttlMu.Unlock()
	if changed {
		b.reapedChanged()
	}
}

func (b *XwalBackend) OnReapedChange(fn func()) {
	b.ttlMu.Lock()
	b.onReaped = append(b.onReaped, fn)
	b.ttlMu.Unlock()
}

func (b *XwalBackend) reapedChanged() {
	b.ttlMu.Lock()
	fns := append([]func(){}, b.onReaped...)
	b.ttlMu.Unlock()
	for _, fn := range fns {
		fn()
	}
}
