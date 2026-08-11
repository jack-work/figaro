package aria

import (
	"context"
	"errors"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// The phase-2 surface: what a WINDOWED reader (the pager) needs of the store
// to stop keeping a second copy of the conversation. Every test here is a
// canary for a specific claim in docs/range-store.md's migration step 2.

func histMsg(turn int, from uint64, n int) Message {
	m := Message{Turn: turn, From: from, Role: livedoc.RoleOutput}
	for i := 0; i < n; i++ {
		m.Nodes = append(m.Nodes, livedoc.Node{Type: livedoc.NodeProse, Markdown: "h"})
	}
	return m
}

// TestTailFromWalksBackward pins the primitive that replaced the pager's
// tail-window cache: the anchor N messages from the end, without walking the
// aria. Revert TailFrom to a forward scan and this still passes: what it
// really guards is the ANSWER, which resetToTail now recomputes every frame.
func TestTailFromWalksBackward(t *testing.T) {
	s := NewStore()
	for turn := 1; turn <= 10; turn++ {
		s.SetTurnLen(uint64(turn), 2)
		s.Insert(histMsg(turn, 0, 2))
	}
	if got := len(s.Ranges()); got != 1 {
		t.Fatalf("contiguous history is one range; got %d", got)
	}
	for n, want := range map[int]Anchor{1: {Turn: 10}, 3: {Turn: 8}, 10: {Turn: 1}} {
		if got, ok := s.TailFrom(n); !ok || got != want {
			t.Fatalf("TailFrom(%d) = %v,%v; want %v", n, got, ok, want)
		}
	}
	if _, ok := s.TailFrom(11); ok {
		t.Fatal("TailFrom past the beginning must report false, not clamp")
	}
	if _, ok := s.TailFrom(0); ok {
		t.Fatal("TailFrom(0) is not an anchor")
	}
}

// TestTailFromCrossesRanges: the pager's window may straddle a hole (history
// fetched around a jump), and the tail count must not restart at each range.
func TestTailFromCrossesRanges(t *testing.T) {
	s := NewStore()
	s.SetTurnLen(1, 2)
	s.SetTurnLen(9, 2)
	s.Insert(histMsg(1, 0, 2), histMsg(2, 0, 2))
	s.Insert(histMsg(9, 0, 2), histMsg(10, 0, 2))
	if got := len(s.Ranges()); got != 2 {
		t.Fatalf("a hole between turn 2 and turn 9 is TWO ranges; got %d", got)
	}
	if got, ok := s.TailFrom(3); !ok || got != (Anchor{Turn: 2}) {
		t.Fatalf("TailFrom(3) = %v,%v; want turn 2: the count spans the hole", got, ok)
	}
}

// TestSkipIsTheForwardMirror pins the primitive the bounded window extends by.
func TestSkipIsTheForwardMirror(t *testing.T) {
	s := NewStore()
	for turn := 1; turn <= 6; turn++ {
		s.SetTurnLen(uint64(turn), 2)
		s.Insert(histMsg(turn, 0, 2))
	}
	if got, ok := s.Skip(Anchor{Turn: 2}, 0); !ok || got != (Anchor{Turn: 2}) {
		t.Fatalf("Skip(_,0) is the message AT the anchor; got %v,%v", got, ok)
	}
	if got, ok := s.Skip(Anchor{Turn: 2}, 3); !ok || got != (Anchor{Turn: 5}) {
		t.Fatalf("Skip(turn2,3) = %v,%v; want turn 5", got, ok)
	}
	if _, ok := s.Skip(Anchor{Turn: 2}, 99); ok {
		t.Fatal("Skip past the end must report false")
	}
}

// TestMergeIsSilentAndDeduped is why transcript.seed could be deleted: a
// fetched page goes into the ONE owner without coming back out through
// OnClosed (which, inline, freezes to the user's scrollback), and folding the
// same history twice does not double it.
func TestMergeIsSilentAndDeduped(t *testing.T) {
	c := NewClient()
	fired := 0
	c.OnClosed = func(Message) { fired++ }
	c.OnLive = func(Message) { fired++ }

	page := []Message{histMsg(1, 0, 2), histMsg(2, 0, 2)}
	c.Merge(page, map[int]uint64{1: 2, 2: 2})
	c.Merge(page, map[int]uint64{1: 2, 2: 2})

	if fired != 0 {
		t.Fatalf("Merge must fire no callbacks; got %d", fired)
	}
	if got := c.Store().Count(); got != 2 {
		t.Fatalf("merging the same page twice retains %d messages; want 2", got)
	}
	if got := len(c.Store().Ranges()); got != 1 {
		t.Fatalf("the extents make turns 1 and 2 neighbours: want one range, got %d", got)
	}
}

// TestMergeWithoutExtentsRefusesToGuess is the corrected spec's clause held at
// the seam the pager actually uses: a page clipped at its tail states no
// extent, so the store keeps the boundary honest rather than fabricating
// adjacency.
func TestMergeWithoutExtentsRefusesToGuess(t *testing.T) {
	c := NewClient()
	c.Merge([]Message{histMsg(1, 0, 2), histMsg(2, 0, 2)}, nil)
	if got := len(c.Store().Ranges()); got != 2 {
		t.Fatalf("no extent for turn 1 => no adjacency across it; got %d ranges", got)
	}
}

// TestMergeBumpsTheRevision: the pager's tail window is derived from the store
// per frame, but every OTHER consumer of ClosedRevision has to see a merge.
func TestMergeBumpsTheRevision(t *testing.T) {
	c := NewClient()
	rev := c.ClosedRevision()
	c.Merge([]Message{histMsg(3, 0, 1)}, nil)
	if c.ClosedRevision() == rev {
		t.Fatal("a merged page changes the retained set; the revision must move")
	}
}

// TestEvictBeforeMakesAHole is retention as the contract states it: eviction
// and never-fetched are the same state.
func TestEvictBeforeMakesAHole(t *testing.T) {
	c := NewClient()
	var msgs []Message
	ext := map[int]uint64{}
	for turn := 1; turn <= 6; turn++ {
		msgs = append(msgs, histMsg(turn, 0, 2))
		ext[turn] = 2
	}
	c.Merge(msgs, ext)
	c.EvictBefore(Anchor{Turn: 4})
	s := c.Store()
	if got := s.Count(); got != 3 {
		t.Fatalf("evicting below turn 4 leaves 3 messages; got %d", got)
	}
	if first := s.First(); first == nil || first.Turn != 4 {
		t.Fatalf("the floor is turn 4; got %v", first)
	}
	// And the same anchor evicted twice is a no-op, not a panic.
	c.EvictBefore(Anchor{Turn: 4})
	if got := s.Count(); got != 3 {
		t.Fatalf("re-evicting changed the store: %d", got)
	}
}

// TestMoreBeforeIsTheWiresAnswer: "is there older history" is a fact only a
// backward read reports, and it now lives in the store instead of as a latched
// bit on the pager (the old noMoreOlder).
func TestMoreBeforeIsTheWiresAnswer(t *testing.T) {
	c := NewClient()
	if c.MoreBefore() {
		t.Fatal("a fresh store claims nothing beyond its edges")
	}
	c.SetMoreBefore(true)
	if !c.MoreBefore() {
		t.Fatal("the read said there is older history; the store must hold that")
	}
	c.SetMoreBefore(false)
	if c.MoreBefore() {
		t.Fatal("an empty backward read proves the floor")
	}
}

// TestBeforeIsTheReturnTrip pins the primitive that replaced the pager's
// payload LRU: the anchor n messages BELOW a, plus how far it actually got.
// A windowed reader lowers its floor with it, so the scroll back up over
// history the store still holds costs a backward walk and no round trip.
func TestBeforeIsTheReturnTrip(t *testing.T) {
	s := NewStore()
	for turn := 1; turn <= 10; turn++ {
		s.SetTurnLen(uint64(turn), 2)
		s.Insert(histMsg(turn, 0, 2))
	}
	if got, n := s.Before(Anchor{Turn: 8}, 3); n != 3 || got != (Anchor{Turn: 5}) {
		t.Fatalf("Before(8, 3) = %v, %d; want turn 5 over 3 messages", got, n)
	}
	// Fewer than asked for is not an error: take what there is, and say so.
	if got, n := s.Before(Anchor{Turn: 3}, 10); n != 2 || got != (Anchor{Turn: 1}) {
		t.Fatalf("Before(3, 10) = %v, %d; want the oldest held over 2", got, n)
	}
	if got, n := s.Before(Anchor{Turn: 1}, 4); n != 0 || got != (Anchor{Turn: 1}) {
		t.Fatalf("Before at the floor = %v, %d; want the anchor itself over 0", got, n)
	}
	// It counts MESSAGES across a hole, exactly as TailFrom does: the window's
	// floor may straddle one.
	s.Evict(Anchor{Turn: 4}, Anchor{Turn: 6, Node: 1})
	if got, n := s.Before(Anchor{Turn: 8}, 3); n != 3 || got != (Anchor{Turn: 2}) {
		t.Fatalf("Before across a hole = %v, %d; want turn 2 over 3", got, n)
	}
}

// TestEnsureFillsAHoleThroughItsFetcher: phase 1 left Ensure a stub returning
// ErrNoFetcher. It is real now: it asks the store what is missing, reads
// BEFORE the hole's far end (so the fill arrives nearest what we already
// hold), folds the extents so the run coalesces, and stops when Query says the
// interval is whole.
func TestEnsureFillsAHoleThroughItsFetcher(t *testing.T) {
	whole := NewStore()
	for turn := 1; turn <= 20; turn++ {
		whole.SetTurnLen(uint64(turn), 2)
		whole.Insert(histMsg(turn, 0, 2))
	}
	s := NewStore()
	for _, turn := range []int{1, 2, 3, 18, 19, 20} {
		s.SetTurnLen(uint64(turn), 2)
		s.Insert(histMsg(turn, 0, 2))
	}
	if got := len(s.Ranges()); got != 2 {
		t.Fatalf("fixture: %d ranges, want a hole between two", got)
	}
	if err := s.Ensure(context.Background(), Anchor{Turn: 1}, Anchor{Turn: 20, Node: 1}); !errors.Is(err, ErrNoFetcher) {
		t.Fatalf("with no fetcher installed, Ensure = %v; want ErrNoFetcher", err)
	}
	reads := []Anchor{}
	s.SetFetcher(func(_ context.Context, before Anchor, limit int) (Fetched, error) {
		reads = append(reads, before)
		got := Fetched{Extents: map[int]uint64{}, More: true}
		// The wire's answer: the `limit` messages immediately before `before`.
		for turn := int(before.Turn) - 1; turn >= 1 && len(got.Msgs) < limit; turn-- {
			got.Msgs = append([]Message{histMsg(turn, 0, 2)}, got.Msgs...)
			got.Extents[turn] = 2
		}
		return got, nil
	})
	if err := s.Ensure(context.Background(), Anchor{Turn: 1}, Anchor{Turn: 20, Node: 1}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(reads) == 0 {
		t.Fatal("Ensure closed the hole without reading")
	}
	// It reads at the anchor AFTER the hole: the first thing we DO hold above
	// it: so the fill lands against the reader's own position first.
	if reads[0] != (Anchor{Turn: 18}) {
		t.Fatalf("first fill read at %v; want the anchor just past the hole", reads[0])
	}
	if got := len(s.Ranges()); got != 1 {
		t.Fatalf("a closed hole must coalesce: %d ranges", got)
	}
	if got, want := s.Count(), whole.Count(); got != want {
		t.Fatalf("filled store holds %d messages, the whole aria has %d", got, want)
	}
	for _, seg := range s.Query(Anchor{Turn: 1}, Anchor{Turn: 20, Node: 1}) {
		if seg.Gap != nil {
			t.Fatalf("Query still reports a hole after Ensure: %+v", seg.Gap)
		}
	}
}

// TestEnsureRefusesToSpin: a fetcher that answers but never closes the hole
// must be reported, not looped on. A server that disagrees with us about what
// exists is a real possibility; a pager that hangs on it is not acceptable.
func TestEnsureRefusesToSpin(t *testing.T) {
	s := NewStore()
	s.SetTurnLen(1, 2)
	s.SetTurnLen(9, 2)
	s.Insert(histMsg(1, 0, 2), histMsg(9, 0, 2))
	calls := 0
	s.SetFetcher(func(_ context.Context, _ Anchor, _ int) (Fetched, error) {
		calls++
		return Fetched{More: true}, nil // "nothing here", forever
	})
	if err := s.Ensure(context.Background(), Anchor{}, Anchor{Turn: 9, Node: 1}); !errors.Is(err, ErrStalled) {
		t.Fatalf("Ensure = %v; want ErrStalled", err)
	}
	if calls != 1 {
		t.Fatalf("a hole that did not shrink was retried %d times", calls)
	}
}

// TestForEachSegmentIsTheGapAwareWalk: the renderer's read path sees runs AND
// holes, in order, and reports no hole at all when there is none (the
// degenerate case, which is what keeps the pager quiet).
func TestForEachSegmentIsTheGapAwareWalk(t *testing.T) {
	s := NewStore()
	for turn := 1; turn <= 6; turn++ {
		s.SetTurnLen(uint64(turn), 1)
		s.Insert(histMsg(turn, 0, 1))
	}
	var turns []int
	var gaps []Gap
	walk := func() {
		turns, gaps = nil, nil
		s.ForEachSegment(Anchor{}, Anchor{Turn: ^uint64(0)},
			func(m Message) bool { turns = append(turns, m.Turn); return true },
			func(g Gap) bool { gaps = append(gaps, g); return true })
	}
	walk()
	if len(gaps) != 0 || len(turns) != 6 {
		t.Fatalf("contiguous history reported %d gaps over %d messages", len(gaps), len(turns))
	}
	s.Evict(Anchor{Turn: 3}, Anchor{Turn: 4})
	walk()
	if len(gaps) != 1 || len(turns) != 4 {
		t.Fatalf("one hole: %d gaps over %d messages", len(gaps), len(turns))
	}
	if got := gaps[0].Turns(); got != 2 {
		t.Fatalf("the hole swallows turns 3 and 4; it says %d", got)
	}
}
