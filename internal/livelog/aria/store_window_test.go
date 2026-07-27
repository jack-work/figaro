package aria

import (
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
// aria. Revert TailFrom to a forward scan and this still passes — what it
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
		t.Fatalf("TailFrom(3) = %v,%v; want turn 2 — the count spans the hole", got, ok)
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
	// It counts MESSAGES across a hole, exactly as TailFrom does — the window's
	// floor may straddle one.
	s.Evict(Anchor{Turn: 4}, Anchor{Turn: 6, Node: 1})
	if got, n := s.Before(Anchor{Turn: 8}, 3); n != 3 || got != (Anchor{Turn: 2}) {
		t.Fatalf("Before across a hole = %v, %d; want turn 2 over 3", got, n)
	}
}
