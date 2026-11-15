package xwal

import (
	"github.com/jack-work/figaro/figwal/tree"
	"slices"
)

func (t *Trunks) Ancestry(id string) []tree.Ref {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return nil
	}
	defer endRead()
	var refs []tree.Ref
	seen := map[string]bool{}
	for cur := id; cur != "" && !seen[cur]; {
		if _, ok := t.idx.Head(cur); !ok {
			break
		}
		seen[cur] = true
		parent, _, base := t.lineage(cur)
		refs = append(refs, tree.Ref{Node: cur, Base: base})
		cur = parent
	}
	slices.Reverse(refs)
	if len(refs) > 0 {
		refs[0].Base = 0
	}
	return refs
}

func (t *Trunks) Lineage(id string) []tree.Ref {
	return servingLineage(t.Ancestry(id))
}

func servingLineage(refs []tree.Ref) []tree.Ref {
	head := len(refs) - 1
	if head < 0 {
		return nil
	}
	for i := head - 1; i >= 0; i-- {
		if i > 0 && refs[i].Base >= refs[head].Base {
			continue
		}
		head--
		refs[head] = refs[i]
	}
	return refs[head:]
}
