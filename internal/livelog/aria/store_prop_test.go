package aria

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// PROPERTY TESTS ON THE RANGE ALGEBRA.
//
// These are the contract's non-negotiable half (skills/figaro/contributing/range-store.md, "Testing
// standard"). They are randomized with a FIXED SEED so a failure reproduces,
// and every failure shrinks its input before reporting, because "some sequence
// of 40 inserts broke it" is not a bug report.
//
// EACH HAS BEEN MADE TO FAIL ON PURPOSE. An assertion that has never failed is
// not evidence, so the canaries are recorded here, with the failure each one
// produced against the code as it stands:
//
//   1. insertRange stops coalescing adjacent ranges (both the fast path and the
//      neighbour merge disabled):
//        TestPropertyNoOverlappingOrAdjacentRanges
//          "ranges 0 and 1 are ADJACENT ({3 0} then {3 1}) and did not coalesce"
//        TestPropertyMergeIsCommutativeAndAssociative
//          "minimal input: (2,1) (2,3) (2,4)"   <- shrunk from ~25 messages
//        TestPropertyInsertIsIdempotent
//
//   2. Query concatenates every segment into ONE flat []Message with no Gap -
//      the fabricated-adjacency bug itself:
//        TestPropertyQuerySegmentsNeverSpanAHole, at iteration 1
//          "segment 0 crosses turn 1->2 with no known extent"
//
//   3. Insert stops subtracting what it already holds:
//        TestPropertyInsertIsIdempotent
//          "re-insert changed the store: 20 msgs -> 60"
//
//   4. adjacent() guesses across a turn boundary when the extent is unknown:
//        TestPropertyNoOverlappingOrAdjacentRanges
//          "range 0: messages 0/1 cross turns 2->3 with no known extent"
//        TestGapNeedsATurnExtent
//          "without turn 1's extent these are two ranges, got 1"

// ---------------------------------------------------------------- helpers --

// node builds a distinguishable node.
func pnode(tag string) livedoc.Node {
	return livedoc.Node{Type: livedoc.NodeProse, Markdown: tag, Role: livedoc.RoleOutput}
}

// msg builds a message covering nodes [from, from+n) of turn t.
func pmsg(turn int, from uint64, n int) Message {
	m := Message{Turn: turn, From: from, Role: livedoc.RoleOutput}
	for i := 0; i < n; i++ {
		m.Nodes = append(m.Nodes, pnode(fmt.Sprintf("t%d.n%d", turn, from+uint64(i))))
	}
	if len(m.Nodes) == 0 {
		m.Role = livedoc.RoleInput
	}
	return m
}

// unit is one single-node message: the finest grain the algebra deals in, so
// that a random set of them is trivially disjoint and every insertion order is
// legal.
func unit(turn int, node uint64) Message {
	return Message{Turn: turn, From: node, Role: livedoc.RoleOutput,
		Nodes: []livedoc.Node{pnode(fmt.Sprintf("t%d.n%d", turn, node))}}
}

// anchorsOf lists every anchor a store covers, in order.
func anchorsOf(s *Store) []Anchor {
	var out []Anchor
	for _, r := range s.ranges {
		for _, m := range r.Msgs {
			f, t := msgSpan(m)
			for a := f; !t.Less(a); a = a.Next() {
				out = append(out, a)
			}
		}
	}
	return out
}

