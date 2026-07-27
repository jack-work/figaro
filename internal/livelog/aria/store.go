package aria

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
)

// This file is the RANGE STORE — the client's model of one aria as a SET OF
// CONTIGUOUS INTERVALS over (turn, node) space, rather than a list plus a pile
// of booleans. See docs/range-store.md; that document is the contract and this
// is its implementation.
//
// The bug class it exists to prevent is FABRICATED ADJACENCY: handing a caller
// a flat []Message that spans a hole, so it believes two messages are
// neighbours when a hundred turns sit between them. Message's own doc records
// three separate bugs from mistaking Turn for an identity; this is the same
// disease at range scale. Query returns Segments precisely so that a caller
// CANNOT be handed that lie.

// maxNode is the largest representable node ordinal. It appears only as the
// predecessor sentinel for node 0 (see Anchor.Prev).
const maxNode = ^uint64(0)

// Less orders anchors lexicographically on (Turn, Node). This is THE ordering;
// nothing else in the tree may open-code the comparison.
func (a Anchor) Less(b Anchor) bool {
	if a.Turn != b.Turn {
		return a.Turn < b.Turn
	}
	return a.Node < b.Node
}

// Next is the immediately following anchor. It does NOT cross a turn boundary,
// because an anchor does not encode its turn's length: whether (t, n) is the
// last node of turn t is knowledge the STORE holds (see Store.SetTurnLen and
// Store.adjacent), not something the coordinate can answer alone.
func (a Anchor) Next() Anchor { return Anchor{Turn: a.Turn, Node: a.Node + 1} }

// Prev is the immediately preceding anchor — the mirror of Next. In (turn,
// node) space ordered lexicographically the predecessor of (t, 0) is the last
// node of turn t-1, which with uint64 ordinals is (t-1, maxNode). The zero
// anchor is its own predecessor: it is the floor.
func (a Anchor) Prev() Anchor {
	if a.Node > 0 {
		return Anchor{Turn: a.Turn, Node: a.Node - 1}
	}
	if a.Turn == 0 {
		return a
	}
	return Anchor{Turn: a.Turn - 1, Node: maxNode}
}

// Range is a contiguous, fully-materialized run. From/To are inclusive and
// describe the SLICE COVERAGE, not just the first and last message: a range
// asserts that it holds every node between them.
type Range struct {
	From, To Anchor
	Msgs     []Message // contiguous; identity is (Turn, From) per Message's doc
}

// Gap is a hole: the store holds nothing in [From, To], and something is
// believed to be there (invariant 2 — adjacent ranges coalesce, so a gap
// always contains at least one real node).
type Gap struct {
	From, To Anchor
}

// Turns is the number of turns the hole touches — the count a one-row gap
// sentinel prints ("13 turns not loaded").
func (g Gap) Turns() int { return int(g.To.Turn-g.From.Turn) + 1 }

// Segment is a contiguous run, optionally followed by a hole. Msgs may be
// empty (a query whose interval opens on a hole).
type Segment struct {
	Msgs []Message
	Gap  *Gap
}

// Pending is a prompt that has been submitted but not yet classified by the
// drain. It has NO server coordinate, because only the drain can assign one —
// it alone knows whether a turn was in flight when the prompt came off the
// queue.
//
// PHASE 1: type only. Nothing constructs or renders one yet; the lifecycle
// (submitted -> committed -> acked) lands in phase 4.
type Pending struct {
	Text string
	At   time.Time
}

// openTail is the ONE streaming suffix. It is today's client machinery moved
// intact under the store, so that invariant 4 — the open turn is disjoint from
// every range — has an owner that can be checked. (docs/range-store.md calls
// this field `open *openTurn`; the name openTurn is already taken in this
// package by the SERVER's open-turn record, so the type is named openTail and
// held by value, with turn == 0 meaning "nothing open", which is the sentinel
// every existing caller already tests.)
type openTail struct {
	turn  int
	from  uint64
	v     int
	nodes []livedoc.Node
}

// Store is the client's whole model of one aria.
type Store struct {
	ranges  []Range           // sorted by From; non-overlapping; NEVER adjacent
	more    More              // is there anything beyond the outermost edges
	open    openTail          // the ONE streaming suffix — unchanged from today
	pending []Pending         // submitted, not yet classified by the drain
	ends    map[uint64]uint64 // turn -> anchors it occupies, once known
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{ends: map[uint64]uint64{}} }

// ErrNoFetcher is returned by Ensure when a hole would have to be filled.
// Phase 1 ships no fetcher: the store is a substrate swap behind the existing
// API, and nothing yet reads a page on its behalf.
var ErrNoFetcher = errors.New("aria: range store cannot fill holes yet (phase 1)")

