package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

type FormRetention struct {
	ID      string
	Pins    int
	Dropped int
	Bytes   int64
}

func (b *XwalBackend) RetainForms(ctx context.Context, dryRun bool, pace time.Duration) ([]FormRetention, error) {
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
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		id := nodes[key].Trunk
		if ok, err := t.MayRetain(key, chanForm); err != nil {
			errs = errors.Join(errs, fmt.Errorf("form %s: %w", id, err))
			continue
		} else if !ok {
			continue
		}
		r, err := t.Retain(key, chanForm, dryRun)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("form %s: %w", id, err))
		}
		if len(r.Dropped) > 0 {
			out = append(out, FormRetention{ID: id, Pins: len(r.Pins), Dropped: len(r.Dropped), Bytes: r.Bytes()})
		}
		if pace > 0 && !dryRun {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(pace):
			}
		}
	}
	return out, errs
}
