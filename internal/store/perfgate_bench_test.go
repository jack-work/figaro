package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/perffix"
)

const (
	perfBoardLeaves  = 3250
	perfBoardPatches = 13200
	perfBoardSegment = 8 << 20
)

func perfBoardLeaf(i int) string {
	return fmt.Sprintf("board.s%02d.items.i%03d.f%d", i%25, (i/25)%130, i/3250)
}

// The #122 board: 3,250 leaves, 13,200 one-leaf patches, one 8 MiB segment.
func perfBoard(tb testing.TB) (dir, id string) {
	dir = perffix.Dir(tb, "store-board-v1", func(dir string) {
		b, err := NewXwalBackend(dir, perfBoardSegment)
		if err != nil {
			tb.Fatal(err)
		}
		defer b.Close()
		id := mustNewAriaTB(tb, b)
		pad := strings.Repeat("w", 90)
		for i := 0; i < perfBoardPatches; i++ {
			raw, _ := json.Marshal(fmt.Sprintf("%d %s", i, pad))
			if _, err := b.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{
				perfBoardLeaf(i % perfBoardLeaves): raw,
			}, nil)); err != nil {
				tb.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "perf-board-id"), []byte(id), 0o600); err != nil {
			tb.Fatal(err)
		}
	})
	raw, err := os.ReadFile(filepath.Join(dir, "perf-board-id"))
	if err != nil {
		tb.Fatal(err)
	}
	return dir, string(raw)
}

// The perf gate's cold open: a fresh backend over the #122 board, up to the
// form's state in memory.
func BenchmarkGateFormColdOpen(b *testing.B) {
	dir, id := perfBoard(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		be, err := NewXwalBackend(dir, perfBoardSegment)
		if err != nil {
			b.Fatal(err)
		}
		f, err := be.form(id)
		if err != nil {
			b.Fatal(err)
		}
		if _, v := f.Snapshot(); v < perfBoardPatches {
			b.Fatalf("board opened at version %d, want >= %d", v, perfBoardPatches)
		}
		b.StopTimer()
		be.Close()
		b.StartTimer()
	}
}
