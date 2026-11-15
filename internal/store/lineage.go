package store

import fwtree "github.com/jack-work/figaro/figwal/tree"

type Link struct {
	Node          string
	Kind          string
	BaseLT        uint64
	BaseTurn      uint64
	BaseTurnKnown bool
	BaseTurnExact bool
}

func (s *XwalStore) LineageLinks(id string) []Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	var links []Link
	for _, ref := range s.trunks.Ancestry(id) {
		t, ok := s.trunks.InfoLight(ref.Node)
		if !ok {
			break
		}
		links = append(links, Link{Node: ref.Node, Kind: t.Kind, BaseLT: ref.Base, BaseTurn: t.BranchedTurn, BaseTurnKnown: t.BranchedTurnKnown, BaseTurnExact: t.BranchedTurnExact})
	}
	if len(links) > 0 {
		links[0].BaseTurn, links[0].BaseTurnKnown, links[0].BaseTurnExact = 0, true, true
	}
	return links
}

func (s *XwalStore) Lineage(id string) []fwtree.Ref { return s.trunks.Lineage(id) }

func (b *XwalBackend) Lineage(id string) []fwtree.Ref { return b.store.Lineage(id) }
func (b *XwalBackend) LineageLinks(id string) []Link  { return b.store.LineageLinks(id) }
func (b *XwalBackend) TopologyRev() uint64            { return b.store.presentRev() }

type LineageBackend interface{ Lineage(id string) []fwtree.Ref }
type ChainBackend interface {
	LineageLinks(id string) []Link
	TopologyRev() uint64
}
