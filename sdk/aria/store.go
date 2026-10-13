package aria

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jack-work/figaro/api/livedoc"
)

// The contract is skills/figaro/contributing/range-store.md.

// Range is a contiguous run: it holds every node in [From, To], inclusive.
type Range struct {
	From, To Anchor
	Msgs     []Message
}

// Gap is a hole: nothing held in [From, To], at least one real node believed there.
type Gap struct {
	From, To Anchor
}

// Turns is how many turns the hole swallows whole.
func (g Gap) Turns() int {
	lo, hi := g.From.Turn, g.To.Turn
	if g.From.Node > 0 {
		lo++
	}
	if g.To.Node != maxNode {
		if hi == 0 {
			return 0
		}
		hi--
	}
	if hi < lo {
		return 0
	}
	return int(hi-lo) + 1
}

// Segment is a contiguous run, optionally followed by a hole. Msgs may be empty.
type Segment struct {
	Msgs []Message
	Gap  *Gap
}

// Pending is a submitted prompt the drain has not yet classified, so it has no
// server coordinate.
type Pending struct {
	Text string
	At   time.Time
}

// openTail is the one streaming suffix, disjoint from every range; turn == 0
// means nothing is open. base is the lowest ordinal delivered (the read may
// have met the turn partway up); from (Live.From) is the lowest still mutable.
type openTail struct {
	turn  int
	base  uint64 // meaningless until held
	held  bool
	from  uint64
	v     int
	nodes []livedoc.Node
}

type Store struct {
	ranges  []Range // sorted by From; non-overlapping; never adjacent
	more    More
	open    openTail
	pending []Pending
	ends    map[uint64]uint64 // turn -> anchors it occupies, once known
}

// Complete reports whether a turn's extent is known, which it is once sealed.
func (s *Store) Complete(turn int) bool { return s.ends[uint64(turn)] != 0 }

func NewStore() *Store { return &Store{ends: map[uint64]uint64{}} }

// Fetcher reads the history immediately before an anchor; Ensure calls it.
type Fetcher func(ctx context.Context, before Anchor, limit int) (Page, error)

var ErrNoFetcher = errors.New("aria: range store has no fetcher")

// ErrStalled: the fetcher keeps answering but the hole stops shrinking.
var ErrStalled = errors.New("aria: the hole stopped shrinking")

const ensureRounds = 64

// spanEnd is the last node ordinal a message covers. A node-less message
// still occupies exactly one anchor, its own (Turn, From).
func spanEnd(m Message) uint64 {
	if len(m.Nodes) == 0 {
		return m.From
	}
	return m.From + uint64(len(m.Nodes)) - 1
}

func msgSpan(m Message) (Anchor, Anchor) {
	t := uint64(m.Turn)
	return Anchor{Turn: t, Node: m.From}, Anchor{Turn: t, Node: spanEnd(m)}
}

// sliceNodes cuts a message down to [lo, hi]. The inquiry survives only at
// offset 0, or a later slice would print the question again.
func sliceNodes(m Message, lo, hi uint64) Message {
	if len(m.Nodes) == 0 || (lo <= m.From && hi >= spanEnd(m)) {
		return m
	}
	if lo < m.From {
		lo = m.From
	}
	if e := spanEnd(m); hi > e {
		hi = e
	}
	out := Message{Turn: m.Turn, From: lo, Role: m.Role, Nodes: m.Nodes[lo-m.From : hi-m.From+1]}
	if out.From == 0 {
		out.Inquiry = m.Inquiry
		out.InquirySegments = m.InquirySegments
		out.InquiryLT = m.InquiryLT
	}
	return out
}

// SetTurnLen records how many anchors a turn occupies: the only way to know
// that (t, n) and (t+1, 0) are neighbours. An inquiry-only turn occupies one.
func (s *Store) SetTurnLen(turn uint64, n uint64) {
	if s.ends == nil {
		s.ends = map[uint64]uint64{}
	}
	if cur, ok := s.ends[turn]; ok && cur >= n {
		return
	}
	s.ends[turn] = n
	if n == 0 {
		// An empty turn can bridge ranges arbitrarily far apart.
		s.coalesce()
		return
	}
	s.coalesceAround(turn)
}

func (s *Store) TurnLen(turn uint64) (uint64, bool) {
	n, ok := s.ends[turn]
	return n, ok
}