// checkInvariants asserts 1, 2 and 3 of skills/figaro/contributing/range-store.md over a whole store.
//
// truth is the GENERATOR's model of how many anchors each turn really has -
// deliberately not the store's own `ends`, which is bookkeeping the store
// prunes once a merge has consumed it. Checking a structure against its own
// bookkeeping proves only that it is self-consistent; checking it against the
// data that was actually inserted is what catches a fabricated merge.
func checkInvariants(t *testing.T, s *Store, truth map[uint64]uint64) {
	t.Helper()
	for i, r := range s.ranges {
		if len(r.Msgs) == 0 {
			t.Fatalf("range %d is empty", i)
		}
		// Invariant 3: Msgs is contiguous with no holes, and From/To describe
		// the coverage rather than the endpoints.
		want, _ := msgSpan(r.Msgs[0])
		if r.From != want {
			t.Fatalf("range %d: From %v but first message starts at %v", i, r.From, want)
		}
		cur := r.From
		for j, m := range r.Msgs {
			f, to := msgSpan(m)
			if j > 0 && f != cur {
				t.Fatalf("range %d: message %d starts at %v, expected %v: Msgs SPAN A HOLE", i, j, f, cur)
			}
			cur = to.Next()
			if j+1 < len(r.Msgs) {
				nf, _ := msgSpan(r.Msgs[j+1])
				if nf.Turn != to.Turn {
					// Crossing a turn boundary inside a range asserts that
					// nothing lies between: the left side must really end its
					// turn, and no turn may sit in the middle.
					if n, ok := truth[to.Turn]; !ok || to.Node+1 != n {
						t.Fatalf("range %d: messages %d/%d cross turns %d->%d but turn %d has %v anchors: FABRICATED ADJACENCY",
							i, j, j+1, to.Turn, nf.Turn, to.Turn, truth[to.Turn])
					}
					if nf.Turn != to.Turn+1 || nf.Node != 0 {
						t.Fatalf("range %d: messages %d/%d jump %v -> %v, skipping a turn",
							i, j, j+1, to, nf)
					}
					cur = nf
				}
			}
		}
		_, last := msgSpan(r.Msgs[len(r.Msgs)-1])
		if r.To != last {
			t.Fatalf("range %d: To %v but last message ends at %v", i, r.To, last)
		}
		if r.To.Less(r.From) {
			t.Fatalf("range %d: inverted %v..%v", i, r.From, r.To)
		}
		if i == 0 {
			continue
		}
		prev := s.ranges[i-1]
		// Invariant 1: sorted, strictly non-overlapping.
		if !prev.To.Less(r.From) {
			t.Fatalf("ranges %d and %d OVERLAP or are misordered: %v..%v then %v..%v",
				i-1, i, prev.From, prev.To, r.From, r.To)
		}
		// Invariant 2: never adjacent.
		if s.adjacent(prev.To, r.From) {
			t.Fatalf("ranges %d and %d are ADJACENT (%v then %v) and did not coalesce",
				i-1, i, prev.To, r.From)
		}
	}
}

// ------------------------------------------------------- generated inputs --

// scatter draws a random disjoint set of single-node messages, plus the turn
// extents that make the coordinate space decidable.
func scatter(rng *rand.Rand, n int) ([]Message, map[uint64]uint64) {
	const turns, width = 6, 5
	ends := map[uint64]uint64{}
	var all []Message
	for t := 1; t <= turns; t++ {
		ends[uint64(t)] = width
		for n := uint64(0); n < width; n++ {
			all = append(all, unit(t, n))
		}
	}
	rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	if n > len(all) {
		n = len(all)
	}
	return all[:n], ends
}

func loaded(msgs []Message, ends map[uint64]uint64) *Store {
	s := NewStore()
	for t, n := range ends {
		s.SetTurnLen(t, n)
	}
	s.Insert(msgs...)
	return s
}

// shrink finds a minimal sub-multiset of msgs that still fails pred, by
// repeatedly trying to drop one element.
func shrink(msgs []Message, fails func([]Message) bool) []Message {
	cur := append([]Message(nil), msgs...)
	for progress := true; progress; {
		progress = false
		for i := range cur {
			cand := append(append([]Message(nil), cur[:i]...), cur[i+1:]...)
			if len(cand) > 0 && fails(cand) {
				cur, progress = cand, true
				break
			}
		}
	}
	return cur
}

func names(msgs []Message) string {
	out := ""
	for _, m := range msgs {
		out += fmt.Sprintf("(%d,%d) ", m.Turn, m.From)
	}
	return out
}

// ------------------------------------------------------------ properties --

// TestPropertyNoOverlappingOrAdjacentRanges: NO OPERATION may leave the store
// with overlapping or adjacent ranges. Insert, evict, trim, and learning a
// turn's extent are all exercised against the same assertion.
func TestPropertyNoOverlappingOrAdjacentRanges(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5EED))
	for iter := 0; iter < 300; iter++ {
		msgs, ends := scatter(rng, 1+rng.Intn(30))
		s := NewStore()
		// Extents arrive interleaved with the data, as they do in Apply: a
		// turn's length is learned when it seals, which may be long after some
		// of its nodes landed.
		for i, m := range msgs {
			s.Insert(m)
			if i%3 == 0 {
				tn := uint64(1 + rng.Intn(len(ends)))
				s.SetTurnLen(tn, ends[tn])
			}
			checkInvariants(t, s, ends)
		}
		for tn, n := range ends {
			s.SetTurnLen(tn, n)
		}
		checkInvariants(t, s, ends)

		if rng.Intn(2) == 0 {
			lo := Anchor{Turn: uint64(1 + rng.Intn(6)), Node: uint64(rng.Intn(5))}
			hi := Anchor{Turn: uint64(1 + rng.Intn(6)), Node: uint64(rng.Intn(5))}
			if hi.Less(lo) {
				lo, hi = hi, lo
			}
			s.Evict(lo, hi)
			checkInvariants(t, s, ends)
		}
		s.TrimOldestTo(rng.Intn(10))
		checkInvariants(t, s, ends)
	}
}

