package store

import (
	"bytes"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
)

func (b *XwalBackend) mirrorQuotasAtOpen() error {
	f, err := readSchema(b.root)
	if err != nil {
		return err
	}
	if f.QuotaMirror {
		return nil
	}
	start := time.Now()
	var ids []string
	for _, t := range b.store.trunks.ListLight() {
		if t.Kind == "" || t.Kind == string(kindConversation) {
			ids = append(ids, t.ID)
		}
	}
	var (
		mu      sync.Mutex
		carried []string
	)
	if _, err := eachForm(ids, func(id string) (bool, error) {
		if !b.carriesUnmirroredQuota(id) {
			return false, nil
		}
		mu.Lock()
		carried = append(carried, id)
		mu.Unlock()
		return true, nil
	}); err != nil {
		return fmt.Errorf("store %s: fan-out quota mirror: %w", b.root, err)
	}
	mirrored, dropped := 0, 0
	for _, id := range carried {
		switch b.settleQuotaAtOpen(id) {
		case quotaMirrored:
			mirrored++
		case quotaDropped:
			dropped++
		}
	}
	if f, err = readSchema(b.root); err != nil {
		return err
	}
	f.QuotaMirror = true
	if err := writeSchema(b.root, f); err != nil {
		return err
	}
	slog.Info("fan-out quotas mirrored from the forms", "root", b.root, "conversations", len(ids),
		"mirrored", mirrored, "inherited_dropped", dropped, "ms", time.Since(start).Milliseconds())
	return nil
}

func (b *XwalBackend) carriesUnmirroredQuota(id string) bool {
	if meta, _ := b.Meta(id); meta != nil && meta.FanoutQuota != nil {
		return false
	}
	f, release, err := b.openFormBriefly(id)
	if err != nil {
		slog.Warn("fan-out quota mirror: form unreadable", "node", id, "err", err)
		return false
	}
	defer release()
	snap, _ := f.Snapshot()
	_, ok := snap.Get(form.FanoutQuotaKey)
	return ok
}

type quotaSettled int

const (
	quotaUntouched quotaSettled = iota
	quotaMirrored
	quotaDropped
)

func (b *XwalBackend) settleQuotaAtOpen(id string) quotaSettled {
	f, release, err := b.openFormBriefly(id)
	if err != nil {
		slog.Warn("fan-out quota mirror: form unreadable", "node", id, "err", err)
		return quotaUntouched
	}
	defer release()
	snap, _ := f.Snapshot()
	raw, ok := snap.Get(form.FanoutQuotaKey)
	if !ok {
		return quotaUntouched
	}
	if b.quotaCopiedAtFork(id, f) {
		if _, _, err := f.applyRename(form.Build(snap, nil, []string{form.FanoutQuotaKey})); err != nil {
			slog.Warn("fan-out quota mirror: inherited quota not dropped", "node", id, "err", err)
			return quotaUntouched
		}
		return quotaDropped
	}
	c, _ := lifetimeFrom(id, func(key string) (string, bool) {
		if key != form.FanoutQuotaKey {
			return "", false
		}
		return lifetimeString(raw), true
	})
	if c.quota == nil {
		return quotaUntouched
	}
	b.mirrorLifetime(id, c)
	return quotaMirrored
}

func (b *XwalBackend) quotaCopiedAtFork(id string, f *Form) bool {
	info, ok := b.store.trunks.InfoLight(id)
	if !ok || info.Parent == "" {
		return false
	}
	_, head := f.Snapshot()
	mine, ok := lastQuotaWrite(f, head)
	if !ok {
		return false
	}
	pf, release, err := b.openFormBriefly(info.Parent)
	if err != nil {
		return false
	}
	defer release()
	theirs, err := pf.PatchesBetween(mine.Version-1, mine.Version)
	if err != nil || len(theirs) != 1 {
		return false
	}
	a, _ := mine.Patch.Entry(form.FanoutQuotaKey)
	p, ok := theirs[0].Patch.Entry(form.FanoutQuotaKey)
	return ok && bytes.Equal(a.New, p.New) && bytes.Equal(a.Old, p.Old)
}

func lastQuotaWrite(f *Form, head uint64) (VersionedPatch, bool) {
	ps, err := f.PatchesBetween(0, head)
	if err != nil {
		return VersionedPatch{}, false
	}
	for i := len(ps) - 1; i >= 0; i-- {
		if _, ok := ps[i].Patch.Entry(form.FanoutQuotaKey); ok {
			return ps[i], true
		}
	}
	return VersionedPatch{}, false
}

func (b *XwalBackend) openFormBriefly(id string) (*Form, func(), error) {
	b.mu.Lock()
	resident := b.forms[id] != nil
	b.mu.Unlock()
	f, err := b.form(id)
	if err != nil {
		return nil, nil, err
	}
	if resident {
		return f, func() {}, nil
	}
	return f, func() { b.dropForm(id) }, nil
}
