package aria

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// fatTurn builds a sealed turn of roughly kb kilobytes with an LT
// bracket, so it is evictable and recomposable.
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

// sourceFor answers recompositions from a fixed history, counting them.
func sourceFor(history map[uint64]Turn, count *int) TurnSource {
	return func(fromLT, toLT uint64) []Turn {
		*count++
		var out []Turn
		for _, t := range history {
			if ltBracket(t) >= fromLT && ltBracket2(t) <= toLT {
				out = append(out, t)
			}
		}
		return out
	}
}

// Over budget, old turns hollow -- index kept, nodes gone -- and a read
// that lands in them recomposes exactly that range from the source.
func TestTurnCacheEvictsAndRecomposes(t *testing.T) {
	history := map[uint64]Turn{}
	var recomposed int
	budget := NewUIBudget(1) // 1 MiB
	c := NewTurnCache(sourceFor(history, &recomposed), budget)

	for id := uint64(1); id <= 40; id++ {
		tn := fatTurn(id, 64) // 64 KiB each; 40 x 64KiB = 2.5 MiB >> 1 MiB
		history[id] = tn
		c.Append(tn)
	}
	resident := 0
	for i := range c.meta {
		if c.meta[i].resident {
			resident++
		}
	}
	if resident == 40 {
		t.Fatal("nothing evicted: the budget bound nothing")
	}
	if c.meta[len(c.meta)-1].resident == false {
		t.Fatal("the TAIL was evicted; it is pinned by design")
	}
	if got, _, _ := budget.Stats(); got > 2<<20 {
		t.Fatalf("resident bytes %d far exceed the 1MiB budget", got)
	}

	// A hollow turn still answers by id, and a slice through it comes
	// back whole via the source.
	i, ok := c.IndexOf(3)
	if !ok {
		t.Fatal("the index must survive eviction")
	}
	if c.meta[i].resident {
		t.Skip("turn 3 unexpectedly resident; budget too generous for the fixture")
	}
	before := recomposed
	got := c.Slice(i, i)
	if len(got) != 1 || len(got[0].Nodes) == 0 || got[0].Inquiry != "q3" {
		t.Fatalf("recompose did not restore turn 3: %+v", got)
	}
	if recomposed != before+1 {
		t.Fatalf("expected exactly one recompose, got %d", recomposed-before)
	}
}

// A turn with no LT bracket cannot be recomposed; it pins ITSELF -- and
// only itself. v1 latched the whole cache off one such turn, which
// disabled eviction and blinded the meter for every aria after its
// first live turn: the convicted cause of the >1GB session (S1,
// plans/storm-triage.md). This test holds all three prongs of the fix.
func TestUnbracketedTurnPinsOnlyItself(t *testing.T) {
	budget := NewUIBudget(1)
	history := map[uint64]Turn{}
	var rec int
	c := NewTurnCache(sourceFor(history, &rec), budget)
	// One unbracketed turn (a live-path tail), then a budget-busting run
	// of bracketed ones.
	c.Append(Turn{ID: 1, Sealed: true, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: strings.Repeat("y", 256<<10)}}})
	for id := uint64(2); id <= 40; id++ {
		tn := fatTurn(id, 64)
		history[id] = tn
		c.Append(tn)
	}
	if !c.meta[0].resident || !c.meta[0].pinned {
		t.Fatal("the unbracketed turn must stay resident, pinned")
	}
	evicted := 0
	for i := 1; i < len(c.meta)-1; i++ {
		if !c.meta[i].resident {
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

// The live path end to end: OpenTurn (bracket-less tail), stream, Close,
// then Seal WITH a bracket -- the sealed turn must unpin, join the LRU,
// and be evictable. Seal(nil) is the regression this pins.
func TestSealWithBracketUnpinsTheTail(t *testing.T) {
	budget := NewUIBudget(1)
	c := NewServer()
	c.BindCache(nil, budget)
	c.OpenTurn(7)
	c.Update(nil, []livedoc.Node{{Type: livedoc.NodeProse, Markdown: strings.Repeat("z", 64<<10)}}, 0)
	c.Close()
	c.Seal([]uint64{71, 75})
	c.mu.Lock()
	tl := c.cache.Tail()
	pinned := c.cache.meta[len(c.cache.meta)-1].pinned
	counted := c.cache.meta[len(c.cache.meta)-1].counted
	c.mu.Unlock()
	if tl == nil || !tl.Sealed || len(tl.LTs) != 2 {
		t.Fatalf("seal must stamp the bracket: %+v", tl)
	}
	if pinned {
		t.Fatal("a bracketed sealed turn must not stay pinned")
	}
	if !counted {
		t.Fatal("the sealed turn's bytes must be on the meter")
	}
}

// ChunkFor picks a byte-exact range from the index, with one turn of
// margin, whatever the residency.
func TestTurnCacheChunkForUsesTheIndex(t *testing.T) {
	c := NewTurnCache(nil, nil)
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

// The server's windowed page equals the full walk's page: the window is
// an optimization, not a different answer.
func TestWindowedPageEqualsFullWalk(t *testing.T) {
	history := map[uint64]Turn{}
	var recomposed int
	budget := NewUIBudget(1)
	src := sourceFor(history, &recomposed)

	bounded := NewServer()
	bounded.BindCache(src, budget)
	full := NewServer()
	for id := uint64(1); id <= 40; id++ {
		tn := fatTurn(id, 64)
		history[id] = tn
		bounded.Commit(tn)
		full.Commit(tn)
	}
	for _, at := range []Anchor{{}, {Turn: 3, Node: 0}, {Turn: 20, Node: 0}, {Turn: 39, Node: 0}} {
		for _, budgetBytes := range []int{10 * 1024, 200 * 1024} {
			a := bounded.ReadBefore(at, budgetBytes)
			b := full.ReadBefore(at, budgetBytes)
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
