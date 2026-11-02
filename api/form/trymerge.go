package form

func TryMerge(p, q Patch) (Patch, bool) {
	switch {
	case p.IsIdentity():
		return q, true
	case q.IsIdentity():
		return p, true
	case p.Scalar != nil && q.Scalar != nil:
		return Patch{Scalar: &ScalarPatch{Before: p.Scalar.Before, After: q.Scalar.After}}, true
	case p.Scalar != nil && q.Object != nil:
		after, err := q.Apply(p.Scalar.After)
		if err != nil {
			return Patch{}, false
		}
		return Patch{Scalar: &ScalarPatch{Before: p.Scalar.Before, After: after}}, true
	case p.Object != nil && q.Object != nil:
		o, ok := tryMergeObject(p.Object, q.Object)
		if !ok {
			return Patch{}, false
		}
		return Patch{Object: o}, true
	}
	return Patch{}, false
}

type objectOp uint8

const (
	opNone objectOp = iota
	opSet
	opDelete
	opUpdate
	opMixed
)

func opOf(o *ObjectPatch, k string) objectOp {
	n, op := 0, opNone
	if _, ok := o.Set[k]; ok {
		n, op = n+1, opSet
	}
	if _, ok := o.Delete[k]; ok {
		n, op = n+1, opDelete
	}
	if _, ok := o.Update[k]; ok {
		n, op = n+1, opUpdate
	}
	if n > 1 {
		return opMixed
	}
	return op
}

func tryMergeObject(p, q *ObjectPatch) (*ObjectPatch, bool) {
	out := &ObjectPatch{}
	keys := map[string]bool{}
	for _, o := range []*ObjectPatch{p, q} {
		for k := range o.Set {
			keys[k] = true
		}
		for k := range o.Delete {
			keys[k] = true
		}
		for k := range o.Update {
			keys[k] = true
		}
	}
	for k := range keys {
		if !mergeKey(out, k, p, q) {
			return nil, false
		}
	}
	return out, true
}

func mergeKey(out *ObjectPatch, k string, p, q *ObjectPatch) bool {
	pOp, qOp := opOf(p, k), opOf(q, k)
	if pOp == opMixed || qOp == opMixed {
		return false
	}
	switch {
	case qOp == opNone:
		copyOp(out, p, k)
	case pOp == opNone:
		copyOp(out, q, k)
	case qOp == opSet || qOp == opDelete:
		copyOp(out, q, k)
	case pOp == opDelete:
		putDelete(out, k, p.Delete[k])
		copyOp(out, q, k)
	case pOp == opSet:
		next, err := q.Update[k].Apply(p.Set[k])
		if err != nil {
			return false
		}
		putSet(out, k, next)
	case q.Update[k].Scalar != nil:
		putSet(out, k, q.Update[k].Scalar.After)
	default:
		child, ok := TryMerge(p.Update[k], q.Update[k])
		if !ok {
			return false
		}
		putUpdate(out, k, child, p.New[k] || q.New[k])
	}
	return true
}

func copyOp(out, from *ObjectPatch, k string) {
	if v, ok := from.Set[k]; ok {
		putSet(out, k, v)
	}
	if v, ok := from.Delete[k]; ok {
		putDelete(out, k, v)
	}
	if c, ok := from.Update[k]; ok {
		putUpdate(out, k, c, from.New[k])
	}
}

func putSet(o *ObjectPatch, k string, v Value) {
	if o.Set == nil {
		o.Set = map[string]Value{}
	}
	o.Set[k] = v
}

func putDelete(o *ObjectPatch, k string, v Value) {
	if o.Delete == nil {
		o.Delete = map[string]Value{}
	}
	o.Delete[k] = v
}

func putUpdate(o *ObjectPatch, k string, c Patch, isNew bool) {
	if o.Update == nil {
		o.Update = map[string]Patch{}
	}
	o.Update[k] = c
	if isNew {
		if o.New == nil {
			o.New = map[string]bool{}
		}
		o.New[k] = true
	}
}
