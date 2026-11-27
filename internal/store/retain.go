package store

import (
	"github.com/jack-work/figaro/api/form"

	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"
)

type FormRetention struct {
	ID      string
	Pins    int
	Dropped int
	Bytes   int64
}

func (b *XwalBackend) RetainForms(ctx context.Context, dryRun bool, pace time.Duration) ([]FormRetention, error) {
	out, _, err := b.RetainFormsCounted(ctx, dryRun, pace)
	return out, err
}

func (b *XwalBackend) RetainFormsCounted(ctx context.Context, dryRun bool, pace time.Duration) ([]FormRetention, int, error) {
	t := b.store.trunks
	nodes := t.Nodes()
	keys := make([]string, 0, len(nodes))
	for key, n := range nodes {
		if n.Kind == "form" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var out []FormRetention
	var errs error
	scanned := 0
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return out, scanned, err
		}
		scanned++
		id := nodes[key].Trunk
		if ok, err := t.MayRetain(key, chanForm); err != nil {
			errs = errors.Join(errs, fmt.Errorf("form %s: %w", id, err))
			continue
		} else if !ok {
			continue
		}
		floor, skip := b.retentionFloor(key)
		if skip {
			continue
		}
		r, err := t.RetainBelow(key, chanForm, floor, dryRun)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("form %s: %w", id, err))
		}
		if len(r.Dropped) > 0 {
			out = append(out, FormRetention{ID: id, Pins: len(r.Pins), Dropped: len(r.Dropped), Bytes: r.Bytes()})
		}
		if pace > 0 && !dryRun {
			select {
			case <-ctx.Done():
				return out, scanned, ctx.Err()
			case <-time.After(pace):
			}
		}
	}
	return out, scanned, errs
}

func (b *XwalBackend) retentionFloor(key string) (uint64, bool) {
	snap, ok := b.headState(key)
	if !ok {
		return 0, false
	}
	mode := RetentionModeOf(snap)
	if mode == "" && HistoryOf(snap) == HistoryShort {
		mode = RetentionMarks
	}
	if mode == "" {
		mode = *retentionDefault.Load()
	}
	switch mode {
	case RetentionNone:
		return 0, true
	case RetentionMarks:
		low := LowestMark(MarksOf(snap))
		return low, low == 0
	}
	return 0, false
}

func (b *XwalBackend) headState(key string) (form.Snapshot, bool) {
	id := key
	if n, ok := b.store.trunks.Nodes()[key]; ok && n.Trunk != "" {
		id = n.Trunk
	}
	b.mu.Lock()
	f := b.forms[id]
	b.mu.Unlock()
	if f != nil {
		snap, _ := f.Snapshot()
		return snap, true
	}
	xw, err := b.store.openNode(id)
	if err != nil {
		return form.Snapshot{}, false
	}
	defer xw.Close()
	_, last, _ := xw.ChannelBounds(chanForm)
	if last == 0 {
		return form.Snapshot{}, true
	}
	raw, err := xw.StateAt(chanForm, last)
	if err != nil {
		return form.Snapshot{}, false
	}
	snap, _, err := decodeFormWatermark(raw)
	return snap, err == nil
}

var retentionDefault atomic.Pointer[string]

func init() { SetRetentionDefault("") }

func SetRetentionDefault(mode string) { retentionDefault.Store(&mode) }
