package form

import "encoding/json"

func OldApply(s Snapshot, p Patch) Snapshot {
	t, changed := oldApplyToTree(s.t, p)
	if !changed {
		return s
	}
	return Snapshot{t: t, raw: &rawBox{}}
}

func OldDiff(next, prev Snapshot) Patch {
	p := oldDiffTrees(prev.t, next.t)
	if p.IsIdentity() {
		return Patch{}
	}
	return p
}

func oldDiffTrees(prev, next ptree) Patch {
	if prev.root == next.root {
		return Patch{}
	}
	out := &ObjectPatch{}
	rangeEntries(next.root, func(n *node) bool {
		old := lookupExact(prev.root, n.key)
		if old == n {
			return true
		}
		switch {
		case old == nil:
			if n.branch {
				child := oldDiffTrees(ptree{}, n.kids)
				if child.IsIdentity() {
					if out.Set == nil {
						out.Set = map[string]Value{}
					}
					out.Set[n.key] = NewValue(json.RawMessage(`{}`))
				} else {
					if out.Update == nil {
						out.Update = map[string]Patch{}
					}
					if out.New == nil {
						out.New = map[string]bool{}
					}
					out.Update[n.key], out.New[n.key] = child, true
				}
				return true
			}
			if out.Set == nil {
				out.Set = map[string]Value{}
			}
			out.Set[n.key] = n.value
		case old.branch && n.branch:
			child := oldDiffTrees(old.kids, n.kids)
			if !child.IsIdentity() {
				if out.Update == nil {
					out.Update = map[string]Patch{}
				}
				out.Update[n.key] = child
			}
		case !old.branch && !n.branch:
			if !old.value.Equal(n.value) {
				if out.Update == nil {
					out.Update = map[string]Patch{}
				}
				out.Update[n.key] = Patch{Scalar: &ScalarPatch{Before: old.value, After: n.value}}
			}
		default:
			if out.Update == nil {
				out.Update = map[string]Patch{}
			}
			out.Update[n.key] = Patch{Scalar: &ScalarPatch{
				Before: entryValue(old), After: entryValue(n)}}
		}
		return true
	})
	rangeEntries(prev.root, func(n *node) bool {
		if lookupExact(next.root, n.key) == nil {
			if out.Delete == nil {
				out.Delete = map[string]Value{}
			}
			out.Delete[n.key] = entryValue(n)
		}
		return true
	})
	return Patch{Object: out}
}

func oldApplyToTree(t ptree, p Patch) (ptree, bool) {
	switch {
	case p.Object != nil:
		return oldApplyObjectToTree(t, p.Object)
	case p.Scalar != nil:
		if _, isObj := p.Scalar.After.members(); !isObj {
			return t, false
		}
		next := leafOrBranch("", p.Scalar.After).kids
		if oldDiffTrees(t, next).IsIdentity() {
			return t, false
		}
		return next, true
	}
	return t, false
}

func oldApplyObjectToTree(t ptree, o *ObjectPatch) (ptree, bool) {
	changed := false
	for k := range o.Delete {
		if root, found := deleteNode(t.root, k); found {
			t, changed = ptree{root: root}, true
		}
	}
	for k, v := range o.Set {
		if cur := lookupExact(t.root, k); cur != nil && !cur.branch && cur.value.Equal(v) {
			continue
		}
		t = ptree{root: setEntry(t.root, leafOrBranch(k, v))}
		changed = true
	}
	for k, child := range o.Update {
		next, ok := oldApplyAt(lookupExact(t.root, k), k, child)
		if !ok {
			continue
		}
		t = ptree{root: setEntry(t.root, next)}
		changed = true
	}
	return t, changed
}

func oldApplyAt(cur *node, key string, p Patch) (*node, bool) {
	if p.IsIdentity() {
		return nil, false
	}
	switch {
	case p.Scalar != nil:
		if cur != nil && !cur.branch && cur.value.Equal(p.Scalar.After) {
			return nil, false
		}
		return leafOrBranch(key, p.Scalar.After), true
	case p.Object != nil:
		var kids ptree
		switch {
		case cur == nil:
		case cur.branch:
			kids = cur.kids
		case !nullish(cur.value):
			return nil, false
		}
		sub, changed := oldApplyObjectToTree(kids, p.Object)
		if !changed && cur != nil && cur.branch {
			return nil, false
		}
		return &node{key: key, kids: sub, branch: true}, true
	case p.List != nil:
		if cur == nil || cur.branch {
			return nil, false
		}
		next := p.List.apply(cur.value)
		if next.Equal(cur.value) {
			return nil, false
		}
		return leafOrBranch(key, next), true
	}
	return nil, false
}
