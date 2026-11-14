package store

import (
	"fmt"
	"slices"

	fwtree "github.com/jack-work/figaro/figwal/tree"
	"github.com/jack-work/figaro/figwal/xwal"
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

func (s *XwalStore) lineageInfos(id string) ([]xwal.TrunkInfo, error) {
	snap := s.topologySnapshot()
	var chain []xwal.TrunkInfo
	for cur := id; cur != ""; {
		i, ok := snap.info[cur]
		if !ok {
			return nil, fmt.Errorf("lineage of %s: %w %q", id, xwal.ErrUnknownTrunk, cur)
		}
		if len(chain) >= len(snap.infos) {
			return nil, fmt.Errorf("lineage of %s: a cycle through %s", id, cur)
		}
		t := snap.infos[i]
		if t.BaseErr != nil {
			return nil, fmt.Errorf("lineage of %s: %w", id, t.BaseErr)
		}
		chain = append(chain, t)
		cur = t.Parent
	}
	slices.Reverse(chain)
	return chain, nil
}

func (s *XwalStore) LineageLinks(id string) ([]Link, error) {
	chain, err := s.lineageInfos(id)
	if err != nil || len(chain) == 0 {
		return nil, err
	}
	links := make([]Link, len(chain))
	for i, t := range chain {
		links[i] = Link{Node: t.ID, Kind: t.Kind, BaseLT: t.BranchedLT}
		if i > 0 && t.BranchedLT > 0 {
			links[i].BaseTurn, links[i].BaseTurnKnown, links[i].BaseTurnExact = s.trunks.RecordedBaseTurn(t.ID, t.BranchedLT)
		}
	}
	links[0].BaseLT = 0
	links[0].BaseTurn, links[0].BaseTurnKnown, links[0].BaseTurnExact = 0, true, true
	return links, nil
}

func (s *XwalStore) Lineage(id string) ([]fwtree.Ref, error) {
	chain, err := s.lineageInfos(id)
	if err != nil || len(chain) == 0 {
		return nil, err
	}
	refs := make([]fwtree.Ref, len(chain))
	for i, t := range chain {
		refs[i] = fwtree.Ref{Node: t.ID, Base: t.BranchedLT}
	}
	refs[0].Base = 0
	return refs, nil
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
