package store

import (
	"errors"
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

func (e treeEngine) Component(string) ([]string, error) {
	return nil, errors.New("tree engine: no move components")
}

func (e treeEngine) Version() hierarchy.Version { return hierarchy.Version{Drawn: e.x.Rev()} }

func (e treeEngine) Node(id string) (hierarchy.Node, bool) {
	if _, ok := e.x.topo.From(id); !ok {
		return hierarchy.Node{}, false
	}
	return hierarchy.Node{ID: id, Aria: id != "null"}, true
}

func (e treeEngine) Drawn(id string) (string, bool)   { return e.x.Parent(id) }
func (e treeEngine) Stored(id string) (string, bool)  { return e.x.topo.From(id) }
func (e treeEngine) DrawnChildren(id string) []string { return e.x.Children(id) }
func (e treeEngine) Overrides() map[string]string     { return e.x.Edges() }
func (e treeEngine) DeleteSet(id string) []string     { return e.x.DeleteSet(id) }
func (e treeEngine) StorageOrphans(string) []string   { return nil }
func (e treeEngine) Placement(string) (string, int64) { return "local", 0 }
func (e treeEngine) WriteDrawn(set map[string]string, remove []string) error {
	return e.x.Write(set, remove)
}
func (e treeEngine) MoveStorage(string, string) error { return hierarchy.ErrStorageRefused }
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
