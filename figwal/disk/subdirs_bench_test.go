package disk

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func benchDir(b *testing.B, files, dirs int) string {
	b.Helper()
	dir := b.TempDir()
	for i := 0; i < files; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%020d.jsonl", i)), nil, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < dirs; i++ {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("n%d", i)), 0o755); err != nil {
			b.Fatal(err)
		}
	}
	return dir
}

func BenchmarkHasSubdirs(b *testing.B) {
	for _, c := range []struct {
		name        string
		files, dirs int
	}{
		{"leaf3files", 3, 0},
		{"root1file10kdirs", 1, 10000},
		{"root1file1kdirs", 1, 1000},
	} {
		dir := benchDir(b, c.files, c.dirs)
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := hasSubdirs(dir); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkWriteOneRecord(b *testing.B) {
	dir := filepath.Join(b.TempDir(), "log")
	l, err := Open(dir, Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer l.Close()
	payload := []byte(`{"k":"v"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := l.Write(uint64(i+1), payload); err != nil {
			b.Fatal(err)
		}
	}
}
