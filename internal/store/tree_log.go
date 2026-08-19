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
	inner Log[T]
	// key is the coordinate this channel is ADDRESSED BY: FigaroLT on both
	// channels, because that is what every reader passes.
	//
	// ON THE TRANSLATION CHANNEL IT IS NOT UNIQUE -- FigaroLT there is a
	// FOREIGN KEY naming the IR record a row translates, so a turn's rows all
	// carry the same value and one coordinate holds several units. The tree
	// allows that (a run is a bracket, not a row); what it forbids is seeding
	// ONE row as if it were the whole coordinate, which is why seedOnAppend
	// exists.
	key func(Entry[T]) uint64

	// seedOnAppend is true only where a coordinate holds exactly one entry.
	seedOnAppend bool
	cache        *fwtree.Cache[Entry[T]]
	lineage      func() []fwtree.Ref
	node         string
	sizeOf       func(Entry[T]) int

	// openNode resolves an ANCESTOR's node to its substrate, for the peek path
	// that must read a prefix nobody holds without materializing it.
	openNode func(node string) Log[T]
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
func NewIRCache[T any](budget *fwtree.Budget, open func(node string) Log[T], sizeOf func(Entry[T]) int, key func(Entry[T]) uint64) *fwtree.Cache[Entry[T]] {
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
			// CLIPPED TO THE BRACKET. ReadFrom counts entries, and the key
			// space is not dense -- FigaroLT skips wherever a record was
			// filtered or rewritten -- so a count can walk past To and the
			// cache would hold units outside the run it charged them to.
			return clipToBracket(sub.ReadFrom(co.From+1, n), co.From, co.To, key), nil
		},
		budget,
		func(e Entry[T]) int {
			if sizeOf == nil {
				return 0
			}
			return sizeOf(e)
		},
		key,
	)
}

// newTreeLog binds one node of the shared cache to its substrate. lineage
// reports the node chain, root-first, so a fork reads its prefix out of its
// ancestor's node.
func newTreeLog[T any](inner Log[T], node string, cache *fwtree.Cache[Entry[T]], sizeOf func(Entry[T]) int, key func(Entry[T]) uint64, lineage func() []fwtree.Ref) *treeLog[T] {
	return &treeLog[T]{inner: inner, node: node, cache: cache, sizeOf: sizeOf, key: key, lineage: lineage}
}

// irKey addresses a channel whose FigaroLT is unique per record: the fig IR.
func irKey[T any](e Entry[T]) uint64 { return e.FigaroLT }

// transKey addresses the translation channel: FigaroLT, the IR record a row
// translates, which is what every reader passes and what the substrate seeks
// by. A row written WITHOUT one cannot be addressed by any reader either, so it
// is not a case this cache can rescue -- it is a malformed write.
func transKey[T any](e Entry[T]) uint64 { return e.FigaroLT }

// seedingTail marks a channel whose coordinate holds exactly one entry, so the
// writer's own append can be published without replacing anything.
func (l *treeLog[T]) seedingTail() *treeLog[T] { l.seedOnAppend = true; return l }