// TestPropertyMergeIsCommutativeAndAssociative: over disjoint inputs the order
// and grouping of insertion cannot matter. If it does, the store is carrying
// history it should not have.
func TestPropertyMergeIsCommutativeAndAssociative(t *testing.T) {
	rng := rand.New(rand.NewSource(0xC0FFEE))
	sameShape := func(a, b *Store) bool {
		if len(a.ranges) != len(b.ranges) {
			return false
		}
		for i := range a.ranges {
			if a.ranges[i].From != b.ranges[i].From || a.ranges[i].To != b.ranges[i].To {
				return false
			}
			if !reflect.DeepEqual(a.ranges[i].Msgs, b.ranges[i].Msgs) {
				return false
			}
		}
		return true
	}
	for iter := 0; iter < 300; iter++ {
		msgs, ends := scatter(rng, 1+rng.Intn(25))
		ref := loaded(msgs, ends)

		fails := func(cand []Message) bool {
			r := loaded(cand, ends)
			// commutativity: any permutation
			p := append([]Message(nil), cand...)
			rand.New(rand.NewSource(1)).Shuffle(len(p), func(i, j int) { p[i], p[j] = p[j], p[i] })
			if !sameShape(r, loaded(p, ends)) {
				return true
			}
			// associativity: fold in two groups, then merge the groups
			for k := 1; k < len(cand); k++ {
				right := NewStore()
				for tn, n := range ends {
					right.SetTurnLen(tn, n)
				}
				right.Insert(cand[k:]...)
				both := loaded(cand[:k], ends)
				for _, rr := range right.ranges {
					both.Insert(rr.Msgs...)
				}
				if !sameShape(r, both) {
					return true
				}
			}
			return false
		}
		if fails(msgs) {
			min := shrink(msgs, fails)
			t.Fatalf("merge is not commutative/associative; minimal input: %s\n(full ref shape %d ranges)",
				names(min), len(ref.ranges))
		}
	}
}

// TestPropertyQuerySegmentsNeverSpanAHole is THE property. A Segment's Msgs
// must be contiguous; a hole must be reported as a Gap and never papered over.
func TestPropertyQuerySegmentsNeverSpanAHole(t *testing.T) {
	rng := rand.New(rand.NewSource(0xBEEF))
	for iter := 0; iter < 400; iter++ {
		msgs, ends := scatter(rng, 1+rng.Intn(25))
		s := loaded(msgs, ends)
		lo := Anchor{Turn: uint64(rng.Intn(8)), Node: uint64(rng.Intn(6))}
		hi := Anchor{Turn: uint64(rng.Intn(8)), Node: uint64(rng.Intn(6))}
		if hi.Less(lo) {
			lo, hi = hi, lo
		}
		segs := s.Query(lo, hi)

		held := map[Anchor]bool{}
		for _, a := range anchorsOf(s) {
			held[a] = true
		}
		for si, seg := range segs {
			var prev Anchor
			for mi, m := range seg.Msgs {
				f, to := msgSpan(m)
				if mi > 0 && f != prev {
					t.Fatalf("iter %d: segment %d spans a hole: message %d starts at %v, previous ended before %v\nquery %v..%v msgs %s",
						iter, si, mi, f, prev, lo, hi, names(msgs))
				}
				for a := f; !to.Less(a); a = a.Next() {
					if !held[a] {
						t.Fatalf("iter %d: segment %d claims %v, which the store does not hold", iter, si, a)
					}
				}
				prev = to.Next()
				if mi+1 < len(seg.Msgs) {
					if nf, _ := msgSpan(seg.Msgs[mi+1]); nf.Turn != to.Turn {
						// Checked against the GENERATOR's extents, not the
						// store's: see checkInvariants.
						if n, ok := ends[to.Turn]; !ok || to.Node+1 != n || nf.Turn != to.Turn+1 || nf.Node != 0 {
							t.Fatalf("iter %d: segment %d crosses %v -> %v but turn %d has %v anchors: FABRICATED ADJACENCY",
								iter, si, to, nf, to.Turn, ends[to.Turn])
						}
						prev = nf
					}
				}
			}
			// A gap must actually be a hole: nothing inside it is held.
			if seg.Gap != nil {
				for a := seg.Gap.From; !seg.Gap.To.Less(a) && a.Turn <= seg.Gap.To.Turn+1; a = a.Next() {
					if held[a] {
						t.Fatalf("iter %d: segment %d reports a gap over %v, which IS held", iter, si, a)
					}
					if a.Node > 64 {
						break // sentinel-bounded gap; the point is made
					}
				}
			}
		}
		// Every held anchor inside the query interval is reported exactly once.
		seen := map[Anchor]int{}
		for _, seg := range segs {
			for _, m := range seg.Msgs {
				f, to := msgSpan(m)
				for a := f; !to.Less(a); a = a.Next() {
					seen[a]++
				}
			}
		}
		for a := range held {
			if a.Less(lo) || hi.Less(a) {
				continue
			}
			if seen[a] != 1 {
				t.Fatalf("iter %d: held anchor %v reported %d times over %v..%v", iter, a, seen[a], lo, hi)
			}
		}
	}
}

