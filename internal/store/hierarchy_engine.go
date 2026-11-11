package store

import (
	"crypto/rand"
	"encoding/hex"
	"maps"

	"github.com/jack-work/figaro/internal/hierarchy"
)

type edgeWriter interface {
	Write(set map[string]string, remove []string) error
}

type hierarchyEngine struct {
	b     *XwalBackend
	epoch string
}

func (b *XwalBackend) HierarchyEngine() hierarchy.Engine {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return &hierarchyEngine{b: b, epoch: hex.EncodeToString(raw[:])}
}

func (e *hierarchyEngine) s() *XwalStore { return e.b.store }

func (e *hierarchyEngine) Version() hierarchy.Version {
	v := hierarchy.Version{Epoch: e.epoch}
	s := e.s()
	s.mu.Lock()
	v.Stored = s.trunks.Version()
	s.mu.Unlock()
	if s.tree != nil {
		v.Drawn = s.tree.Rev()
	}
	return v
}

func (e *hierarchyEngine) Node(id string) (hierarchy.Node, bool) {
	n, ok := e.s().Node(id)
	if !ok {
		return hierarchy.Node{}, false
	}
	out := hierarchy.Node{ID: n.ID, Aria: n.Kind == KindConversation, Stump: n.Stump, BranchedLT: n.BranchedLT}
	if out.Aria {
		if meta, _ := e.b.Meta(id); meta != nil {
			out.Owner = meta.MintedBy
		}
	}
	return out, true
}

func (e *hierarchyEngine) Drawn(id string) (string, bool) {
	if e.s().tree == nil {
		return e.Stored(id)
	}
	return e.s().tree.Parent(id)
}

func (e *hierarchyEngine) Stored(id string) (string, bool) {
	return e.s().TopologyAdjacency().From(id)
}

func (e *hierarchyEngine) DrawnChildren(id string) []string {
	if e.s().tree == nil {
		return e.s().TopologyAdjacency().ChildrenOf(id)
	}
	return e.s().tree.Children(id)
}

func (e *hierarchyEngine) Overrides() map[string]string {
	out := map[string]string{}
	if e.s().tree != nil {
		maps.Copy(out, e.s().tree.Edges())
	}
	return out
}

func (e *hierarchyEngine) DeleteSet(id string) []string {
	if e.s().tree == nil {
		return []string{id}
	}
	return e.s().tree.DeleteSet(id)
}

func (e *hierarchyEngine) StorageOrphans(id string) []string {
	if e.s().tree == nil {
		return nil
	}
	return e.s().deleteOrphans(id)
}

func (e *hierarchyEngine) Placement(id string) (string, int64) {
	zone := "local"
	if snap, err := e.b.FormState(id); err == nil {
		if v := snap.Lookup("system.sandbox"); v != nil && *v != "" {
			zone = *v
		}
	}
	return zone, e.b.deadlineOf(id)
}

func (e *hierarchyEngine) WriteDrawn(set map[string]string, remove []string) error {
	w, ok := e.s().tree.(edgeWriter)
	if !ok {
		return hierarchy.ErrUnsupported
	}
	return w.Write(set, remove)
}

func (e *hierarchyEngine) MoveStorage(id, under string) error {
	return e.s().move(id, under)
}

func (e *hierarchyEngine) Remove(id string, recursive bool, expect []string, pins map[string]string) error {
	return e.s().removeLeaf(id, recursive, expect, pins, e.b.bury)
}

func (b *XwalBackend) deadlineOf(id string) int64 {
	b.ttlMu.Lock()
	defer b.ttlMu.Unlock()
	b.seedLifetimesLocked()
	if e, ok := b.ttl[id]; ok && e.TTL > 0 {
		return e.DeadlineMS
	}
	return 0
}