// withNodeOpener gives the log a way to read an ancestor's substrate directly,
// which the peek path needs and the materializing path gets from the Source.
func (l *treeLog[T]) withNodeOpener(open func(node string) Log[T]) *treeLog[T] {
	l.openNode = open
	return l
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

// Read is the whole channel, SERVED FROM WHAT IS ALREADY RESIDENT AND READ
// FROM THE SUBSTRATE FOR THE REST -- and it inserts nothing.
//
// TWO POLICIES MEET HERE AND BOTH SURVIVE. A fork's first projection calls
// Read, so passing it straight to the substrate would mint a fresh copy of
// every string its ancestor already holds (measured: 296 of 296). But routing
// it INTO the cache would make one whole-history read evict every other aria's
// ranges, which is the pass-through scan_policy_test.go was written to guard --
// "green before the re-seat and green after, or the policy did not survive".
//
// Peeking gives both: the resident prefix is the ancestor's own units, and a
// scan of what nobody holds costs the neighbours nothing.
func (l *treeLog[T]) Read() []Entry[T] {
	tail, ok := l.inner.PeekTail()
	if !ok {
		return nil
	}
	return l.peek(0, l.key(tail))
}

// peek serves (from..to] from resident runs where they exist and from the
// substrate where they do not, WITHOUT materializing anything into the cache.
func (l *treeLog[T]) peek(from, to uint64) []Entry[T] {
	if to <= from {
		return nil
	}
	var out []Entry[T]
	pos := from
	for _, cut := range l.cuts(from, to) {
		p := cut.From
		for _, r := range l.cache.Index(cut.Node) {
			if !r.Resident || r.To <= p || r.From >= cut.To {
				continue
			}
			if r.From > p {
				out = append(out, l.substrate(cut.Node, p, r.From)...)
			}
			units, ok := l.cache.ResidentAt(cut.Node, r.From, r.To)
			if !ok {
				units = l.substrate(cut.Node, r.From, r.To)
			}
			for _, u := range units {
				if k := l.key(u); k > p && k <= cut.To {
					out = append(out, u)
				}
			}
			if r.To > p {
				p = r.To
			}
		}
		if p < cut.To {
			out = append(out, l.substrate(cut.Node, p, cut.To)...)
		}
		pos = cut.To
	}
	_ = pos
	return out
}

// cuts splits (from..to] across the lineage by fork base, root first: the same
// division tree.Range makes, so a peek and a materializing read see one shape.
func (l *treeLog[T]) cuts(from, to uint64) []fwtree.Coord {
	refs := l.refs()
	var out []fwtree.Coord
	lo := from
	for i, ref := range refs {
		hi := to
		if i+1 < len(refs) && refs[i+1].Base > 0 && refs[i+1].Base-1 < hi {
			hi = refs[i+1].Base - 1
		}
		if hi > lo {
			out = append(out, fwtree.Coord{Node: ref.Node, From: lo, To: hi})
			lo = hi
		}
		if lo >= to {
			break
		}
	}
	return out
}

// substrate reads a bracket from the durable log of ONE node, never the cache.
func (l *treeLog[T]) substrate(node string, from, to uint64) []Entry[T] {
	n := int(to - from)
	if n <= 0 {
		return nil
	}
	if node == l.node {
		return clipToBracket(l.inner.ReadFrom(from+1, n), from, to, l.key)
	}
	if l.openNode == nil {
		return nil
	}
	sub := l.openNode(node)
	if sub == nil {
		return nil
	}
	return clipToBracket(sub.ReadFrom(from+1, n), from, to, l.key)
}

// clipToBracket keeps the entries whose key lies in (from..to].
func clipToBracket[T any](rows []Entry[T], from, to uint64, key func(Entry[T]) uint64) []Entry[T] {
	out := rows[:0:0]
	for _, e := range rows {
		if k := key(e); k > from && k <= to {
			out = append(out, e)
		}
	}
	return out
}

func (l *treeLog[T]) Len() int { return l.inner.Len() }

func (l *treeLog[T]) ReadFrom(figaroLT uint64, n int) []Entry[T] {
	if figaroLT == 0 {
		figaroLT = 1
	}
	tail, _ := l.inner.PeekTail()
	to := l.key(tail)
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

// Lookup addresses by FigaroLT, WHICH IS NOT ALWAYS THIS CACHE'S COORDINATE.
// On the fig IR the two are the same and the cache answers; on the translation
// channel FigaroLT is a foreign key -- every row of a turn carries the IR
// record it translates -- so the substrate answers, because it owns that
// addressing and the cache does not.
func (l *treeLog[T]) Lookup(figaroLT uint64) (Entry[T], bool) {
	for _, e := range l.span(figaroLT-1, figaroLT) {
		if e.FigaroLT == figaroLT {
			return e, true
		}
	}
	return l.inner.Lookup(figaroLT)
}

func (l *treeLog[T]) PeekTail() (Entry[T], bool) { return l.inner.PeekTail() }

func (l *treeLog[T]) Append(e Entry[T]) (Entry[T], error) {
	stamped, err := l.inner.Append(e)
	if err != nil {
		return stamped, err
	}
	// The freshest record is seeded rather than left for a read to fetch: the
	// writer already holds it, and a tail read is the next thing that happens.
	if k := l.key(stamped); l.seedOnAppend && k > 0 {
		l.cache.Put(fwtree.Coord{Node: l.node, From: k - 1, To: k}, []Entry[T]{stamped}, false)
	}
	return stamped, nil
}

func (l *treeLog[T]) Clear() error {
	l.cache.DropNode(l.node)
	return l.inner.Clear()
}

// TailAfter is the projection's read: everything after the WAL coordinate lt,
// plus the channel's total.
//
// ITS WATERMARK IS AN LT, NOT A FigaroLT, and the cache is keyed by FigaroLT.
// The two agree on an unforked lineage and diverge after a rewrite, so the
// boundary is resolved through the substrate rather than assumed -- assuming
// they are the same number is how a fork would serve the wrong suffix.
func (l *treeLog[T]) TailAfter(lt uint64) ([]Entry[T], int) {
	total := l.inner.Len()
	suffix, _ := TailAfter[T](l.inner, lt)
	if len(suffix) == 0 {
		return nil, total
	}
	from := l.key(suffix[0]) - 1
	to := l.key(suffix[len(suffix)-1])
	return l.span(from, to), total
}

// TailSnapshot is the last n ENTRIES, ascending -- a count, not a coordinate
// span. The key space is sparse, so the bracket is resolved through the
// substrate rather than by subtracting n from the tail's key.
func (l *treeLog[T]) TailSnapshot(n int) []Entry[T] {
	if n <= 0 {
		return nil
	}
	rows := TailSnapshot[T](l.inner, n)
	if len(rows) == 0 {
		return nil
	}
	return l.span(l.key(rows[0])-1, l.key(rows[len(rows)-1]))
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

// Trim drops this node's runs down to AT MOST keep entries -- run granularity,
// not row granularity, because a run is the unit the tree evicts. It is the
// reaper's
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
	i := sort.Search(len(got), func(i int) bool { return l.key(got[i]) >= figaroLT })
	return got[:i]
}
