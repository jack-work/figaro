package store

import (
	"fmt"
	"math/rand"
	"testing"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// THE GATE plans/log-cache-policy.md NAMED AND NOBODY MADE: "a realistic
// scroll/hop pattern counting FALL-THROUGHS BELOW THE WINDOW". The policy said
// locality would be a better justification for the tree shape than the
// fork-sharing argument that collapsed, and then the measurement was never
// taken.
//
// It is not a simulation. Both structures here are the production ones -- the
// flat window is `newWindowedLog`, the tree is `tree.Cache` -- driven by the
// SAME access trace against the SAME byte budget, counting ENTRIES SERVED FROM
// BELOW. Counting rather than timing, because the question is "how many
// times", and entries rather than calls, because one Read of the whole channel
// and one page read are not the same event.
//
// WHAT IT DOES NOT MEASURE, said here so the number is not spent on the wrong
// question: the cost of a materialization (bytes, decode, allocation), the
// composed layer, and any behaviour of a REAL trace. The trace below is a
// stated model, not an observation.

// countingInner wraps a log and counts the entries it hands up from below.
type countingInner[T any] struct {
	Log[T]
	entries                               int
	calls                                 int
	reads, pages, froms                   int
	readEntries, pageEntries, fromEntries int
}

func (c *countingInner[T]) Read() []Entry[T] {
	out := c.Log.Read()
	c.calls++
	c.reads++
	c.readEntries += len(out)
	c.entries += len(out)
	return out
}

func (c *countingInner[T]) ReadPage(from, before uint64, n int) ([]Entry[T], int) {
	page, total := c.Log.ReadPage(from, before, n)
	c.calls++
	c.pages++
	c.pageEntries += len(page)
	c.entries += len(page)
	return page, total
}

func (c *countingInner[T]) ReadFrom(lt uint64, n int) []Entry[T] {
	out := c.Log.ReadFrom(lt, n)
	c.calls++
	c.froms++
	c.fromEntries += len(out)
	c.entries += len(out)
	return out
}

// TailBudgeted makes the fixture behave like the PRODUCTION inner log rather
// than like MemLog: xwalLog reads backward from the tail, decoding only what it
// will keep, so a bounded window's construction costs the tail and not the
// channel. Without this the flat window is seeded with the WHOLE history for
// free and the comparison is decided by the fixture.
func (c *countingInner[T]) TailBudgeted(budget, maxRows, inflation int) ([]Entry[T], int) {
	all := c.Log.Read()
	if inflation < 1 {
		inflation = 1
	}
	keep, bytes := 0, 0
	for i := len(all) - 1; i >= 0; i-- {
		sz := len(fmt.Sprint(all[i].Payload)) * inflation
		if keep > 0 && budget > 0 && bytes+sz > budget {
			break
		}
		if maxRows > 0 && keep >= maxRows {
			break
		}
		bytes += sz
		keep++
	}
	out := all[len(all)-keep:]
	c.calls++
	c.reads++
	c.readEntries += len(out)
	c.entries += len(out)
	return out, len(all)
}

// op is one access: a page of `n` entries starting at `from`.
type op struct {
	from uint64
	n    int
}

// hopTrace models a reader who mostly works at the tail and periodically hops
// back to an older region and reads AROUND it -- opening a transcript, scrolling
// up to something, reading a few pages there, coming back. The locality that
// matters is WITHIN a hop, which is exactly what a tail window cannot hold and
// an LRU over ranges can.
func hopTrace(total int, rng *rand.Rand) []op {
	const page = 32
	var ops []op
	for i := 0; i < 40; i++ {
		// tail work
		ops = append(ops, op{from: uint64(total - page), n: page})
		// a hop, then three pages of locality around it
		anchor := 1 + rng.Intn(total-4*page)
		for k := 0; k < 3; k++ {
			ops = append(ops, op{from: uint64(anchor + k*page/2), n: page})
		}
		// and the same anchor again, the way a reader re-reads what they hopped to
		ops = append(ops, op{from: uint64(anchor), n: page})
	}
	return ops
}

func TestLocalityBelowTheWindow_FlatVersusTree(t *testing.T) {
	// THE CONTROL IS THE SECOND ROW. At a budget that holds the whole channel
	// neither structure can fall through, so a difference there would mean the
	// harness is measuring itself. The claim lives in the difference between
	// the rows, not in either row alone.
	for _, budget := range []int{512 << 10, 8 << 20} {
		t.Run(fmt.Sprintf("budget-%dKiB", budget>>10), func(t *testing.T) {
			localityRun(t, budget)
		})
	}
}

func localityRun(t *testing.T, budget int) {
	const (
		entries   = 4000
		bodyBytes = 512
	)

	mem := NewMemLog[string]()
	body := string(make([]byte, bodyBytes))
	for i := 0; i < entries; i++ {
		if _, err := mem.Append(Entry[string]{Payload: fmt.Sprintf("%d%s", i, body)}); err != nil {
			t.Fatal(err)
		}
	}
	sizeOf := func(e Entry[string]) int { return len(e.Payload) }

	rng := rand.New(rand.NewSource(20260819))
	ops := hopTrace(entries, rng)

	// --- the flat tail window, production code, production policy ---
	inner := &countingInner[string]{Log: mem}
	flat := newWindowedLog[string](inner, 0, budget, 1, sizeOf)
	// CONSTRUCTION IS COUNTED FOR BOTH, and the fixture implements
	// TailBudgeted so the flat window pays what production pays: the tail it
	// keeps. An earlier version of this test EXCLUDED the construction read
	// because MemLog served the whole channel for free -- and the control row
	// then showed the flat window falling through ZERO times at an unbinding
	// budget while the tree paid its cold start, which is the fixture deciding
	// the comparison. The exclusion was the wrong fix; the fixture was.
	construction := inner.entries
	for _, o := range ops {
		flat.ReadPage(o.from, o.from+uint64(o.n), o.n)
	}
	flatEntries := inner.entries

	// --- the tree cache, same budget, same trace ---
	var treeServed int
	b := fwtree.NewBudget(int64(budget))
	all := mem.Read()
	cache := fwtree.New[Entry[string]](
		func(co fwtree.Coord) ([]Entry[string], error) {
			var out []Entry[string]
			for _, e := range all {
				if e.LT > co.From && e.LT <= co.To {
					out = append(out, e)
				}
			}
			treeServed += len(out)
			return out, nil
		},
		b,
		func(e Entry[string]) int { return sizeOf(e) },
		func(e Entry[string]) uint64 { return e.LT },
	)
	defer cache.Close()
	lineage := []fwtree.Ref{{Node: "aria"}}
	for _, o := range ops {
		if _, err := cache.Range(lineage, o.from, o.from+uint64(o.n)); err != nil {
			t.Fatal(err)
		}
	}

	asked := 0
	for _, o := range ops {
		asked += o.n
	}
	t.Logf("trace: %d reads, %d entries asked for, budget %d bytes", len(ops), asked, budget)
	t.Logf("(of the flat window's total, %d entries were its construction tail)", construction)
	t.Logf("FLAT tail window: %d entries served from below (%d calls: %d Read/%d entries, %d ReadPage/%d entries, %d ReadFrom/%d entries)",
		flatEntries, inner.calls, inner.reads, inner.readEntries, inner.pages, inner.pageEntries, inner.froms, inner.fromEntries)
	t.Logf("TREE range cache:  %d entries served from below", treeServed)
	t.Logf("ratio flat/tree: %.2fx", float64(flatEntries)/float64(treeServed))

	if budget >= 8<<20 {
		// THE CONTROL: at a budget that holds the whole channel, NOTHING may be
		// materialized twice. Each structure is allowed the channel once --
		// the flat window takes it whole at construction, the tree takes only
		// the ranges asked for -- and anything above that is re-reading, which
		// at an unbinding budget can only be the harness.
		if flatEntries > entries || treeServed > entries {
			t.Fatalf("control: an unbinding budget re-materialized -- flat=%d tree=%d, channel=%d",
				flatEntries, treeServed, entries)
		}
		return
	}
	if flatEntries == 0 || treeServed == 0 {
		t.Fatalf("fixture served nothing from below: flat=%d tree=%d -- the budget is not binding",
			flatEntries, treeServed)
	}
}
