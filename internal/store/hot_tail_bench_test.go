package store

import (
	"fmt"
	"testing"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// THE COMPARISON THE CARVE-OUT RESTS ON, IN ONE BINARY AND ONE RUN.
//
// plans/log-cache-policy.md carved out "LRU owns the COLD ranges, never the HOT
// TAIL" on a figwal measurement of 1218ns against a lock-free view's 516ns,
// with the sign flipping as readers were added. That number was taken on
// another machine against another tree, it has never been reproduced here, and
// it is the whole reason figaro still runs a flat tail window beside
// tree.Cache. It is also the premise for the last open question of this
// consolidation.
//
// TWO BENCHMARKS IN THE SAME BINARY IS THE PROTOCOL. Every A/B I ran tonight
// compared two builds across time and needed a calibration I did not run
// (plans/tree-shaped-log.md, the retraction). Two benchmarks in one binary
// share the machine, the second, the page cache and the scheduler, so no
// interleaving, counterbalancing or benchstat pairing can go wrong: read the
// two lines of one output.
//
// WHAT IS COMPARED: the hot-tail read each structure actually offers -- the
// flat window's published-view slice, and the tree's Range over the same span.
// Both are lock-free on the read path since 63902f44.

func hotTailFixture(entries, body int) (*cachedLog[string], *fwtree.Cache[Entry[string]], []fwtree.Ref) {
	mem := NewMemLog[string]()
	payload := string(make([]byte, body))
	for i := 0; i < entries; i++ {
		mem.Append(Entry[string]{Payload: fmt.Sprintf("%d%s", i, payload)})
	}
	sizeOf := func(e Entry[string]) int { return len(e.Payload) }
	flat := newWindowedLog[string](mem, 0, 64<<20, 1, 1, sizeOf)

	all := mem.Read()
	cache := fwtree.New[Entry[string]](
		func(co fwtree.Coord) ([]Entry[string], error) {
			var out []Entry[string]
			for _, e := range all {
				if e.LT > co.From && e.LT <= co.To {
					out = append(out, e)
				}
			}
			return out, nil
		},
		fwtree.NewBudget(64<<20),
		sizeOf,
		func(e Entry[string]) uint64 { return e.LT },
	)
	lineage := []fwtree.Ref{{Node: "aria"}}
	cache.Range(lineage, 0, uint64(entries)) // warm
	return flat, cache, lineage
}

const (
	hotEntries = 2000
	hotBody    = 256
	hotSpan    = 64
)

func BenchmarkHotTailFlatWindow(b *testing.B) {
	flat, cache, _ := hotTailFixture(hotEntries, hotBody)
	defer cache.Close()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if got := flat.ReadFrom(hotEntries-hotSpan, hotSpan); len(got) != hotSpan {
				b.Fatalf("flat tail read returned %d", len(got))
			}
		}
	})
}

func BenchmarkHotTailTreeRange(b *testing.B) {
	_, cache, lineage := hotTailFixture(hotEntries, hotBody)
	defer cache.Close()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			got, err := cache.Range(lineage, hotEntries-hotSpan, hotEntries)
			if err != nil || len(got) != hotSpan {
				b.Fatalf("tree tail read returned %d err=%v", len(got), err)
			}
		}
	})
}

func BenchmarkHotTailFlatWindowSerial(b *testing.B) {
	flat, cache, _ := hotTailFixture(hotEntries, hotBody)
	defer cache.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		flat.ReadFrom(hotEntries-hotSpan, hotSpan)
	}
}

func BenchmarkHotTailTreeRangeSerial(b *testing.B) {
	_, cache, lineage := hotTailFixture(hotEntries, hotBody)
	defer cache.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Range(lineage, hotEntries-hotSpan, hotEntries)
	}
}
