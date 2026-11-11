package hierarchytest

import (
	"context"

	"github.com/jack-work/figaro/internal/hierarchy"
)

type Open struct{}

func (Open) Check(context.Context, hierarchy.Principal, hierarchy.Plan) (hierarchy.Verdict, error) {
	return hierarchy.Verdict{}, nil
}

type Engined interface {
	HierarchyEngine() hierarchy.Engine
}

type Tree struct{ h *hierarchy.Hierarchy }

func Of(b Engined) Tree { return Tree{hierarchy.New(b.HierarchyEngine(), Open{})} }

func (t Tree) apply(p hierarchy.Plan) (hierarchy.Plan, error) {
	return t.h.Apply(context.Background(), hierarchy.Principal{Name: "test"}, p)
}

func (t Tree) Promote(id string, levels int) (int, error) {
	p, err := t.apply(hierarchy.Plan{Op: hierarchy.Promote, Node: id, Levels: levels})
	return p.Levels, err
}

func (t Tree) Demote(id, prefer string) (string, error) {
	p, err := t.apply(hierarchy.Plan{Op: hierarchy.Demote, Node: id, To: prefer})
	return p.To, err
}

func (t Tree) Arrange(parents map[string]string, restore []string) error {
	_, err := t.apply(hierarchy.Plan{Op: hierarchy.Arrange, Parents: parents, Restore: restore})
	return err
}

func (t Tree) Move(id, under string) error {
	_, err := t.apply(hierarchy.Plan{Op: hierarchy.Arrange, Parents: map[string]string{id: under}, Storage: true})
	return err
}

func (t Tree) Remove(id string, recursive bool) error {
	_, err := t.apply(hierarchy.Plan{Op: hierarchy.Kill, Node: id, Recursive: recursive})
	return err
}

func (t Tree) RemoveIf(id string, expect []string) error {
	_, err := t.apply(hierarchy.Plan{Op: hierarchy.Kill, Node: id, Recursive: true, Delete: expect})
	return err
}