// adjacent reports whether nothing can exist strictly between a and b. Unknown
// turn extents answer false: a false gap is honest, a false adjacency fabricates
// neighbours.
func (s *Store) adjacent(a, b Anchor) bool {
	if !a.Less(b) {
		return false
	}
	if b == a.Next() {
		return true
	}
	if b.Node != 0 || b.Turn <= a.Turn {
		return false
	}
	if n, ok := s.ends[a.Turn]; !ok || a.Node+1 != n {
		return false
	}
	// An intervening turn is a hole until proven empty.
	for t := a.Turn + 1; t < b.Turn; t++ {
		if n, ok := s.ends[t]; !ok || n != 0 {
			return false
		}
	}
	return true
}

// mergeAt fuses ranges i and i+1 if they are adjacent, reporting whether it did.
func (s *Store) mergeAt(i int) bool {
	if i < 0 || i+1 >= len(s.ranges) {
		return false
	}
	boundary := s.ranges[i].To
	if !s.adjacent(boundary, s.ranges[i+1].From) {
		return false
	}
	s.ranges[i].Msgs = append(s.ranges[i].Msgs, s.ranges[i+1].Msgs...)
	s.ranges[i].To = s.ranges[i+1].To
	s.ranges = append(s.ranges[:i+1], s.ranges[i+2:]...)
	s.consume(boundary, s.ranges[i].To)
	return true
}

// consume drops a turn extent once its boundary is interior to a range, so
// `ends` stays proportional to range boundaries rather than turns.
func (s *Store) consume(boundary, to Anchor) {
	if boundary.Turn < to.Turn {
		delete(s.ends, boundary.Turn)
	}
}

func (s *Store) coalesce() {
	for i := 0; i+1 < len(s.ranges); {
		if !s.mergeAt(i) {
			i++
		}
	}
}

// coalesceAround merges only the neighbourhood of one turn: learning its extent
// can only join a range that ends in it to its successor.
func (s *Store) coalesceAround(turn uint64) {
	i := sort.Search(len(s.ranges), func(k int) bool { return turn < s.ranges[k].From.Turn }) - 1
	if i < 0 {
		i = 0
	}
	for s.mergeAt(i) {
	}
	for s.mergeAt(i - 1) {
	}
}

// Insert folds messages into the store and returns what was new: held parts
// are dropped, partial overlaps clipped to their novel part.
func (s *Store) Insert(msgs ...Message) []Message {
	// When every message goes in whole, the answer is the caller's own slice.
	var out []Message
	same := 0
	keep := func(i int, m Message, whole bool) {
		if out == nil {
			if whole && same == i {
				same++
				return
			}
			out = append(make([]Message, 0, len(msgs)), msgs[:same]...)
		}
		out = append(out, m)
	}
	for i, m := range msgs {
		// The append case must cost what appending to a slice costs.
		if n := len(s.ranges); n > 0 {
			last := &s.ranges[n-1]
			if f, t := msgSpan(m); last.To.Less(f) && s.adjacent(last.To, f) {
				boundary := last.To
				last.Msgs = append(last.Msgs, m)
				last.To = t
				s.consume(boundary, t)
				keep(i, m, true)
				continue
			}
		}
		for _, piece := range s.subtractHeld(m) {
			from, to := msgSpan(piece)
			s.insertRange(Range{From: from, To: to, Msgs: []Message{piece}})
			keep(i, piece, piece.From == m.From && len(piece.Nodes) == len(m.Nodes))
		}
	}
	if out == nil {
		return msgs[:same]
	}
	return out
}

func (s *Store) subtractHeld(m Message) []Message {
	t := uint64(m.Turn)
	cur, hi := m.From, spanEnd(m)
	var out []Message
	for _, r := range s.ranges[s.firstRangeAtOrAfter(Anchor{Turn: t, Node: cur}):] {
		if r.To.Turn < t {
			continue
		}
		if r.From.Turn > t {
			break
		}
		lo := uint64(0)
		if r.From.Turn == t {
			lo = r.From.Node
		}
		end := maxNode
		if r.To.Turn == t {
			end = r.To.Node
		}
		if end < cur {
			continue
		}
		if lo > hi {
			break
		}
		if cur < lo {
			out = append(out, sliceNodes(m, cur, lo-1))
		}
		if end >= hi {
			return out
		}
		cur = end + 1
	}
	if cur <= hi {
		out = append(out, sliceNodes(m, cur, hi))
	}
	return out
}

