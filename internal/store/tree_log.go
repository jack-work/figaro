package store

import (
	"sort"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// treeLog is a Log[T] whose residency IS the canonical tree cache: one node per
// lineage step, one run per entry, keyed by FigaroLT, with the budget, the
// eviction order and the index-that-survives-eviction all tree's.
//
// It replaces cachedLog's private window: a rows slice, a byte accountant, a
// trim path, and -- once the lineage is wired -- the one-shot fork donation and
// the seam probe that guarded it.
//
// THE KEY IS FigaroLT, WHICH IS WHY THE MAPPING IS EXACT. tree addresses
// (from..to] over a key space the tenant chooses; every read on this interface
// is already an LT bracket, so nothing has to be translated and no second index
// is needed to find a record.
type treeLog[T any] struct {
	inner   Log[T]
	cache   *fwtree.Cache[Entry[T]]
	lineage func() []fwtree.Ref
	node    string
	sizeOf  func(Entry[T]) int
}

var _ Log[any] = (*treeLog[any])(nil)

// NewIRCache builds THE decoded-IR cache: one instance for the whole process,
// with a node per aria. One cache is what makes fork sharing structural -- a
// child's lineage names its ancestor's node, and the prefix it reads is the
// ancestor's own runs. A cache per log would give each aria its own nodes map
// and share nothing.
//
// open resolves a node to its substrate; the Source asks that substrate for
// exactly the bracket the coord names.
func NewIRCache[T any](budget *fwtree.Budget, open func(node string) Log[T], sizeOf func(Entry[T]) int) *fwtree.Cache[Entry[T]] {
	return fwtree.New[Entry[T]](
		func(co fwtree.Coord) ([]Entry[T], error) {
			n := int(co.To - co.From)
			if n <= 0 {
				return nil, nil
			}
			sub := open(co.Node)
			if sub == nil {
				return nil, nil
			}
			return sub.ReadFrom(co.From+1, n), nil
		},
		budget,
		func(e Entry[T]) int {
			if sizeOf == nil {
				return 0
			}
			return sizeOf(e)
		},
		func(e Entry[T]) uint64 { return e.FigaroLT },
	)
}

// newTreeLog binds one node of the shared cache to its substrate. lineage
// reports the node chain, root-first, so a fork reads its prefix out of its
// ancestor's node.
func newTreeLog[T any](inner Log[T], node string, cache *fwtree.Cache[Entry[T]], sizeOf func(Entry[T]) int, lineage func() []fwtree.Ref) *treeLog[T] {
	return &treeLog[T]{inner: inner, node: node, cache: cache, sizeOf: sizeOf, lineage: lineage}
}

func (l *treeLog[T]) refs() []fwtree.Ref {
	if l.lineage != nil {
		if refs := l.lineage(); len(refs) > 0 {
			return refs
		}
	}
	return []fwtree.Ref{{Node: l.node}}
}

// span reads (from..to] through the cache, materializing what is missing.
func (l *treeLog[T]) span(from, to uint64) []Entry[T] {
	if to <= from {
		return nil
	}
	out, err := l.cache.Range(l.refs(), from, to)
	if err != nil {
		return nil
	}
	return out
}

// Read is the whole channel, THROUGH THE CACHE. Passing it to the substrate
// instead would mint a fresh copy of every string on every call -- and Read is
// what a fork's first projection uses, so the prefix its ancestor holds would
// be duplicated rather than shared.
func (l *treeLog[T]) Read() []Entry[T] {
	tail, ok := l.inner.PeekTail()
	if !ok {
		return nil
	}
	return l.span(0, tail.FigaroLT)
}

func (l *treeLog[T]) Len() int { return l.inner.Len() }

func (l *treeLog[T]) ReadFrom(figaroLT uint64, n int) []Entry[T] {
	if figaroLT == 0 {
		figaroLT = 1
	}
	tail, _ := l.inner.PeekTail()
	to := tail.FigaroLT
	if n > 0 && figaroLT+uint64(n)-1 < to {
		to = figaroLT + uint64(n) - 1
	}
	return l.span(figaroLT-1, to)
}

func (l *treeLog[T]) ReadPage(from, before uint64, n int) ([]Entry[T], int) {
	total := l.inner.Len()
	rows, _ := l.inner.ReadPage(from, before, n)
	return rows, total
}

func (l *treeLog[T]) Lookup(figaroLT uint64) (Entry[T], bool) {
	got := l.span(figaroLT-1, figaroLT)
	for _, e := range got {
		if e.FigaroLT == figaroLT {
			return e, true
		}
	}
	var zero Entry[T]
	return zero, false
}

func (l *treeLog[T]) PeekTail() (Entry[T], bool) { return l.inner.PeekTail() }

func (l *treeLog[T]) Append(e Entry[T]) (Entry[T], error) {
	stamped, err := l.inner.Append(e)
	if err != nil {
		return stamped, err
	}
	// The freshest record is seeded rather than left for a read to fetch: the
	// writer already holds it, and a tail read is the next thing that happens.
	l.cache.Put(fwtree.Coord{Node: l.node, From: stamped.FigaroLT - 1, To: stamped.FigaroLT},
		[]Entry[T]{stamped}, false)
	return stamped, nil
}

func (l *treeLog[T]) Clear() error {
	l.cache.DropNode(l.node)
	return l.inner.Clear()
}

// TailAfter is the projection's read: everything after lt, plus the channel's
// total.
func (l *treeLog[T]) TailAfter(lt uint64) ([]Entry[T], int) {
	tail, ok := l.inner.PeekTail()
	if !ok {
		return nil, l.inner.Len()
	}
	return l.span(lt, tail.FigaroLT), l.inner.Len()
}

// TailSnapshot is the last n entries, ascending.
func (l *treeLog[T]) TailSnapshot(n int) []Entry[T] {
	if n <= 0 {
		return nil
	}
	tail, ok := l.inner.PeekTail()
	if !ok {
		return nil
	}
	from := uint64(0)
	if tail.FigaroLT > uint64(n) {
		from = tail.FigaroLT - uint64(n)
	}
	return l.span(from, tail.FigaroLT)
}

// ResidentBytes and Resident report what the tree holds for this node, so
// doctor and the reaper see one number from one place.
func (l *treeLog[T]) ResidentBytes() int {
	var n int64
	for _, r := range l.cache.Index(l.node) {
		if r.Resident {
			n += r.Bytes
		}
	}
	return int(n)
}

func (l *treeLog[T]) Resident() int {
	n := 0
	for _, r := range l.cache.Index(l.node) {
		if r.Resident {
			n += int(r.To - r.From)
		}
	}
	return n
}

// Trim drops this node's runs down to keep entries, which is the reaper's
// control surface. The budget does the ordinary work; this is the lifecycle
// half, for an aria nobody is talking to.
func (l *treeLog[T]) Trim(keep int) int {
	idx := l.cache.Index(l.node)
	if keep < 0 {
		keep = 0
	}
	resident := 0
	for _, r := range idx {
		if r.Resident {
			resident += int(r.To - r.From)
		}
	}
	released := 0
	for _, r := range idx {
		if resident-released <= keep {
			break
		}
		if !r.Resident {
			continue
		}
		l.cache.Drop(fwtree.Coord{Node: l.node, From: r.From, To: r.To})
		released += int(r.To - r.From)
	}
	return released
}

// residentBelow is the fork donation's old shape, kept only while both caches
// coexist: with the lineage wired, a child reads its ancestor's node directly
// and nothing is donated.
func (l *treeLog[T]) residentBelow(figaroLT uint64) []Entry[T] {
	if figaroLT == 0 {
		return nil
	}
	got := l.span(0, figaroLT-1)
	i := sort.Search(len(got), func(i int) bool { return got[i].FigaroLT >= figaroLT })
	return got[:i]
}
