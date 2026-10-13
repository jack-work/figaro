package aria

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// fatTurn builds a sealed, bracketed turn of roughly kb kilobytes.
func fatTurn(id uint64, kb int) Turn {
	return Turn{
		ID:     id,
		LTs:    []uint64{id*10 + 1, id*10 + 5},
		Sealed: true,
		Nodes: []livedoc.Node{{
			Type:     livedoc.NodeProse,
			Markdown: strings.Repeat("x", kb*1024),
			Src:      []livedoc.Src{{LT: id*10 + 2, Block: 0}},
		}},
		Inquiry: fmt.Sprintf("q%d", id),
	}
}

// composerFor answers a miss from a fixed history, counting the calls, and
// returns whole turns as the production composer does.
func composerFor(history map[uint64]Turn, count *int) Composer {
	return func(node string, fromLT, toLT uint64) []Turn {
		*count++
		var out []Turn
		for id := uint64(1); id <= uint64(len(history)); id++ {
			t, ok := history[id]
			if !ok || len(t.LTs) == 0 {
				continue
			}
			if t.LTs[0] >= fromLT && t.LTs[0] <= toLT {
				out = append(out, t)
			}
		}
		return out
	}
}

func boundTo(cc *ComposedCache, node string) *Server {
	s := NewServer()
	s.BindCache(node, cc)
	return s
}

// residentTurns counts what the cache holds for a node without faulting.
func residentTurns(c *TurnCache) (resident int) {
	for i := range c.keys {
		if _, ok := c.cache.At(c.node, c.keys[i].coord()); ok {
			resident++
		}
	}
	return resident
}

// Over budget, old turns hollow: the key list survives, the payloads go.
func TestTurnCacheEvictsAndRecomposes(t *testing.T) {
	history := map[uint64]Turn{}
	var recomposed int
	budget := fwtree.NewBudget(1 << 20)
	cc := NewComposedCache(budget, composerFor(history, &recomposed), nil)
	c := NewTurnCache(nil)
	c.bind("aria", cc)

	for id := uint64(1); id <= 40; id++ {
		tn := fatTurn(id, 64) // 64 KiB each; 40 x 64KiB = 2.5 MiB >> 1 MiB
		history[id] = tn
		c.Append(tn)
	}
	if !budget.Settle(2e9) {
		t.Fatal("the budget never settled")
	}
	if resident := residentTurns(c); resident == 40 {
		t.Fatal("nothing evicted: the budget bound nothing")
	}
	if c.Tail() == nil || c.Tail().ID != 40 {
		t.Fatal("the staging tail is not in the cache and can never be evicted")
	}
	if got, _, _ := budget.Stats(); got > 2<<20 {
		t.Fatalf("resident bytes %d far exceed the 1MiB budget", got)
	}

	i, ok := c.IndexOf(3)
	if !ok {
		t.Fatal("the index must survive eviction")
	}
	before := recomposed
	got := c.Slice(i, i)
	if len(got) != 1 || len(got[0].Nodes) == 0 || got[0].Inquiry != "q3" {
		t.Fatalf("recompose did not restore turn 3: %+v", got)
	}
	if recomposed == before {
		t.Fatal("turn 3 was served without a recompose: the fixture evicted nothing it then read")
	}
}

// A turn with no LT bracket is held pinned and counted, and pins only itself.
func TestUnbracketedTurnPinsOnlyItself(t *testing.T) {
	budget := fwtree.NewBudget(1 << 20)
	history := map[uint64]Turn{}
	var rec int
	cc := NewComposedCache(budget, composerFor(history, &rec), nil)
	c := NewTurnCache(nil)
	c.bind("aria", cc)

	c.Append(Turn{ID: 1, Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: strings.Repeat("y", 256<<10)}}})
	for id := uint64(2); id <= 40; id++ {
		tn := fatTurn(id, 64)
		history[id] = tn
		c.Append(tn)
	}
	budget.Settle(2e9)

	if !c.keys[0].phantom {
		t.Fatal("an unbracketed turn must be keyed in the reserved space")
	}
	got := c.Slice(0, 0)
	if len(got) != 1 || len(got[0].Nodes) != 1 || len(got[0].Nodes[0].Markdown) != 256<<10 {
		t.Fatalf("the pinned turn must stay whole through eviction: %+v", got)
	}
	evicted := 0
	for i := 1; i < len(c.keys)-1; i++ {
		if _, ok := c.cache.At(c.node, c.keys[i].coord()); !ok {
			evicted++
		}
	}
	if evicted == 0 {
		t.Fatal("bracketed turns must still evict: one unbracketed turn latched the cache (the S1 bug)")
	}
	res, _, _ := budget.Stats()
	if res < 256<<10 {
		t.Fatalf("the pinned turn's bytes must be COUNTED (meter=%d): a meter that reads zero at peak retention is the S1 blindness", res)
	}
}

