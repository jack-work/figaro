package form

// Benchmarks for the persistent tree itself. Snapshot-level benchmarks (Clone,
// Apply, Diff through the public API) belong to the bench worker's file; these
// measure the data structure in isolation.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var benchSizes = []int{16, 128, 1024}

func benchKey(i int) string { return fmt.Sprintf("key%06d", i) }

func benchTree(tb testing.TB, n int) ptree {
	tb.Helper()
	var t ptree
	for i := range n {
		t = t.Set(benchKey(i), NewValue(json.RawMessage(fmt.Sprintf(`{"i":%d,"s":"value-%d"}`, i, i))))
	}
	return t
}

func BenchmarkTreeSetExisting(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			tree := benchTree(b, n)
			v := NewValue(json.RawMessage(`{"i":-1,"s":"replacement"}`))
			key := benchKey(n / 2)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				_ = tree.Set(key, v)
			}
		})
	}
}

func BenchmarkTreeSetNew(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			tree := benchTree(b, n)
			v := NewValue(json.RawMessage(`1`))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				_ = tree.Set(fmt.Sprintf("zz%09d", i), v)
			}
		})
	}
}

// The semantically-identical write: no path copy at all.
func BenchmarkTreeSetNoOp(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			tree := benchTree(b, n)
			key := benchKey(n / 2)
			stored, _ := tree.Get(key)
			same := NewValue(stored.Raw())
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = tree.Set(key, same)
			}
		})
	}
}

func BenchmarkTreeGet(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			tree := benchTree(b, n)
			keys := make([]string, n)
			for i := range keys {
				keys[i] = benchKey(i)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				if _, ok := tree.Get(keys[i%n]); !ok {
					b.Fatal("miss")
				}
			}
		})
	}
}

func BenchmarkTreeDelete(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			tree := benchTree(b, n)
			key := benchKey(n / 2)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = tree.Delete(key)
			}
		})
	}
}

func BenchmarkTreeAll(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			tree := benchTree(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				count := 0
				for range tree.All() {
					count++
				}
				if count != n {
					b.Fatalf("walked %d", count)
				}
			}
		})
	}
}

// The headline: a one-key delta between two trees that share everything else.
func BenchmarkDiffOneKeyChanged(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			prev := benchTree(b, n)
			next := prev.Set(benchKey(n/2), NewValue(json.RawMessage(`"changed"`)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if p := diffTrees(prev.root, next.root); len(p.Set) != 1 {
					b.Fatalf("patch has %d sets", len(p.Set))
				}
			}
		})
	}
}

func BenchmarkDiffUnchanged(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			tree := benchTree(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if p := diffTrees(tree.root, tree.root); !p.IsEmpty() {
					b.Fatal("non-empty")
				}
			}
		})
	}
}

// The worst case: two trees of the same size sharing no structure at all.
func BenchmarkDiffUnrelated(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			prev := benchTree(b, n)
			var next ptree
			for i := range n {
				next = next.Set(benchKey(i), NewValue(json.RawMessage(fmt.Sprintf(`{"i":%d,"s":"other-%d"}`, i, i))))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if p := diffTrees(prev.root, next.root); len(p.Set) != n {
					b.Fatalf("patch has %d sets", len(p.Set))
				}
			}
		})
	}
}

// Canonicalisation is the one place a value's size shows up as CPU: it runs
// once per NewValue, never per read.
func BenchmarkNewValue(b *testing.B) {
	small := json.RawMessage(`{"a":1,"b":"two"}`)
	medium := json.RawMessage(`{"name":"skill","description":"` + strings.Repeat("x", 512) + `","tags":["a","b","c"]}`)
	large := json.RawMessage(`{"body":"` + strings.Repeat("y", 32*1024) + `"}`)
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{{"small", small}, {"medium_512B", medium}, {"large_32KiB", large}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.raw)))
			for b.Loop() {
				if v := NewValue(tc.raw); !v.IsJSON() {
					b.Fatal("invalid")
				}
			}
		})
	}
}

func BenchmarkValueEqual(b *testing.B) {
	a := NewValue(json.RawMessage(`{"name":"skill","body":"` + strings.Repeat("y", 4096) + `"}`))
	c := NewValue(json.RawMessage(`{"body":"` + strings.Repeat("y", 4096) + `","name":"skill"}`))
	b.ReportAllocs()
	for b.Loop() {
		if !a.Equal(c) {
			b.Fatal("expected equal")
		}
	}
}