// ---------------------------------------------------------------- coverage --

// spanEnd is the last node ordinal a message covers. A message with no nodes —
// an inquiry whose turn produced nothing — still occupies exactly one anchor,
// its own (Turn, From): it is a real element of the conversation and a range
// that "holds every node between From and To" must be able to say it holds it.
func spanEnd(m Message) uint64 {
	if len(m.Nodes) == 0 {
		return m.From
	}
	return m.From + uint64(len(m.Nodes)) - 1
}

// msgSpan is a message's inclusive anchor coverage.
func msgSpan(m Message) (Anchor, Anchor) {
	t := uint64(m.Turn)
	return Anchor{Turn: t, Node: m.From}, Anchor{Turn: t, Node: spanEnd(m)}
}

// sliceNodes cuts a message down to the node interval [lo, hi] of its own
// turn. Inquiry survives only at offset 0, per Message's doc: a later slice
// carrying it would print the question a second time.
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
	}
	return out
}

// ------------------------------------------------------------- adjacency ----

// SetTurnLen records how many anchors a turn occupies, which is the ONLY way
// to know that (t, n) and (t+1, 0) are neighbours: an anchor cannot answer it
// (see Anchor.Next). A turn that produced no nodes but carried an inquiry
// occupies one anchor — its phantom node 0 — because that is what the client
// materializes for it.
//
// Learning a length can make two existing ranges adjacent, so this coalesces.
func (s *Store) SetTurnLen(turn uint64, n uint64) {
	if s.ends == nil {
		s.ends = map[uint64]uint64{}
	}
	if cur, ok := s.ends[turn]; ok && cur >= n {
		return
	}
	s.ends[turn] = n
	s.coalesce()
}

// TurnLen reports a recorded turn length.
func (s *Store) TurnLen(turn uint64) (uint64, bool) {
	n, ok := s.ends[turn]
	return n, ok
}

// adjacent reports whether NOTHING can exist strictly between a and b — the
// predicate invariant 2 is stated in terms of. Within a turn it is pure
// arithmetic. Across turns it needs the extent of every turn in between, and
// answers false when that is unknown: a false gap says "there may be something
// here", which is honest; a false adjacency would fabricate neighbours, which
// is the bug this whole design exists to prevent.
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
	// Every turn occupies at least one anchor unless we know it occupies none,
	// so an intervening turn is a hole until proven empty. The loop bails on
	// the first unknown, which is why it cannot run long.
	for t := a.Turn + 1; t < b.Turn; t++ {
		if n, ok := s.ends[t]; !ok || n != 0 {
			return false
		}
	}
	return true
}

// coalesce merges every pair of neighbouring ranges that adjacency now permits.
func (s *Store) coalesce() {
	for i := 0; i+1 < len(s.ranges); {
		if s.adjacent(s.ranges[i].To, s.ranges[i+1].From) {
			s.ranges[i].Msgs = append(s.ranges[i].Msgs, s.ranges[i+1].Msgs...)
			s.ranges[i].To = s.ranges[i+1].To
			s.ranges = append(s.ranges[:i+1], s.ranges[i+2:]...)
			continue
		}
		i++
	}
}

// ---------------------------------------------------------------- insert ----

// Insert folds messages into the store. Anything already held is dropped
// rather than duplicated: a catch-up read that overlaps the live stream must
// not double-apply, and the coverage invariant leaves no room for two copies
// of one node anyway. A message that only PARTLY overlaps is clipped to its
// novel part, so no information is lost either way.
func (s *Store) Insert(msgs ...Message) {
	for _, m := range msgs {
		for _, piece := range s.subtractHeld(m) {
			from, to := msgSpan(piece)
			s.insertRange(Range{From: from, To: to, Msgs: []Message{piece}})
		}
	}
}

