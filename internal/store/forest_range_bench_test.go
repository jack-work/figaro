package store

import (
	"testing"

	fwforest "github.com/jack-work/figwal/forest"
)

// A fresh fork holds 298 inherited rows and 0 of its own, so under the
// corrected seam every read of a new branch takes the shared (mutex) path
// until it appends. That is not the path the 516ns lock-free number covers.
//
// The unit carries its own coordinate so the Keyer is a field read: a keyer
// that parses would benchmark the fixture instead of the cache.
type benchUnit struct {
	lt   uint64
	body string
}

func benchCache(units int) (*fwforest.Cache[benchUnit], []fwforest.Ref) {
	body := string(make([]byte, 256))
	c := fwforest.New(
		func(co fwforest.Coord) ([]benchUnit, error) {
			out := make([]benchUnit, 0, co.To-co.From)
			for i := co.From + 1; i <= co.To; i++ {
				out = append(out, benchUnit{lt: i, body: body})
			}
			return out, nil
		},
		fwforest.NewBudget(64<<20),
		func(u benchUnit) int { return len(u.body) + 48 },
		func(u benchUnit) uint64 { return u.lt },
	)
	lineage := []fwforest.Ref{{Node: "parent", Base: 0}, {Node: "child", Base: uint64(units + 1)}}
	c.Range(lineage, 0, uint64(units)) // warm
	return c, lineage
}

func BenchmarkForestRangeParallel(b *testing.B) {
	c, lineage := benchCache(2000)
	defer c.Close()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = c.Range(lineage, 1500, 1564)
		}
	})
}

func BenchmarkForestRangeSerial(b *testing.B) {
	c, lineage := benchCache(2000)
	defer c.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = c.Range(lineage, 1500, 1564)
	}
}
