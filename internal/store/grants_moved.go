package store

import (
	"log/slog"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
)

func (b *XwalBackend) moveListedGrantsAtOpen() error {
	f, err := readSchema(b.root)
	if err != nil {
		return err
	}
	if f.GrantsMoved {
		return nil
	}
	start := time.Now()
	var ids []string
	for _, t := range b.store.trunks.ListLight() {
		if t.Kind == "" || t.Kind == string(KindConversation) || t.Kind == string(KindForm) {
			ids = append(ids, t.ID)
		}
	}
	var (
		mu        sync.Mutex
		conflicts []string
	)
	moved, _ := eachForm(ids, func(id string) (bool, error) {
		did, conflict := b.moveListedGrants(id)
		if conflict {
			mu.Lock()
			conflicts = append(conflicts, id)
			mu.Unlock()
		}
		return did, nil
	})
	for _, id := range conflicts {
		slog.Warn("a list at "+form.GrantsKey+" beside "+form.ScopesKey+" is left in place: the figaro's scopes fail closed until the owner unsets "+form.GrantsKey, "node", id)
	}
	if f, err = readSchema(b.root); err != nil {
		return err
	}
	f.GrantsMoved = true
	if err := writeSchema(b.root, f); err != nil {
		return err
	}
	slog.Info(form.GrantsKey+" lists moved to "+form.ScopesKey, "root", b.root, "forms", len(ids),
		"moved", moved, "conflicts", len(conflicts), "ms", time.Since(start).Milliseconds())
	return nil
}

func (b *XwalBackend) moveListedGrants(id string) (moved, conflict bool) {
	f, release, err := b.openFormBriefly(id)
	if err != nil {
		slog.Warn(form.GrantsKey+" move: form unreadable", "node", id, "err", err)
		return false, false
	}
	defer release()
	snap, _ := f.Snapshot()
	p, conflict := form.MoveListedGrants(snap)
	if p.IsIdentity() {
		return false, conflict
	}
	if _, _, err := f.applyRename(p); err != nil {
		slog.Warn(form.GrantsKey+" move: not written", "node", id, "err", err)
		return false, false
	}
	return true, false
}
