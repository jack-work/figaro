package store

import (
	fwforest "github.com/jack-work/figaro/internal/store/tree"
)

// Lineage renders a trunk's ancestry as forest refs, root first, so a cache
// read below the window resolves through the ancestors that own it.
//
// Ref.Base is the first coordinate the node OWNS. figwal writes that same
// value into each node's .fork marker and figaro carries it as BranchedLT, so
// no arithmetic is needed here; forkbase_convention_test.go pins the three
// against each other.
func (s *XwalStore) Lineage(id string) []fwforest.Ref {
	s.mu.Lock()
	infos := s.trunks.ListLight()
	s.mu.Unlock()

	by := make(map[string]int, len(infos))
	for i, t := range infos {
		by[t.ID] = i
	}

	var refs []fwforest.Ref
	seen := map[string]struct{}{}
	for cur := id; cur != ""; {
		i, ok := by[cur]
		if !ok {
			break
		}
		// A cycle would loop forever; topology should not have one, and a
		// partial lineage degrades to a miss rather than a lie.
		if _, dup := seen[cur]; dup {
			break
		}
		seen[cur] = struct{}{}
		t := infos[i]
		refs = append(refs, fwforest.Ref{Node: t.ID, Base: t.BranchedLT})
		cur = t.Parent
	}
	if len(refs) == 0 {
		return nil
	}
	for l, r := 0, len(refs)-1; l < r; l, r = l+1, r-1 {
		refs[l], refs[r] = refs[r], refs[l]
	}
	refs[0].Base = 0 // the root owns from the beginning
	return refs
}

// Lineage on the backend is the same walk, exposed where a caller that holds a
// Backend (the angelus) can reach it. It is an OPTIONAL interface, asserted by
// the caller, so a backend that has no notion of ancestry needs no stub:
//
//	type LineageBackend interface{ Lineage(id string) []fwforest.Ref }
func (b *XwalBackend) Lineage(id string) []fwforest.Ref { return b.store.Lineage(id) }

// LineageBackend is implemented by backends that can name an aria's ancestry.
// The composed-turn seed asks for it and does nothing when it is absent.
type LineageBackend interface {
	Lineage(id string) []fwforest.Ref
}
