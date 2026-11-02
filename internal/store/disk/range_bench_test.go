package disk

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkChildRangeThroughParent(b *testing.B) {
	root := b.TempDir()
	parentDir, childDir := filepath.Join(root, "p"), filepath.Join(root, "c")
	p, err := Open(parentDir, Options{SegmentSize: 64 << 10})
	if err != nil {
		b.Fatal(err)
	}
	defer p.Close()
	for i := uint64(1); i <= 20000; i++ {
		if err := p.Write(i, []byte(fmt.Sprintf(`{"n":%d,"pad":"0123456789abcdef0123456789abcdef"}`, i))); err != nil {
			b.Fatal(err)
		}
	}
	if err := p.Sync(); err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, ".fork"), []byte("base=15001\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	c, err := Open(childDir, Options{SegmentSize: 64 << 10, Parent: p})
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		if err := c.Range(1, func(uint64, []byte) error { n++; return nil }); err != nil {
			b.Fatal(err)
		}
		if n != 15000 {
			b.Fatalf("ranged %d", n)
		}
	}
}
