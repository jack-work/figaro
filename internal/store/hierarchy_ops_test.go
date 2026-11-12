package store

import (
	"fmt"

	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/hierarchy/hierarchytest"
)

func (b *XwalBackend) Remove(id string, recursive bool) error {
	return hierarchytest.Of(b).Remove(id, recursive)
}

func (b *XwalBackend) RemoveIf(id string, expect []string) error {
	return hierarchytest.Of(b).RemoveIf(id, expect)
}

func (b *XwalBackend) Promote(id string, levels int) (int, error) {
	return hierarchytest.Of(b).Promote(id, levels)
}

func (b *XwalBackend) Demote(id, prefer string) (string, error) {
	return hierarchytest.Of(b).Demote(id, prefer)
}

func (b *XwalBackend) Arrange(parents map[string]string, restore []string, _ func(string) error) error {
	return hierarchytest.Of(b).Arrange(parents, restore)
}

func (b *XwalBackend) Move(id, under string) error {
	return hierarchytest.Of(b).Move(id, under)
}

type treeEngine struct{ x *TopologyTree }

func (e treeEngine) Version() hierarchy.Version { return hierarchy.Version{Drawn: e.x.Rev()} }

func (e treeEngine) Node(id string) (hierarchy.Node, bool) {
	if _, ok := e.x.topo.From(id); !ok {
		return hierarchy.Node{}, false
	}
	return hierarchy.Node{ID: id, Aria: id != "null"}, true
}

func (e treeEngine) Drawn(id string) (string, bool)     { return e.x.Parent(id) }
func (e treeEngine) Placed(id string) (string, bool)    { return e.x.topo.From(id) }
func (e treeEngine) ForkPoint(id string) (string, bool) { return e.x.topo.From(id) }
func (e treeEngine) DrawnChildren(id string) []string   { return e.x.Children(id) }
func (e treeEngine) Overrides() map[string]string       { return e.x.Edges() }
func (e treeEngine) DeleteSet(id string) []string       { return e.x.DeleteSet(id) }
func (e treeEngine) ForkOrphans(string) []string        { return nil }
func (e treeEngine) Placement(string) (string, int64)   { return "local", 0 }
func (e treeEngine) WriteDrawn(set map[string]string, remove []string) error {
	return e.x.Write(set, remove)
}
func (e treeEngine) MovePlacement(string, string) error { return hierarchy.ErrPlacementRefused }
func (e treeEngine) Remove(string, bool, []string, map[string]string) error {
	return hierarchy.ErrUnsupported
}

func (x *TopologyTree) HierarchyEngine() hierarchy.Engine { return treeEngine{x} }

func (x *TopologyTree) Promote(id string) error {
	_, err := hierarchytest.Of(x).Promote(id, 1)
	return err
}

func (x *TopologyTree) Demote(id, prefer string) (string, error) {
	return hierarchytest.Of(x).Demote(id, prefer)
}

// seatAsOldReleasesDid rewrites a fork point the way 0.63.0 through 0.65.4
// seated a fork cut inside an ancestor's turns: stores they wrote hold the shape.
func (s *XwalStore) seatAsOldReleasesDid(id, under string) error {
	node, ok := s.trunks.HeadNode(id)
	if !ok {
		return fmt.Errorf("seat: no aria or form %q", id)
	}
	parent, ok := s.trunks.HeadNode(under)
	if !ok {
		return fmt.Errorf("seat: no aria or form %q", under)
	}
	if err := s.trunks.Refork(node, parent); err != nil {
		return err
	}
	return s.trunks.Place(node, parent)
}
