package outfit_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/outfit"
)

// settle backdates every file and directory, as a config that was not just
// written looks: the steady state the send path sees.
func settle(b *testing.B, dir string) {
	b.Helper()
	old := time.Now().Add(-time.Hour)
	err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, old, old)
	})
	if err != nil {
		b.Fatal(err)
	}
}

func benchRevalidateSettled(b *testing.B, nested bool) {
	dir := manyOutfits(b, nested)
	settle(b, dir)
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

func BenchmarkPerfLoadRevalidate200Settled(b *testing.B) { benchRevalidateSettled(b, false) }

func BenchmarkPerfLoadRevalidate200NestedSettled(b *testing.B) { benchRevalidateSettled(b, true) }

func BenchmarkPerfLoadCold200Settled(b *testing.B) {
	dir := manyOutfits(b, false)
	settle(b, dir)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := outfit.NewAt(dir, b.TempDir()).Load("top"); err != nil {
			b.Fatal(err)
		}
	}
}