// A turn sealed with a bracket becomes an ordinary evictable run once displaced.
func TestSealWithBracketLeavesAnEvictableTurn(t *testing.T) {
	cc := NewComposedCache(fwtree.NewBudget(1<<20), nil, nil)
	s := boundTo(cc, "aria")
	s.OpenTurn(7)
	s.Update(nil, []livedoc.Node{{Type: livedoc.NodeProse, Markdown: strings.Repeat("z", 64<<10)}}, 0)
	s.Close()
	s.Seal([]uint64{71, 75})
	s.Commit(fatTurn(8, 4)) // displaces turn 7 into the cache

	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.cache.keys[0]
	if k.phantom {
		t.Fatal("a bracketed sealed turn must not be keyed as unrecomposable")
	}
	if _, ok := s.cache.cache.At("aria", k.coord()); !ok {
		t.Fatal("the displaced turn must be resident in the cache")
	}
	if res, _, _ := cc.Budget().Stats(); res < 64<<10 {
		t.Fatalf("the sealed turn's bytes must be on the meter, got %d", res)
	}
}

func TestTurnCacheChunkForUsesTheIndex(t *testing.T) {
	c := NewTurnCache(nil)
	for id := uint64(1); id <= 10; id++ {
		c.Append(fatTurn(id, 4)) // ~4KiB each
	}
	lo, hi := c.ChunkFor(Anchor{}, Backward, 8*1024) // ~2 turns + margin
	if hi != 9 {
		t.Fatalf("backward zero anchor starts at the tail, got hi=%d", hi)
	}
	if n := hi - lo + 1; n < 3 || n > 5 {
		t.Fatalf("chunk should be budget-sized plus margin, got %d turns", n)
	}
	lo, hi = c.ChunkFor(Anchor{Turn: 5, Node: 0}, Forward, 4*1024)
	if lo > 4 || hi < 5 {
		t.Fatalf("chunk must cover the anchor with margin: [%d,%d]", lo, hi)
	}
}

// The server's windowed page equals the full walk's page.
func TestWindowedPageEqualsFullWalk(t *testing.T) {
	history := map[uint64]Turn{}
	var recomposed int
	budget := fwtree.NewBudget(1 << 20)
	cc := NewComposedCache(budget, composerFor(history, &recomposed), nil)
	bounded := boundTo(cc, "aria")
	full := NewServer()
	for id := uint64(1); id <= 40; id++ {
		tn := fatTurn(id, 64)
		history[id] = tn
		bounded.Commit(tn)
		full.Commit(tn)
	}
	budget.Settle(2e9)
	for _, at := range []Anchor{{}, {Turn: 3, Node: 0}, {Turn: 20, Node: 0}, {Turn: 39, Node: 0}} {
		for _, budgetBytes := range []int{10 * 1024, 200 * 1024} {
			a := bounded.ReadBefore(at, Anchor{}, budgetBytes)
			b := full.ReadBefore(at, Anchor{}, budgetBytes)
			if len(a.Parts) != len(b.Parts) {
				t.Fatalf("at=%+v budget=%d: %d vs %d parts", at, budgetBytes, len(a.Parts), len(b.Parts))
			}
			for i := range a.Parts {
				if a.Parts[i].ID != b.Parts[i].ID || a.Parts[i].From != b.Parts[i].From ||
					len(a.Parts[i].Nodes) != len(b.Parts[i].Nodes) {
					t.Fatalf("at=%+v budget=%d part %d diverges: %+v vs %+v",
						at, budgetBytes, i, a.Parts[i], b.Parts[i])
				}
			}
			if a.More.Before != b.More.Before {
				t.Fatalf("at=%+v budget=%d: More.Before %v vs %v", at, budgetBytes, a.More.Before, b.More.Before)
			}
		}
	}
	if recomposed == 0 {
		t.Fatal("the bounded server never recomposed: the test exercised nothing")
	}
}