// TestPropertyInsertIsIdempotent: re-applying anything already held changes
// nothing. A catch-up read that overlaps the live stream must not double-apply.
func TestPropertyInsertIsIdempotent(t *testing.T) {
	rng := rand.New(rand.NewSource(0x1DEA))
	for iter := 0; iter < 200; iter++ {
		msgs, ends := scatter(rng, 1+rng.Intn(25))
		s := loaded(msgs, ends)
		before := append([]Message(nil), s.All()...)
		s.Insert(msgs...)
		// and again, in a different order, and in fatter slices
		rng.Shuffle(len(msgs), func(i, j int) { msgs[i], msgs[j] = msgs[j], msgs[i] })
		s.Insert(msgs...)
		if got := s.All(); !reflect.DeepEqual(before, got) {
			t.Fatalf("iter %d: re-insert changed the store: %d msgs -> %d", iter, len(before), len(got))
		}
		checkInvariants(t, s, ends)

		// A FATTER message overlapping what we hold must contribute only its
		// novel part: never a second copy of a node we already have.
		held := map[Anchor]bool{}
		for _, a := range anchorsOf(s) {
			held[a] = true
		}
		s.Insert(pmsg(1, 0, 5), pmsg(2, 0, 5))
		seen := map[Anchor]int{}
		for _, a := range anchorsOf(s) {
			seen[a]++
			if seen[a] > 1 {
				t.Fatalf("iter %d: anchor %v held twice after an overlapping insert", iter, a)
			}
		}
		for a := range held {
			if seen[a] != 1 {
				t.Fatalf("iter %d: anchor %v lost to an overlapping insert", iter, a)
			}
		}
		checkInvariants(t, s, ends)
	}
}

// --------------------------------------------------------- worked examples --

func TestAnchorOrdering(t *testing.T) {
	a := Anchor{Turn: 3, Node: 4}
	if !(Anchor{Turn: 3, Node: 3}).Less(a) || a.Less(Anchor{Turn: 3, Node: 3}) {
		t.Fatal("Less is not lexicographic on (Turn, Node)")
	}
	if !(Anchor{Turn: 2, Node: 99}).Less(a) {
		t.Fatal("turn dominates node")
	}
	if a.Next() != (Anchor{Turn: 3, Node: 5}) || a.Prev() != (Anchor{Turn: 3, Node: 3}) {
		t.Fatal("Next/Prev within a turn")
	}
	if got := (Anchor{Turn: 3}).Prev(); got != (Anchor{Turn: 2, Node: maxNode}) {
		t.Fatalf("Prev of a turn's node 0 = %v; want the last node of the previous turn", got)
	}
	if got := (Anchor{}).Prev(); got != (Anchor{}) {
		t.Fatalf("the zero anchor is the floor, got %v", got)
	}
}

// TestGapNeedsATurnExtent pins the one place adjacency is not arithmetic: the
// store may not assume (t, last) and (t+1, 0) are neighbours until it knows
// how long turn t is. Guessing would fabricate adjacency.
func TestGapNeedsATurnExtent(t *testing.T) {
	s := NewStore()
	s.Insert(pmsg(1, 0, 3), pmsg(2, 0, 2))
	if len(s.ranges) != 2 {
		t.Fatalf("without turn 1's extent these are two ranges, got %d", len(s.ranges))
	}
	segs := s.Query(Anchor{Turn: 1}, Anchor{Turn: 2, Node: 1})
	if len(segs) != 2 || segs[0].Gap == nil {
		t.Fatalf("an unknown extent is a hole; got %#v", segs)
	}
	s.SetTurnLen(1, 3)
	if len(s.ranges) != 1 {
		t.Fatalf("learning the extent must coalesce; got %d ranges", len(s.ranges))
	}
	segs = s.Query(Anchor{Turn: 1}, Anchor{Turn: 2, Node: 1})
	if len(segs) != 1 || segs[0].Gap != nil || len(segs[0].Msgs) != 2 {
		t.Fatalf("one whole segment expected; got %#v", segs)
	}
}

