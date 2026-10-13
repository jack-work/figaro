package store

import (
	fwtree "github.com/jack-work/figaro/internal/store/tree"
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
func (s *XwalStore) LineageLinks(id string) []Link {
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
		links = append(links, Link{
			Node: t.ID, Kind: t.Kind, BaseLT: t.BranchedLT,
			BaseTurn: t.BranchedTurn, BaseTurnKnown: t.BranchedTurnKnown, BaseTurnExact: t.BranchedTurnExact,
		})
		cur = t.Parent
	}
	if len(links) == 0 {
		return nil
	}
	for l, r := 0, len(links)-1; l < r; l, r = l+1, r-1 {
		links[l], links[r] = links[r], links[l]
	}
	links[0].BaseLT = 0
	links[0].BaseTurn, links[0].BaseTurnKnown, links[0].BaseTurnExact = 0, true, true
	return links
}

func (s *XwalStore) Lineage(id string) []fwtree.Ref {
	links := s.LineageLinks(id)
	if len(links) == 0 {
		return nil
	}
	refs := make([]fwtree.Ref, len(links))
	for i, l := range links {
		refs[i] = fwtree.Ref{Node: l.Node, Base: l.BaseLT}
	}
	return refs
}

func (b *XwalBackend) Lineage(id string) []fwtree.Ref { return b.store.Lineage(id) }

func (b *XwalBackend) LineageLinks(id string) []Link { return b.store.LineageLinks(id) }

// TopologyRev is the topology form's version: the epoch a client compares when
// holding anything derived from the shape of the tree.
func (b *XwalBackend) TopologyRev() uint64 { return b.store.presentRev() }

// LineageBackend is optional; a caller asserts it and does nothing when absent.
type LineageBackend interface {
	Lineage(id string) []fwtree.Ref
}

type ChainBackend interface {
	LineageLinks(id string) []Link
	TopologyRev() uint64
}
