package form

import (
	"runtime"
	"sync/atomic"
)

const (
	nodeOverhead  = 96
	valueOverhead = 112
	entryOverhead = 64
)

var memoBytes, canonBytes atomic.Int64

func MemoBytes() (members, canonical int64) {
	return memoBytes.Load(), canonBytes.Load()
}

func accountMemo(box *objBox, members map[string]Value, raw int) {
	n := int64(raw + len(members)*(entryOverhead+valueOverhead))
	memoBytes.Add(n)
	runtime.AddCleanup(box, func(n int64) { memoBytes.Add(-n) }, n)
}

func accountCanon(box *canonBox, canon []byte) {
	n := int64(len(canon))
	canonBytes.Add(n)
	runtime.AddCleanup(box, func(n int64) { canonBytes.Add(-n) }, n)
}

func (v Value) ApproxBytes() int {
	return valueOverhead + len(v.raw)
}

func (s Snapshot) ApproxBytes() int {
	return treeBytes(s.t.root)
}

func treeBytes(n *node) int {
	if n == nil {
		return 0
	}
	b := nodeOverhead + len(n.key) + treeBytes(n.left) + treeBytes(n.right)
	if n.branch {
		return b + treeBytes(n.kids.root)
	}
	return b + n.value.ApproxBytes()
}

func (p Patch) ApproxBytes() int {
	switch {
	case p.Scalar != nil:
		return p.Scalar.Before.ApproxBytes() + p.Scalar.After.ApproxBytes()
	case p.Object != nil:
		return p.Object.approxBytes()
	case p.List != nil:
		return p.List.approxBytes()
	}
	return 0
}

func (o *ObjectPatch) approxBytes() int {
	n := 0
	for k, v := range o.Set {
		n += entryOverhead + len(k) + v.ApproxBytes()
	}
	for k, v := range o.Delete {
		n += entryOverhead + len(k) + v.ApproxBytes()
	}
	for k, c := range o.Update {
		n += entryOverhead + len(k) + c.ApproxBytes()
	}
	return n + len(o.New)*entryOverhead
}

func (l *ListPatch) approxBytes() int {
	n := 0
	for _, kv := range l.Create {
		n += len(kv.Key) + kv.Value.ApproxBytes()
	}
	for _, kv := range l.Delete {
		n += len(kv.Key) + kv.Value.ApproxBytes()
	}
	for k, c := range l.Update {
		n += entryOverhead + len(k) + c.ApproxBytes()
	}
	if l.Order != nil {
		for _, k := range l.Order.Declared {
			n += len(k) + 16
		}
		for _, k := range l.Order.Prior {
			n += len(k) + 16
		}
	}
	return n
}
