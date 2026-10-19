package troupe

import (
	"fmt"
	"testing"
	"time"
)

func benchEntries(b *testing.B, n int) (*estate, []Entry) {
	s := newEstate(b)
	k := newKey(b)
	c, err := IssueIntermediate(s.rk, s.root, "estate", "a", k.Public(), s.now, IntermediateTTL)
	if err != nil {
		b.Fatal(err)
	}
	s.inter["a"], s.ikey["a"] = c, k
	es := make([]Entry, n)
	for i := range es {
		e, err := SignJoin(k, c, "estate", fmt.Sprintf("node-%d", i), "n.lan:7443", "user:owner", s.now.Add(time.Duration(i)*time.Millisecond))
		if err != nil {
			b.Fatal(err)
		}
		es[i] = e
	}
	return s, es
}

// A thousand entries is a troupe far larger than any owner's: merging them
// is a peer's first exchange, one more is every exchange after.
func BenchmarkLedgerMerge(b *testing.B) {
	s, es := benchEntries(b, 1001)
	b.Run("1000 at once", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			l := NewLedger("estate", s.root)
			if _, err := l.Merge(es[:1000]...); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("one into 1000", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			l := NewLedger("estate", s.root)
			if _, err := l.Merge(es[:1000]...); err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
			if _, err := l.Merge(es[1000]); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("1000 already held", func(b *testing.B) {
		l := NewLedger("estate", s.root)
		if _, err := l.Merge(es[:1000]...); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := l.Merge(es[:1000]...); err != nil {
				b.Fatal(err)
			}
		}
	})
}
