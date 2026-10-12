package store

import (
	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// Link is one node of an ancestry walk: which node, what kind it is, and the
// first LT it owns. Kind is what tells a conversation from the outfit stump
// and the genesis root above it, which matters to anyone who wants the part of
// a lineage that has readable turns in it.
//
// BaseTurn is BaseLT as a turn of the PARENT, when one was recorded (see
// RecordBaseTurn). BaseTurnKnown is false for a fork made before turns were
// recorded, or whose record was lost; the caller computes it then, and should
// record what it computed.
type Link struct {
	Node          string
	Kind          string
	BaseLT        uint64
	BaseTurn      uint64
	BaseTurnKnown bool
	BaseTurnExact bool
}

// LineageLinks walks a trunk's ancestry root first. The root owns from the
// beginning, so its base is zero whatever the tree recorded.
//
// ONE LOOKUP PER LINK. This used to list every trunk in the store to build a
// map and then walk a handful of entries of it: a full walk of the index, and
// a marker read per trunk, on every hop between two arias. The walk is
// proportional to the depth of the ancestry now, not to the size of the store.
func (s *XwalStore) LineageLinks(id string) []Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	var links []Link
	seen := map[string]struct{}{}
	for cur := id; cur != ""; {
		// A cycle would loop forever; topology should not have one, and a
		// partial lineage degrades to a miss rather than a lie.
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

// Lineage renders a trunk's ancestry as forest refs, root first, so a cache
// read below the window resolves through the ancestors that own it.
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

// Lineage on the backend is the same walk, exposed where a caller that holds a
// Backend (the angelus) can reach it. It is an OPTIONAL interface, asserted by
// the caller, so a backend that has no notion of ancestry needs no stub:
func (b *XwalBackend) Lineage(id string) []fwtree.Ref { return b.store.Lineage(id) }

// LineageLinks is the same walk with the kinds kept.
func (b *XwalBackend) LineageLinks(id string) []Link { return b.store.LineageLinks(id) }

// TopologyRev is the presentation clock: the topology form's version. It is
// the epoch a client compares when it is holding anything derived from the
// shape of the tree.
func (b *XwalBackend) TopologyRev() uint64 { return b.store.presentRev() }

// LineageBackend is implemented by backends that can name an aria's ancestry.
// The composed-turn seed asks for it and does nothing when it is absent.
type LineageBackend interface {
	Lineage(id string) []fwtree.Ref
}

// ChainBackend is implemented by backends that can name an ancestry with the
// kinds and the topology clock: what the lineage read answers from.
type ChainBackend interface {
	LineageLinks(id string) []Link
	TopologyRev() uint64
}