// firstRangeAtOrAfter is the index of the first range that can hold or follow a.
func (s *Store) firstRangeAtOrAfter(a Anchor) int {
	return sort.Search(len(s.ranges), func(k int) bool { return !s.ranges[k].To.Less(a) })
}

func firstMsgAtOrAfter(msgs []Message, a Anchor) int {
	return sort.Search(len(msgs), func(k int) bool {
		_, to := msgSpan(msgs[k])
		return !to.Less(a)
	})
}

// insertRange splices in a range known to overlap nothing; the sort and
// coalesce invariants are re-established here and nowhere else.
func (s *Store) insertRange(nr Range) {
	i := sort.Search(len(s.ranges), func(k int) bool { return nr.From.Less(s.ranges[k].From) })
	s.ranges = append(s.ranges, Range{})
	copy(s.ranges[i+1:], s.ranges[i:])
	s.ranges[i] = nr
	for s.mergeAt(i) {
	}
	for s.mergeAt(i - 1) {
	}
}

// Evict forgets [from, to]; a straddling range splits. Evicted and
// never-fetched are the same state.
func (s *Store) Evict(from, to Anchor) {
	if to.Less(from) {
		return
	}
	out := make([]Range, 0, len(s.ranges)+1)
	for _, r := range s.ranges {
		if r.To.Less(from) || to.Less(r.From) {
			out = append(out, r)
			continue
		}
		head := s.keep(r, r.From, from.Prev())
		tail := s.keep(r, to.Next(), r.To)
		if head != nil {
			out = append(out, *head)
		}
		if tail != nil {
			out = append(out, *tail)
		}
	}
	s.ranges = out
}

// CloneBelow is a new store holding the turns below turn; this one is left
// untouched because another owner may hold it. Only bookkeeping is copied;
// closed nodes are immutable and shared.
func (s *Store) CloneBelow(turn uint64) *Store {
	out := NewStore()
	// More.After is set only when something was actually dropped.
	out.more = More{Before: s.more.Before, After: s.more.After}
	if turn == 0 {
		return out
	}
	cut := Anchor{Turn: turn}
	for _, r := range s.ranges {
		if !r.From.Less(cut) {
			out.more.After = true
			continue
		}
		hi := r.To
		cutHere := false
		if !hi.Less(cut) {
			hi, cutHere = cut.Prev(), true
		}
		head := s.keep(r, r.From, hi)
		if head == nil {
			continue
		}
		out.ranges = append(out.ranges, Range{
			From: head.From, To: head.To,
			Msgs: append(make([]Message, 0, len(head.Msgs)), head.Msgs...),
		})
		if cutHere {
			out.more.After = true
			// A cut proves the turn below it is whole, which is what lets the
			// suffix that arrives next be declared the prefix's neighbour.
			out.ends[head.To.Turn] = head.To.Node + 1
		}
	}
	for t, n := range s.ends {
		if t < turn {
			out.ends[t] = n
		}
	}
	return out
}

// keep cuts the part of r inside [lo, hi], clipping the boundary messages;
// nil when nothing survives.
func (s *Store) keep(r Range, lo, hi Anchor) *Range {
	if hi.Less(lo) {
		return nil
	}
	var msgs []Message
	for _, m := range r.Msgs[firstMsgAtOrAfter(r.Msgs, lo):] {
		mf, mt := msgSpan(m)
		if hi.Less(mf) {
			break
		}
		if mt.Less(lo) {
			continue
		}
		a, b := m.From, spanEnd(m)
		if lo.Turn == mf.Turn && lo.Node > a {
			a = lo.Node
		}
		if hi.Turn == mf.Turn && hi.Node < b {
			b = hi.Node
		}
		if a > b {
			continue
		}
		msgs = append(msgs, sliceNodes(m, a, b))
	}
	if len(msgs) == 0 {
		return nil
	}
	from, _ := msgSpan(msgs[0])
	_, to := msgSpan(msgs[len(msgs)-1])
	return &Range{From: from, To: to, Msgs: msgs}
}

// TrimOldestTo forgets the oldest messages until at most limit remain. It only
// removes a prefix, so it cannot make a hole.
func (s *Store) TrimOldestTo(limit int) {
	if limit < 0 {
		limit = 0
	}
	drop := s.Count() - limit
	for drop > 0 && len(s.ranges) > 0 {
		r := s.ranges[0]
		if len(r.Msgs) <= drop {
			drop -= len(r.Msgs)
			s.ranges = append(s.ranges[:0], s.ranges[1:]...)
			continue
		}
		for i := 0; i < drop; i++ {
			r.Msgs[i] = Message{}
		}
		r.Msgs = r.Msgs[drop:]
		r.From, _ = msgSpan(r.Msgs[0])
		s.ranges[0] = r
		return
	}
}

