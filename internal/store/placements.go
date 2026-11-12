package store

import (
	"sort"
	"strings"

	"github.com/jack-work/figaro/figwal/xwal"
)

type ForkRestore struct {
	ID        string
	Was       string
	ForkPoint string
	Kept      string
}

type PlacedNode struct {
	ID        string
	ForkPoint string
	Under     string
	Names     string
}

type PlacementReport struct {
	Placed   []PlacedNode
	Lost     []PlacedNode
	Dangling []PlacedNode
}

func idOfNode(nodes map[string]xwal.NodeInfo, key string) string {
	n, ok := nodes[key]
	switch {
	case !ok:
		return ""
	case n.IsRoot:
		return rootID
	case n.Trunk != "":
		return n.Trunk
	}
	return key
}

func (s *XwalStore) RestoreForks(dryRun bool) ([]ForkRestore, error) {
	found, err := s.RestoreForkPoints(dryRun)
	if err != nil {
		return nil, err
	}
	nodes := s.trunks.Nodes()
	out := make([]ForkRestore, 0, len(found))
	restored := false
	for _, r := range found {
		out = append(out, ForkRestore{ID: string(r.Trunk), Was: idOfNode(nodes, r.Under), ForkPoint: idOfNode(nodes, r.Origin), Kept: r.Kept})
		restored = restored || r.Kept == ""
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if dryRun || !restored {
		return out, nil
	}
	_, err = s.CleanTopology(false)
	return out, err
}

func (s *XwalStore) Placements() PlacementReport {
	var r PlacementReport
	for _, n := range s.PlacedNodes() {
		p := PlacedNode{ID: n.ID, ForkPoint: n.ForkPoint}
		if n.Unplaced {
			p.Names = strings.TrimPrefix(n.Parent, unplacedPrefix)
			r.Lost = append(r.Lost, p)
			continue
		}
		p.Under = n.Parent
		r.Placed = append(r.Placed, p)
	}
	nodes := s.trunks.Nodes()
	for key, n := range nodes {
		if n.IsRoot || n.From == "" {
			continue
		}
		if _, held := nodes[n.From]; !held {
			r.Dangling = append(r.Dangling, PlacedNode{ID: idOfNode(nodes, key), Names: n.From})
		}
	}
	for _, rows := range [][]PlacedNode{r.Placed, r.Lost, r.Dangling} {
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	}
	return r
}

func (b *XwalBackend) RestoreForks(dryRun bool) ([]ForkRestore, error) {
	return b.store.RestoreForks(dryRun)
}

func (b *XwalBackend) Placements() PlacementReport { return b.store.Placements() }