// subtractHeld returns the parts of m the store does not already cover.
func (s *Store) subtractHeld(m Message) []Message {
	t := uint64(m.Turn)
	cur, hi := m.From, spanEnd(m)
	var out []Message
	for _, r := range s.ranges {
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

// insertRange splices in a range that is known to overlap nothing, merging it
// with either neighbour it turns out to be adjacent to. Invariants 1 and 2 are
// re-established here and nowhere else.
func (s *Store) insertRange(nr Range) {
	// The streaming case — one message extending the newest range — is the hot
	// path of every long aria, and doing it by rebuilding the message slice
	// would make folding a conversation quadratic in its length.
	if n := len(s.ranges); n > 0 {
		last := &s.ranges[n-1]
		if s.adjacent(last.To, nr.From) {
			last.Msgs = append(last.Msgs, nr.Msgs...)
			last.To = nr.To
			return
		}
	}
	i := sort.Search(len(s.ranges), func(k int) bool { return nr.From.Less(s.ranges[k].From) })
	if i > 0 && s.adjacent(s.ranges[i-1].To, nr.From) {
		prev := s.ranges[i-1]
		nr = Range{From: prev.From, To: nr.To, Msgs: append(append([]Message(nil), prev.Msgs...), nr.Msgs...)}
		i--
		s.ranges = append(s.ranges[:i], s.ranges[i+1:]...)
	}
	if i < len(s.ranges) && s.adjacent(nr.To, s.ranges[i].From) {
		next := s.ranges[i]
		nr = Range{From: nr.From, To: next.To, Msgs: append(append([]Message(nil), nr.Msgs...), next.Msgs...)}
		s.ranges = append(s.ranges[:i], s.ranges[i+1:]...)
	}
	s.ranges = append(s.ranges, Range{})
	copy(s.ranges[i+1:], s.ranges[i:])
	s.ranges[i] = nr
}

// ----------------------------------------------------------------- evict ----

// Evict forgets [from, to]. A range straddling the interval SPLITS, and a gap
// appears — eviction and never-fetched are the same state, which is the point:
// retention stops being a special case and becomes "keep the ranges nearest
// the viewport".
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

// keep cuts the part of r inside [lo, hi], clipping the boundary messages.
// It returns nil when nothing survives.
func (s *Store) keep(r Range, lo, hi Anchor) *Range {
	if hi.Less(lo) {
		return nil
	}
	var msgs []Message
	for _, m := range r.Msgs {
		mf, mt := msgSpan(m)
		if mt.Less(lo) || hi.Less(mf) {
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
		// A phantom (node-less) message is all-or-nothing: it occupies one
		// anchor and there is nothing to cut.
		msgs = append(msgs, sliceNodes(m, a, b))
	}
	if len(msgs) == 0 {
		return nil
	}
	from, _ := msgSpan(msgs[0])
	_, to := msgSpan(msgs[len(msgs)-1])
	return &Range{From: from, To: to, Msgs: msgs}
}

// TrimOldestTo forgets the oldest messages until at most limit remain. This is
// today's bottom-only retention expressed as eviction; it cannot make a hole,
// because it only ever removes a prefix.
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
		r.Msgs = append([]Message(nil), r.Msgs[drop:]...)
		r.From, _ = msgSpan(r.Msgs[0])
		s.ranges[0] = r
		return
	}
}

// ------------------------------------------------------------------ read ----

// Query reports what the store HOLDS over [from, to]. It never fetches and
// never blocks. A caller that does not care about gaps writes
//
//	for _, seg := range store.Query(a, b) { use(seg.Msgs) }
//
// ignores .Gap, and is NEVER LIED TO — it simply gets less.
func (s *Store) Query(from, to Anchor) []Segment {
	if to.Less(from) {
		return nil
	}
	if len(s.ranges) == 0 {
		if s.more.Before || s.more.After {
			return []Segment{{Gap: &Gap{From: from, To: to}}}
		}
		return nil
	}
	// Beyond the outermost edges there is nothing to be missing unless More
	// says so, so asking for [0, +inf] over a whole aria reports no gaps.
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
	for _, r := range s.ranges {
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
		kept := s.keep(r, lo, hi)
		seg := Segment{}
		if kept != nil {
			seg.Msgs = kept.Msgs
		}
		segs = append(segs, seg)
		cur = hi.Next()
		if to.Less(cur) {
			break
		}
	}
	if !to.Less(cur) {
		// A hole runs off the end of the queried interval.
		g := &Gap{From: cur, To: to}
		if n := len(segs); n > 0 && segs[n-1].Gap == nil {
			segs[n-1].Gap = g
		} else {
			segs = append(segs, Segment{Gap: g})
		}
	}
	// Fuse the gap that precedes a segment onto its predecessor, so a Segment
	// means "run, then hole" rather than "hole, then run".
	return fuseGaps(segs)
}

// fuseGaps rewrites [ {gap} {msgs} ] into [ {msgs-so-far, gap} ... ], the shape
// Segment documents: Msgs then an optional trailing Gap.
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

// Ensure fills every hole in [from, to], fetching as needed, so that Query
// over the same interval then returns exactly one Segment with a nil Gap.
//
// PHASE 1 IS A STUB. There is no fetcher yet: the store landed behind the
// existing API with no renderer changes, so nothing calls Ensure and nothing
// can serve it. It reports success when the interval is already whole and
// ErrNoFetcher when it is not, so a caller written against the final contract
// fails loudly rather than silently rendering a hole.
func (s *Store) Ensure(ctx context.Context, from, to Anchor) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, seg := range s.Query(from, to) {
		if seg.Gap != nil {
			return ErrNoFetcher
		}
	}
	return nil
}

