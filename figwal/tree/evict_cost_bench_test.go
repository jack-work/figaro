package tree

import (
	"container/list"
	"sync"
	"testing"
)

// Epoch buckets versus the eviction scan, measured on the paths each lives
// on: the scan is on the daemon's standing sweep, paid once per beat by one
// goroutine; a bucket index would be maintained on touch, on the read path,
// where every reader is concurrent and nothing shared is written today.

func buildCache(tb testing.TB, b *Budget, runs int) *Cache[fatUnit] {
	c := New[fatUnit](
		func(co Coord) ([]fatUnit, error) {
			out := make([]fatUnit, 0, co.To-co.From)
			for k := co.From + 1; k <= co.To; k++ {
				out = append(out, fatUnit{key: k, payload: make([]byte, 1024)})
			}
			return out, nil
		},
		b,
		func(u fatUnit) int { return len(u.payload) },
		func(u fatUnit) uint64 { return u.key },
	)
	tb.Cleanup(c.Close)
	lineage := []Ref{{Node: "p"}}
	for i := 0; len(c.runs("p")) < runs; i++ {
		lo := uint64(i * runChunk)
		if _, err := c.Range(lineage, lo, lo+runChunk); err != nil {
			tb.Fatal(err)
		}
		if i > runs*4 {
			tb.Fatalf("fixture: %d runs after %d ranges", len(c.runs("p")), i)
		}
	}
	return c
}

// fatUnit is a 1 KiB unit: the fixture measures RUN COUNT, and a run is cut
// by bytes, so the unit size is what decides how many runs a range builds.
type fatUnit struct {
	key     uint64
	payload []byte
}

// The scan at production size: R in the high hundreds to low thousands. A
// full sweep, the compounding case.
func BenchmarkSweepFull(b *testing.B) {
	for _, runs := range []int{256, 1024, 4096} {
		b.Run(sizeName(runs), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				bud := NewBudget(0)
				c := buildCache(b, bud, runs)
				resident := len(c.runs("p"))
				b.StartTimer()
				dropped, _ := bud.TrimIdle(0)
				b.StopTimer()
				if dropped != resident-1 {
					b.Fatalf("dropped %d of %d", dropped, resident)
				}
				c.Close()
				b.StartTimer()
			}
		})
	}
}

// ONE EVICTION under pressure, which is what the standing sweep actually does
// per beat when a tenant has just charged past its limit.
func BenchmarkSweepOneEviction(b *testing.B) {
	for _, runs := range []int{256, 1024, 4096} {
		b.Run(sizeName(runs), func(b *testing.B) {
			bud := NewBudget(0)
			c := buildCache(b, bud, runs)
			resident, _, _ := bud.Stats()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				bud.SetLimit(resident - int64(i+1)*1024)
				bud.pressure.Store(true)
				bud.Sweep()
			}
			b.StopTimer()
			c.Close()
		})
	}
}

// THE MAINTENANCE, ON THE PATH IT WOULD LIVE ON. An epoch bucket index must be
// updated when a run is TOUCHED, and a touch happens on every warm read. Today
// that is one atomic store, and only when the epoch is stale.
//
// The in-run control is the read itself: both arms do the same Range over the
// same warm cache in the same binary, and the only difference is what the
// touch does.
func BenchmarkTouchShape(b *testing.B) {
	bud := NewBudget(0)
	c := buildCache(b, bud, 256)
	lineage := []Ref{{Node: "p"}}

	// One unit per read: a 64-unit Range copies 64 KiB and a mutex
	// acquisition disappears inside it.
	b.Run("today_atomic_epoch", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			var i uint64
			for pb.Next() {
				k := i % 2000
				i++
				if _, err := c.Range(lineage, k, k+1); err != nil {
					b.Fatal(err)
				}
			}
		})
	})

	// The bucket arm: the same read, plus the shared write an index needs. A
	// container/list under one mutex is the cheapest honest stand-in.
	var mu sync.Mutex
	l := list.New()
	elems := make([]*list.Element, 256)
	for i := range elems {
		elems[i] = l.PushFront(i)
	}
	b.Run("with_bucket_move", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			var i uint64
			for pb.Next() {
				k := i % 2000
				if _, err := c.Range(lineage, k, k+1); err != nil {
					b.Fatal(err)
				}
				mu.Lock()
				l.MoveToFront(elems[i%256])
				mu.Unlock()
				i++
			}
		})
	})
}

func sizeName(n int) string {
	switch n {
	case 256:
		return "R256"
	case 1024:
		return "R1024"
	case 4096:
		return "R4096"
	}
	return "R?"
}