// ForEach walks every retained message in order, stopping when fn returns false.
func (s *Store) ForEach(fn func(Message) bool) {
	for _, r := range s.ranges {
		for _, m := range r.Msgs {
			if !fn(m) {
				return
			}
		}
	}
}

// Query reports what the store holds over [from, to]. It never fetches.
func (s *Store) Query(from, to Anchor) []Segment {
	// There is no coordinate below (1, 0): turns are numbered from one, and a
	// client that believes in a hole there asks the wire for it forever.
	if from.Turn == 0 {
		from = Anchor{Turn: 1}
	}
	if to.Turn == 0 {
		return nil
	}
	if to.Less(from) {
		return nil
	}
	if len(s.ranges) == 0 {
		if s.more.Before || s.more.After {
			return []Segment{{Gap: &Gap{From: from, To: to}}}
		}
		return nil
	}
	// Beyond the outermost edges nothing is missing unless More says so.
	if lo := s.ranges[0].From; from.Less(lo) && !s.more.Before {
		from = lo
	}
	if hi := s.ranges[len(s.ranges)-1].To; hi.Less(to) && !s.more.After {
		to = hi
	}
	if to.Less(from) {
		return nil
	}

	var segs []Segment
	cur := from
	for _, r := range s.ranges[s.firstRangeAtOrAfter(from):] {
		if r.To.Less(from) {
			continue
		}
		if to.Less(r.From) {
			break
		}
		lo, hi := r.From, r.To
		if lo.Less(cur) {
			lo = cur
		}
		if to.Less(hi) {
			hi = to
		}
		if cur.Less(lo) {
			segs = append(segs, Segment{Gap: &Gap{From: cur, To: lo.Prev()}})
		}
		seg := Segment{Msgs: s.window(r, lo, hi)}
		segs = append(segs, seg)
		cur = hi.Next()
		if to.Less(cur) {
			break
		}
	}
	if !to.Less(cur) {
		g := &Gap{From: cur, To: to}
		if n := len(segs); n > 0 && segs[n-1].Gap == nil {
			segs[n-1].Gap = g
		} else {
			segs = append(segs, Segment{Gap: g})
		}
	}
	return fuseGaps(segs)
}

// window is the part of r inside [lo, hi], copying only when a boundary
// message has to be cut.
func (s *Store) window(r Range, lo, hi Anchor) []Message {
	if lo == r.From && hi == r.To {
		return r.Msgs
	}
	i := firstMsgAtOrAfter(r.Msgs, lo)
	if i >= len(r.Msgs) {
		return nil
	}
	j := sort.Search(len(r.Msgs), func(k int) bool {
		f, _ := msgSpan(r.Msgs[k])
		return hi.Less(f)
	}) - 1
	if j < i {
		return nil
	}
	if f, _ := msgSpan(r.Msgs[i]); !f.Less(lo) {
		if _, t := msgSpan(r.Msgs[j]); !hi.Less(t) {
			return r.Msgs[i : j+1]
		}
	}
	if kept := s.keep(r, lo, hi); kept != nil {
		return kept.Msgs
	}
	return nil
}

// fuseGaps rewrites [ {gap} {msgs} ] into [ {msgs-so-far, gap} ... ].
func fuseGaps(in []Segment) []Segment {
	out := make([]Segment, 0, len(in))
	for _, seg := range in {
		if seg.Gap != nil && len(seg.Msgs) == 0 && len(out) > 0 && out[len(out)-1].Gap == nil {
			out[len(out)-1].Gap = seg.Gap
			continue
		}
		out = append(out, seg)
	}
	return out
}

const fillLimit = 30

// bound clamps a queried interval the way Query does; the two walkers must
// agree on it or a hole appears below the first turn.
func (s *Store) bound(from, to Anchor) (Anchor, Anchor, bool) {
	if from.Turn == 0 {
		from = Anchor{Turn: 1}
	}
	if to.Turn == 0 || to.Less(from) {
		return from, to, false
	}
	if len(s.ranges) > 0 {
		if lo := s.ranges[0].From; from.Less(lo) && !s.more.Before {
			from = lo
		}
		if hi := s.ranges[len(s.ranges)-1].To; hi.Less(to) && !s.more.After {
			to = hi
		}
	}
	if to.Less(from) {
		return from, to, false
	}
	return from, to, true
}