// Ranges exposes the interval set. The returned slice is a copy; the Msgs
// inside are not, and must not be mutated.
func (s *Store) Ranges() []Range { return append([]Range(nil), s.ranges...) }

// Count is how many messages the store retains.
func (s *Store) Count() int {
	n := 0
	for _, r := range s.ranges {
		n += len(r.Msgs)
	}
	return n
}

// First is the oldest retained message, or nil.
func (s *Store) First() *Message {
	if len(s.ranges) == 0 {
		return nil
	}
	return &s.ranges[0].Msgs[0]
}

// All flattens every retained message into one slice, in (Turn, From) order.
//
// THIS IS THE ONE PLACE THAT SPANS HOLES, and it exists only for the phase-1
// shim: Client.View() has always returned exactly this flat list, holes and
// all, and the migration's whole claim is that the substrate swap is invisible
// from outside. Consumers move to Query one at a time afterwards. Do not add
// callers.
func (s *Store) All() []Message {
	out := make([]Message, 0, s.Count())
	for _, r := range s.ranges {
		out = append(out, r.Msgs...)
	}
	return out
}

// SetMore records what lies beyond the outermost edges.
func (s *Store) SetMore(m More) { s.more = m }

// More reports what lies beyond the outermost edges.
func (s *Store) More() More { return s.more }

// Pending returns the prompts awaiting classification (phase 4; always empty).
func (s *Store) Pending() []Pending { return append([]Pending(nil), s.pending...) }

// ------------------------------------------------------------ open turn -----

// The open turn keeps its own machinery, unchanged: these are the client's
// former openTurn/openFrom/openV/openNodesSlice fields with their behaviour
// intact. They live here so that invariant 4 — open is disjoint from every
// range, because nodes below Live.From are RELEASED INTO the head range rather
// than held in both — has a single owner.

func (s *Store) OpenTurn() int        { return s.open.turn }
func (s *Store) OpenV() int           { return s.open.v }
func (s *Store) SetOpenV(v int)       { s.open.v = v }
func (s *Store) LiveFrom() uint64     { return s.open.from }
func (s *Store) SetLiveFrom(n uint64) { s.open.from = n }
func (s *Store) OpenLen() int         { return len(s.open.nodes) }

// ClaimOpen hands the open-turn slots to a turn, resetting them if another
// turn held them. The slots are ONE buffer and exactly one turn may hold them.
func (s *Store) ClaimOpen(turn int) {
	if s.open.turn != turn {
		s.ResetOpen()
		s.open.turn = turn
	}
}

// ResetOpen releases the open-turn slots.
func (s *Store) ResetOpen() { s.open = openTail{} }

// OpenNodes copies the whole materialized open turn — including the head
// already released to the ranges. It is what a seal promotes from.
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
}

// FoldAt applies a delta at its positional id, growing the buffer as the open
// suffix appends.
func (s *Store) FoldAt(nd NodeDelta) {
	for uint64(len(s.open.nodes)) <= nd.ID {
		s.open.nodes = append(s.open.nodes, livedoc.Node{})
	}
	s.open.nodes[nd.ID] = foldDelta(s.open.nodes[nd.ID], nd)
}

// OpenBase is the first node of the open turn still ours to show: Live.From,
// floored by the caller's emit cursor. They differ only when a clipped
// catch-up read raised the cursor above the boundary — the turn's head was
// never delivered, so the region starts where our knowledge does, not at zero.
func (s *Store) OpenBase(emitted int) int {
	n := int(s.open.from)
	if emitted > n {
		n = emitted
	}
	if n < 0 || n > len(s.open.nodes) {
		n = 0
	}
	return n
}

// OpenSuffix is the still-mutable tail: everything at or above OpenBase. The
// committed head has already been released into the ranges.
func (s *Store) OpenSuffix(emitted int) []livedoc.Node {
	return append([]livedoc.Node(nil), s.open.nodes[s.OpenBase(emitted):]...)
}

// OpenSlice is the head of the open turn released as a closed run: nodes
// [emitted, n). It is the caller's job to insert the resulting message.
func (s *Store) OpenSlice(emitted, n int) []livedoc.Node {
	return append([]livedoc.Node(nil), s.open.nodes[emitted:n]...)
}

// OpenAnimating reports whether the open turn has a running tool.
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
