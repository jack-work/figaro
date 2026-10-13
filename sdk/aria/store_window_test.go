package aria

import (
	"context"
	"errors"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// histPage is a sealed backward read of whole two-node turns.
func histPage(turns ...int) Page {
	var p Page
	for _, turn := range turns {
		p.Parts = append(p.Parts, TurnPart{Turn: Turn{
			ID: uint64(turn), Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "h"}, {Type: livedoc.NodeProse, Markdown: "h"}},
		}})
	}
	return p
}

func histMsg(turn int, from uint64, n int) Message {
	m := Message{Turn: turn, From: from, Role: livedoc.RoleOutput}
	for i := 0; i < n; i++ {
		m.Nodes = append(m.Nodes, livedoc.Node{Type: livedoc.NodeProse, Markdown: "h"})
	}
	return m
}

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

func TestQuietFoldIsSilentAndDeduped(t *testing.T) {
	c := NewClient()
	fired := 0
	c.OnClosed = func(Message) { fired++ }
	c.OnLive = func(Message) { fired++ }

	c.Apply(histPage(1, 2), Quiet)
	c.Apply(histPage(1, 2), Quiet)

	if fired != 0 {
		t.Fatalf("a quiet fold must fire no callbacks; got %d", fired)
	}
	if got := c.Store().Count(); got != 2 {
		t.Fatalf("folding the same page twice retains %d messages; want 2", got)
	}
	if got := len(c.Store().Ranges()); got != 1 {
		t.Fatalf("the stated extents make turns 1 and 2 neighbours: want one range, got %d", got)
	}
}

func TestClippedTailStatesNoExtent(t *testing.T) {
	c := NewClient()
	p := histPage(1, 2)
	p.Parts[0].ClippedTail = true
	c.Apply(p, Quiet)
	if got := len(c.Store().Ranges()); got != 2 {
		t.Fatalf("no extent for turn 1 => no adjacency across it; got %d ranges", got)
	}
}

func TestQuietFoldBumpsTheRevision(t *testing.T) {
	c := NewClient()
	rev := c.ClosedRevision()
	c.Apply(histPage(3), Quiet)
	if c.ClosedRevision() == rev {
		t.Fatal("a folded page changes the retained set; the revision must move")
	}
}

func TestEvictBeforeMakesAHole(t *testing.T) {
	c := NewClient()
	c.Apply(histPage(1, 2, 3, 4, 5, 6), Quiet)
	c.EvictBefore(Anchor{Turn: 4})
	s := c.Store()
	if got := s.Count(); got != 3 {
		t.Fatalf("evicting below turn 4 leaves 3 messages; got %d", got)
	}
	if first := s.First(); first == nil || first.Turn != 4 {
		t.Fatalf("the floor is turn 4; got %v", first)
	}
	c.EvictBefore(Anchor{Turn: 4})
	if got := s.Count(); got != 3 {
		t.Fatalf("re-evicting changed the store: %d", got)
	}
}

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

func TestBeforeIsTheReturnTrip(t *testing.T) {
	s := NewStore()
	for turn := 1; turn <= 10; turn++ {
		s.SetTurnLen(uint64(turn), 2)
		s.Insert(histMsg(turn, 0, 2))
	}
	if got, n := s.Before(Anchor{Turn: 8}, 3); n != 3 || got != (Anchor{Turn: 5}) {
		t.Fatalf("Before(8, 3) = %v, %d; want turn 5 over 3 messages", got, n)
	}
	if got, n := s.Before(Anchor{Turn: 3}, 10); n != 2 || got != (Anchor{Turn: 1}) {
		t.Fatalf("Before(3, 10) = %v, %d; want the oldest held over 2", got, n)
	}
	if got, n := s.Before(Anchor{Turn: 1}, 4); n != 0 || got != (Anchor{Turn: 1}) {
		t.Fatalf("Before at the floor = %v, %d; want the anchor itself over 0", got, n)
	}
	// Counts messages across a hole, as TailFrom does.
	s.Evict(Anchor{Turn: 4}, Anchor{Turn: 6, Node: 1})
	if got, n := s.Before(Anchor{Turn: 8}, 3); n != 3 || got != (Anchor{Turn: 2}) {
		t.Fatalf("Before across a hole = %v, %d; want turn 2 over 3", got, n)
	}
}

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
	c := NewClient()
	c.Apply(histPage(1, 2, 3, 18, 19, 20), Quiet)
	if err := c.Ensure(context.Background(), Anchor{Turn: 1}, Anchor{Turn: 20, Node: 1}); !errors.Is(err, ErrNoFetcher) {
		t.Fatalf("with no fetcher installed, Ensure = %v; want ErrNoFetcher", err)
	}
	reads := []Anchor{}
	c.SetFetcher(func(_ context.Context, before Anchor, limit int) (Page, error) {
		reads = append(reads, before)
		var turns []int
		for turn := int(before.Turn) - 1; turn >= 1 && len(turns) < limit; turn-- {
			turns = append([]int{turn}, turns...)
		}
		p := histPage(turns...)
		p.More.Before = true
		return p, nil
	})
	if err := c.Ensure(context.Background(), Anchor{Turn: 1}, Anchor{Turn: 20, Node: 1}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(reads) == 0 {
		t.Fatal("Ensure closed the hole without reading")
	}
	if reads[0] != (Anchor{Turn: 18}) {
		t.Fatalf("first fill read at %v; want the anchor just past the hole", reads[0])
	}
	if got := len(c.Store().Ranges()); got != 1 {
		t.Fatalf("a closed hole must coalesce: %d ranges", got)
	}
	if got, want := c.Count(), whole.Count(); got != want {
		t.Fatalf("filled store holds %d messages, the whole aria has %d", got, want)
	}
	for _, seg := range c.Query(Anchor{Turn: 1}, Anchor{Turn: 20, Node: 1}) {
		if seg.Gap != nil {
			t.Fatalf("Query still reports a hole after Ensure: %+v", seg.Gap)
		}
	}
}

func TestEnsureRefusesToSpin(t *testing.T) {
	c := NewClient()
	c.Apply(histPage(1, 9), Quiet)
	calls := 0
	c.SetFetcher(func(_ context.Context, _ Anchor, _ int) (Page, error) {
		calls++
		return Page{More: More{Before: true}}, nil // "nothing here", forever
	})
	if err := c.Ensure(context.Background(), Anchor{}, Anchor{Turn: 9, Node: 1}); !errors.Is(err, ErrStalled) {
		t.Fatalf("Ensure = %v; want ErrStalled", err)
	}
	if calls != 1 {
		t.Fatalf("a hole that did not shrink was retried %d times", calls)
	}
}

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