func (s *Store) firstGap(from, to Anchor) (Gap, bool) {
	for _, seg := range s.Query(from, to) {
		if seg.Gap != nil {
			return *seg.Gap, true
		}
	}
	return Gap{}, false
}

// fillAt is the anchor a backward read closes a hole from: the first thing after it.
func fillAt(g Gap) Anchor { return g.To.Next() }

// ForEachIn walks the retained messages whose span touches [from, to], in
// order. It is gap-blind and yields whole messages, never slices.
func (s *Store) ForEachIn(from, to Anchor, fn func(Message) bool) {
	if to.Less(from) {
		return
	}
	for _, r := range s.ranges[s.firstRangeAtOrAfter(from):] {
		if to.Less(r.From) {
			return
		}
		for _, m := range r.Msgs[firstMsgAtOrAfter(r.Msgs, from):] {
			if f, _ := msgSpan(m); to.Less(f) {
				return
			}
			if !fn(m) {
				return
			}
		}
	}
}

// ForEachSegment walks [from, to] as runs and the holes between them, without
// allocating.
func (s *Store) ForEachSegment(from, to Anchor, msg func(Message) bool, gap func(Gap) bool) {
	if len(s.ranges) == 0 {
		return
	}
	from, to, ok := s.bound(from, to)
	if !ok {
		return
	}
	cur := from
	for _, r := range s.ranges[s.firstRangeAtOrAfter(from):] {
		if to.Less(r.From) {
			break
		}
		if cur.Less(r.From) && gap != nil && !gap(Gap{From: cur, To: r.From.Prev()}) {
			return
		}
		for _, m := range r.Msgs[firstMsgAtOrAfter(r.Msgs, cur):] {
			if f, _ := msgSpan(m); to.Less(f) {
				return
			}
			if !msg(m) {
				return
			}
		}
		if cur = r.To.Next(); to.Less(cur) {
			return
		}
	}
	if !to.Less(cur) && gap != nil {
		gap(Gap{From: cur, To: to})
	}
}

// Ranges returns a copy of the interval set; the Msgs inside are shared and
// must not be mutated.
func (s *Store) Ranges() []Range { return append([]Range(nil), s.ranges...) }

// Top is the highest anchor the store covers: the end of the last range, not
// the start of the last message.
func (s *Store) Top() (Anchor, bool) {
	if len(s.ranges) == 0 {
		return Anchor{}, false
	}
	return s.ranges[len(s.ranges)-1].To, true
}

// TailFrom is the anchor of the n-th message from the end, and whether the
// store holds that many.
func (s *Store) TailFrom(n int) (Anchor, bool) {
	if n <= 0 {
		return Anchor{}, false
	}
	for i := len(s.ranges) - 1; i >= 0; i-- {
		msgs := s.ranges[i].Msgs
		if n <= len(msgs) {
			from, _ := msgSpan(msgs[len(msgs)-n])
			return from, true
		}
		n -= len(msgs)
	}
	return Anchor{}, false
}

// Skip is the anchor of the n-th message at or after a, and whether the store
// holds one; zero means the first message at or after a itself.
func (s *Store) Skip(a Anchor, n int) (Anchor, bool) {
	if n < 0 {
		return Anchor{}, false
	}
	for _, r := range s.ranges[s.firstRangeAtOrAfter(a):] {
		i := firstMsgAtOrAfter(r.Msgs, a)
		if rest := len(r.Msgs) - i; n >= rest {
			n -= rest
			continue
		}
		from, _ := msgSpan(r.Msgs[i+n])
		return from, true
	}
	return Anchor{}, false
}

// Before is the anchor n messages before a, and how many messages [got, a)
// actually gained; fewer than n is not an error.
func (s *Store) Before(a Anchor, n int) (Anchor, int) {
	if n <= 0 {
		return a, 0
	}
	got, moved := a, 0
	for i := len(s.ranges) - 1; i >= 0 && moved < n; i-- {
		msgs := s.ranges[i].Msgs
		if !s.ranges[i].From.Less(a) {
			continue
		}
		for k := firstMsgAtOrAfter(msgs, a) - 1; k >= 0 && moved < n; k-- {
			from, _ := msgSpan(msgs[k])
			if !from.Less(a) {
				continue
			}
			got, moved = from, moved+1
		}
	}
	return got, moved
}

