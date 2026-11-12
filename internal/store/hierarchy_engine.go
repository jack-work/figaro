package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
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
	out := hierarchy.Node{ID: n.ID, Aria: n.Kind == KindConversation, Form: n.Kind == KindForm, Stump: n.Stump, BranchedLT: n.BranchedLT}
	if meta, _ := e.b.Meta(id); meta != nil {
		out.Owner = meta.MintedBy
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

func (e *hierarchyEngine) Component(id string) ([]string, error) {
	c, err := e.b.Component(id)
	if err != nil {
		return nil, err
	}
	return c.TrunkIDs(), nil
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
	return e.s().removeLeaf(id, recursive, expect, pins, e.b.bury, e.b.unlinked)
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

func (s *XwalStore) move(id, under string) error {
	node, ok := s.trunks.HeadNode(id)
	if !ok {
		return fmt.Errorf("move: no aria or form %q", id)
	}
	parent := ""
	if under != "" {
		if parent, ok = s.trunks.HeadNode(under); !ok {
			return fmt.Errorf("move: no aria or form %q", under)
		}
	}
	if err := s.trunks.Reparent(node, parent); err != nil {
		if errors.Is(err, ErrMoveCopies) || errors.Is(err, ErrMoveCycle) {
			return fmt.Errorf("move %s under %s: %w: %w", id, under, hierarchy.ErrStorageRefused, err)
		}
		return fmt.Errorf("move %s under %s: %w", id, under, err)
	}
	_, err := s.CleanTopology(false)
	return err
}

func (s *XwalStore) removeLeaf(id string, recursive bool, expect []string, pins map[string]string, bury, unlinked func([]string)) error {
	s.deleting.Lock()
	defer s.deleting.Unlock()
	// Refuse before touching anything: the boundary repair rewrites surviving arias.
	taken := s.tree.DeleteSet(id)
	if !recursive && len(taken) > 1 {
		return hierarchy.HasChildren(id, len(taken)-1)
	}
	if expect != nil && !sameIDs(orSelf(taken, id), expect) {
		return fmt.Errorf("%w: %q takes %v, decided on %v", ErrDeleteSetChanged, id, taken, expect)
	}
	taken = orSelf(taken, id)
	stumps := map[string]bool{}
	s.mu.Lock()
	for _, t := range taken {
		if _, ok := s.trunks.Kind(t); !ok {
			s.mu.Unlock()
			return fmt.Errorf("%w: %q would take %s, which the store does not hold", ErrDeleteSetChanged, id, t)
		}
	}
	s.mu.Unlock()
	if bury != nil {
		bury(taken)
	}
	// Repair the boundary first: every survivor reading its history through
	// the delete set absorbs that prefix. Only then unlink, so a crash between
	// the two leaves survivors reading through directories still present.
	homes := map[string]string{}
	doomed := make(map[string]bool, len(taken))
	for _, t := range taken {
		doomed[t] = true
	}
	for child, up := range s.tree.Edges() {
		if doomed[child] || !doomed[up] {
			continue
		}
		home, ok := pins[child]
		if !ok {
			return fmt.Errorf("%w: %q strands %s, which the plan does not re-home", ErrDeleteSetChanged, id, child)
		}
		homes[child] = home
	}
	for _, orphan := range s.deleteOrphans(id) {
		home, ok := pins[orphan]
		if !ok {
			return fmt.Errorf("%w: %q strands %s, which the plan does not re-home", ErrDeleteSetChanged, id, orphan)
		}
		homes[orphan] = home
		// Boundary speaks in aria ids; Detach addresses the node directory.
		node, ok := s.trunks.HeadNode(orphan)
		if !ok {
			return fmt.Errorf("%w: no node for %s", ErrWouldOrphan, orphan)
		}
		if err := s.trunks.Detach(node); err != nil {
			return fmt.Errorf("%w: detaching %s: %v", ErrWouldOrphan, orphan, err)
		}
	}
	// The hosting stumps are read before the removal.
	snap := s.topologySnapshot()
	stump := snap.byID[id].Stump
	for _, t := range taken {
		if st := snap.byID[t].Stump; st != "" && st != stump {
			stumps[st] = true
		}
	}
	if err := s.removeLocked(id, recursive); err != nil {
		return err
	}
	if err := s.removeEach(taken); err != nil {
		return err
	}
	if unlinked != nil {
		unlinked(taken)
	}
	// Presentation is repaired with s.mu released: an edge resolves through
	// the topology snapshot, which takes that lock itself.
	if err := s.tree.Forget(taken...); err != nil {
		slog.Warn("forget presentation edges", "aria", id, "err", err)
	}
	keep := false
	set := map[string]string{}
	var unpin []string
	for orphan, home := range homes {
		if home == "" {
			continue
		}
		if home == stump {
			keep = true
		}
		if up, _ := s.TopologyAdjacency().From(orphan); up == home {
			unpin = append(unpin, orphan)
			continue
		}
		set[orphan] = home
	}
	if w, ok := s.tree.(edgeWriter); ok && (len(set) > 0 || len(unpin) > 0) {
		if err := w.Write(set, unpin); err != nil {
			slog.Warn("rehome detached aria", "pins", set, "err", err)
		}
	}
	// The stump goes only if nothing is left drawn beneath it.
	if !keep {
		s.collectStumpAfterDelete(stump)
	}
	for st := range stumps {
		s.collectStumpAfterDelete(st)
	}
	return nil
}

func (s *XwalStore) removeEach(taken []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range taken {
		if _, ok := s.trunks.Kind(t); !ok {
			continue
		}
		if err := s.trunks.Remove(t, true); err != nil {
			return fmt.Errorf("remove %s: %w", t, err)
		}
	}
	var left []string
	for _, t := range taken {
		if _, ok := s.trunks.Kind(t); ok {
			left = append(left, t)
		}
	}
	if len(left) > 0 {
		return fmt.Errorf("%w: %v outlived their delete", ErrWouldOrphan, left)
	}
	return nil
}