// Every turn comes back whole across a run boundary: the tenant snaps the
// source bracket to whole turns and clips the answer back to the coord.
func TestEveryTurnFaultsBackWholeAcrossRunBoundaries(t *testing.T) {
	history := map[uint64]Turn{}
	var rec int
	budget := fwtree.NewBudget(256 << 10) // far below the 40 x 64 KiB history
	cc := NewComposedCache(budget, composerFor(history, &rec), nil)
	c := NewTurnCache(nil)
	c.bind("aria", cc)

	for id := uint64(1); id <= 40; id++ {
		tn := fatTurn(id, 64)
		history[id] = tn
		c.Append(tn)
	}
	budget.Settle(2e9)
	if residentTurns(c) > 20 {
		t.Fatalf("the fixture retained %d of 40 turns: it is not testing a fault", residentTurns(c))
	}
	for i := range c.keys {
		got := c.Slice(i, i)
		if len(got) != 1 {
			t.Fatalf("turn index %d: %d turns back", i, len(got))
		}
		want := history[c.keys[i].id]
		if got[0].ID != want.ID || got[0].Inquiry != want.Inquiry {
			t.Fatalf("turn %d came back as an index stub: %+v", c.keys[i].id, got[0])
		}
		if len(got[0].Nodes) != 1 || len(got[0].Nodes[0].Markdown) != len(want.Nodes[0].Markdown) {
			t.Fatalf("turn %d lost its content: %d nodes", c.keys[i].id, len(got[0].Nodes))
		}
	}
}

// The same property where the cache's own gap chunking cuts inside turns.
func TestAFaultedGapStillReturnsWholeTurns(t *testing.T) {
	history := map[uint64]Turn{}
	var rec int
	cc := NewComposedCache(fwtree.NewBudget(64<<20), composerFor(history, &rec), nil)
	c := NewTurnCache(nil)
	c.bind("aria", cc)
	for id := uint64(1); id <= 40; id++ {
		tn := fatTurn(id, 1)
		history[id] = tn
		c.Append(tn)
	}
	cc.cache.DropNode("aria") // the node's runs are gone; the key list is not
	if n := residentTurns(c); n != 0 {
		t.Fatalf("release left %d turns resident: the fault below is not exercised", n)
	}
	got := c.Slice(0, len(c.keys)-1)
	if len(got) != 40 {
		t.Fatalf("got %d turns of 40", len(got))
	}
	for i, tn := range got {
		want := history[uint64(i+1)]
		if tn.ID != want.ID || len(tn.Nodes) != 1 || len(tn.Nodes[0].Markdown) != len(want.Nodes[0].Markdown) {
			t.Fatalf("turn %d came back cut: %+v", i+1, tn)
		}
	}
}

// An unstamped aria's composer numbers each bracket from 1; the cache must
// not renumber a turn on a fault.
func TestAFaultDoesNotRenumberATurn(t *testing.T) {
	history := map[uint64]Turn{}
	var rec int
	renumbering := Composer(func(node string, fromLT, toLT uint64) []Turn {
		rec++
		var out []Turn
		next := uint64(1)
		for id := uint64(1); id <= uint64(len(history)); id++ {
			t, ok := history[id]
			if !ok || len(t.LTs) == 0 || t.LTs[0] < fromLT || t.LTs[0] > toLT {
				continue
			}
			t.ID = next
			next++
			out = append(out, t)
		}
		return out
	})
	budget := fwtree.NewBudget(256 << 10)
	cc := NewComposedCache(budget, renumbering, nil)
	c := NewTurnCache(nil)
	c.bind("aria", cc)
	for id := uint64(1); id <= 40; id++ {
		tn := fatTurn(id, 64)
		history[id] = tn
		c.Append(tn)
	}
	budget.Settle(2e9)
	if residentTurns(c) > 20 {
		t.Fatal("nothing evicted: no fault to renumber")
	}
	for i := range c.keys {
		got := c.Slice(i, i)
		if len(got) != 1 || got[0].ID != c.keys[i].id {
			t.Fatalf("index %d: served turn %d, the index says %d", i, got[0].ID, c.keys[i].id)
		}
		if got[0].Inquiry != history[c.keys[i].id].Inquiry {
			t.Fatalf("turn %d: content %q, want %q", c.keys[i].id, got[0].Inquiry, history[c.keys[i].id].Inquiry)
		}
	}
}
