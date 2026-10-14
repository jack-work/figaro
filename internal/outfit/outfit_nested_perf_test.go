package outfit_test

import (
	"testing"

	"github.com/jack-work/figaro/internal/outfit"
)

func BenchmarkPerfLoadCold200Nested(b *testing.B) {
	dir := manyOutfits(b, true)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := outfit.NewAt(dir, b.TempDir()).Load("top"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPerfLoadRevalidate200Nested(b *testing.B) {
	dir := manyOutfits(b, true)
	o := outfit.NewAt(dir, b.TempDir())
	o.SetStaleWindow(0)
	if _, err := o.Load("top"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := o.Load("top"); err != nil {
			b.Fatal(err)
		}
	}
}
