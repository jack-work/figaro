package outfit_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/internal/outfit"
)

func manyOutfits(b *testing.B, nested bool) string {
	b.Helper()
	dir := b.TempDir()
	for i := 0; i < 200; i++ {
		rel := fmt.Sprintf("o%03d.toml", i)
		if nested {
			rel = filepath.Join(fmt.Sprintf("d%02d", i%20), rel)
		}
		path := filepath.Join(dir, "outfits", rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(fmt.Sprintf("[system]\nk%03d = \"v\"\n", i)), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "outfits", "top.toml"), []byte("layers = [\"o010\", \"o110\", \"o190\"]\n[system]\nmodel = \"m\"\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	return dir
}

func BenchmarkPerfLoadCold200(b *testing.B) {
	dir := manyOutfits(b, false)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := outfit.NewAt(dir, b.TempDir()).Load("top"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPerfLoadRevalidate200(b *testing.B) {
	dir := manyOutfits(b, false)
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
