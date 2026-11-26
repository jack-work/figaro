package store

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/jack-work/figaro/api/form"
)

const queueKey = "queue"

type queueEntryAge struct {
	State   string `json:"state"`
	StateAt string `json:"state_at"`
}

func finishedQueueEntries(s form.Snapshot, cutoff time.Time) []string {
	raw, ok := s.Get(queueKey)
	if !ok {
		return nil
	}
	var entries map[string]queueEntryAge
	if json.Unmarshal(raw, &entries) != nil {
		return nil
	}
	var out []string
	for id, e := range entries {
		if e.State != "done" && e.State != "cancelled" {
			continue
		}
		at, err := time.Parse(time.RFC3339, e.StateAt)
		if err != nil || at.After(cutoff) {
			continue
		}
		out = append(out, queueKey+"."+id)
	}
	sort.Strings(out)
	return out
}

func (b *XwalBackend) SweepQueueEntries(now time.Time, grace time.Duration) (int, error) {
	if grace <= 0 {
		return 0, nil
	}
	cutoff := now.Add(-grace)
	nodes := b.store.trunks.Nodes()
	var keys []string
	for key, n := range nodes {
		if n.Kind == KindForm {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	removed := 0
	for _, key := range keys {
		id := nodes[key].Trunk
		snap, ok := b.headState(key)
		if !ok || HistoryOf(snap) != HistoryShort {
			continue
		}
		stale := finishedQueueEntries(snap, cutoff)
		if len(stale) == 0 {
			continue
		}
		if _, err := b.ApplyFormPrivileged(id, form.Build(snap, nil, stale)); err != nil {
			return removed, err
		}
		removed += len(stale)
	}
	return removed, nil
}
