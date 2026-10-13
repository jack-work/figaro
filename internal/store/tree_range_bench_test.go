package store

import (
	"testing"

	fwtree "github.com/jack-work/figaro/internal/store/tree"
)

// A fresh fork holds only inherited rows, so every read of a new branch takes
// the shared path until it appends. The unit carries its own coordinate so
// the Keyer is a field read.
type benchUnit struct {
	lt   uint64
	body string
}

func benchCache(units int) (*fwtree.Cache[benchUnit], []fwtree.Ref) {
	body := string(make([]byte, 256))
	c := fwtree.New(
		func(co fwtree.Coord) ([]benchUnit, error) {
			out := make([]benchUnit, 0, co.To-co.From)
			for i := co.From + 1; i <= co.To; i++ {
				out = append(out, benchUnit{lt: i, body: body})
			}
			return out, nil
		},
		fwtree.NewBudget(64<<20),
		func(u benchUnit) int { return len(u.body) + 48 },
		func(u benchUnit) uint64 { return u.lt },
	)
	lineage := []fwtree.Ref{{Node: "parent", Base: 0}, {Node: "child", Base: uint64(units + 1)}}
	c.Range(lineage, 0, uint64(units)) // warm
	return c, lineage
}

func BenchmarkTreeRangeParallel(b *testing.B) {
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

func BenchmarkTreeRangeSerial(b *testing.B) {
	c, lineage := benchCache(2000)
	defer c.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = c.Range(lineage, 1500, 1564)
	}
}

// A single-unit read under many readers: the shape where a lock on the read
// path can show; a 64-unit range is dominated by copying.
func BenchmarkTreeRangeOneUnitParallel(b *testing.B) {
	c, lineage := benchCache(2000)
	defer c.Close()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = c.Range(lineage, 1500, 1501)
		}
	})
}

func BenchmarkTreeRangeOneUnitSerial(b *testing.B) {
	c, lineage := benchCache(2000)
	defer c.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = c.Range(lineage, 1500, 1501)
	}
}