func (s *Store) Count() int {
	n := 0
	for _, r := range s.ranges {
		n += len(r.Msgs)
	}
	return n
}

func (s *Store) First() *Message {
	if len(s.ranges) == 0 {
		return nil
	}
	return &s.ranges[0].Msgs[0]
}

func (s *Store) All() []Message {
	out := make([]Message, 0, s.Count())
	for _, r := range s.ranges {
		out = append(out, r.Msgs...)
	}
	return out
}

func (s *Store) SetMore(m More) { s.more = m }

func (s *Store) More() More { return s.more }

func (s *Store) Pending() []Pending { return append([]Pending(nil), s.pending...) }

// Nodes below Live.From are released into the head range rather than held in
// both, which is what keeps the open tail disjoint from every range.

func (s *Store) OpenTurn() int        { return s.open.turn }
func (s *Store) OpenV() int           { return s.open.v }
func (s *Store) SetOpenV(v int)       { s.open.v = v }
func (s *Store) LiveFrom() uint64     { return s.open.from }
func (s *Store) SetLiveFrom(n uint64) { s.open.from = n }
func (s *Store) OpenLen() int         { return len(s.open.nodes) }

// ClaimOpen hands the open-turn slots to a turn, resetting them if another held them.
func (s *Store) ClaimOpen(turn int) {
	if s.open.turn != turn {
		s.ResetOpen()
		s.open.turn = turn
	}
}

func (s *Store) ResetOpen() { s.open = openTail{} }

// OpenNodes copies the whole open turn, including the head already released.
func (s *Store) OpenNodes() []livedoc.Node {
	return append([]livedoc.Node(nil), s.open.nodes...)
}

// Absorb slots a contiguous run of nodes in at their positional ids.
func (s *Store) Absorb(from uint64, nodes []livedoc.Node) {
	need := int(from) + len(nodes)
	for len(s.open.nodes) < need {
		s.open.nodes = append(s.open.nodes, livedoc.Node{})
	}
	copy(s.open.nodes[from:], nodes)
	s.noteHeld(from, uint64(need))
}

// FoldAt applies a delta at its positional id, growing the buffer as needed.
func (s *Store) FoldAt(nd NodeDelta) {
	for uint64(len(s.open.nodes)) <= nd.ID {
		s.open.nodes = append(s.open.nodes, livedoc.Node{})
	}
	s.open.nodes[nd.ID] = FoldDelta(s.open.nodes[nd.ID], nd)
	s.noteHeld(nd.ID, nd.ID+1)
}

// OpenHead is the lowest node ordinal of the open turn actually held; slots
// below it are positional padding. It only ever falls, by backward reads.
func (s *Store) OpenHead() int {
	if !s.open.held {
		return 0
	}
	return int(s.open.base)
}

// noteHeld records that [from, end) has been delivered. A run that does not
// reach the head is refused: a head below a hole would show padding as content.
func (s *Store) noteHeld(from, end uint64) {
	if !s.open.held {
		s.open.base, s.open.held = from, true
		return
	}
	if from < s.open.base && end >= s.open.base {
		s.open.base = from
	}
}

// OpenBase is the first node of the open turn still ours to show: Live.From,
// floored by the caller's emit cursor and by what is held.
func (s *Store) OpenBase(emitted int) int {
	n := int(s.open.from)
	if emitted > n {
		n = emitted
	}
	if h := s.OpenHead(); h > n {
		n = h
	}
	if n < 0 || n > len(s.open.nodes) {
		n = 0
	}
	return n
}

// OpenSuffix is the still-mutable tail: everything at or above OpenBase.
func (s *Store) OpenSuffix(emitted int) []livedoc.Node {
	return append([]livedoc.Node(nil), s.open.nodes[s.OpenBase(emitted):]...)
}

// OpenSlice is nodes [emitted, n) of the open turn; the caller inserts them.
func (s *Store) OpenSlice(emitted, n int) []livedoc.Node {
	return append([]livedoc.Node(nil), s.open.nodes[emitted:n]...)
}

func (s *Store) OpenAnimating() bool {
	if s.open.turn == 0 {
		return false
	}
	for _, n := range s.open.nodes {
		if n.Type == livedoc.NodeTool && n.Status == livedoc.StatusRunning {
			return true
		}
	}
	return false
}
