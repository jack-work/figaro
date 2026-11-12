package xwal

import (
	"fmt"
	"sort"
)

type RestoredForkPoint struct {
	Node   string
	Trunk  TrunkID
	Under  string
	Origin string
	Kept   string
}

func (t *Trunks) RestoreForkPoints(dryRun bool) ([]RestoredForkPoint, error) {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return nil, err
	}
	var out []RestoredForkPoint
	for key, n := range t.idx.All() {
		if n.IsRoot || n.From == "" || n.Trunk == "" {
			continue
		}
		up := t.node(n.From)
		if up == nil || up.IsRoot || up.Trunk == "" {
			continue
		}
		base, upBase := t.forkBaseOf(key), t.forkBaseOf(n.From)
		if base >= upBase {
			continue
		}
		r := RestoredForkPoint{Node: key, Trunk: n.Trunk, Under: n.From}
		at := uint64(0)
		if base > 0 {
			at = base - 1
		}
		origin, err := t.ownerOf(n.From, at)
		switch o := t.node(origin); {
		case err != nil:
			r.Kept = err.Error()
		case o == nil || o.IsRoot:
			r.Kept = "its cut lies in no node of its lineage"
		default:
			r.Origin = origin
		}
		out = append(out, r)
	}
	endRead()
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	if dryRun {
		return out, nil
	}
	for i := range out {
		if out[i].Origin == "" {
			continue
		}
		if err := t.Refork(out[i].Node, out[i].Origin); err != nil {
			out[i].Origin, out[i].Kept = "", fmt.Sprintf("its history does not read the same from %s: %v", out[i].Origin, err)
		}
	}
	return out, nil
}

func (s *Store) RestoreForkPoints(dryRun bool) ([]RestoredForkPoint, error) {
	s.syncDirty()
	return s.Trunks.RestoreForkPoints(dryRun)
}
