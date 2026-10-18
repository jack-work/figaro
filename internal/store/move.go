package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/jack-work/figaro/internal/store/xwal"
)

type Outbound struct {
	*xwal.Export
	Metas map[string]json.RawMessage
}

func (b *XwalBackend) Component(id string) (xwal.Component, error) {
	return b.store.trunks.Component(id)
}

func (b *XwalBackend) ExportComponent(id, token string) (*Outbound, error) {
	e, err := b.store.trunks.Export(id, token)
	if err != nil {
		return nil, err
	}
	out := &Outbound{Export: e, Metas: map[string]json.RawMessage{}}
	for _, trunk := range e.TrunkIDs() {
		raw, err := os.ReadFile(b.metaPath(trunk))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			e.Close()
			return nil, err
		}
		out.Metas[trunk] = raw
	}
	return out, nil
}

func (b *XwalBackend) BeginImport(token string, c xwal.Component) (*xwal.Graft, error) {
	return b.store.trunks.BeginGraft(token, c)
}

func (b *XwalBackend) OpenImport(token string) (*xwal.Graft, error) {
	return b.store.trunks.OpenGraft(token)
}

func (b *XwalBackend) PendingImports() []string { return b.store.trunks.PendingGrafts() }

func (b *XwalBackend) CommitImport(g *xwal.Graft, digest string, metas map[string]json.RawMessage) error {
	members := map[string]bool{}
	for _, id := range g.Component().TrunkIDs() {
		members[id] = true
	}
	decoded := make(map[string]*AriaMeta, len(metas))
	for id, raw := range metas {
		if !members[id] {
			return fmt.Errorf("store: meta for %s, which is not in this move", id)
		}
		var m AriaMeta
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("store: meta for %s: %w", id, err)
		}
		decoded[id] = &m
	}
	for id, m := range decoded {
		c := b.metaCache(id)
		c.mu.Lock()
		err := b.writeMetaRawLocked(id, c, m)
		c.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if err := g.Commit(digest); err != nil {
		if !errors.Is(err, xwal.ErrTrunkExists) {
			for id := range decoded {
				b.forgetMeta(id)
			}
		}
		return err
	}
	for id, m := range decoded {
		b.ttlNoted(id, m)
	}
	return nil
}

func (b *XwalBackend) AbortImport(g *xwal.Graft) error {
	if err := g.Abort(); err != nil {
		return err
	}
	for _, id := range g.Component().TrunkIDs() {
		if _, err := b.store.trunks.Head(id); err != nil {
			b.forgetMeta(id)
		}
	}
	return nil
}

func (b *XwalBackend) forgetMeta(id string) {
	b.mu.Lock()
	delete(b.metas, id)
	b.mu.Unlock()
	_ = os.Remove(b.metaPath(id))
}

func (b *XwalBackend) RemoveMoved(c xwal.Component, token string) error {
	ids := c.TrunkIDs()
	stump := ""
	if len(ids) > 0 {
		stump = b.store.topologySnapshot().byID[ids[0]].Stump
	}
	b.store.deleting.Lock()
	defer b.store.deleting.Unlock()
	b.buryAs("moved", ids)
	b.store.mu.Lock()
	_, err := b.store.trunks.RemoveComponent(c, token)
	b.store.mu.Unlock()
	if err != nil {
		return err
	}
	if err := b.store.tree.Forget(ids...); err != nil {
		return err
	}
	b.store.collectStumpAfterDelete(stump)
	return nil
}

func (b *XwalBackend) Moves() *Moves { return b.moves }
