package store

import (
	"fmt"
	"slices"

	fwtree "github.com/jack-work/figaro/figwal/tree"
)

// Link is one node of an ancestry walk. BaseTurn is BaseLT as a turn of the
// parent when one was recorded; BaseTurnKnown false means the caller computes
// it and should record what it computed.
type Link struct {
	Node          string
	Kind          string
	BaseLT        uint64
	BaseTurn      uint64
	BaseTurnKnown bool
	BaseTurnExact bool
}

// LineageLinks walks a trunk's ancestry root first; the root's base is zero
// whatever the tree recorded. One lookup per link: the walk is proportional to
// ancestry depth, not store size.
func (s *XwalStore) LineageLinks(id string) ([]Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var links []Link
	seen := map[string]struct{}{}
	for cur := id; cur != ""; {
		// A cycle degrades to a partial lineage rather than a loop.
		if _, dup := seen[cur]; dup {
			break
		}
		seen[cur] = struct{}{}
		t, ok := s.trunks.InfoLight(cur)
		if !ok {
			break
		}
		if t.BaseErr != nil {
			return nil, fmt.Errorf("lineage of %s: %w", id, t.BaseErr)
		}
		links = append(links, Link{
			Node: t.ID, Kind: t.Kind, BaseLT: t.BranchedLT,
			BaseTurn: t.BranchedTurn, BaseTurnKnown: t.BranchedTurnKnown, BaseTurnExact: t.BranchedTurnExact,
		})
		cur = t.Parent
	}
	if len(links) == 0 {
		return nil, nil
	}
	for l, r := 0, len(links)-1; l < r; l, r = l+1, r-1 {
		links[l], links[r] = links[r], links[l]
	}
	links[0].BaseLT = 0
	links[0].BaseTurn, links[0].BaseTurnKnown, links[0].BaseTurnExact = 0, true, true
	return links, nil
}

func (s *XwalStore) Lineage(id string) ([]fwtree.Ref, error) {
	links, err := s.LineageLinks(id)
	if err != nil {
		return nil, err
	}
	return servingLineage(links), nil
}

func servingLineage(links []Link) []fwtree.Ref {
	if len(links) == 0 {
		return nil
	}
	leaf := links[len(links)-1]
	refs := []fwtree.Ref{{Node: leaf.Node, Base: leaf.BaseLT}}
	bound := leaf.BaseLT
	for i := len(links) - 2; i >= 0; i-- {
		if i > 0 && links[i].BaseLT >= bound {
			continue
		}
		refs = append(refs, fwtree.Ref{Node: links[i].Node, Base: links[i].BaseLT})
		bound = links[i].BaseLT
	}
	slices.Reverse(refs)
	return refs
}

func (b *XwalBackend) Lineage(id string) ([]fwtree.Ref, error) { return b.store.Lineage(id) }

func (b *XwalBackend) LineageLinks(id string) ([]Link, error) { return b.store.LineageLinks(id) }

// TopologyRev is the topology form's version: the epoch a client compares when
// holding anything derived from the shape of the tree.
func (b *XwalBackend) TopologyRev() uint64 { return b.store.presentRev() }

// LineageBackend is optional; a caller asserts it and does nothing when absent.
type LineageBackend interface {
	Lineage(id string) ([]fwtree.Ref, error)
}

type ChainBackend interface {
	LineageLinks(id string) ([]Link, error)
	TopologyRev() uint64
}