// TestEvictionOpensAGap: eviction and never-fetched are the same state.
func TestEvictionOpensAGap(t *testing.T) {
	s := NewStore()
	s.SetTurnLen(1, 9)
	s.Insert(pmsg(1, 0, 9))
	if len(s.ranges) != 1 {
		t.Fatalf("one range, got %d", len(s.ranges))
	}
	s.Evict(Anchor{Turn: 1, Node: 3}, Anchor{Turn: 1, Node: 5})
	if len(s.ranges) != 2 {
		t.Fatalf("a mid-range eviction SPLITS; got %d ranges", len(s.ranges))
	}
	segs := s.Query(Anchor{Turn: 1}, Anchor{Turn: 1, Node: 8})
	if len(segs) != 2 || segs[0].Gap == nil {
		t.Fatalf("the hole must be reported; got %#v", segs)
	}
	if g := segs[0].Gap; g.From != (Anchor{Turn: 1, Node: 3}) || g.To != (Anchor{Turn: 1, Node: 5}) {
		t.Fatalf("gap bounds %v..%v", g.From, g.To)
	}
	var n int
	for _, seg := range segs {
		for _, m := range seg.Msgs {
			n += len(m.Nodes)
		}
	}
	if n != 6 {
		t.Fatalf("6 nodes survive eviction, got %d", n)
	}
}

// TestEnsureIsAStub records phase 1's honest limitation.
func TestEnsureIsAStub(t *testing.T) {
	s := NewStore()
	s.SetTurnLen(1, 3)
	s.Insert(pmsg(1, 0, 3))
	if err := s.Ensure(context.Background(), Anchor{Turn: 1}, Anchor{Turn: 1, Node: 2}); err != nil {
		t.Fatalf("a whole interval needs no fetch: %v", err)
	}
	s.Evict(Anchor{Turn: 1, Node: 1}, Anchor{Turn: 1, Node: 1})
	err := s.Ensure(context.Background(), Anchor{Turn: 1}, Anchor{Turn: 1, Node: 2})
	if !errors.Is(err, ErrNoFetcher) {
		t.Fatalf("phase 1 cannot fill a hole; got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Ensure(ctx, Anchor{Turn: 1}, Anchor{Turn: 1, Node: 2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure must be cancellable; got %v", err)
	}
}

// TestPhantomMessageOccupiesOneAnchor, an inquiry whose turn produced nothing
// is a real element and a range must be able to say it holds it.
func TestPhantomMessageOccupiesOneAnchor(t *testing.T) {
	s := NewStore()
	m := Message{Turn: 4, From: 0, Role: livedoc.RoleInput, Inquiry: "why?"}
	s.Insert(m)
	if got := s.ranges[0]; got.From != (Anchor{Turn: 4}) || got.To != (Anchor{Turn: 4}) {
		t.Fatalf("phantom coverage %v..%v", got.From, got.To)
	}
	s.SetTurnLen(4, 1)
	s.SetTurnLen(5, 2)
	s.Insert(pmsg(5, 0, 2))
	if len(s.ranges) != 1 {
		t.Fatalf("a phantom turn is one anchor and coalesces; got %d ranges", len(s.ranges))
	}
	if all := s.All(); all[0].Inquiry != "why?" {
		t.Fatalf("the question must survive: %#v", all[0])
	}
}

// TestIntraTurnMergeKeepsTheExtent guards the pruning in mergeAt. An extent is
// consumed only by a merge that CROSSES the turn it describes; a merge inside
// that turn still needs it afterwards, and dropping it there would open a
// false gap at the next turn boundary.
func TestIntraTurnMergeKeepsTheExtent(t *testing.T) {
	s := NewStore()
	s.Insert(pmsg(5, 0, 3)) // 0..2
	s.Insert(pmsg(5, 4, 3)) // 4..6, with a hole at node 3
	s.SetTurnLen(5, 7)
	if len(s.ranges) != 2 {
		t.Fatalf("the hole at node 3 keeps these apart; got %d ranges", len(s.ranges))
	}
	s.Insert(pmsg(5, 3, 1)) // fills the hole: an INTRA-TURN merge
	if len(s.ranges) != 1 {
		t.Fatalf("filling the hole merges; got %d ranges", len(s.ranges))
	}
	s.Insert(pmsg(6, 0, 2))
	if len(s.ranges) != 1 {
		t.Fatalf("turn 5's extent was still known, so turn 6 must coalesce; got %d ranges", len(s.ranges))
	}
}
